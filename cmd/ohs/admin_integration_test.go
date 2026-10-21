// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

func TestAdminTenantCreate(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	t.Setenv("OHS_ENCRYPTION_KEY", "")

	var buf bytes.Buffer
	if err := run([]string{"admin", "tenant", "create", "--name", "Acme"}, &buf); err != nil {
		t.Fatalf("admin tenant create: %v", err)
	}
	key := regexp.MustCompile(`ohs_sk_[0-9A-Za-z]{43}`).FindString(buf.String())
	if key == "" || strings.Count(buf.String(), "ohs_sk_") != 1 {
		t.Fatalf("the key is printed exactly once: %q", buf.String())
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := tenant.New(pool).LookupKey(context.Background(), auth.Hash(key)); err != nil {
		t.Fatalf("printed key does not authenticate: %v", err)
	}
}

func TestAdminKeyRotation(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	var buf bytes.Buffer
	if err := run([]string{"admin", "tenant", "create", "--name", "Acme"}, &buf); err != nil {
		t.Fatal(err)
	}
	first := regexp.MustCompile(`ohs_sk_[0-9A-Za-z]{43}`).FindString(buf.String())
	id := regexp.MustCompile(`id ([0-9a-f-]{36})`).FindStringSubmatch(buf.String())[1]

	buf.Reset()
	if err := run([]string{"admin", "tenant", "list"}, &buf); err != nil || !strings.Contains(buf.String(), id) ||
		!strings.Contains(buf.String(), "Acme") {
		t.Fatalf("tenant list: %v %q", err, buf.String())
	}

	buf.Reset()
	if err := run([]string{"admin", "key", "create", "--tenant", id}, &buf); err != nil {
		t.Fatal(err)
	}
	second := regexp.MustCompile(`ohs_sk_[0-9A-Za-z]{43}`).FindString(buf.String())
	if second == "" || second == first {
		t.Fatalf("key create: %q", buf.String())
	}

	buf.Reset()
	if err := run([]string{"admin", "key", "list", "--tenant", id}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), first[:12]) || !strings.Contains(buf.String(), second[:12]) ||
		strings.Contains(buf.String(), first) {
		t.Fatalf("key list shows prefixes only: %q", buf.String())
	}

	buf.Reset()
	if err := run([]string{"admin", "key", "revoke", "--tenant", id, "--key", first[:12]}, &buf); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := tenant.New(pool).LookupKey(context.Background(), auth.Hash(first)); err == nil {
		t.Fatal("the revoked key still authenticates")
	}
	if _, err := tenant.New(pool).LookupKey(context.Background(), auth.Hash(second)); err != nil {
		t.Fatalf("the new key: %v", err)
	}
	if err := run([]string{"admin", "key", "revoke", "--tenant", id, "--key", "ohs_sk_zzzzz"}, &buf); err == nil {
		t.Fatal("an unknown prefix must fail")
	}
	if err := run([]string{"admin", "key", "create", "--tenant", "not-a-uuid"}, &buf); err == nil {
		t.Fatal("a bad tenant id must fail")
	}
}

func TestAdminKeyEdges(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	var buf bytes.Buffer
	if err := run([]string{"admin", "tenant", "create", "--name", "Solo"}, &buf); err != nil {
		t.Fatal(err)
	}
	key := regexp.MustCompile(`ohs_sk_[0-9A-Za-z]{43}`).FindString(buf.String())
	id := regexp.MustCompile(`id ([0-9a-f-]{36})`).FindStringSubmatch(buf.String())[1]

	// An unknown tenant is an error, not an empty list.
	err := run([]string{"admin", "key", "list", "--tenant", "00000000-0000-0000-0000-000000000000"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "no tenant") {
		t.Fatalf("key list unknown tenant: %v", err)
	}
	// A not found revoke never echoes more than the prefix of the argument.
	err = run([]string{"admin", "key", "revoke", "--tenant", id, "--key", "ohs_sk_" + strings.Repeat("Z", 43)}, &buf)
	if err == nil || strings.Contains(err.Error(), strings.Repeat("Z", 20)) {
		t.Fatalf("revoke echoed the key: %v", err)
	}
	// Revoking the last active key warns.
	buf.Reset()
	if err := run([]string{"admin", "key", "revoke", "--tenant", id, "--key", key}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no active API key") {
		t.Fatalf("no warning: %q", buf.String())
	}
}

func TestAdminPersonalSetup(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	t.Setenv("OHS_PUBLIC_URL", "http://localhost:8081/")
	env := func(out, name string) string {
		m := regexp.MustCompile(`(?m)^` + name + `=(.*)$`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no %s line in %q", name, out)
		}
		return m[1]
	}
	var first bytes.Buffer
	if err := run([]string{"admin", "personal", "setup"}, &first); err != nil {
		t.Fatalf("personal setup: %v", err)
	}
	if got := env(first.String(), "OHS_URL"); got != "http://localhost:8081" {
		t.Fatalf("OHS_URL = %q, want OHS_PUBLIC_URL without the trailing slash", got)
	}
	var second bytes.Buffer
	if err := run([]string{"admin", "personal", "setup"}, &second); err != nil {
		t.Fatalf("personal setup again: %v", err)
	}
	if env(first.String(), "OHS_USER") != env(second.String(), "OHS_USER") {
		t.Fatal("a second run must reuse the user")
	}
	if env(first.String(), "OHS_KEY") == env(second.String(), "OHS_KEY") {
		t.Fatal("a second run must print a new key")
	}

	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := tenant.New(pool)
	var tenantID string
	for _, out := range []string{first.String(), second.String()} {
		p, err := s.LookupKey(context.Background(), auth.Hash(env(out, "OHS_KEY")))
		if err != nil {
			t.Fatalf("printed key does not authenticate: %v", err)
		}
		if tenantID != "" && p.TenantID.String() != tenantID {
			t.Fatal("both keys must belong to the one personal tenant")
		}
		tenantID = p.TenantID.String()
	}

	var other bytes.Buffer
	if err := run([]string{"admin", "personal", "setup", "--person", "alex", "--url", "https://health.example.org"}, &other); err != nil {
		t.Fatalf("personal setup --person alex: %v", err)
	}
	if env(other.String(), "OHS_USER") == env(first.String(), "OHS_USER") {
		t.Fatal("another --person must get another user")
	}
	if got := env(other.String(), "OHS_URL"); got != "https://health.example.org" {
		t.Fatalf("OHS_URL = %q, want the --url value", got)
	}
	if err := run([]string{"admin", "personal", "setup", "--person", "a b"}, io.Discard); err == nil {
		t.Fatal("a --person with a space must be refused")
	}
}

// The first tenant is the instance admin; tenant create --admin and tenant
// admin name others, --off clears the mark, tenant list shows it.
func TestAdminTenantAdmin(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	idOf := regexp.MustCompile(`id ([0-9a-f-]{36})`)
	var buf bytes.Buffer
	if err := run([]string{"admin", "tenant", "create", "--name", "Ops"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "instance admin") {
		t.Fatalf("the first tenant is the admin: %q", buf.String())
	}
	ops := idOf.FindStringSubmatch(buf.String())[1]
	buf.Reset()
	if err := run([]string{"admin", "tenant", "create", "--name", "Acme"}, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "instance admin") {
		t.Fatalf("a later tenant is not: %q", buf.String())
	}
	acme := idOf.FindStringSubmatch(buf.String())[1]
	buf.Reset()
	if err := run([]string{"admin", "tenant", "create", "--name", "Lab", "--admin"}, &buf); err != nil ||
		!strings.Contains(buf.String(), "instance admin") {
		t.Fatalf("--admin: %v %q", err, buf.String())
	}

	buf.Reset()
	if err := run([]string{"admin", "tenant", "admin", "--tenant", acme}, &buf); err != nil ||
		!strings.Contains(buf.String(), "is now an instance admin") {
		t.Fatalf("tenant admin: %v %q", err, buf.String())
	}
	buf.Reset()
	if err := run([]string{"admin", "tenant", "admin", "--tenant", ops, "--off"}, &buf); err != nil ||
		!strings.Contains(buf.String(), "no longer") {
		t.Fatalf("tenant admin --off: %v %q", err, buf.String())
	}
	buf.Reset()
	if err := run([]string{"admin", "tenant", "list"}, &buf); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		switch {
		case strings.Contains(line, ops) && strings.HasSuffix(strings.TrimSpace(line), "yes"):
			t.Errorf("ops is no longer admin: %q", line)
		case strings.Contains(line, acme) && !strings.HasSuffix(strings.TrimSpace(line), "yes"):
			t.Errorf("acme is admin: %q", line)
		}
	}
	if err := run([]string{"admin", "tenant", "admin", "--tenant", "00000000-0000-0000-0000-000000000001"}, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "no tenant") {
		t.Fatalf("unknown tenant: %v", err)
	}
	if err := run([]string{"admin", "tenant", "admin", "--tenant", "nope"}, io.Discard); err == nil {
		t.Fatal("a malformed id is refused")
	}
}
