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
