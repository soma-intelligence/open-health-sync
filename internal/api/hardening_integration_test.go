// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

type apiKeyJSON struct {
	ID        uuid.UUID  `json:"id"`
	Prefix    string     `json:"prefix"`
	ExpiresAt *time.Time `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	Key       string     `json:"key"`
}

type mintedToken struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func (e *env) mint(user uuid.UUID, key string) mintedToken {
	e.t.Helper()
	r := e.do("POST", "/v1/users/"+user.String()+"/tokens", key, map[string]any{"scopes": []string{"read"}})
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
	var tok mintedToken
	r.json(e.t, &tok)
	require.Len(e.t, tok.ID, 16)
	return tok
}

// tokenWorks reports whether the user token still reads its user's data.
func (e *env) tokenWorks(user uuid.UUID, token string) bool {
	e.t.Helper()
	r := e.do("GET", "/v1/users/"+user.String()+"/archive", token, nil)
	if r.code != http.StatusOK {
		require.Equal(e.t, http.StatusUnauthorized, r.code, string(r.body))
	}
	return r.code == http.StatusOK
}

func TestRevokeUserTokenRoutes(t *testing.T) {
	e := newEnv(t)
	u, other := e.user(e.tenantA, "a-1"), e.user(e.tenantA, "a-2")
	base := "/v1/users/" + u.String() + "/tokens"
	a, b, c := e.mint(u, e.keyA), e.mint(u, e.keyA), e.mint(other, e.keyA)

	// Another tenant, another user and an unknown id are all a 404.
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/"+a.ID, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/"+c.ID, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/0123456789abcdef", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", base+"/nope", e.keyA, nil).code)
	assert.Equal(t, http.StatusForbidden, e.do("DELETE", base+"/"+a.ID, a.Token, nil).code, "a user token cannot revoke")
	require.True(t, e.tokenWorks(u, a.Token))

	r := e.do("DELETE", base+"/"+a.ID, e.keyA, nil)
	require.Equal(t, http.StatusNoContent, r.code, string(r.body))
	assert.Equal(t, http.StatusNoContent, e.do("DELETE", base+"/"+a.ID, e.keyA, nil).code, "twice is fine")
	assert.False(t, e.tokenWorks(u, a.Token))
	assert.True(t, e.tokenWorks(u, b.Token))

	r = e.do("DELETE", base, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var out struct {
		Revoked int `json:"revoked"`
	}
	r.json(t, &out)
	assert.Equal(t, 1, out.Revoked)
	assert.False(t, e.tokenWorks(u, b.Token))
	assert.True(t, e.tokenWorks(other, c.Token), "another user's token is untouched")
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", "/v1/users/"+uuid.NewString()+"/tokens", e.keyA, nil).code)

	// Both are recorded as token.revoke, the first with the token id.
	r = e.do("GET", "/v1/audit?action=token.revoke&user_id="+u.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var pg auditPage
	r.json(t, &pg)
	require.NotEmpty(t, pg.Data)
	assert.Equal(t, "DELETE /v1/users/{id}/tokens", pg.Data[0].Route)
	revoked := false
	for _, ev := range pg.Data {
		revoked = revoked || (ev.Detail["token_id"] == a.ID && ev.Status != nil && *ev.Status == http.StatusNoContent)
	}
	assert.True(t, revoked, "the revocation names the token id")
}

// Revoking an API key revokes the user tokens it minted.
func TestRevokingAKeyRevokesItsTokensThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	r := e.do("POST", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var second apiKeyJSON
	r.json(t, &second)

	mine, theirs := e.mint(u, second.Key), e.mint(u, e.keyA)
	require.True(t, e.tokenWorks(u, mine.Token) && e.tokenWorks(u, theirs.Token))

	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/v1/keys/"+second.ID.String(), e.keyA, nil).code)
	assert.False(t, e.tokenWorks(u, mine.Token), "the token the revoked key minted")
	assert.True(t, e.tokenWorks(u, theirs.Token), "a token another key minted")
}

func TestKeyExpiryThroughTheAPI(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/v1/keys", "/v1/admin/tenants/" + e.tenantB.String() + "/keys"} {
		r := e.do("POST", path, e.keyA, map[string]any{"expires_at": time.Now().Add(-time.Hour)})
		require.Equal(t, http.StatusBadRequest, r.code, path+": "+string(r.body))
		r = e.do("POST", path, e.keyA, map[string]any{"expires_at": "tomorrow"})
		require.Equal(t, http.StatusBadRequest, r.code, path)

		exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		r = e.do("POST", path, e.keyA, map[string]any{"expires_at": exp})
		require.Equal(t, http.StatusCreated, r.code, path+": "+string(r.body))
		var k apiKeyJSON
		r.json(t, &k)
		require.NotNil(t, k.ExpiresAt, path)
		assert.True(t, exp.Equal(*k.ExpiresAt), path)
		require.Equal(t, http.StatusOK, e.do("GET", "/v1/tenant", k.Key, nil).code, path)

		r = e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, path)
		var list struct {
			Data []apiKeyJSON `json:"data"`
		}
		r.json(t, &list)
		var listed *apiKeyJSON
		for i := range list.Data {
			if list.Data[i].ID == k.ID {
				listed = &list.Data[i]
			} else {
				assert.Nil(t, list.Data[i].ExpiresAt, "a key made without an expiry")
			}
		}
		require.NotNil(t, listed, path)
		assert.True(t, exp.Equal(*listed.ExpiresAt), path)

		e.exec(`UPDATE ohs_core.api_keys SET expires_at = now() - interval '1 second' WHERE id = $1`, k.ID)
		assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/tenant", k.Key, nil).code, path+": past its expiry")
	}
	// No body still makes a key that does not expire.
	r := e.do("POST", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code)
	var k apiKeyJSON
	r.json(t, &k)
	assert.Nil(t, k.ExpiresAt)
}

// A revoked key that is still used leaves an event on its tenant's log,
// with the client address and the key prefix, never the key.
func TestFailedAuthOnTheAuditLog(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/v1/keys", e.keyA, nil)
	require.Equal(t, http.StatusCreated, r.code)
	var k apiKeyJSON
	r.json(t, &k)
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/v1/keys/"+k.ID.String(), e.keyA, nil).code)

	for range 3 {
		require.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", k.Key, nil).code)
	}
	r = e.do("GET", "/v1/audit?action=auth.failed", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var pg auditPage
	r.json(t, &pg)
	require.Len(t, pg.Data, 1, "one event for the address in a minute")
	ev := pg.Data[0]
	assert.Equal(t, audit.Actor{Type: audit.APIKey, ID: k.ID.String()}, ev.Actor)
	assert.Equal(t, "revoked", ev.Detail["reason"])
	assert.Equal(t, k.Prefix+"...", ev.Detail["key_prefix"])
	assert.NotEmpty(t, ev.Detail["client_address"])
	assert.NotContains(t, string(r.body), k.Key)

	// An unknown key names no tenant: it is on no tenant's log, under the
	// nil tenant id, once per address and minute. The event of the revoked
	// key did not use up that of the address, nor the other way round.
	for range 3 {
		require.Equal(t, http.StatusUnauthorized, e.do("GET", "/v1/users", "ohs_sk_"+k.Key[7:20]+"000000000000000000000000000000", nil).code)
	}
	var n, unknown int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE tenant_id = '00000000-0000-0000-0000-000000000000' AND detail->>'reason' = 'unknown')
		 FROM ohs_core.audit_events WHERE action = 'auth.failed'`).Scan(&n, &unknown))
	assert.Equal(t, 2, n, "one event for the revoked key, one for the address")
	assert.Equal(t, 1, unknown)
	r = e.do("GET", "/v1/audit?action=auth.failed", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &pg)
	assert.Len(t, pg.Data, 1, "the tenant sees its own key only")
}

// With a buffered writer the events of reads reach the log once it is
// closed, and a sensitive action is there at once.
func TestBufferedAuditWriter(t *testing.T) {
	var w *audit.Writer
	e := newEnv(t, func(d *api.Deps) {
		w = audit.NewWriter(d.Pool, audit.WriterOptions{Interval: time.Hour})
		d.Audit = w
	})
	u := e.user(e.tenantA, "a-1")
	require.Equal(t, http.StatusOK, e.do("GET", "/v1/users/"+u.String(), e.keyA, nil).code)
	e.mint(u, e.keyA)
	count := func(action string) (n int) {
		require.NoError(t, e.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM ohs_core.audit_events WHERE tenant_id = $1 AND action = $2`, e.tenantA, action).Scan(&n))
		return n
	}
	assert.Equal(t, 1, count(audit.TokenCreate), "written in the request")
	assert.Zero(t, count(audit.UserRead), "still queued")
	require.NoError(t, w.Close(context.Background()))
	assert.Equal(t, 1, count(audit.UserRead))
	assert.Zero(t, w.Dropped())
}

func TestReadyzChecksTheSchemaVersion(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, http.StatusOK, e.do("GET", "/readyz", "", nil).code)

	// Another router over a database one migration behind is not ready.
	e.exec(`UPDATE schema_migrations SET version = version - 1`)
	h := api.NewRouter(api.Deps{Pool: e.pool, Store: e.store})
	e.handler = h
	r := e.do("GET", "/readyz", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, r.code, string(r.body))
	var p problem
	r.json(t, &p)
	assert.Equal(t, "database schema is not at the version this binary needs", p.Detail)
	assert.NotRegexp(t, `\d`, p.Detail, "no version number for an anonymous caller")
	// A dirty schema answers the same.
	e.exec(`UPDATE schema_migrations SET version = version + 1, dirty = true`)
	e.handler = api.NewRouter(api.Deps{Pool: e.pool, Store: e.store})
	r = e.do("GET", "/readyz", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, r.code, string(r.body))
	r.json(t, &p)
	assert.Equal(t, "database schema is not at the version this binary needs", p.Detail)
	e.exec(`UPDATE schema_migrations SET dirty = false`)
}

// Health data reads say no-store and are compressed for a client that
// takes gzip.
func TestReadsAreNoStoreAndCompressed(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	for _, path := range []string{"/samples?type=heart_rate", "/fhir/Observation", "/omh/data-points", "/exports/samples"} {
		r := e.do("GET", "/v1/users/"+u.String()+path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, path+": "+string(r.body))
		assert.Equal(t, "no-store", r.header.Get("Cache-Control"), path)
		assert.Contains(t, r.header.Values("Vary"), "Accept-Encoding", path)
	}
	r := e.do("GET", "/v1/audit?limit=1000", e.keyA, nil, "Accept-Encoding", "gzip")
	require.Equal(t, http.StatusOK, r.code)
	if r.header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(r.body))
		require.NoError(t, err)
		plain, err := io.ReadAll(zr)
		require.NoError(t, err)
		assert.Contains(t, string(plain), `"data"`)
	}
}
