// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeKeys records that the cache zeroed it.
type fakeKeys struct {
	name   string
	zeroed bool
}

func (f *fakeKeys) Zero() { f.zeroed = true }

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestCache(max int, ttl time.Duration) (*cache[string, *fakeKeys], *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	return newCache[string, *fakeKeys](max, ttl, clk.now), clk
}

func cached(c *cache[string, *fakeKeys], k string) (name string, ok bool) {
	ok = c.get(k, func(f *fakeKeys) { name = f.name })
	return name, ok
}

func TestCacheHitAndMiss(t *testing.T) {
	c, _ := newTestCache(4, time.Minute)
	_, ok := cached(c, "a")
	require.False(t, ok)
	a := &fakeKeys{name: "a"}
	c.put("a", a, c.epoch())
	name, ok := cached(c, "a")
	require.True(t, ok)
	require.Equal(t, "a", name)
	require.False(t, a.zeroed)
}

func TestCacheTTLExpiryZeroes(t *testing.T) {
	c, clk := newTestCache(4, time.Minute)
	a := &fakeKeys{name: "a"}
	c.put("a", a, c.epoch())
	clk.t = clk.t.Add(59 * time.Second)
	_, ok := cached(c, "a")
	require.True(t, ok, "a use does not extend the time to live, but it is not over yet")
	clk.t = clk.t.Add(time.Second)
	_, ok = cached(c, "a")
	require.False(t, ok, "expired")
	require.True(t, a.zeroed, "an expired entry is zeroed")
	require.Equal(t, 0, c.len())
}

func TestCacheSweepZeroesExpired(t *testing.T) {
	c, clk := newTestCache(4, time.Minute)
	a, b := &fakeKeys{name: "a"}, &fakeKeys{name: "b"}
	c.put("a", a, c.epoch())
	clk.t = clk.t.Add(30 * time.Second)
	c.put("b", b, c.epoch())
	clk.t = clk.t.Add(45 * time.Second)
	c.sweep()
	require.True(t, a.zeroed)
	require.False(t, b.zeroed)
	require.Equal(t, 1, c.len())
}

func TestCacheLRUEvictionZeroes(t *testing.T) {
	c, _ := newTestCache(2, time.Minute)
	a, b, d := &fakeKeys{name: "a"}, &fakeKeys{name: "b"}, &fakeKeys{name: "d"}
	c.put("a", a, c.epoch())
	c.put("b", b, c.epoch())
	_, _ = cached(c, "a") // b is now the least recently used
	c.put("d", d, c.epoch())
	require.True(t, b.zeroed, "the least recently used entry is evicted and zeroed")
	require.False(t, a.zeroed)
	require.False(t, d.zeroed)
	_, ok := cached(c, "b")
	require.False(t, ok)
	require.Equal(t, 2, c.len())
}

func TestCacheReplaceZeroesOld(t *testing.T) {
	c, _ := newTestCache(2, time.Minute)
	a1, a2 := &fakeKeys{name: "a1"}, &fakeKeys{name: "a2"}
	c.put("a", a1, c.epoch())
	c.put("a", a2, c.epoch())
	require.True(t, a1.zeroed)
	name, _ := cached(c, "a")
	require.Equal(t, "a2", name)
}

func TestCacheRemoveAndPurgeZero(t *testing.T) {
	c, _ := newTestCache(4, time.Minute)
	a, b := &fakeKeys{name: "a"}, &fakeKeys{name: "b"}
	c.put("a", a, c.epoch())
	c.put("b", b, c.epoch())
	c.remove("a")
	require.True(t, a.zeroed)
	c.remove("missing")
	c.purge()
	require.True(t, b.zeroed)
	require.Equal(t, 0, c.len())
}

// An entry loaded before an eviction is not cached after it: it may hold the
// keys a shred just deleted.
func TestCachePutAfterEvictionIsDropped(t *testing.T) {
	c, _ := newTestCache(4, time.Minute)
	e := c.epoch()
	c.remove("a")
	a := &fakeKeys{name: "a"}
	c.put("a", a, e)
	require.True(t, a.zeroed, "a stale load is zeroed, not cached")
	_, ok := cached(c, "a")
	require.False(t, ok)
}

func TestCacheUsersForLowMemory(t *testing.T) {
	require.Equal(t, 1024, cacheUsersFor(1024, false))
	require.Equal(t, 128, cacheUsersFor(1024, true), "the default shrinks on the low-memory profile")
	require.Equal(t, 300, cacheUsersFor(300, true), "a size the operator set stays")
	require.Equal(t, 1024, cacheUsersFor(0, false))
	require.Equal(t, 128, cacheUsersFor(0, true))
}

func TestMetricsRender(t *testing.T) {
	m := NewMetrics()
	m.seal(true)
	m.open(false)
	m.openFailure(failRow)
	m.cacheResult(true)
	m.providerTime(30 * time.Millisecond)
	var b bytes.Buffer
	m.Write(&b)
	out := b.String()
	for _, want := range []string{
		`ohs_vault_seals_total{result="ok"} 1`,
		`ohs_vault_seals_total{result="error"} 0`,
		`ohs_vault_opens_total{result="error"} 1`,
		`ohs_vault_open_failures_total{kind="row"} 1`,
		`ohs_vault_open_failures_total{kind="key"} 0`,
		`ohs_vault_cache_total{result="hit"} 1`,
		`ohs_vault_cache_total{result="miss"} 0`,
		"# TYPE ohs_vault_provider_seconds histogram",
		`ohs_vault_provider_seconds_bucket{le="0.025"} 0`,
		`ohs_vault_provider_seconds_bucket{le="0.05"} 1`,
		`ohs_vault_provider_seconds_bucket{le="+Inf"} 1`,
		"ohs_vault_provider_seconds_count 1",
	} {
		require.Contains(t, out, want)
	}
	require.True(t, strings.HasSuffix(out, "\n"))
}
