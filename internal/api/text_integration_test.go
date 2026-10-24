// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres refuses a NUL and invalid UTF-8 in text: every route that passed
// such a value on to a query answered 500. They are refused with 400 at the
// edge (path and query) or by the body decoder (JSON).
func TestNULAndInvalidUTF8Are400(t *testing.T) {
	e := webhookEnv(t, false)
	u := e.user(e.tenantA, "text-user").String()
	for _, bad := range []string{"%00", "%ff%fe"} {
		for _, target := range []string{
			"/v1/users/" + u + "/samples?type=heart_rate" + bad,
			"/v1/users/" + u + "/category-samples?type=" + bad,
			"/v1/users/" + u + "/workouts?type=" + bad,
			"/v1/users/" + u + "/exports/samples?type=" + bad,
			"/v1/users/" + u + "/rr-intervals?series_id=" + bad,
			"/v1/users/" + u + "/hrv?series_id=" + bad,
			"/v1/users/" + u + "/coverage?provider=" + bad,
			"/v1/users/" + u + "/sync-runs?provider=" + bad,
			"/v1/pipeline/sync-runs?provider=" + bad,
			"/v1/pipeline/etl-jobs?provider=" + bad,
			"/v1/audit?action=" + bad,
			"/v1/users/" + u + "/fhir/Observation?code=" + bad,
		} {
			r := e.do("GET", target, e.keyA, nil)
			require.Equal(t, http.StatusBadRequest, r.code, "%s: %s", target, r.body)
			var p problem
			r.json(t, &p)
			assert.Contains(t, p.Detail, "NUL character or invalid UTF-8", target)
		}
		r := e.do("POST", "/v1/users/"+u+"/connections/oura"+bad+"/sync", e.keyA, nil)
		assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	}

	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/users", `{"external_ref":"a\u0000b"}`},
		{"POST", "/v1/webhooks", `{"url":"https://example.com/h\u0000","event_types":["sync.completed"]}`},
		{"PUT", "/v1/users/" + u + "/consents/oura", `{"granted":["heart_rate"],"evidence":"signed\u0000"}`},
		{"POST", "/v1/pipeline/etl-jobs/retry", `{"provider":"oura\u0000"}`},
	} {
		r := e.do(tc.method, tc.path, e.keyA, tc.body)
		require.Equal(t, http.StatusBadRequest, r.code, "%s %s: %s", tc.method, tc.path, r.body)
		assert.True(t, strings.Contains(string(r.body), "NUL character"), string(r.body))
	}
}
