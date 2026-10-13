// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimit is a token bucket: Rate requests per second sustained, Burst at
// once. The zero value is no limit.
type RateLimit struct {
	Rate  float64
	Burst int
}

func (l RateLimit) on() bool { return l.Rate > 0 && l.Burst > 0 }

// The route groups of the rate limiter, the label of ohs_rate_limited_total.
const (
	groupInbound       = "inbound"
	groupOAuthCallback = "oauth_callback"
)

// Limiter bookkeeping: buckets full and idle for idleEvict go at the next
// sweep, at most every sweepEvery; beyond maxTrackedAddrs, new addresses
// share one overflow bucket, so a flood of spoofed addresses costs no
// memory.
const (
	sweepEvery      = time.Minute
	idleEvict       = 10 * time.Minute
	maxTrackedAddrs = 100_000
)

// Why a request was refused (debug log only).
const (
	scopeAddress  = "address"
	scopeOverflow = "overflow"
	scopeProvider = "provider"
)

type bucket struct {
	tokens float64
	last   time.Time
}

func fullBucket(now time.Time, l RateLimit) *bucket {
	return &bucket{tokens: float64(l.Burst), last: now}
}

// level is the tokens the bucket holds at now, up to the burst.
func (b *bucket) level(now time.Time, l RateLimit) float64 {
	t := b.tokens
	if el := now.Sub(b.last).Seconds(); el > 0 {
		t += el * l.Rate
	}
	return math.Min(t, float64(l.Burst))
}

func (b *bucket) refill(now time.Time, l RateLimit) {
	b.tokens = b.level(now, l)
	if now.After(b.last) {
		b.last = now
	}
}

// wait is how long until the bucket holds one token.
func (b *bucket) wait(l RateLimit) time.Duration {
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - b.tokens) / l.Rate * float64(time.Second))
}

// rateLimiter holds one bucket per client address, and, when global is
// set, one per key (the provider) shared by every address. A request takes a
// token from both or from neither.
type rateLimiter struct {
	perAddr, global RateLimit
	now             func() time.Time
	maxAddrs        int

	mu        sync.Mutex
	addrs     map[string]*bucket
	overflow  *bucket
	globals   map[string]*bucket
	lastSweep time.Time
}

func newRateLimiter(perAddr, global RateLimit) *rateLimiter {
	return &rateLimiter{
		perAddr: perAddr, global: global, now: time.Now, maxAddrs: maxTrackedAddrs,
		addrs: map[string]*bucket{}, globals: map[string]*bucket{},
	}
}

// allow takes a token for addr and key. When refused, it says how long until
// a retry can pass and which bucket ran dry.
func (l *rateLimiter) allow(addr, key string) (ok bool, wait time.Duration, scope string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(now)
	}
	var a, g *bucket
	if l.perAddr.on() {
		a, scope = l.addrs[addr], scopeAddress
		if a == nil {
			if len(l.addrs) < l.maxAddrs {
				a = fullBucket(now, l.perAddr)
				l.addrs[addr] = a
			} else {
				if l.overflow == nil {
					l.overflow = fullBucket(now, l.perAddr)
				}
				a, scope = l.overflow, scopeOverflow
			}
		}
		a.refill(now, l.perAddr)
		if a.tokens < 1 {
			return false, a.wait(l.perAddr), scope
		}
	}
	if l.global.on() {
		// Keys are the providers enabled here plus one for the rest, a
		// bounded set.
		g = l.globals[key]
		if g == nil {
			g = fullBucket(now, l.global)
			l.globals[key] = g
		}
		g.refill(now, l.global)
		if g.tokens < 1 {
			return false, g.wait(l.global), scopeProvider
		}
	}
	if a != nil {
		a.tokens--
	}
	if g != nil {
		g.tokens--
	}
	return true, 0, ""
}

// sweep drops the address buckets that are full and were last used
// idleEvict ago or more: a fresh bucket in their place is the same.
func (l *rateLimiter) sweep(now time.Time) {
	l.lastSweep = now
	for k, b := range l.addrs {
		if now.Sub(b.last) >= idleEvict && b.level(now, l.perAddr) >= float64(l.perAddr.Burst) {
			delete(l.addrs, k)
		}
	}
}

func (l *rateLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.addrs)
}

// retryAfter is the Retry-After value for wait: whole seconds, rounded up,
// at least 1.
func retryAfter(wait time.Duration) string {
	s := int64(math.Ceil(wait.Seconds()))
	return strconv.FormatInt(max(s, 1), 10)
}

// rateLimit refuses a request over l with 429 and Retry-After before next
// does any work. key names the shared bucket of the request (the provider)
// for a limiter with a global limit. It logs at debug level, without the
// client address or the query string: callback URLs carry codes and states.
func (s *server) rateLimit(group string, l *rateLimiter, key func(*http.Request) string, next http.Handler) http.Handler {
	if !l.perAddr.on() && !l.global.on() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := ""
		if key != nil {
			k = key(r)
		}
		ok, wait, scope := l.allow(clientAddress(r, s.d.TrustedProxies), k)
		if !ok {
			s.metrics.rateLimited[group].Add(1)
			slog.DebugContext(r.Context(), "api: rate limited", "route_group", group, "route", r.Pattern, "scope", scope)
			w.Header().Set("Retry-After", retryAfter(wait))
			problem(w, http.StatusTooManyRequests, "too many requests, retry later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientAddress is the address a request is limited by: the direct peer,
// or, when the peer is one of the trusted proxies, the rightmost address of
// X-Forwarded-For that is not itself a trusted proxy. Entries further left
// were written by the client and are never read. An IPv6 client is counted
// by its /64, the block one host usually holds.
func clientAddress(r *http.Request, trusted []netip.Prefix) string {
	peer, ok := parseHop(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	client := peer
	if isTrusted(peer, trusted) {
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, ok := parseHop(hops[i])
			if !ok {
				break
			}
			client = a
			if !isTrusted(a, trusted) {
				break
			}
		}
	}
	if client.Is6() {
		return netip.PrefixFrom(client, 64).Masked().String()
	}
	return client.String()
}

// parseHop reads an address with or without a port.
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
