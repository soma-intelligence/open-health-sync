// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
)

func TestAdminRekey(t *testing.T) {
	url := db.TestDB(t)
	oldKey, newKey, lostKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)
	b64 := base64.StdEncoding.EncodeToString
	t.Setenv("OHS_DATABASE_URL", url)

	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tenantID, userID, lostUser uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*uuid.UUID{&userID, &lostUser} {
		if err := pool.QueryRow(ctx, `INSERT INTO ohs_core.end_users (tenant_id, external_ref) VALUES ($1, gen_random_uuid()::text) RETURNING id`,
			tenantID).Scan(u); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(key []byte, user uuid.UUID) {
		box, err := crypto.NewBox(key)
		if err != nil {
			t.Fatal(err)
		}
		access, err := box.SealFor([]byte("access-token"), collect.SealAAD(tenantID, user, "oura"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO ohs_sync.connections (tenant_id, user_id, provider, access_token_enc)
			VALUES ($1, $2, 'oura', $3)`, tenantID, user, access); err != nil {
			t.Fatal(err)
		}
	}
	insert(oldKey, userID)
	insert(lostKey, lostUser)

	t.Setenv("OHS_ENCRYPTION_KEY", b64(newKey))
	t.Setenv("OHS_PREVIOUS_ENCRYPTION_KEYS", b64(oldKey))
	var buf bytes.Buffer
	err = run([]string{"admin", "rekey", "--batch", "1"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "1 sealed value(s)") {
		t.Fatalf("a value no key opens fails the command: %v\n%s", err, buf.String())
	}
	out := buf.String()
	row := regexp.MustCompile(`ohs_sync\.connections\.access_token_enc +(\d+) +(\d+) +(\d+)`).FindStringSubmatch(out)
	if row == nil || row[1] != "1" || row[2] != "0" || row[3] != "1" {
		t.Fatalf("want 1 rewritten, 0 current, 1 failed:\n%s", out)
	}
	if !strings.Contains(out, "failed  ohs_sync.connections.access_token_enc") || !strings.Contains(out, lostUser.String()) {
		t.Fatalf("the failed row is named:\n%s", out)
	}
	if strings.Contains(out, "access-token") || strings.Contains(out, b64(newKey)) || strings.Contains(out, b64(oldKey)) {
		t.Fatalf("output leaks a secret:\n%s", out)
	}

	// The token now opens with the new key alone; a second run rewrites nothing.
	alone, _ := crypto.NewBox(newKey)
	var access []byte
	if err := pool.QueryRow(ctx, `SELECT access_token_enc FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID).Scan(&access); err != nil {
		t.Fatal(err)
	}
	if plain, err := alone.OpenFor(access, collect.SealAAD(tenantID, userID, "oura")); err != nil || string(plain) != "access-token" {
		t.Fatalf("rekeyed token: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2`, tenantID, lostUser); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := run([]string{"admin", "rekey"}, &buf); err != nil {
		t.Fatalf("second run: %v\n%s", err, buf.String())
	}
	row = regexp.MustCompile(`total +(\d+) +(\d+) +(\d+)`).FindStringSubmatch(buf.String())
	if row == nil || row[1] != "0" || row[2] != "1" || row[3] != "0" {
		t.Fatalf("want 0 rewritten, 1 current, 0 failed:\n%s", buf.String())
	}
}
