// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// key create --scopes makes a key that holds those scopes: printed with
// them, listed with them, recorded with them. Without the flag the key does
// everything.
func TestAdminKeyCreateWithScopes(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	var buf bytes.Buffer
	if err := run([]string{"admin", "tenant", "create", "--name", "Acme"}, &buf); err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`id ([0-9a-f-]{36})`).FindStringSubmatch(buf.String())[1]

	buf.Reset()
	if err := run([]string{"admin", "key", "create", "--tenant", id, "--scopes", "data:read, users:read"}, &buf); err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`ohs_sk_[0-9A-Za-z]{43}`).FindString(buf.String())
	if key == "" || !strings.Contains(buf.String(), "scopes users:read,data:read") {
		t.Fatalf("key create --scopes: %q", buf.String())
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	p, err := tenant.New(pool).LookupKey(context.Background(), auth.Hash(key))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Can(auth.ScopeDataRead) || !p.Can(auth.ScopeUsersRead) || p.Can(auth.ScopeUsersWrite) || len(p.KeyScopes) != 2 {
		t.Fatalf("the key holds %v", p.KeyScopes)
	}
	var detail string
	if err := pool.QueryRow(context.Background(),
		`SELECT detail::text FROM ohs_core.audit_events WHERE action = 'key.create' ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "users:read") || !strings.Contains(detail, "data:read") {
		t.Fatalf("the audit event names the scopes: %s", detail)
	}

	buf.Reset()
	if err := run([]string{"admin", "key", "list", "--tenant", id}, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 || !strings.HasSuffix(strings.TrimSpace(lines[0]), "SCOPES") ||
		!strings.HasSuffix(strings.TrimSpace(lines[1]), "*") || !strings.HasSuffix(strings.TrimSpace(lines[2]), "users:read,data:read") {
		t.Fatalf("key list: %q", buf.String())
	}

	// No flag, and an empty one: everything, and nothing said about scopes.
	for _, args := range [][]string{{}, {"--scopes", ""}, {"--scopes", "*"}} {
		buf.Reset()
		if err := run(append([]string{"admin", "key", "create", "--tenant", id}, args...), &buf); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "scopes") {
			t.Fatalf("key create %v: %q", args, buf.String())
		}
	}
}
