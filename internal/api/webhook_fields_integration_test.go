// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webhookStateJSON is the delivery state an endpoint shows an operator.
type webhookStateJSON struct {
	ConsecutiveFailures     *int       `json:"consecutive_failures"`
	PausedUntil             *time.Time `json:"paused_until"`
	PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at"`
}

// hasKeys reports the JSON object's keys, present even when null.
func hasKeys(t *testing.T, body []byte, keys ...string) {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), string(body))
	for _, k := range keys {
		assert.Contains(t, m, k, string(body))
	}
}

// An endpoint shows its pause, its failures in a row and the end of a
// secret rotation's overlap; none of them can be written.
func TestWebhookEndpointShowsDeliveryState(t *testing.T) {
	e := webhookEnv(t, false)
	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	hasKeys(t, r.body, "consecutive_failures", "paused_until", "previous_secret_expires_at", "secret")
	var created webhookJSON
	r.json(t, &created)
	path := "/v1/webhooks/" + created.ID.String()

	var st webhookStateJSON
	g := e.do("GET", path, e.keyA, nil)
	require.Equal(t, http.StatusOK, g.code)
	hasKeys(t, g.body, "consecutive_failures", "paused_until", "previous_secret_expires_at")
	g.json(t, &st)
	require.NotNil(t, st.ConsecutiveFailures)
	assert.Zero(t, *st.ConsecutiveFailures)
	assert.Nil(t, st.PausedUntil)
	assert.Nil(t, st.PreviousSecretExpiresAt)

	// Paused by the delivery worker after failures in a row.
	e.exec(`UPDATE ohs_core.webhook_endpoints SET consecutive_failures = 6, paused_until = now() + interval '5 minutes' WHERE id = $1`, created.ID)
	for _, p := range []string{path, "/v1/webhooks"} {
		g = e.do("GET", p, e.keyA, nil)
		require.Equal(t, http.StatusOK, g.code)
		body := g.body
		if p != path {
			var list page[json.RawMessage]
			g.json(t, &list)
			require.Len(t, list.Data, 1)
			body = list.Data[0]
		}
		st = webhookStateJSON{}
		require.NoError(t, json.Unmarshal(body, &st))
		require.NotNil(t, st.ConsecutiveFailures, p)
		assert.Equal(t, 6, *st.ConsecutiveFailures, p)
		require.NotNil(t, st.PausedUntil, p)
		assert.WithinDuration(t, time.Now().Add(5*time.Minute), *st.PausedUntil, time.Minute, p)
	}

	// Read only: a PATCH naming them is refused like any unknown field.
	for _, f := range []string{"paused_until", "consecutive_failures", "previous_secret_expires_at"} {
		assert.Equal(t, http.StatusBadRequest, e.do("PATCH", path, e.keyA, map[string]any{f: nil}).code, f)
	}
	// Enabling the endpoint ends the pause, as before.
	p := e.do("PATCH", path, e.keyA, map[string]any{"enabled": true})
	require.Equal(t, http.StatusOK, p.code, string(p.body))
	st = webhookStateJSON{}
	p.json(t, &st)
	assert.Nil(t, st.PausedUntil)
}

// A rotation keeps the old secret signing for the grace period, unless the
// call asks to end it at once (a leaked secret).
func TestWebhookRotateSecretEndsThePreviousOne(t *testing.T) {
	e := webhookEnv(t, false)
	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks"})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var created webhookJSON
	r.json(t, &created)
	path := "/v1/webhooks/" + created.ID.String()

	type rotated struct {
		Secret                  string     `json:"secret"`
		PreviousSecretExpiresAt *time.Time `json:"previous_secret_expires_at"`
	}
	// No body, an empty object and false: the grace period of the instance.
	last := created.Secret
	for _, body := range []any{nil, `{}`, map[string]any{"expire_previous": false}} {
		r = e.do("POST", path+"/rotate-secret", e.keyA, body)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
		var rot rotated
		r.json(t, &rot)
		assert.NotEqual(t, last, rot.Secret)
		last = rot.Secret
		require.NotNil(t, rot.PreviousSecretExpiresAt, "%v", body)
		assert.WithinDuration(t, time.Now().Add(24*time.Hour), *rot.PreviousSecretExpiresAt, time.Minute)
		var st webhookStateJSON
		e.do("GET", path, e.keyA, nil).json(t, &st)
		require.NotNil(t, st.PreviousSecretExpiresAt)
		assert.WithinDuration(t, *rot.PreviousSecretExpiresAt, *st.PreviousSecretExpiresAt, time.Second)
	}

	// The old secret ends at once.
	r = e.do("POST", path+"/rotate-secret", e.keyA, map[string]any{"expire_previous": true})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	hasKeys(t, r.body, "secret", "previous_secret_expires_at")
	var rot rotated
	r.json(t, &rot)
	assert.NotEqual(t, last, rot.Secret)
	assert.Nil(t, rot.PreviousSecretExpiresAt)
	var st webhookStateJSON
	e.do("GET", path, e.keyA, nil).json(t, &st)
	assert.Nil(t, st.PreviousSecretExpiresAt)
	var kept int
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM ohs_core.webhook_endpoints WHERE id = $1 AND secret_previous_sealed IS NOT NULL`, created.ID).Scan(&kept))
	assert.Zero(t, kept, "the replaced secret is not kept")

	// A body that is not understood changes nothing.
	for _, body := range []string{`{"expire_previous":"yes"}`, `{"grace":0}`, `[]`} {
		assert.Equal(t, http.StatusBadRequest, e.do("POST", path+"/rotate-secret", e.keyA, body).code, body)
	}
	assert.Equal(t, http.StatusNotFound, e.do("POST", path+"/rotate-secret", e.keyB, map[string]any{"expire_previous": true}).code)
}
