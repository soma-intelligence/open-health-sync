// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"

	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	// RequestID repeats the X-Request-Id response header, to quote when
	// asking the operator about a failed call.
	RequestID string `json:"request_id,omitempty"`
}

// WriteProblem writes an RFC 9457 application/problem+json response. The type is
// "about:blank", as the RFC allows when the title and status say it all.
func WriteProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	id := w.Header().Get(reqid.Header)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: title, Status: status, Detail: detail, RequestID: id})
}
