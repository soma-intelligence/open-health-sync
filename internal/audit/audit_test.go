// SPDX-License-Identifier: AGPL-3.0-only

package audit

import "testing"

func TestClassify(t *testing.T) {
	for _, c := range []struct{ method, pattern, want string }{
		{"GET", "GET /v1/users/{id}/samples", DataRead},
		{"GET", "GET /v1/users/{id}/fhir/Observation/{observation_id}", DataRead},
		{"GET", "GET /v1/users/{id}/omh/data-points", DataRead},
		{"GET", "GET /v1/users/{id}/freshness", DataRead},
		{"POST", "/mcp", DataRead},
		{"GET", "GET /v1/users/{id}/exports/{dataset}", DataExport},
		{"GET", "GET /v1/users/{id}/archive", DataExport},
		{"POST", "POST /v1/ingest/apple-health", DataWrite},
		{"POST", "POST /v1/users", UserCreate},
		{"GET", "GET /v1/users", UserRead},
		{"GET", "GET /v1/users/{id}", UserRead},
		{"DELETE", "DELETE /v1/users/{id}", UserDelete},
		{"GET", "GET /v1/deletions/{job_id}", UserRead},
		{"POST", "POST /v1/users/{id}/tokens", TokenCreate},
		{"GET", "GET /v1/users/{id}/connections", ConnectionRead},
		{"POST", "POST /v1/users/{id}/connections/{provider}", ConnectionCreate},
		{"DELETE", "DELETE /v1/users/{id}/connections/{provider}", ConnectionDelete},
		{"POST", "POST /v1/users/{id}/connections/{provider}/sync", ConnectionSync},
		{"GET", "GET /v1/users/{id}/sync-runs", ConnectionRead},
		{"GET", "GET /v1/users/{id}/sync-runs/{run}", ConnectionRead},
		{"POST", "POST /v1/users/{id}/connect-sessions", ConnectionCreate},
		{"GET", "GET /v1/users/{id}/menstrual-cycles", DataRead},
		{"GET", "GET /v1/webhooks", WebhookRead},
		{"PATCH", "PATCH /v1/webhooks/{webhook_id}", WebhookWrite},
		{"GET", "GET /v1/audit", AuditRead},
		{"GET", "GET /v1/providers", ConfigRead},
		{"GET", "", ConfigRead},
		{"POST", "", ConfigWrite},
	} {
		if got := Classify(c.method, c.pattern); got != c.want {
			t.Errorf("Classify(%q, %q) = %q, want %q", c.method, c.pattern, got, c.want)
		}
	}
}

func TestTokenID(t *testing.T) {
	if got := TokenID("0123456789abcdef0123456789abcdef"); got != "0123456789abcdef" {
		t.Fatalf("TokenID = %q", got)
	}
	if got := TokenID("abc"); got != "abc" {
		t.Fatalf("short TokenID = %q", got)
	}
}
