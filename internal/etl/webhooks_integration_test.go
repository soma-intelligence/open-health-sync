// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// webhookEvents returns the payloads queued for the tenant, oldest first.
func (e *env) webhookEvents(tenant uuid.UUID) []map[string]any {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT payload FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 ORDER BY id`, tenant)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		require.NoError(e.t, rows.Scan(&raw))
		var p map[string]any
		require.NoError(e.t, json.Unmarshal(raw, &p))
		out = append(out, p)
	}
	require.NoError(e.t, rows.Err())
	return out
}

func (e *env) webhookEndpoint(tenant uuid.UUID, types ...string) {
	e.t.Helper()
	if types == nil {
		types = []string{}
	}
	e.exec(`INSERT INTO ohs_core.webhook_endpoints (tenant_id, url, event_types, secret_sealed)
		VALUES ($1, 'https://example.com/hook', $2, '\x00')`, tenant, types)
}

func TestWebhooks_WrittenDataIsAnnouncedOnce(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	e.exec(`UPDATE ohs_core.end_users SET external_ref = 'app-42' WHERE id = $1`, e.user)
	e.webhookEndpoint(e.tenant)
	other := e.newTenant() // no endpoint: nothing queued for it
	otherUser := e.newUser(other)

	at := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	job := e.upload(e.tenant, e.user, "apple_health", "heart_rate",
		q("heart_rate", 62, "bpm", at), q("step_count", 100, "count", at.Add(26*time.Hour)))
	e.process(job)
	e.process(e.upload(other, otherUser, "apple_health", "heart_rate", q("heart_rate", 60, "bpm", at)))

	evs := e.webhookEvents(e.tenant)
	require.Len(t, evs, 1)
	assert.Equal(t, "data.written", evs[0]["type"])
	data := evs[0]["data"].(map[string]any)
	assert.Equal(t, e.user.String(), data["user_id"])
	assert.Equal(t, "app-42", data["external_ref"])
	assert.Equal(t, "apple_health", data["provider"])
	assert.Equal(t, "2026-10-05", data["first_day"])
	assert.Equal(t, "2026-10-06", data["last_day"])
	assert.ElementsMatch(t, []any{"heart_rate", "step_count"}, data["types"])
	assert.EqualValues(t, 2, data["records"].(map[string]any)["quantity_samples"])
	assert.Empty(t, e.webhookEvents(other))

	// Reprocessing writes nothing new, so nothing more is announced.
	e.process(job)
	assert.Len(t, e.webhookEvents(e.tenant), 1)
}

func TestWebhooks_ProviderDeletionsAreAnnounced(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.webhookEndpoint(e.tenant, "data.deleted")
	e.process(e.upload(e.tenant, e.user, "prov", "records", rec("sleep", "s-1", deletionBase)))
	assert.Empty(t, e.webhookEvents(e.tenant), "the endpoint wants data.deleted only")

	e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1")))
	evs := e.webhookEvents(e.tenant)
	require.Len(t, evs, 1)
	assert.Equal(t, "data.deleted", evs[0]["type"])
	data := evs[0]["data"].(map[string]any)
	assert.EqualValues(t, 1, data["records_deleted"])
	assert.Equal(t, "prov", data["provider"])
}
