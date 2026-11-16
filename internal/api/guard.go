// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// DefaultRateLimitAPI is the limit of one credential on the authenticated
// routes (/v1, FHIR, Open mHealth, MCP) when Deps.RateLimitAPI is zero. It
// is far above what a backend or a device sends: it stops a runaway loop,
// not a busy client.
var DefaultRateLimitAPI = RateLimit{Rate: 200, Burst: 2000}

// DefaultRateLimitAuthFailures is how many refused credentials (401) one
// client address may present when Deps.RateLimitAuthFailures is zero: 60 at
// once, then one a second. Past it, its refused credentials answer 429.
var DefaultRateLimitAuthFailures = RateLimit{Rate: 1, Burst: 60}

// Bounds on the audit events of failed authentication. An unknown
// credential: one event per client address and authFailWindow. A known one
// (a revoked or expired key or token, which names its tenant): one event
// per credential and window, whatever else its address presented. The
// process writes authFailEventsPerWindow events a window at most, of which
// authFailKnownReserve are kept for known credentials, so guessing cannot
// crowd out the record of a retired key that is still used. The failures
// past the bounds are counted (ohs_auth_failures_total, and "suppressed" on
// the next event of the address or credential), never written one by one.
const (
	authFailWindow          = time.Minute
	authFailEventsPerWindow = 600
	authFailKnownReserve    = 150
	// maxTrackedCredentials bounds the known credentials whose events are
	// held to one a window; past it a known credential is bounded by the
	// budget of the process alone.
	maxTrackedCredentials = 10_000
)

// limitKey names the bucket of an authenticated caller: an API key by its
// id, a user token by its user, so minting more tokens for one user buys no
// more requests.
func limitKey(p auth.Principal) string {
	if p.UserID != nil {
		return "u:" + p.TenantID.String() + ":" + p.UserID.String()
	}
	return "k:" + p.KeyID
}

// limitedLookup is lookup followed by the rate limit of the credential it
// found: every authenticated route goes through it, whatever its middleware.
// Over the limit it returns auth.LimitedError, which the middleware answers
// with 429 and Retry-After before the handler does any work.
func (s *server) limitedLookup(lookup auth.Lookup, live *liveLimiter) auth.Lookup {
	return func(ctx context.Context, hash string) (auth.Principal, error) {
		p, err := lookup(ctx, hash)
		if err != nil {
			return p, err
		}
		l := live.current()
		if !l.perAddr.on() {
			return p, nil
		}
		if ok, wait, _ := l.allow(limitKey(p), ""); !ok {
			s.metrics.rateLimited[groupAPI].Add(1)
			slog.DebugContext(ctx, "api: rate limited", "route_group", groupAPI)
			return auth.Principal{}, &auth.LimitedError{RetryAfter: wait}
		}
		return p, nil
	}
}

// authFailures throttles the refused credentials of one client address and
// bounds the audit events they leave. It only ever hears about a credential
// the lookup refused: a valid one is never counted, so never throttled.
type authFailures struct {
	// limit is the failures of one address. The zero value throttles
	// nothing; the events are bounded all the same.
	limit RateLimit
	// get, when set, is the limit read live: on a change limit follows it
	// and every address starts with a full bucket.
	get      func() RateLimit
	now      func() time.Time
	maxAddrs int

	mu    sync.Mutex
	addrs map[string]*authFailEntry
	// overflow stands for every address past maxAddrs. It only counts
	// events: an address that is not tracked is never throttled, or filling
	// the table would get everyone else refused.
	overflow authFailEntry
	// known holds the revoked and expired credentials seen lately, by hash.
	known     map[string]*authFailEntry
	lastSweep time.Time
	// window, events and unknown bound the events of the whole process:
	// events counts all of them, unknown those of unknown credentials.
	window          time.Time
	events, unknown int
}

type authFailEntry struct {
	b bucket
	// last is the last failure of the address or credential.
	last time.Time
	// reported is when its last event was written; suppressed counts its
	// failures since.
	reported   time.Time
	suppressed int
}

func newAuthFailures(l RateLimit) *authFailures {
	return &authFailures{limit: l, now: time.Now, maxAddrs: maxTrackedAddrs,
		addrs: map[string]*authFailEntry{}, known: map[string]*authFailEntry{}}
}

// newLiveAuthFailures is newAuthFailures with a limit that follows get.
func newLiveAuthFailures(get func() RateLimit) *authFailures {
	a := newAuthFailures(get())
	a.get = get
	return a
}

// follow adopts the live limit. When it changed the buckets restart full;
// the bookkeeping of the audit events stays. The caller holds a.mu.
func (a *authFailures) follow(now time.Time) {
	if a.get == nil {
		return
	}
	l := a.get()
	if l == a.limit {
		return
	}
	a.limit = l
	for _, e := range a.addrs {
		e.b = *fullBucket(now, l)
	}
}

// fail counts one refused credential of addr. limited is true when the
// address had used up its failures: the request then answers 429 after
// wait instead of 401, and leaves no event.
func (a *authFailures) fail(addr string) (wait time.Duration, limited bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.follow(now)
	if now.Sub(a.lastSweep) >= sweepEvery {
		a.sweep(now)
	}
	e := a.addrs[addr]
	if e == nil {
		if len(a.addrs) >= a.maxAddrs {
			return 0, false
		}
		e = &authFailEntry{b: *fullBucket(now, a.limit)}
		a.addrs[addr] = e
	}
	e.last = now
	if !a.limit.on() {
		return 0, false
	}
	e.b.refill(now, a.limit)
	if e.b.tokens < 1 {
		e.suppressed++
		return e.b.wait(a.limit), true
	}
	e.b.tokens--
	return 0, false
}

// report decides whether a failure that answered 401 is written to the
// audit log. credential is the hash of a known credential (revoked or
// expired), empty for an unknown one. suppressed is then how many failures
// of the address, or of the known credential, were not written since its
// last event.
func (a *authFailures) report(addr, credential string) (report bool, suppressed int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if now.Sub(a.window) >= authFailWindow {
		a.window, a.events, a.unknown = now, 0, 0
	}
	var e *authFailEntry
	room := a.events < authFailEventsPerWindow
	if credential == "" {
		if e = a.addrs[addr]; e == nil {
			e = &a.overflow
		}
		room = room && a.unknown < authFailEventsPerWindow-authFailKnownReserve
	} else if e = a.known[credential]; e == nil {
		// Past the cap the credential is not remembered: each of its
		// failures is an event while the process has room.
		e = &authFailEntry{}
		if len(a.known) < maxTrackedCredentials {
			a.known[credential] = e
		}
	}
	e.last = now
	if !room || (!e.reported.IsZero() && now.Sub(e.reported) < authFailWindow) {
		e.suppressed++
		return false, 0
	}
	a.events++
	if credential == "" {
		a.unknown++
	}
	suppressed, e.reported, e.suppressed = e.suppressed, now, 0
	return true, suppressed
}

// sweep drops the addresses whose bucket is full again and whose last
// failure is idleEvict old, and the known credentials not seen for as long.
func (a *authFailures) sweep(now time.Time) {
	a.lastSweep = now
	for k, e := range a.addrs {
		if now.Sub(e.last) >= idleEvict && (!a.limit.on() || e.b.level(now, a.limit) >= float64(a.limit.Burst)) {
			delete(a.addrs, k)
		}
	}
	for k, e := range a.known {
		if now.Sub(e.last) >= idleEvict {
			delete(a.known, k)
		}
	}
}

// refused is asked by the auth middleware about each credential the lookup
// refused (auth.Slot.OnFailure): it counts the failure against the client
// address and says whether that address is out of failures, in which case
// the request answers 429 instead of 401. A request whose credential is
// valid never comes here, so one client guessing behind an address others
// share (a NAT, a reverse proxy not listed in OHS_TRUSTED_PROXIES) cannot
// get their requests refused.
func (s *server) refused(r *http.Request) (wait time.Duration, limited bool) {
	s.metrics.authFailures.Add(1)
	if wait, limited = s.authFail.fail(clientAddress(r, s.d.TrustedProxies)); limited {
		s.metrics.rateLimited[groupAuthFailure].Add(1)
		slog.DebugContext(r.Context(), "api: rate limited", "route_group", groupAuthFailure)
	}
	return wait, limited
}

// authFailed records a refused credential that answered 401 in the audit
// log, within the bounds of authFailures: on the tenant of a revoked or
// expired credential, on the nil tenant id for an unknown one. The event
// names the client address, the reason and the key prefix or token id,
// never the secret. Telling a known credential from an unknown one is one
// more indexed read (whyRefused), spent only on a failure that was not
// throttled.
func (s *server) authFailed(r *http.Request, f auth.Failure) {
	addr := clientAddress(r, s.d.TrustedProxies)
	why := tenant.Refused{Reason: "unknown"}
	if s.whyRefused != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
		w, err := s.whyRefused(ctx, f.Hash)
		cancel()
		if err != nil {
			slog.DebugContext(r.Context(), "api: refused credential not resolved", "error", err)
		} else {
			why = w
		}
	}
	credential := ""
	if why.Reason != "unknown" {
		credential = f.Hash
	}
	report, suppressed := s.authFail.report(addr, credential)
	if !report {
		return
	}
	ev := audit.Event{
		Tenant:    why.Tenant,
		ActorType: audit.APIKey,
		ActorID:   f.Prefix + "...",
		Action:    audit.AuthFailed,
		Method:    r.Method,
		Route:     r.Pattern,
		Status:    http.StatusUnauthorized,
		RequestID: reqid.From(r.Context()),
		Detail:    map[string]any{"client_address": addr, "reason": why.Reason},
	}
	if f.UserToken {
		ev.ActorType, ev.ActorID = audit.UserToken, audit.TokenID(f.Hash)
	} else {
		ev.Detail["key_prefix"] = f.Prefix + "..."
	}
	if why.KeyID != "" {
		ev.ActorID, ev.Detail["key_id"] = why.KeyID, why.KeyID
	}
	if suppressed > 0 {
		ev.Detail["suppressed"] = suppressed
	}
	s.record(r, ev)
}
