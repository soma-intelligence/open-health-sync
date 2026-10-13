// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

type auditPage struct {
	Data       []audit.Entry `json:"data"`
	NextCursor *string       `json:"next_cursor"`
}

func TestAuditLogRecordsAuthenticatedRequests(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	base := "/v1/users/" + u.String()

	r := e.do("GET", base+"/samples?type=heart_rate", e.keyA, nil, "X-Request-Id", "req-audit-1")
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r = e.do("GET", base+"/exports/samples", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r = e.do("GET", "/v1/users/"+e.user(e.tenantB, "b-1").String(), e.keyA, nil)
	require.Equal(t, http.StatusNotFound, r.code, "another tenant's user")
	e.do("GET", base+"/samples", "", nil) // unauthenticated: not recorded
	e.do("GET", "/healthz", "", nil)      // no credential: not recorded

	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, u, []string{"read"}, time.Hour)
	require.NoError(t, err)
	r = e.do("POST", "/mcp", tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"Content-Type", "application/json", "Accept", "application/json, text/event-stream")
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	r = e.do("GET", "/v1/audit", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var page auditPage
	r.json(t, &page)
	require.Len(t, page.Data, 4, "%+v", page.Data)
	assert.Nil(t, page.NextCursor)

	mcp := page.Data[0]
	assert.Equal(t, audit.DataRead, mcp.Action)
	assert.Equal(t, audit.UserToken, mcp.Actor.Type)
	assert.Len(t, mcp.Actor.ID, 16)
	assert.NotContains(t, tok, mcp.Actor.ID, "the token id is not part of the secret")
	assert.Equal(t, u, *mcp.UserID)

	foreign := page.Data[1]
	assert.Equal(t, audit.UserRead, foreign.Action)
	assert.Equal(t, http.StatusNotFound, *foreign.Status)

	exp := page.Data[2]
	assert.Equal(t, audit.DataExport, exp.Action)
	assert.Equal(t, "GET /v1/users/{id}/exports/{dataset}", exp.Route)

	read := page.Data[3]
	assert.Equal(t, audit.DataRead, read.Action)
	assert.Equal(t, audit.APIKey, read.Actor.Type)
	assert.NotEmpty(t, read.Actor.ID)
	assert.Equal(t, u, *read.UserID)
	assert.Equal(t, "GET", read.Method)
	assert.Equal(t, "GET /v1/users/{id}/samples", read.Route, "the pattern, never the query string")
	assert.Equal(t, http.StatusOK, *read.Status)
	assert.Equal(t, "req-audit-1", read.RequestID)

	// Filters and paging; the listing itself was recorded.
	r = e.do("GET", "/v1/audit?action=data.&user_id="+u.String()+"&limit=2", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	page = auditPage{}
	r.json(t, &page)
	require.Len(t, page.Data, 2)
	require.NotNil(t, page.NextCursor)
	r = e.do("GET", "/v1/audit?action=data.&user_id="+u.String()+"&limit=2&cursor="+*page.NextCursor, e.keyA, nil)
	page = auditPage{}
	r.json(t, &page)
	require.Len(t, page.Data, 1)
	assert.Equal(t, "req-audit-1", page.Data[0].RequestID)
	r = e.do("GET", "/v1/audit?action=audit.read", e.keyA, nil)
	page = auditPage{}
	r.json(t, &page)
	assert.Len(t, page.Data, 3, "the three listings before this one")

	// Another tenant sees none of it; a user token cannot read the log.
	r = e.do("GET", "/v1/audit", e.keyB, nil)
	page = auditPage{}
	r.json(t, &page)
	assert.Len(t, page.Data, 0)
	assert.Equal(t, http.StatusForbidden, e.do("GET", "/v1/audit", tok, nil).code)

	for _, q := range []string{"user_id=nope", "since=yesterday", "cursor=abc", "cursor=0", "limit=0"} {
		assert.Equal(t, http.StatusBadRequest, e.do("GET", "/v1/audit?"+q, e.keyA, nil).code, q)
	}
}
