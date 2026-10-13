// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

func TestSeedDemo(t *testing.T) {
	url := db.TestDB(t)
	t.Setenv("OHS_DATABASE_URL", url)
	t.Setenv("OHS_ENCRYPTION_KEY", "")
	t.Setenv("OHS_URL", "")
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var buf bytes.Buffer
	if err := run([]string{"seed", "--demo", "--days", "14"}, &buf); err != nil {
		t.Fatalf("seed: %v\n%s", err, buf.String())
	}
	out := buf.String()
	key := regexp.MustCompile(`ohs_sk_[0-9A-Za-z]{43}`).FindString(out)
	if key == "" {
		t.Fatalf("no API key printed:\n%s", out)
	}
	if _, err := tenant.New(pool).LookupKey(ctx, auth.Hash(key)); err != nil {
		t.Fatalf("printed key does not authenticate: %v", err)
	}
	tenantID := regexp.MustCompile(`tenant  ([0-9a-f-]{36})`).FindStringSubmatch(out)[1]
	userID := regexp.MustCompile(`user    ([0-9a-f-]{36})`).FindStringSubmatch(out)[1]
	if !strings.Contains(out, "OHS_URL=http://localhost:8080") || !strings.Contains(out, "$OHS_URL/v1/users/"+userID+"/daily") {
		t.Fatalf("no curl examples:\n%s", out)
	}

	counts := func() map[string]int {
		t.Helper()
		got := map[string]int{}
		for _, table := range []string{"quantity_samples", "sleep_sessions", "sleep_phases", "workouts",
			"route_points", "daily_summaries"} {
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.`+table+
				` WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			got[table] = n
		}
		return got
	}
	first := counts()
	for table, n := range first {
		if n == 0 {
			t.Errorf("%s: no rows", table)
		}
	}
	var providers, unmarked int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT provider),
		count(*) FILTER (WHERE NOT coalesce((metadata->>'synthetic')::boolean, false))
		FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID).Scan(&providers, &unmarked); err != nil {
		t.Fatal(err)
	}
	if providers != 6 || unmarked != 0 {
		t.Fatalf("%d providers, %d samples not marked synthetic", providers, unmarked)
	}
	var derived int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'fitbit' AND step_count > 0`, tenantID, userID).Scan(&derived); err != nil {
		t.Fatal(err)
	}
	if derived < 13 {
		t.Fatalf("%d derived fitbit days, want 14", derived)
	}

	// A second run reuses the tenant and the user and adds nothing.
	buf.Reset()
	if err := run([]string{"seed", "--demo", "--days", "14"}, &buf); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if strings.Contains(buf.String(), "ohs_sk_") || !strings.Contains(buf.String(), "\n0 rows written") ||
		!strings.Contains(buf.String(), userID) {
		t.Fatalf("second run:\n%s", buf.String())
	}
	if again := counts(); !equalCounts(first, again) {
		t.Fatalf("second run changed the rows: %v -> %v", first, again)
	}

	// --tenant with another external ref: another user of the same tenant.
	buf.Reset()
	if err := run([]string{"seed", "--demo", "--days", "2", "--tenant", tenantID, "--external-ref", "other"}, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "ohs_sk_") || strings.Contains(buf.String(), "user    "+userID) {
		t.Fatalf("--tenant run:\n%s", buf.String())
	}
	if err := run([]string{"seed", "--demo", "--tenant", uuid.NewString()}, &buf); err == nil ||
		!strings.Contains(err.Error(), "no tenant") {
		t.Fatalf("unknown tenant: %v", err)
	}
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
