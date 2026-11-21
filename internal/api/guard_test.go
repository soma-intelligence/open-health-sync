// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// guardServer is a server over a pool that never connects. known maps the
// hash of a secret to its caller; any other secret is refused. Audit events
// go to the returned slice instead of the database.
type guardServer struct {
	s      *server
	h      http.Handler
	mu     sync.Mutex
	events []audit.Event
	// lookups and whys count the credential lookups and the reads of why
	// one was refused.
	lookups, whys atomic.Int64
}

func newGuardServer(t *testing.T, known map[string]auth.Principal, tweak ...func(*Deps)) *guardServer {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	g := &guardServer{}
	d := Deps{Pool: pool, Store: tenant.New(pool), Lookup: func(_ context.Context, hash string) (auth.Principal, error) {
		g.lookups.Add(1)
		if p, ok := known[hash]; ok {
			return p, nil
		}
		return auth.Principal{}, auth.ErrUnauthenticated
	}}
	for _, f := range tweak {
		f(&d)
	}
	g.s = newServer(d)
	g.s.sink = func(ev audit.Event) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.events = append(g.events, ev)
	}
	g.s.whyRefused = func(context.Context, string) (tenant.Refused, error) {
		g.whys.Add(1)
		return tenant.Refused{Reason: "unknown"}, nil
	}
	g.h = g.s.handler()
	return g
}

func (g *guardServer) get(path, secret, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	g.h.ServeHTTP(rec, req)
	return rec
}

func (g *guardServer) recorded(action string) []audit.Event {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []audit.Event
	for _, ev := range g.events {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

func newKey(t *testing.T, prefix string) (secret, hash string) {
	t.Helper()
	secret, hash, err := auth.NewSecret(prefix)
	require.NoError(t, err)
	return secret, hash
}

// The route a guard test calls: behind a tenant key, answered 404 before any
// query (the user id is not one).
const guardPath = "/v1/users/not-a-user/samples"

func TestAPIRateLimitPerCredential(t *testing.T) {
	keyA, hashA := newKey(t, auth.PrefixSecretKey)
	keyB, hashB := newKey(t, auth.PrefixSecretKey)
	g := newGuardServer(t, map[string]auth.Principal{
		hashA: {TenantID: uuid.New(), KeyID: uuid.NewString()},
		hashB: {TenantID: uuid.New(), KeyID: uuid.NewString()},
	}, func(d *Deps) { d.RateLimitAPI = RateLimit{Rate: 0.001, Burst: 3} })

	for i := range 3 {
		require.Equal(t, http.StatusNotFound, g.get(guardPath, keyA, "").Code, "request %d within the burst", i)
	}
	rec := g.get(guardPath, keyA, "")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	status, detail := problemOf(t, rec)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Contains(t, detail, "too many requests")
	assert.Equal(t, int64(1), g.s.metrics.rateLimited[groupAPI].Load())

	assert.Equal(t, http.StatusNotFound, g.get(guardPath, keyB, "").Code, "another key has its own bucket")
	// FHIR, Open mHealth and MCP sit behind the same lookup.
	for _, path := range []string{fhirRoot + "/Observation", "/v1/users/" + testUserID + "/omh/data-points", "/mcp"} {
		assert.Equal(t, http.StatusTooManyRequests, g.get(path, keyA, "").Code, path)
	}
}

// The tokens of one user share a bucket: minting more buys no more requests.
func TestAPIRateLimitSharedByAUsersTokens(t *testing.T) {
	user, tenantID := uuid.New(), uuid.New()
	t1, h1 := newKey(t, auth.PrefixUserToken)
	t2, h2 := newKey(t, auth.PrefixUserToken)
	g := newGuardServer(t, map[string]auth.Principal{
		h1: {TenantID: tenantID, UserID: &user, Scopes: []string{"read"}, KeyID: "aaaa"},
		h2: {TenantID: tenantID, UserID: &user, Scopes: []string{"read"}, KeyID: "bbbb"},
	}, func(d *Deps) { d.RateLimitAPI = RateLimit{Rate: 0.001, Burst: 2} })
	assert.NotEqual(t, http.StatusTooManyRequests, g.get("/mcp", t1, "").Code)
	assert.NotEqual(t, http.StatusTooManyRequests, g.get("/mcp", t2, "").Code)
	assert.Equal(t, http.StatusTooManyRequests, g.get("/mcp", t1, "").Code)
	assert.Equal(t, http.StatusTooManyRequests, g.get("/mcp", t2, "").Code)
}

func TestAPIRateLimitDefaultAndOff(t *testing.T) {
	key, hash := newKey(t, auth.PrefixSecretKey)
	known := map[string]auth.Principal{hash: {TenantID: uuid.New(), KeyID: uuid.NewString()}}

	g := newGuardServer(t, known)
	for i := range 500 {
		require.Equal(t, http.StatusNotFound, g.get(guardPath, key, "").Code, "request %d under the default limit", i)
	}

	g = newGuardServer(t, known, func(d *Deps) {
		d.RateLimitAPI = RateLimit{Rate: 0.001, Burst: 1}
		d.RateLimitAPIOff = true
	})
	for i := range 5 {
		require.Equal(t, http.StatusNotFound, g.get(guardPath, key, "").Code, "request %d with the limit off", i)
	}
}

// A refused credential is recorded once per address and minute, with the
// client address and the key prefix, never the secret, and the address is
// throttled once it used up its failures: its next refused credentials
// answer 429, a valid one is served.
func TestFailedAuthIsRecordedAndThrottled(t *testing.T) {
	good, goodHash := newKey(t, auth.PrefixSecretKey)
	g := newGuardServer(t, map[string]auth.Principal{goodHash: {TenantID: uuid.New(), KeyID: uuid.NewString()}},
		func(d *Deps) { d.RateLimitAuthFailures = RateLimit{Rate: 0.01, Burst: 4} })
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	g.s.authFail.now = clk.now
	const attacker, other = "203.0.113.9:4000", "198.51.100.2:5000"

	bad, _ := newKey(t, auth.PrefixSecretKey)
	for i := range 4 {
		rec := g.get(guardPath, bad, attacker)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "failure %d", i)
	}
	events := g.recorded(audit.AuthFailed)
	require.Len(t, events, 1, "one event for the address in a minute")
	ev := events[0]
	assert.Equal(t, uuid.Nil, ev.Tenant, "an unknown credential names no tenant")
	assert.Equal(t, audit.APIKey, ev.ActorType)
	assert.Equal(t, bad[:auth.KeyPrefixLen]+"...", ev.ActorID)
	assert.Equal(t, http.StatusUnauthorized, ev.Status)
	assert.Equal(t, "GET /v1/users/{id}/samples", ev.Route)
	assert.Equal(t, "203.0.113.9", ev.Detail["client_address"])
	assert.Equal(t, "unknown", ev.Detail["reason"])
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), bad, "never the secret")
	assert.NotContains(t, string(raw), bad[auth.KeyPrefixLen+4:], "nor the rest of it")

	// Out of failures: a refused credential answers 429 instead of 401,
	// after its lookup, and leaves nothing in the audit log.
	whys := g.whys.Load()
	rec := g.get(guardPath, bad, attacker)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	status, _ := problemOf(t, rec)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.Equal(t, int64(1), g.s.metrics.rateLimited[groupAuthFailure].Load())
	assert.Equal(t, int64(5), g.s.metrics.authFailures.Load())
	assert.Equal(t, whys, g.whys.Load(), "a throttled failure costs its lookup, nothing more")
	assert.Len(t, g.recorded(audit.AuthFailed), 1)

	// A valid credential from the same address is served: the throttle
	// only ever answers a request that failed.
	assert.Equal(t, http.StatusNotFound, g.get(guardPath, good, attacker).Code)
	assert.Equal(t, int64(1), g.s.metrics.rateLimited[groupAuthFailure].Load())

	// Another address is not affected, nor a request without a credential.
	assert.Equal(t, http.StatusNotFound, g.get(guardPath, good, other).Code)
	assert.Equal(t, http.StatusUnauthorized, g.get(guardPath, bad, other).Code)
	assert.Equal(t, http.StatusUnauthorized, g.get(guardPath, "", attacker).Code)
	assert.Equal(t, http.StatusOK, g.get("/healthz", "", attacker).Code)
	assert.Len(t, g.recorded(audit.AuthFailed), 2, "a request without a credential is not an event")

	// Later the address may try again; its next event says how many
	// failures were not written.
	clk.advance(2 * time.Minute)
	token, tokenHash := newKey(t, auth.PrefixUserToken)
	require.Equal(t, http.StatusUnauthorized, g.get(guardPath, token, attacker).Code)
	events = g.recorded(audit.AuthFailed)
	require.Len(t, events, 3)
	assert.Equal(t, audit.UserToken, events[2].ActorType)
	assert.Equal(t, audit.TokenID(tokenHash), events[2].ActorID)
	assert.Equal(t, 4, events[2].Detail["suppressed"], "three answered 401, one 429")
	assert.NotContains(t, events[2].Detail, "key_prefix")
}

// Every client behind a reverse proxy that is not listed as trusted shares
// one address. Junk credentials from it, however many, never lock the valid
// ones out, and each costs one lookup and nothing else once throttled.
func TestJunkCredentialsDoNotLockOutASharedAddress(t *testing.T) {
	keyA, hashA := newKey(t, auth.PrefixSecretKey)
	token, hashT := newKey(t, auth.PrefixUserToken)
	user := uuid.New()
	g := newGuardServer(t, map[string]auth.Principal{
		hashA: {TenantID: uuid.New(), KeyID: uuid.NewString()},
		hashT: {TenantID: uuid.New(), UserID: &user, Scopes: []string{"read"}, KeyID: "aaaa"},
	})
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	g.s.authFail.now = clk.now
	const proxy = "172.18.0.5:39000"
	junk, _ := newKey(t, auth.PrefixSecretKey)
	for i := range 200 {
		want := http.StatusUnauthorized
		if i >= DefaultRateLimitAuthFailures.Burst {
			want = http.StatusTooManyRequests
		}
		require.Equal(t, want, g.get(guardPath, junk, proxy).Code, "junk request %d", i)
		if i%20 == 0 {
			require.Equal(t, http.StatusNotFound, g.get(guardPath, keyA, proxy).Code, "a tenant's key after %d junk requests", i)
			require.NotEqual(t, http.StatusTooManyRequests, g.get("/mcp", token, proxy).Code, "a user's token after %d junk requests", i)
		}
	}
	assert.Equal(t, int64(200+2*10), g.lookups.Load(), "one lookup per request")
	assert.Equal(t, int64(DefaultRateLimitAuthFailures.Burst), g.whys.Load(), "none for a throttled failure")
	assert.Len(t, g.recorded(audit.AuthFailed), 1)
	assert.Equal(t, int64(200-DefaultRateLimitAuthFailures.Burst), g.s.metrics.rateLimited[groupAuthFailure].Load())
	assert.Equal(t, int64(200), g.s.metrics.authFailures.Load())
}

// With the throttle off no failure answers 429, and failures are still
// recorded within the same bounds.
func TestFailedAuthIsRecordedWithTheThrottleOff(t *testing.T) {
	g := newGuardServer(t, nil, func(d *Deps) {
		d.RateLimitAuthFailures = RateLimit{Rate: 0.001, Burst: 2}
		d.RateLimitAuthFailuresOff = true
	})
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	g.s.authFail.now = clk.now
	bad, _ := newKey(t, auth.PrefixSecretKey)
	for i := range 10 {
		require.Equal(t, http.StatusUnauthorized, g.get(guardPath, bad, "203.0.113.9:4000").Code, "failure %d", i)
	}
	require.Equal(t, http.StatusUnauthorized, g.get(guardPath, bad, "198.51.100.2:5000").Code)
	events := g.recorded(audit.AuthFailed)
	require.Len(t, events, 2, "one event per address and minute")
	assert.Equal(t, int64(11), g.s.metrics.authFailures.Load())
	assert.Zero(t, g.s.metrics.rateLimited[groupAuthFailure].Load())

	clk.advance(authFailWindow)
	require.Equal(t, http.StatusUnauthorized, g.get(guardPath, bad, "203.0.113.9:4000").Code)
	events = g.recorded(audit.AuthFailed)
	require.Len(t, events, 3)
	assert.Equal(t, 9, events[2].Detail["suppressed"])
}

// A revoked or expired credential is recorded on its tenant whatever junk
// came before from the same address, once per credential and minute.
func TestFailedAuthOfAKnownCredentialIsNotSuppressedByJunk(t *testing.T) {
	g := newGuardServer(t, nil)
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	g.s.authFail.now = clk.now
	tenantID, keyID := uuid.New(), uuid.NewString()
	revoked, revokedHash := newKey(t, auth.PrefixSecretKey)
	g.s.whyRefused = func(_ context.Context, hash string) (tenant.Refused, error) {
		if hash == revokedHash {
			return tenant.Refused{Tenant: tenantID, Reason: "revoked", KeyID: keyID}, nil
		}
		return tenant.Refused{Reason: "unknown"}, nil
	}
	const proxy = "172.18.0.5:39000"
	junk, _ := newKey(t, auth.PrefixSecretKey)
	for range 3 {
		require.Equal(t, http.StatusUnauthorized, g.get(guardPath, junk, proxy).Code)
	}
	require.Len(t, g.recorded(audit.AuthFailed), 1)
	for range 3 {
		require.Equal(t, http.StatusUnauthorized, g.get(guardPath, revoked, proxy).Code)
	}
	events := g.recorded(audit.AuthFailed)
	require.Len(t, events, 2, "the revoked key is recorded, once")
	assert.Equal(t, tenantID, events[1].Tenant)
	assert.Equal(t, keyID, events[1].ActorID)
	assert.Equal(t, "revoked", events[1].Detail["reason"])
	assert.NotContains(t, events[1].Detail, "suppressed", "the junk of the address is not this key's")

	clk.advance(authFailWindow)
	require.Equal(t, http.StatusUnauthorized, g.get(guardPath, revoked, proxy).Code)
	events = g.recorded(audit.AuthFailed)
	require.Len(t, events, 3)
	assert.Equal(t, 2, events[2].Detail["suppressed"])
}

// A revoked key names its tenant: the failure lands on that tenant's log.
func TestFailedAuthNamesTheTenantOfARevokedKey(t *testing.T) {
	g := newGuardServer(t, nil)
	tenantID, keyID := uuid.New(), uuid.NewString()
	g.s.whyRefused = func(context.Context, string) (tenant.Refused, error) {
		return tenant.Refused{Tenant: tenantID, Reason: "revoked", KeyID: keyID}, nil
	}
	bad, _ := newKey(t, auth.PrefixSecretKey)
	require.Equal(t, http.StatusUnauthorized, g.get(guardPath, bad, "").Code)
	events := g.recorded(audit.AuthFailed)
	require.Len(t, events, 1)
	assert.Equal(t, tenantID, events[0].Tenant)
	assert.Equal(t, keyID, events[0].ActorID)
	assert.Equal(t, "revoked", events[0].Detail["reason"])
}

func TestAuthFailuresBoundTheEvents(t *testing.T) {
	a := newAuthFailures(RateLimit{Rate: 1000, Burst: 1000})
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	a.now = clk.now

	// One address: one event a minute, the rest counted.
	reports := 0
	for range 50 {
		if ok, _ := a.report("192.0.2.1", ""); ok {
			reports++
		}
	}
	assert.Equal(t, 1, reports)
	clk.advance(authFailWindow)
	ok, suppressed := a.report("192.0.2.1", "")
	assert.True(t, ok)
	assert.Equal(t, 49, suppressed)

	// Many addresses presenting unknown credentials: the process writes at
	// most authFailEventsPerWindow less the reserve in a window.
	clk.advance(time.Hour)
	reports = 0
	for i := range 5 * authFailEventsPerWindow {
		addr := "198.51.100." + strconv.Itoa(i)
		a.fail(addr)
		if ok, _ := a.report(addr, ""); ok {
			reports++
		}
	}
	assert.Equal(t, authFailEventsPerWindow-authFailKnownReserve, reports)
	// The reserve is for the failures of known credentials: one event per
	// credential and window, up to the whole budget.
	reports = 0
	for i := range 2 * authFailKnownReserve {
		for range 2 {
			if ok, _ := a.report("203.0.113.7", "hash-"+strconv.Itoa(i)); ok {
				reports++
			}
		}
	}
	assert.Equal(t, authFailKnownReserve, reports)
	ok, _ = a.report("203.0.113.8", "")
	assert.False(t, ok, "nothing left in the window")
	clk.advance(authFailWindow)
	ok, suppressed = a.report("203.0.113.7", "hash-0")
	assert.True(t, ok)
	assert.Equal(t, 1, suppressed)
	// With nothing unknown in the window, known credentials may use it all.
	clk.advance(authFailWindow)
	reports = 0
	for i := range 2 * authFailEventsPerWindow {
		if ok, _ := a.report("203.0.113.7", "other-"+strconv.Itoa(i)); ok {
			reports++
		}
	}
	assert.Equal(t, authFailEventsPerWindow, reports)
}

// Past the cap on tracked addresses a new address is counted, never
// throttled: its failures cost a tracked address nothing, and an attacker
// who fills the table gets no one refused.
func TestAuthFailuresOverflowNeverRefuses(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	a := newAuthFailures(RateLimit{Rate: 0.001, Burst: 2})
	a.now, a.maxAddrs = clk.now, 2
	for _, addr := range []string{"a", "a", "b"} {
		_, limited := a.fail(addr)
		assert.False(t, limited, addr)
	}
	wait, limited := a.fail("a")
	assert.True(t, limited, "a tracked address out of failures")
	assert.Positive(t, wait)
	for i := range 50 {
		_, limited := a.fail("untracked-" + strconv.Itoa(i%5))
		require.False(t, limited, "failure %d of an address past the cap", i)
	}
	assert.Len(t, a.addrs, 2)
	// Their events share one entry: one a minute, the rest counted.
	ok, _ := a.report("untracked-0", "")
	assert.True(t, ok)
	ok, _ = a.report("untracked-1", "")
	assert.False(t, ok)
	clk.advance(authFailWindow)
	ok, suppressed := a.report("untracked-2", "")
	assert.True(t, ok)
	assert.Equal(t, 1, suppressed)

	// With the throttle off nothing is refused, and events are bounded the
	// same way.
	a = newAuthFailures(RateLimit{})
	a.now = clk.now
	for range 500 {
		_, limited := a.fail("a")
		require.False(t, limited)
	}
	ok, _ = a.report("a", "")
	assert.True(t, ok)
	ok, _ = a.report("a", "")
	assert.False(t, ok)
	// An idle address is forgotten.
	clk.advance(idleEvict + sweepEvery)
	a.fail("b")
	assert.NotContains(t, a.addrs, "a")
}

// execRecorder is an audit.Execer that keeps the statements it was sent.
// With block set, a statement waits until it is closed.
type execRecorder struct {
	mu    sync.Mutex
	rows  int
	block chan struct{}
	// entered, when set, receives once per Exec before it blocks.
	entered chan struct{}
}

func (e *execRecorder) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	if e.entered != nil {
		e.entered <- struct{}{}
	}
	if e.block != nil {
		<-e.block
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rows += len(args) / 11
	return pgconn.CommandTag{}, nil
}

// With a buffered writer, reads are queued and sensitive actions are still
// written before the request ends.
func TestAuditBufferedAndSynchronousEvents(t *testing.T) {
	db := &execRecorder{}
	w := audit.NewWriter(db, audit.WriterOptions{Interval: time.Hour})
	g := newGuardServer(t, nil, func(d *Deps) { d.Audit = w })
	g.s.sink = nil
	req := httptest.NewRequest("GET", "/", nil)

	g.s.record(req, audit.Event{ActorType: audit.APIKey, Action: audit.DataRead, Status: 200})
	assert.Equal(t, int64(0), g.s.metrics.auditFailures.Load(), "a read does not touch the database in the request")

	// The pool never connects: a synchronous write fails at once and is
	// counted, which shows it was attempted in the request.
	for i, action := range []string{audit.KeyCreate, audit.KeyRevoke, audit.TokenCreate, audit.TokenRevoke,
		audit.UserDelete, audit.DataExport, audit.ConsentWrite, audit.ShareCreate} {
		g.s.record(req, audit.Event{ActorType: audit.APIKey, Action: action, Status: 200})
		assert.Equal(t, int64(i+1), g.s.metrics.auditFailures.Load(), action)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, w.Close(ctx))
	assert.Equal(t, 1, db.rows, "only the read went through the writer")

	// Once the writer is closed, a read is written in the request too.
	before := g.s.metrics.auditFailures.Load()
	g.s.record(req, audit.Event{ActorType: audit.APIKey, Action: audit.DataRead, Status: 200})
	assert.Equal(t, before+1, g.s.metrics.auditFailures.Load())
}

// A read whose event finds the writer's queue full is not lost: it is
// written in the request instead.
func TestAuditQueueFullFallsBackToASynchronousWrite(t *testing.T) {
	db := &execRecorder{block: make(chan struct{}), entered: make(chan struct{}, 4)}
	w := audit.NewWriter(db, audit.WriterOptions{Queue: 1, Batch: 1, Interval: time.Hour})
	g := newGuardServer(t, nil, func(d *Deps) { d.Audit = w })
	g.s.sink = nil
	req := httptest.NewRequest("GET", "/", nil)
	read := audit.Event{ActorType: audit.APIKey, Action: audit.DataRead, Status: 200}

	// The loop takes the first event and waits in the database; the second
	// fills the queue. Wait for the loop to hold the first one, or the second
	// can find the queue still full and go to the synchronous write.
	g.s.record(req, read)
	select {
	case <-db.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never took the first event")
	}
	require.Eventually(t, func() bool {
		g.s.record(req, read)
		return g.s.metrics.auditFailures.Load() > 0
	}, 5*time.Second, time.Millisecond, "an event over the queue is written in the request")
	// The pool never connects, so the synchronous write fails and is
	// counted, which shows it was attempted. Nothing was dropped silently.
	assert.Zero(t, w.Dropped())

	close(db.block)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, w.Close(ctx))
	assert.Equal(t, 2, db.rows, "the queued events were written by the writer")
}

// Every response behind a credential says no-store; public routes do not.
func TestNoStoreOnAuthenticatedResponses(t *testing.T) {
	key, hash := newKey(t, auth.PrefixSecretKey)
	g := newGuardServer(t, map[string]auth.Principal{hash: {TenantID: uuid.New(), KeyID: uuid.NewString()}})
	for _, path := range []string{guardPath, fhirRoot + "/Observation?limit=5000", "/v1/users/" + testUserID + "/omh/data-points?limit=0",
		"/v1/users", "/v1/tenant"} {
		assert.Equal(t, "no-store", g.get(path, key, "").Header().Get("Cache-Control"), path)
	}
	assert.Equal(t, "no-store", g.get(guardPath, "", "").Header().Get("Cache-Control"), "a 401 too")
	for _, path := range []string{"/openapi.yaml", "/healthz"} {
		assert.Empty(t, g.get(path, "", "").Header().Get("Cache-Control"), path)
	}
}

func TestSchemaCheck(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	binary := func() (uint, error) { return 48, nil }
	calls := 0
	at := func(v uint, dirty bool, err error) func(context.Context) (uint, bool, error) {
		return func(context.Context) (uint, bool, error) {
			calls++
			return v, dirty, err
		}
	}
	ctx := context.Background()
	detail := func(c *schemaCheck, now time.Time, database func(context.Context) (uint, bool, error)) string {
		d, _ := c.check(ctx, now, binary, database)
		return d
	}

	var c schemaCheck
	assert.Empty(t, detail(&c, now, at(48, false, nil)), "same version: ready")
	assert.Empty(t, detail(&c, now.Add(schemaCheckTTL-time.Second), at(47, false, nil)), "kept for the TTL")
	assert.Equal(t, 1, calls, "one query within the TTL")
	got, changed := c.check(ctx, now.Add(schemaCheckTTL), binary, at(47, false, nil))
	assert.Equal(t, "the database is at schema version 47, this binary needs version 48", got)
	assert.True(t, changed, "the first probe that sees it")
	// The same state is told once, however many probes see it.
	for i := 1; i <= 5; i++ {
		got, changed = c.check(ctx, now.Add(time.Duration(i+1)*schemaCheckTTL), binary, at(47, false, nil))
		assert.NotEmpty(t, got)
		assert.False(t, changed, "probe %d of the same state", i)
	}
	got, changed = c.check(ctx, now.Add(10*schemaCheckTTL), binary, at(47, true, nil))
	assert.Contains(t, got, "dirty")
	assert.True(t, changed, "another reason")
	got, changed = c.check(ctx, now.Add(20*schemaCheckTTL), binary, at(48, false, nil))
	assert.Empty(t, got)
	assert.True(t, changed, "ready again")
	_, changed = c.check(ctx, now.Add(30*schemaCheckTTL), binary, at(48, false, nil))
	assert.False(t, changed)

	c = schemaCheck{}
	got, changed = c.check(ctx, now, binary, at(49, false, nil))
	assert.Empty(t, got, "a database ahead of the binary stays ready")
	assert.False(t, changed, "ready from the start is not news")
	c = schemaCheck{}
	assert.Contains(t, detail(&c, now, at(48, true, nil)), "dirty")
	c = schemaCheck{}
	assert.Contains(t, detail(&c, now, at(0, false, nil)), "version 0")

	// A failed read is not kept: the next probe asks again.
	c = schemaCheck{}
	calls = 0
	assert.Contains(t, detail(&c, now, at(0, false, errors.New("down"))), "could not be read")
	assert.Empty(t, detail(&c, now, at(48, false, nil)))
	assert.Equal(t, 2, calls)
}

// The body of /readyz, which anyone may read, does not say which schema
// version the database or the binary is at, nor that the schema is dirty.
func TestReadyzBodyIsGeneric(t *testing.T) {
	assert.Equal(t, "database schema is not at the version this binary needs", schemaNotReady)
	assert.NotRegexp(t, `\d|dirty`, schemaNotReady)
}

func TestRenderMetricsGuards(t *testing.T) {
	m := newHTTPMetrics()
	m.rateLimited[groupAPI].Add(2)
	m.authFailures.Add(3)
	m.auditDropped = func() int64 { return 4 }
	var b strings.Builder
	renderMetrics(&b, m, nil)
	out := b.String()
	assert.Contains(t, out, `ohs_rate_limited_total{route_group="api"} 2`)
	assert.Contains(t, out, `ohs_rate_limited_total{route_group="auth_failure"} 0`)
	assert.Contains(t, out, "ohs_auth_failures_total 3\n")
	assert.Contains(t, out, "ohs_audit_dropped_total 4\n")
}
