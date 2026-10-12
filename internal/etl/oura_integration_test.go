// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura"
	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// providerSink stores every Emit as one raw object of provider.
type providerSink struct {
	e        *env
	provider string
	jobs     []queue.ETLJob
}

func (s *providerSink) Emit(_ context.Context, dataType string, _ time.Time, lines [][]byte) error {
	strs := make([]string, len(lines))
	for i, l := range lines {
		strs[i] = string(l)
	}
	s.jobs = append(s.jobs, s.e.upload(s.e.tenant, s.e.user, s.provider, dataType, strs...))
	return nil
}

// A pull through the real Oura connector lands every data type; a delete
// webhook removes the night with its phases and samples, a later pull does
// not bring it back, and the new data types land with canonical units.
func TestOuraPullThenDeleteWebhook(t *testing.T) {
	e := newEnv(t)
	e.now = func() time.Time { return time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) }
	fake := ourafake.New(t)
	c := oura.New(oura.Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret", VerificationToken: "test-verify",
		AccountBase: fake.URL(), APIBase: fake.URL(), HTTPClient: fake.Client(), PageDelay: -1,
		Now: func() time.Time { return time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC) },
	})
	e.reg.Register(c)
	ctx := context.Background()
	conn := connector.Connection{TenantID: e.tenant, UserID: e.user, Provider: oura.Slug,
		Tokens: connector.Tokens{Access: "fake-access-token-1", ProviderUserID: "oura-user-1"}}
	pull := func() {
		sink := &providerSink{e: e, provider: oura.Slug}
		require.NoError(t, c.Fetch(ctx, conn, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), sink))
		for _, j := range sink.jobs {
			e.process(j)
		}
	}
	pull()
	assert.Zero(t, e.count("failed_records"))
	assert.Equal(t, 2, e.count("sleep_sessions"), "the night and the nap; the deleted period is a tombstone")
	assert.Equal(t, 1, e.tombstones(e.tenant, e.user))
	assert.Equal(t, 2, e.count("category_samples"), "two mindful sessions")
	for qt, unit := range map[string]string{"sleep_score": "score", "stress_high_time": "min", "recovery_high_time": "min", "vo2_max": "mL/kg/min"} {
		var got string
		var n int
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT min(unit), count(*) FROM ohs_data.quantity_samples
			WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = $3`, e.tenant, e.user, qt).Scan(&got, &n))
		assert.Equal(t, unit, got, qt)
		assert.NotZero(t, n, qt)
	}
	nightSamples := func() int {
		var n int
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.quantity_samples
			WHERE tenant_id = $1 AND user_id = $2 AND external_id LIKE 'sleep-0001\_%'`, e.tenant, e.user).Scan(&n))
		return n
	}
	require.Equal(t, 2, nightSamples())
	phasesBefore := e.count("sleep_phases")

	// The user deletes the night and a session in the Oura app.
	for _, n := range []struct{ dataType, id string }{{"sleep", "sleep-0001"}, {"session", "session-0001"}} {
		body := `{"event_type":"delete","data_type":"` + n.dataType + `","object_id":"` + n.id +
			`","event_time":"2026-03-03T11:59:00+00:00","user_id":"oura-user-1"}`
		req := ouraWebhook(t, "test-client-secret", body)
		events, err := c.HandleInbound(ctx, req)
		require.NoError(t, err)
		require.Len(t, events, 1)
		lines := make([]string, len(events[0].Lines))
		for i, l := range events[0].Lines {
			lines[i] = string(l)
		}
		e.process(e.upload(e.tenant, e.user, oura.Slug, events[0].DataType, lines...))
	}
	assert.Equal(t, 1, e.count("sleep_sessions"), "the nap stays")
	assert.Less(t, e.count("sleep_phases"), phasesBefore)
	assert.Zero(t, nightSamples())
	assert.Equal(t, 1, e.count("category_samples"))
	assert.Equal(t, 3, e.tombstones(e.tenant, e.user))

	// Oura still lists the night (a stale cache): it does not come back.
	pull()
	assert.Equal(t, 1, e.count("sleep_sessions"))
	assert.Zero(t, nightSamples())
	assert.Equal(t, 1, e.count("category_samples"))
}

// ouraWebhook builds a signed Oura notification, sent at the connector's
// clock.
func ouraWebhook(t *testing.T, secret, body string) *http.Request {
	t.Helper()
	ts := strconv.FormatInt(time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC).Unix(), 10)
	r := httptest.NewRequest(http.MethodPost, "/inbound/oura", strings.NewReader(body))
	r.Header.Set(oura.HeaderTimestamp, ts)
	r.Header.Set(oura.HeaderSignature, oura.Sign(secret, ts, []byte(body)))
	return r
}
