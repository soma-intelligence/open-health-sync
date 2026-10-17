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
		{"GET", "GET /v1/users/{id}/consents/history", ConsentRead},
		{"PUT", "PUT /v1/users/{id}/consents/{provider}", ConsentWrite},
		{"DELETE", "DELETE /v1/users/{id}/consents/{provider}", ConsentWrite},
		{"POST", "POST /v1/users/{id}/shares", ShareCreate},
		{"GET", "GET /v1/users/{id}/shares", ShareRead},
		{"GET", "GET /v1/users/{id}/shares/{share_id}", ShareRead},
		{"DELETE", "DELETE /v1/users/{id}/shares/{share_id}", ShareRevoke},
		{"GET", "GET /v1/webhooks", WebhookRead},
		{"PATCH", "PATCH /v1/webhooks/{webhook_id}", WebhookWrite},
		{"GET", "GET /v1/audit", AuditRead},
		{"GET", "GET /v1/providers", ConfigRead},
		{"GET", "GET /v1/tenant", ConfigRead},
		{"GET", "GET /v1/keys", ConfigRead},
		{"POST", "POST /v1/keys", KeyCreate},
		{"DELETE", "DELETE /v1/keys/{key_id}", KeyRevoke},
		{"GET", "GET /v1/pipeline", ConfigRead},
		{"POST", "POST /v1/pipeline/etl-jobs/{job_id}/retry", ConfigWrite},
		{"GET", "GET /v1/admin/providers", ConfigRead},
		{"PATCH", "PATCH /v1/admin/providers/{provider}", ConfigWrite},
		{"DELETE", "DELETE /v1/admin/providers/{provider}", ConfigWrite},
		{"GET", "GET /v1/admin/tenants", ConfigRead},
		{"POST", "POST /v1/admin/tenants", TenantCreate},
		{"GET", "GET /v1/admin/tenants/{tenant_id}", ConfigRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/pipeline", ConfigRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/keys", ConfigRead},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/keys", KeyCreate},
		{"DELETE", "DELETE /v1/admin/tenants/{tenant_id}/keys/{key_id}", KeyRevoke},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/demo-user", DataWrite},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/providers", ConfigRead},
		{"PATCH", "PATCH /v1/admin/tenants/{tenant_id}/providers/{provider}", TenantProvider},
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
