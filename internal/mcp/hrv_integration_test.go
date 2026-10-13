// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package mcp_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRRAndHRVMatchREST(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC)
	for i := range 20 {
		rr := 790.0 + 20*float64(i%2)
		at = at.Add(time.Duration(rr) * time.Millisecond)
		e.exec(`INSERT INTO ohs_data.heartbeat_samples (tenant_id, user_id, provider, series_id, "timestamp", rr_ms, content_hash)
			SELECT tenant_id, id, 'apple_health', 'series-1', $2, $3, $4 FROM ohs_core.end_users WHERE id = $1`,
			e.userA, at, rr, fmt.Sprintf("hb-%d", i))
	}
	cs := Connect(t, e.srv.URL+"/mcp", e.keyA)
	base := "/v1/users/" + e.userA.String()

	r := get(t, cs, "get_rr_intervals", map[string]any{"user_id": e.userA.String(), "limit": 5})
	assert.True(t, r.Truncated)
	assert.JSONEq(t, string(e.rest(base+"/rr-intervals?limit=5", e.keyA)), string(r.Records))

	r = get(t, cs, "get_hrv", map[string]any{"user_id": e.userA.String(), "start": "2026-03-01"})
	assert.False(t, r.Truncated)
	assert.Equal(t, 1, count(t, r))
	assert.JSONEq(t, string(e.rest(base+"/hrv?start=2026-03-01", e.keyA)), string(r.Records))

	msg, isErr := call(t, cs, "get_hrv", map[string]any{"user_id": e.userA.String(), "cursor": "nope"})
	assert.True(t, isErr)
	assert.Equal(t, "the cursor is not valid", msg)
}
