// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/consent"
)

type consentListJSON struct {
	Data       []consent.Source `json:"data"`
	Categories []string         `json:"categories"`
}

type consentHistoryJSON struct {
	Data       []consent.Event `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

func TestConsentRoutes(t *testing.T) {
	e := webhookEnv(t, false)
	u := e.user(e.tenantA, "a-1")
	base := "/v1/users/" + u.String() + "/consents"

	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{
		"url": "https://app.example.com/hooks", "event_types": []string{"consent.granted", "consent.revoked"},
	})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))

	var list consentListJSON
	r = e.do("GET", base, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &list)
	assert.Empty(t, list.Data)
	assert.Equal(t, consent.Categories, list.Categories)

	r = e.do("PUT", base+"/apple_health", e.keyA, map[string]any{"granted": []string{"heart", "sleep"}, "evidence": "consent form v3"})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var src consent.Source
	r.json(t, &src)
	assert.Equal(t, "apple_health", src.Provider)
	require.Len(t, src.Categories, len(consent.Categories))

	r = e.do("PUT", base+"/apple_health", e.keyA, map[string]any{"granted": []string{"sleep"}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r = e.do("DELETE", base+"/health_connect?evidence=support%20ticket", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	list = consentListJSON{}
	e.do("GET", base, e.keyA, nil).json(t, &list)
	require.Len(t, list.Data, 2)
	assert.Equal(t, []string{"apple_health", "health_connect"}, []string{list.Data[0].Provider, list.Data[1].Provider})

	var hist consentHistoryJSON
	r = e.do("GET", base+"/history?limit=2", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &hist)
	require.Len(t, hist.Data, 2)
	require.NotNil(t, hist.NextCursor)
	assert.Equal(t, "revoked", hist.Data[0].Action)
	assert.Equal(t, "heart", hist.Data[0].Category)
	assert.NotEmpty(t, hist.Data[0].ActorID, "the API key id")
	r = e.do("GET", base+"/history?cursor="+*hist.NextCursor, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	hist = consentHistoryJSON{}
	r.json(t, &hist)
	require.Len(t, hist.Data, 1)
	assert.Equal(t, "granted", hist.Data[0].Action)
	assert.Equal(t, "heart", hist.Data[0].Category, "the first grant, oldest")
	assert.Equal(t, "consent form v3", hist.Data[0].Evidence)
	assert.Nil(t, hist.NextCursor)

	// Webhooks: one granted event, one revoked event; the health_connect revocation
	// of a source never granted changes nothing and announces nothing.
	rows, err := e.pool.Query(context.Background(), `SELECT event_type, payload FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 ORDER BY id`, e.tenantA)
	require.NoError(t, err)
	type delivery struct {
		typ  string
		data map[string]any
	}
	var got []delivery
	for rows.Next() {
		var (
			typ string
			raw []byte
		)
		require.NoError(t, rows.Scan(&typ, &raw))
		var p struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &p))
		got = append(got, delivery{typ, p.Data})
	}
	require.NoError(t, rows.Err())
	require.Len(t, got, 2, "%+v", got)
	assert.Equal(t, "consent.granted", got[0].typ)
	assert.Equal(t, []any{"heart", "sleep"}, got[0].data["categories"])
	assert.Equal(t, "apple_health", got[0].data["provider"])
	assert.Equal(t, u.String(), got[0].data["user_id"])
	assert.Equal(t, "consent.revoked", got[1].typ)
	assert.Equal(t, []any{"heart"}, got[1].data["categories"])

	// Refusals.
	assert.Equal(t, http.StatusBadRequest, e.do("PUT", base+"/apple_health", e.keyA, map[string]any{"granted": []string{"genome"}}).code)
	assert.Equal(t, http.StatusBadRequest, e.do("PUT", base+"/apple_health", e.keyA, map[string]any{"evidence": "x"}).code, "granted is required")
	assert.Equal(t, http.StatusNotFound, e.do("PUT", base+"/nosuchprovider", e.keyA, map[string]any{"granted": []string{}}).code)
	assert.Equal(t, http.StatusBadRequest, e.do("GET", base+"/history?cursor=abc", e.keyA, nil).code)
	foreign := "/v1/users/" + e.user(e.tenantB, "b-1").String() + "/consents"
	assert.Equal(t, http.StatusNotFound, e.do("GET", foreign, e.keyA, nil).code, "another tenant's user")
	assert.Equal(t, http.StatusNotFound, e.do("PUT", foreign+"/apple_health", e.keyA, map[string]any{"granted": []string{}}).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", base, "", nil).code)

	// Audited as consent actions.
	var page auditPage
	e.do("GET", "/v1/audit?action=consent.", e.keyA, nil).json(t, &page)
	assert.Equal(t, 3, countStatus(page.Data, audit.ConsentWrite, http.StatusOK), "two PUTs and a DELETE")
	assert.Equal(t, 4, countStatus(page.Data, audit.ConsentRead, http.StatusOK), "two listings and two history pages")
}

func countStatus(entries []audit.Entry, action string, status int) int {
	n := 0
	for _, e := range entries {
		if e.Action == action && e.Status != nil && *e.Status == status {
			n++
		}
	}
	return n
}
