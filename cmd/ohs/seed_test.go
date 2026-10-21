// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/etl"
)

func TestParseSeedArgs(t *testing.T) {
	o, err := parseSeedArgs([]string{"--demo"})
	if err != nil {
		t.Fatal(err)
	}
	if o.tenant != uuid.Nil || o.days != 90 || o.externalRef != "demo-user" || o.seed != 1 || o.persona != "mix" {
		t.Fatalf("defaults: %+v", o)
	}
	// A persona brings its own person and user, unless told otherwise.
	o, err = parseSeedArgs([]string{"--demo", "--persona", "runner"})
	if err != nil {
		t.Fatal(err)
	}
	if o.persona != "runner" || o.seed != 2 || o.externalRef != "demo-runner" {
		t.Fatalf("runner: %+v", o)
	}
	o, err = parseSeedArgs([]string{"--demo", "--persona", "weight", "--seed", "0", "--external-ref", "demo-user"})
	if err != nil {
		t.Fatal(err)
	}
	if o.persona != "weight" || o.seed != 0 || o.externalRef != "demo-user" {
		t.Fatalf("weight with flags: %+v", o)
	}
	id := uuid.New()
	o, err = parseSeedArgs([]string{"--demo", "--tenant", id.String(), "--days", "30", "--external-ref", "alice", "--seed", "9"})
	if err != nil {
		t.Fatal(err)
	}
	if o.tenant != id || o.days != 30 || o.externalRef != "alice" || o.seed != 9 || o.persona != "mix" {
		t.Fatalf("flags: %+v", o)
	}
}

func TestParseSeedArgsRefuses(t *testing.T) {
	for _, args := range [][]string{
		nil,                 // no --demo: the intent must be explicit
		{"--days", "10"},    // still no --demo
		{"--demo", "extra"}, // a positional argument
		{"--demo", "--tenant", "acme"},
		{"--demo", "--tenant", "00000000-0000-0000-0000-000000000000"},
		{"--demo", "--days", "0"},
		{"--demo", "--days", "366"},
		{"--demo", "--external-ref", " "},
		{"--demo", "--external-ref", ""},
		{"--demo", "--nope"},
		{"--demo", "--persona", "athlete"},
		{"--demo", "--persona", ""},
	} {
		_, err := parseSeedArgs(args)
		var ue usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v: %v, want a usage error", args, err)
		}
	}
	err := run([]string{"seed"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--demo") || exitCode(err) != 2 {
		t.Fatalf("seed without --demo: %v", err)
	}
}

func TestSeedSummary(t *testing.T) {
	tenantID, userID := uuid.New(), uuid.New()
	t.Setenv("OHS_URL", "http://ohs.local:9000/")
	var buf bytes.Buffer
	if err := printSeedSummary(&buf, tenantID, userID, "ohs_sk_secret"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		tenantID.String(), userID.String(), "ohs_sk_secret", "OHS_URL=http://ohs.local:9000 ",
		"/v1/users/" + userID.String() + "/daily", "/v1/users/" + userID.String() + "/sleep",
		"/v1/users/" + userID.String() + "/fhir/Observation", "Authorization: Bearer $OHS_API_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "ohs_sk_secret") != 2 || strings.Count(out, "curl ") != 3 {
		t.Errorf("summary:\n%s", out)
	}

	t.Setenv("OHS_URL", "")
	buf.Reset()
	if err := printSeedSummary(&buf, tenantID, userID, ""); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); !strings.Contains(out, "http://localhost:8080") ||
		!strings.Contains(out, "ohs admin key create --tenant "+tenantID.String()) || strings.Contains(out, "ohs_sk_") {
		t.Errorf("summary without a key:\n%s", out)
	}
}

func TestFormatCounts(t *testing.T) {
	if got := formatCounts(etl.Counts{}); got != "nothing new" {
		t.Errorf("empty: %q", got)
	}
	got := formatCounts(etl.Counts{"workouts": {Written: 2}, "quantity_samples": {Written: 10, Skipped: 3}})
	if got != "quantity_samples 10, workouts 2" {
		t.Errorf("got %q", got)
	}
}
