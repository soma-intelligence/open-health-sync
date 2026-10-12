// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

func TestListWebhooks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	box, err := crypto.NewBox(bytes.Repeat([]byte{5}, 32))
	require.NoError(t, err)
	ws := webhook.NewStore(e.pool, box)
	srv := httptest.NewServer(api.NewRouter(api.Deps{Pool: e.pool, Store: e.store, Webhooks: ws}))
	t.Cleanup(srv.Close)
	pa, err := e.store.LookupKey(ctx, auth.Hash(e.keyA))
	require.NoError(t, err)
	ep, secret, err := ws.Create(ctx, pa.TenantID, "https://app.example.com/hooks", "prod", []string{webhook.DataWritten})
	require.NoError(t, err)
	_, err = ws.SendTest(ctx, pa.TenantID, ep.ID)
	require.NoError(t, err)

	r := get(t, Connect(t, srv.URL+"/mcp", e.keyA), "list_webhooks", map[string]any{"deliveries": true})
	assert.NotContains(t, string(r.Records), secret)
	var eps []struct {
		ID         string   `json:"id"`
		EventTypes []string `json:"event_types"`
		Recent     []struct {
			EventType string `json:"event_type"`
		} `json:"recent_deliveries"`
	}
	require.NoError(t, json.Unmarshal(r.Records, &eps))
	require.Len(t, eps, 1)
	assert.Equal(t, ep.ID.String(), eps[0].ID)
	assert.Equal(t, []string{webhook.DataWritten}, eps[0].EventTypes)
	require.Len(t, eps[0].Recent, 1)
	assert.Equal(t, webhook.Test, eps[0].Recent[0].EventType)

	assert.Zero(t, count(t, get(t, Connect(t, srv.URL+"/mcp", e.keyB), "list_webhooks", nil)), "another tenant's endpoints")
	msg, isErr := call(t, Connect(t, srv.URL+"/mcp", e.token(e.keyA, e.userA, "read")), "list_webhooks", nil)
	assert.True(t, isErr)
	assert.Equal(t, "list_webhooks needs a tenant API key", msg)
}
