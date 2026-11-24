// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
)

// The access line level follows the status, and the line carries the matched route, never the
// raw path or the query string.
func TestAccessLineLevelFollowsStatus(t *testing.T) {
	buf := logtest.Capture(t, zerolog.DebugLevel)
	for _, status := range []int{200, 302, 404, 429, 500, 503} {
		r := httptest.NewRequest(http.MethodGet, "/v1/users/abc?token=secret-value", nil)
		r.Pattern = "GET /v1/users/{id}"
		r.RemoteAddr = "192.0.2.7:4242"
		accessLine(r, status, 123, 1500*time.Millisecond)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 6)
	want := map[int]string{200: "debug", 302: "debug", 404: "info", 429: "info", 500: "error", 503: "error"}
	for _, l := range lines {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &m))
		assert.Equal(t, "http_request", m["message"])
		assert.Equal(t, want[int(m["status"].(float64))], m["level"], l)
		assert.Equal(t, "GET /v1/users/{id}", m["route"])
		assert.Equal(t, "192.0.2.7", m["remote_ip"])
		assert.EqualValues(t, 1500, m["duration_ms"])
		assert.EqualValues(t, 123, m["bytes"])
	}
	assert.NotContains(t, buf.String(), "secret-value")
	assert.NotContains(t, buf.String(), "/v1/users/abc")
}

func TestAccessLineUnmatchedRoute(t *testing.T) {
	buf := logtest.Capture(t, zerolog.DebugLevel)
	accessLine(httptest.NewRequest(http.MethodGet, "/nope", nil), 404, 0, time.Millisecond)
	assert.Contains(t, buf.String(), `"route":"unmatched"`)
}
