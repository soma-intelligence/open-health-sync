// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"container/list"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// LowMemoryCacheUsers is the size of the user key cache on the low-memory
// profile, when the operator kept the default.
const LowMemoryCacheUsers = 128

// cacheUsersFor is the size of the user key cache: what the operator
// configured, except that the default shrinks to LowMemoryCacheUsers when the
// process runs under the low-memory profile. 0 means the default.
func cacheUsersFor(configured int, low bool) int {
	if configured <= 0 {
		configured = config.DefaultVaultCacheUsers
	}
	if low && configured == config.DefaultVaultCacheUsers {
		return LowMemoryCacheUsers
	}
	return configured
}

// zeroer is a cached secret: the cache zeroes it when the entry leaves.
type zeroer interface{ Zero() }

// cache is an LRU of secrets with a time to live counted from insertion: a
// use does not extend it, so a key unwrapped once is unwrapped again at least
// every ttl. Every entry that leaves (expired, evicted, replaced, removed) is
// zeroed. Values never leave the cache: get hands the value to a function
// that runs under the lock, which copies what it needs, so no caller holds a
// value the cache may zero under it.
type cache[K comparable, V zeroer] struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	now   func() time.Time
	ll    *list.List // front is the most recently used
	items map[K]*list.Element
	// removals counts remove and purge calls, so a load that started
	// before one is not cached after it (see put).
	removals uint64
}

type cacheEntry[K comparable, V zeroer] struct {
	key     K
	val     V
	expires time.Time
}

func newCache[K comparable, V zeroer](max int, ttl time.Duration, now func() time.Time) *cache[K, V] {
	return &cache[K, V]{max: max, ttl: ttl, now: now, ll: list.New(), items: map[K]*list.Element{}}
}

// get runs use on the value of k and reports true, or reports false when k is
// absent or expired (an expired entry is zeroed then).
func (c *cache[K, V]) get(k K, use func(V)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return false
	}
	e := el.Value.(*cacheEntry[K, V])
	if !c.now().Before(e.expires) {
		c.drop(el)
		return false
	}
	c.ll.MoveToFront(el)
	use(e.val)
	return true
}

// epoch is read before loading a value from the database; put takes it back.
func (c *cache[K, V]) epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removals
}

// put caches v under k, evicting the least recently used entries past max.
// When a remove or purge happened since epoch was read, v may hold keys that
// were deleted meanwhile: it is zeroed instead of cached.
func (c *cache[K, V]) put(k K, v V, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch != c.removals {
		v.Zero()
		return
	}
	if el, ok := c.items[k]; ok {
		c.drop(el)
	}
	c.items[k] = c.ll.PushFront(&cacheEntry[K, V]{key: k, val: v, expires: c.now().Add(c.ttl)})
	for c.ll.Len() > c.max {
		c.drop(c.ll.Back())
	}
}

// remove drops k, if cached.
func (c *cache[K, V]) remove(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removals++
	if el, ok := c.items[k]; ok {
		c.drop(el)
	}
}

// purge drops every entry.
func (c *cache[K, V]) purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removals++
	for el := c.ll.Front(); el != nil; el = c.ll.Front() {
		c.drop(el)
	}
}

// sweep drops the expired entries, so a key nobody asks for again does not
// stay in memory until it is evicted.
func (c *cache[K, V]) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for el := c.ll.Back(); el != nil; {
		prev := el.Prev()
		if !now.Before(el.Value.(*cacheEntry[K, V]).expires) {
			c.drop(el)
		}
		el = prev
	}
}

func (c *cache[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// drop removes el and zeroes its value. The caller holds the lock.
func (c *cache[K, V]) drop(el *list.Element) {
	e := c.ll.Remove(el).(*cacheEntry[K, V])
	delete(c.items, e.key)
	e.val.Zero()
}

// flights merges concurrent loads of one key: the first caller runs the load,
// the others wait for its result. A result is shared read-only by every
// caller of the flight, so it must not be a value the cache owns.
type flights[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]*flight[V]
}

type flight[V any] struct {
	done chan struct{}
	val  V
	err  error
}

func (g *flights[K, V]) do(k K, load func() (V, error)) (V, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = map[K]*flight[V]{}
	}
	if f, ok := g.m[k]; ok {
		g.mu.Unlock()
		<-f.done
		return f.val, f.err
	}
	f := &flight[V]{done: make(chan struct{})}
	g.m[k] = f
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.m, k)
		g.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = load()
	return f.val, f.err
}
