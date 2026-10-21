// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	key, _, _ := NewSecret("ohs_sk_")
	boom := func(context.Context, string) (Principal, error) { return Principal{}, errors.New("db down") }
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	rec := do(t, Middleware(boom, TenantKey)(next), "Bearer "+key)
	require.Equal(t, 500, rec.Code)
	require.Contains(t, buf.String(), "db down")
	require.NotContains(t, buf.String(), key)
	require.NotContains(t, buf.String(), Hash(key))
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
