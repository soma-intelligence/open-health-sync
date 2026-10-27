// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"testing"
)

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
		{"GET", "GET /v1/admin/tenants/{tenant_id}/providers/{provider}/keys", ConfigRead},
		{"PUT", "PUT /v1/admin/tenants/{tenant_id}/providers/{provider}/keys", TenantProviderKeys},
		{"DELETE", "DELETE /v1/admin/tenants/{tenant_id}/providers/{provider}/keys", TenantProviderKeys},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users", UserRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}", UserRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}/daily", DataRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}/samples/stats", DataRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}/coverage", DataRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}/freshness", DataRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}/connections", ConnectionRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/usersx", ConfigRead},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/users", UserCreate},
		{"DELETE", "DELETE /v1/admin/tenants/{tenant_id}/users/{id}", UserDelete},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/deletions/{job_id}", UserRead},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/users/{id}/connect-sessions", ConnectionCreate},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/users/{id}/connections/{provider}/sync", ConnectionSync},
		{"DELETE", "DELETE /v1/admin/tenants/{tenant_id}/users/{id}/connections/{provider}", ConnectionDelete},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/users/{id}/sync-runs", ConnectionRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/webhooks", WebhookRead},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/webhooks", WebhookWrite},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/webhooks/{webhook_id}/deliveries/{delivery_id}/replay", WebhookWrite},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/pipeline/etl-jobs", ConfigRead},
		{"POST", "POST /v1/admin/tenants/{tenant_id}/pipeline/etl-jobs/{job_id}/retry", ConfigWrite},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/audit", AuditRead},
		{"GET", "GET /v1/admin/tenants/{tenant_id}/auditx", ConfigRead},
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

type stringer string

func (s stringer) String() string { return string(s) }

// Whatever the type of a detail value, no NUL reaches the INSERT, numbers
// keep their digits and clean details are left as encoded.
func TestDetailJSONCleansEveryType(t *testing.T) {
	type pair struct {
		Name string `json:"name"`
		N    int64  `json:"n"`
	}
	got, err := detailJSON(map[string]any{
		"provider": "oura\x00",
		"list":     []string{"a\x00", "b"},
		"struct":   pair{Name: "x\x00y", N: 9007199254740993},
		"ptr":      &pair{Name: "\x00"},
		"named":    stringer("s\x00"),
		"nested":   map[string][]any{"k\x00": {"v\x00", 1.5, true, nil}},
		"bad\xff":  "\xfe",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"bad�":"�","list":["a","b"],"named":"s","nested":{"k":["v",1.5,true,null]},` +
		`"provider":"oura","ptr":{"n":0,"name":""},"struct":{"n":9007199254740993,"name":"xy"}}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if bytes.Contains(got, []byte(`\u0000`)) {
		t.Fatal("a NUL is left")
	}
	clean, err := detailJSON(map[string]any{"job_id": "j1", "n": 2})
	if err != nil || string(clean) != `{"job_id":"j1","n":2}` {
		t.Fatalf("%s %v", clean, err)
	}
}
