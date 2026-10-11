// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

func TestRequestIDOnEveryResponse(t *testing.T) {
	var seen string
	s := &server{metrics: newHTTPMetrics()}
	h := s.instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = reqid.From(r.Context())
		badRequest(w, "nope")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	id := rec.Header().Get("X-Request-Id")
	require.Len(t, id, 32)
	assert.Equal(t, id, seen, "the handler's context carries the id")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, id, body["request_id"], "a problem body names the id")

	// A proxy's id is kept; a forged one is replaced.
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", "edge-42")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "edge-42", rec.Header().Get("X-Request-Id"))
	req.Header.Set("X-Request-Id", "bad id\" injected")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Len(t, rec.Header().Get("X-Request-Id"), 32)
}
