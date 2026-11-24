// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
)

func TestNewSecretFormat(t *testing.T) {
	sk := regexp.MustCompile(`^ohs_sk_[0-9A-Za-z]{43}$`)
	ut := regexp.MustCompile(`^ohs_ut_[0-9A-Za-z]{43}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		full, hash, err := NewSecret("ohs_sk_")
		require.NoError(t, err)
		require.Regexp(t, sk, full)
		require.Equal(t, Hash(full), hash)
		require.Regexp(t, `^[0-9a-f]{64}$`, hash)
		require.False(t, seen[full])
		seen[full] = true
		full, _, err = NewSecret("ohs_ut_")
		require.NoError(t, err)
		require.Regexp(t, ut, full)
	}
}

func TestNewSecretRejectsPrefix(t *testing.T) {
	_, _, err := NewSecret("sk_")
	require.Error(t, err)
}

func principalFor(tenant uuid.UUID, user *uuid.UUID, scopes ...string) Principal {
	return Principal{TenantID: tenant, UserID: user, Scopes: scopes}
}

// lookupMap resolves by the hash of a known token.
func lookupMap(m map[string]Principal) Lookup {
	return func(_ context.Context, hash string) (Principal, error) {
		if p, ok := m[hash]; ok {
			return p, nil
		}
		return Principal{}, ErrUnauthenticated
	}
}

func do(t *testing.T, h http.Handler, authz string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func requireProblem(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	require.Equal(t, status, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, float64(status), body["status"])
	require.NotEmpty(t, body["type"])
	require.NotEmpty(t, body["title"])
	require.NotEmpty(t, body["detail"])
}

func TestMiddleware(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	key, _, _ := NewSecret("ohs_sk_")
	upTok, _, _ := NewSecret("ohs_ut_")
	rdTok, _, _ := NewSecret("ohs_ut_")
	lookup := lookupMap(map[string]Principal{
		Hash(key):   principalFor(tenant, nil),
		Hash(upTok): principalFor(tenant, &user, "upload", "connect"),
		Hash(rdTok): principalFor(tenant, &user, "read"),
	})

	var got Principal
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = From(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("no header", func(t *testing.T) {
		requireProblem(t, do(t, Middleware(lookup, TenantKey)(next), ""), 401)
	})
	t.Run("not bearer", func(t *testing.T) {
		requireProblem(t, do(t, Middleware(lookup, TenantKey)(next), "Basic abc"), 401)
	})
	t.Run("wrong prefix", func(t *testing.T) {
		requireProblem(t, do(t, Middleware(lookup, TenantKey)(next), "Bearer sk_live_abc"), 401)
	})
	t.Run("unknown token", func(t *testing.T) {
		other, _, _ := NewSecret("ohs_sk_")
		requireProblem(t, do(t, Middleware(lookup, TenantKey)(next), "Bearer "+other), 401)
	})
	t.Run("lookup failure is 500 without detail leak", func(t *testing.T) {
		boom := func(context.Context, string) (Principal, error) {
			return Principal{}, errors.New("db down: secret-dsn")
		}
		rec := do(t, Middleware(boom, TenantKey)(next), "Bearer "+key)
		requireProblem(t, rec, 500)
		require.NotContains(t, rec.Body.String(), "secret-dsn")
	})
	t.Run("tenant key on tenant route", func(t *testing.T) {
		ok = false
		rec := do(t, Middleware(lookup, TenantKey)(next), "Bearer "+key)
		require.Equal(t, 204, rec.Code)
		require.True(t, ok)
		require.Equal(t, tenant, got.TenantID)
		require.Nil(t, got.UserID)
	})
	t.Run("user token on tenant route is 403", func(t *testing.T) {
		requireProblem(t, do(t, Middleware(lookup, TenantKey)(next), "Bearer "+upTok), 403)
	})
	t.Run("user token with scope", func(t *testing.T) {
		rec := do(t, Middleware(lookup, UserUpload)(next), "Bearer "+upTok)
		require.Equal(t, 204, rec.Code)
		require.Equal(t, &user, got.UserID)
		require.Equal(t, []string{"upload", "connect"}, got.Scopes)
	})
	t.Run("user token missing scope is 403", func(t *testing.T) {
		requireProblem(t, do(t, Middleware(lookup, UserUpload)(next), "Bearer "+rdTok), 403)
		requireProblem(t, do(t, Middleware(lookup, UserRead)(next), "Bearer "+upTok), 403)
		require.Equal(t, 204, do(t, Middleware(lookup, UserRead)(next), "Bearer "+rdTok).Code)
	})
	t.Run("tenant key on user route is 403", func(t *testing.T) {
		requireProblem(t, do(t, Middleware(lookup, UserUpload)(next), "Bearer "+key), 403)
		requireProblem(t, do(t, Middleware(lookup, UserConnect)(next), "Bearer "+key), 403)
		requireProblem(t, do(t, Middleware(lookup, UserRead)(next), "Bearer "+key), 403)
	})
}

func TestFromEmpty(t *testing.T) {
	_, ok := From(context.Background())
	require.False(t, ok)
}

func TestWriteProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, 422, "Unprocessable", "bad thing")
	requireProblem(t, rec, 422)
}

func TestMiddlewareLogsLookupErrorWithoutSecrets(t *testing.T) {
	buf := logtest.Capture(t, zerolog.DebugLevel)

	key, _, _ := NewSecret("ohs_sk_")
	boom := func(context.Context, string) (Principal, error) { return Principal{}, errors.New("db down") }
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	rec := do(t, Middleware(boom, TenantKey)(next), "Bearer "+key)
	require.Equal(t, 500, rec.Code)
	require.Contains(t, buf.String(), "db down")
	require.NotContains(t, buf.String(), key)
	require.NotContains(t, buf.String(), Hash(key))
}

// A lookup that cannot reach the database is a 503 the caller can retry; any
// other failure stays a 500.
func TestMiddlewareDatabaseDownIs503(t *testing.T) {
	key, _, _ := NewSecret("ohs_sk_")
	down := func(context.Context, string) (Principal, error) {
		return Principal{}, fmt.Errorf("tenant: lookup: %w", &pgconn.ConnectError{})
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	rec := do(t, Middleware(down, TenantKey)(next), "Bearer "+key)
	requireProblem(t, rec, http.StatusServiceUnavailable)
	require.Equal(t, "5", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "the database is unavailable")

	broken := func(context.Context, string) (Principal, error) {
		return Principal{}, &pgconn.PgError{Code: "42P01"}
	}
	rec = do(t, Middleware(broken, TenantKey)(next), "Bearer "+key)
	requireProblem(t, rec, http.StatusInternalServerError)
	require.Empty(t, rec.Header().Get("Retry-After"))
}

// A database that does not answer at all (paused, partitioned: no reset)
// used to hold every request until it came back, 300 s and more in a
// stress run. The lookup now gives up after LookupTimeout with a 503.
func TestMiddlewareSilentDatabaseIs503(t *testing.T) {
	old := LookupTimeout
	LookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { LookupTimeout = old })
	key, _, _ := NewSecret("ohs_sk_")
	silent := func(ctx context.Context, _ string) (Principal, error) {
		<-ctx.Done()
		return Principal{}, fmt.Errorf("tenant: lookup: %w", ctx.Err())
	}
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	start := time.Now()
	rec := do(t, Middleware(silent, TenantKey)(next), "Bearer "+key)
	require.Less(t, time.Since(start), 2*time.Second)
	requireProblem(t, rec, http.StatusServiceUnavailable)
	require.Equal(t, "5", rec.Header().Get("Retry-After"))
	require.Contains(t, rec.Body.String(), "did not answer in time")
	require.False(t, called)

	// A caller that went away is not an outage.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+key)
	rec = httptest.NewRecorder()
	Middleware(silent, TenantKey)(next).ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusServiceUnavailable, rec.Code)
}

func TestMiddlewareAny(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	key, _, _ := NewSecret("ohs_sk_")
	upTok, _, _ := NewSecret("ohs_ut_")
	rdTok, _, _ := NewSecret("ohs_ut_")
	lookup := lookupMap(map[string]Principal{
		Hash(key):   principalFor(tenant, nil),
		Hash(upTok): principalFor(tenant, &user, "upload", "connect"),
		Hash(rdTok): principalFor(tenant, &user, "read"),
	})
	var got Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = From(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	h := MiddlewareAny(lookup, TenantKey, UserRead)(next)

	require.Equal(t, http.StatusNoContent, do(t, h, "Bearer "+key).Code)
	require.Nil(t, got.UserID)
	require.Equal(t, http.StatusNoContent, do(t, h, "Bearer "+rdTok).Code)
	require.Equal(t, user, *got.UserID)
	requireProblem(t, do(t, h, "Bearer "+upTok), 403)
	requireProblem(t, do(t, h, ""), 401)
	require.Panics(t, func() { MiddlewareAny(lookup) })
}

func TestForbiddenNamesTheCredential(t *testing.T) {
	cases := []struct {
		wants []Scope
		want  string
	}{
		{[]Scope{TenantKey}, "this endpoint needs a tenant API key (ohs_sk_)"},
		{[]Scope{UserUpload}, "this endpoint needs a user token (ohs_ut_) with scope upload"},
		{[]Scope{TenantKey, UserRead}, "this endpoint needs a tenant API key (ohs_sk_) or a user token (ohs_ut_) with scope read"},
	}
	for _, c := range cases {
		if got := forbiddenDetail(c.wants); got != c.want {
			t.Errorf("%v: got %q, want %q", c.wants, got, c.want)
		}
	}
}

func TestTenantAdmin(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	admin, _, _ := NewSecret("ohs_sk_")
	plain, _, _ := NewSecret("ohs_sk_")
	tok, _, _ := NewSecret("ohs_ut_")
	lookup := lookupMap(map[string]Principal{
		Hash(admin): {TenantID: tenant, Admin: true},
		Hash(plain): principalFor(tenant, nil),
		// A user token never acts as an admin, whatever its tenant.
		Hash(tok): {TenantID: tenant, UserID: &user, Scopes: []string{"read"}, Admin: true},
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Middleware(lookup, TenantAdmin)(next)

	require.Equal(t, http.StatusNoContent, do(t, h, "Bearer "+admin).Code)
	rec := do(t, h, "Bearer "+plain)
	requireProblem(t, rec, 403)
	require.Contains(t, rec.Body.String(), "instance admin tenant")
	requireProblem(t, do(t, h, "Bearer "+tok), 403)
	requireProblem(t, do(t, h, ""), 401)
	// An admin key still passes every tenant key route.
	require.Equal(t, http.StatusNoContent, do(t, Middleware(lookup, TenantKey)(next), "Bearer "+admin).Code)
}

// Every response of a route behind a credential says no-store, unless the
// handler sets its own value.
func TestMiddlewareSetsNoStore(t *testing.T) {
	key, hash, err := NewSecret(PrefixSecretKey)
	require.NoError(t, err)
	lookup := lookupMap(map[string]Principal{hash: principalFor(uuid.New(), nil)})
	plain := Middleware(lookup, TenantKey)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	own := Middleware(lookup, TenantKey)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}))
	require.Equal(t, "no-store", do(t, plain, "Bearer "+key).Header().Get("Cache-Control"))
	require.Equal(t, "no-store", do(t, plain, "").Header().Get("Cache-Control"), "a 401 too")
	require.Equal(t, "private, max-age=60", do(t, own, "Bearer "+key).Header().Get("Cache-Control"))
}

// A refused credential is left on the Slot for the audit log: its hash and,
// for an API key, its prefix, never the secret. A request with no credential
// leaves nothing.
func TestMiddlewareReportsTheRefusedCredential(t *testing.T) {
	h := Middleware(lookupMap(nil), TenantKey)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler must not run")
	}))
	call := func(authz string) *Slot {
		ctx, slot := WithSlot(context.Background())
		req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		return slot
	}
	key, hash, err := NewSecret(PrefixSecretKey)
	require.NoError(t, err)
	f, ok := call("Bearer " + key).Failure()
	require.True(t, ok)
	require.Equal(t, Failure{Hash: hash, Prefix: key[:KeyPrefixLen]}, f)

	token, hash, err := NewSecret(PrefixUserToken)
	require.NoError(t, err)
	f, ok = call("Bearer " + token).Failure()
	require.True(t, ok)
	require.Equal(t, Failure{Hash: hash, UserToken: true}, f)

	for _, authz := range []string{"", "Bearer nothing", "Basic abc"} {
		_, ok = call(authz).Failure()
		require.False(t, ok, authz)
	}
}

// A lookup that says the credential is over its rate limit is a 429 with
// Retry-After in the problem format.
func TestMiddlewareRateLimited(t *testing.T) {
	key, _, err := NewSecret(PrefixSecretKey)
	require.NoError(t, err)
	lookup := func(context.Context, string) (Principal, error) {
		return Principal{}, fmt.Errorf("wrapped: %w", &LimitedError{RetryAfter: 1500 * time.Millisecond})
	}
	h := Middleware(lookup, TenantKey)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the handler must not run")
	}))
	rec := do(t, h, "Bearer "+key)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "2", rec.Header().Get("Retry-After"))
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
}

// The Slot's failure hook decides whether a refused credential answers 401
// or, for a caller that failed too often, 429. It is never asked about a
// credential the lookup accepts, nor about a request without one.
func TestMiddlewareAsksTheFailureHook(t *testing.T) {
	good, goodHash, err := NewSecret(PrefixSecretKey)
	require.NoError(t, err)
	bad, badHash, err := NewSecret(PrefixSecretKey)
	require.NoError(t, err)
	h := Middleware(lookupMap(map[string]Principal{goodHash: {TenantID: uuid.New()}}), TenantKey)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	var asked []Failure
	limited := false
	call := func(authz string) *httptest.ResponseRecorder {
		ctx, slot := WithSlot(context.Background())
		slot.OnFailure(func(f Failure) (time.Duration, bool) {
			asked = append(asked, f)
			return 90 * time.Second, limited
		})
		req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusUnauthorized, call("Bearer "+bad).Code)
	require.Equal(t, []Failure{{Hash: badHash, Prefix: bad[:KeyPrefixLen]}}, asked)

	limited = true
	rec := call("Bearer " + bad)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "90", rec.Header().Get("Retry-After"))
	require.Empty(t, rec.Header().Get("WWW-Authenticate"))
	require.Len(t, asked, 2)

	// A valid credential passes whatever the hook would say.
	require.Equal(t, http.StatusNoContent, call("Bearer "+good).Code)
	require.Equal(t, http.StatusUnauthorized, call("").Code)
	require.Equal(t, http.StatusUnauthorized, call("Bearer nothing").Code)
	require.Len(t, asked, 2)
}

// cookieReq is a request carrying only the console session cookie.
func cookieReq(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	return req
}

func okHandler(got *Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := From(r.Context()); ok && got != nil {
			*got = p
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestCookieAuthenticatesWhenAccepted(t *testing.T) {
	tenant := uuid.New()
	var calls []string
	ctx, slot := WithSlot(context.Background())
	slot.AcceptSession(func(_ *http.Request, token string) (Principal, error) {
		calls = append(calls, token)
		return Principal{TenantID: tenant, SessionID: uuid.New()}, nil
	})
	var seen Principal
	h := MiddlewareAny(lookupMap(nil), TenantKey)(okHandler(&seen))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("tok").WithContext(ctx))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"tok"}, calls)
	require.Equal(t, tenant, seen.TenantID)
	p, ok := slot.Principal()
	require.True(t, ok)
	require.Equal(t, tenant, p.TenantID)
}

func TestCookieIgnoredWithoutSessionFunc(t *testing.T) {
	ctx, _ := WithSlot(context.Background())
	h := MiddlewareAny(lookupMap(nil), TenantKey)(okHandler(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("tok").WithContext(ctx))
	requireProblem(t, rec, http.StatusUnauthorized)
	require.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))

	// An empty cookie is absent, with a SessionFunc installed too.
	called := false
	ctx, slot := WithSlot(context.Background())
	slot.AcceptSession(func(*http.Request, string) (Principal, error) {
		called = true
		return Principal{}, nil
	})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, cookieReq("").WithContext(ctx))
	requireProblem(t, rec, http.StatusUnauthorized)
	require.False(t, called)
}

func TestBearerWinsOverCookie(t *testing.T) {
	tenant := uuid.New()
	secret, hash, err := NewSecret(PrefixSecretKey)
	require.NoError(t, err)
	lookups := 0
	lookup := func(_ context.Context, h string) (Principal, error) {
		lookups++
		return lookupMap(map[string]Principal{hash: principalFor(tenant, nil)})(context.Background(), h)
	}
	called := false
	ctx, slot := WithSlot(context.Background())
	slot.AcceptSession(func(*http.Request, string) (Principal, error) {
		called = true
		return Principal{}, nil
	})
	req := cookieReq("tok").WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	MiddlewareAny(lookup, TenantKey)(okHandler(nil)).ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, lookups)
	require.False(t, called)
}

func TestSessionFuncErrors(t *testing.T) {
	failures := 0
	run := func(err error) *httptest.ResponseRecorder {
		ctx, slot := WithSlot(context.Background())
		slot.OnFailure(func(Failure) (time.Duration, bool) { failures++; return 0, false })
		slot.AcceptSession(func(*http.Request, string) (Principal, error) { return Principal{}, err })
		rec := httptest.NewRecorder()
		MiddlewareAny(lookupMap(nil), TenantKey)(okHandler(nil)).ServeHTTP(rec, cookieReq("tok").WithContext(ctx))
		return rec
	}
	rec := run(ErrUnauthenticated)
	requireProblem(t, rec, http.StatusUnauthorized)
	require.Equal(t, 0, failures)

	rec = run(&ForbiddenError{Detail: DetailCSRF})
	requireProblem(t, rec, http.StatusForbidden)
	require.Contains(t, rec.Body.String(), DetailCSRF)

	rec = run(&LimitedError{RetryAfter: 2 * time.Second})
	requireProblem(t, rec, http.StatusTooManyRequests)
	require.Equal(t, "2", rec.Header().Get("Retry-After"))

	rec = run(errors.New("boom"))
	requireProblem(t, rec, http.StatusInternalServerError)
}

func TestCookiePrincipalStillNeedsScope(t *testing.T) {
	ctx, slot := WithSlot(context.Background())
	slot.RequireScope(func(*http.Request) string { return "webhooks:write" })
	slot.AcceptSession(func(*http.Request, string) (Principal, error) {
		return Principal{TenantID: uuid.New(), KeyScopes: []string{"users:read"}}, nil
	})
	rec := httptest.NewRecorder()
	MiddlewareAny(lookupMap(nil), TenantKey)(okHandler(nil)).ServeHTTP(rec, cookieReq("tok").WithContext(ctx))
	requireProblem(t, rec, http.StatusForbidden)
	require.Equal(t, "webhooks:write", slot.MissingScope())
}

func TestPrincipalActor(t *testing.T) {
	tenant, actor := uuid.New(), uuid.New()
	require.Equal(t, tenant, Principal{TenantID: tenant}.Actor())
	require.Equal(t, actor, Principal{TenantID: tenant, ActorTenantID: actor}.Actor())
}
