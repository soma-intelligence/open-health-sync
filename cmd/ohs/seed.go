// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

const seedUsage = "usage: ohs seed --demo [--tenant <id>] [--days 90] [--external-ref demo-user] [--seed N]"

// demoTenantName is the tenant ohs seed --demo uses when no --tenant is given.
const demoTenantName = "demo"

// seedOptions is a parsed ohs seed command line.
type seedOptions struct {
	tenant      uuid.UUID // uuid.Nil: the "demo" tenant
	days        int
	externalRef string
	seed        int64
}

// parseSeedArgs reads the seed flags. --demo is required: the command writes
// made-up data, and the flag says so on the command line.
func parseSeedArgs(args []string) (seedOptions, error) {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	demoFlag := fs.Bool("demo", false, "write synthetic demo data")
	tenantFlag := fs.String("tenant", "", "tenant id")
	days := fs.Int("days", demo.DefaultDays, "days of data")
	ref := fs.String("external-ref", "demo-user", "external_ref of the demo user")
	seed := fs.Int64("seed", 1, "random seed")
	if err := fs.Parse(args); err != nil {
		return seedOptions{}, usageErr(fmt.Sprintf("%v (%s)", err, seedUsage))
	}
	if fs.NArg() != 0 {
		return seedOptions{}, usageErr(seedUsage)
	}
	if !*demoFlag {
		return seedOptions{}, usageErr("ohs seed writes synthetic data; pass --demo to confirm (" + seedUsage + ")")
	}
	o := seedOptions{days: *days, externalRef: strings.TrimSpace(*ref), seed: *seed}
	if o.days < 1 || o.days > demo.MaxDays {
		return seedOptions{}, usageErr(fmt.Sprintf("--days must be between 1 and %d", demo.MaxDays))
	}
	if o.externalRef == "" {
		return seedOptions{}, usageErr("--external-ref must not be empty")
	}
	if *tenantFlag != "" {
		id, err := uuid.Parse(*tenantFlag)
		if err != nil || id == uuid.Nil {
			return seedOptions{}, usageErr(fmt.Sprintf("--tenant %q is not a tenant id (ohs admin tenant list shows them)", *tenantFlag))
		}
		o.tenant = id
	}
	return o, nil
}

// runSeed fills an instance with synthetic data for one user: the demo mode.
func runSeed(rest []string, out io.Writer) error {
	o, err := parseSeedArgs(rest)
	if err != nil {
		return err
	}
	url, err := databaseURL()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := tenant.New(pool)

	tenantID, key := o.tenant, ""
	if tenantID == uuid.Nil {
		t, k, err := store.EnsureTenant(ctx, demoTenantName)
		if err != nil {
			return err
		}
		tenantID, key = t.ID, k
	} else if _, err := store.GetTenant(ctx, tenantID); errors.Is(err, tenant.ErrNotFound) {
		return fmt.Errorf("no tenant with id %s", tenantID)
	} else if err != nil {
		return err
	}

	fmt.Fprintf(out, "writing %d days of synthetic data for user %q (tenant %s)\n", o.days, o.externalRef, tenantID)
	res, err := demo.Seed(ctx, store, tenantID, demo.SeedOptions{Days: o.days, Seed: o.seed, ExternalRef: o.externalRef,
		Written: func(provider string, c etl.Counts) { fmt.Fprintf(out, "  %-13s %s\n", provider, formatCounts(c)) }})
	switch {
	case errors.Is(err, tenant.ErrNotFound):
		return fmt.Errorf("no tenant with id %s", tenantID)
	case errors.Is(err, tenant.ErrConflict), errors.Is(err, etl.ErrUserNotActive):
		return fmt.Errorf("the user with external_ref %q is being deleted: pass another --external-ref", o.externalRef)
	case err != nil:
		return err
	}
	fmt.Fprintf(out, "%d rows written, %d already there\n", res.Written, res.Skipped)
	return printSeedSummary(out, tenantID, res.User.ID, key)
}

// formatCounts lists the rows written per table, in table order.
func formatCounts(c etl.Counts) string {
	tables := make([]string, 0, len(c))
	for t := range c {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	parts := make([]string, 0, len(tables))
	for _, t := range tables {
		parts = append(parts, fmt.Sprintf("%s %d", t, c[t].Written))
	}
	if len(parts) == 0 {
		return "nothing new"
	}
	return strings.Join(parts, ", ")
}

// printSeedSummary prints the ids, the key when one was created, and calls to
// try. The key is shown once: it is not stored in clear.
func printSeedSummary(out io.Writer, tenantID, userID uuid.UUID, key string) error {
	base := os.Getenv("OHS_URL")
	if base == "" {
		base = "http://localhost:8080"
	}
	base = strings.TrimRight(base, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "\ntenant  %s\nuser    %s\n", tenantID, userID)
	if key != "" {
		fmt.Fprintf(&b, "API key, shown only this once, store it now:\n%s\n", key)
		fmt.Fprintf(&b, "\nTry it:\n  export OHS_URL=%s OHS_API_KEY=%s\n", base, key)
	} else {
		fmt.Fprintf(&b, "\nNo new API key: use one of this tenant's keys, or make one with\n"+
			"  ohs admin key create --tenant %s\n", tenantID)
		fmt.Fprintf(&b, "\nTry it:\n  export OHS_URL=%s OHS_API_KEY=<tenant API key>\n", base)
	}
	user := "$OHS_URL/v1/users/" + userID.String()
	for _, path := range []string{"/daily", "/sleep", "/fhir/Observation?code=http://loinc.org%7C8867-4&limit=5"} {
		fmt.Fprintf(&b, "  curl -s -H \"Authorization: Bearer $OHS_API_KEY\" \"%s%s\"\n", user, path)
	}
	b.WriteString("More in docs/demo.md.\n")
	_, err := io.WriteString(out, b.String())
	return err
}
