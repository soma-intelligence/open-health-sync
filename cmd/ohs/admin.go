// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rekey"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

const adminUsage = "usage: ohs admin tenant create --name <name> | tenant list | " +
	"key create --tenant <id> | key list --tenant <id> | key revoke --tenant <id> --key <key or prefix> | " +
	"personal setup [--person <name>] [--url <url>] | rekey [--batch <rows>]"

// personalTenant is the tenant `ohs admin personal setup` keeps a person's
// own data in.
const personalTenant = "personal"

var personPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// runAdmin handles the tenant and API key commands. A key is printed once,
// when it is created: it is never stored in clear and cannot be shown again.
func runAdmin(rest []string, out io.Writer) error {
	if len(rest) > 0 && rest[0] == "rekey" {
		return runRekey(rest[1:], out)
	}
	if len(rest) < 2 {
		return usageErr(adminUsage)
	}
	// Each command has its own flags: a flag another command takes is refused.
	fs := flag.NewFlagSet("admin "+rest[0]+" "+rest[1], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var name, tenantFlag, key, person, publicURL *string
	cmd := rest[0] + " " + rest[1]
	switch cmd {
	case "personal setup":
		person = fs.String("person", "me", "who the data belongs to")
		publicURL = fs.String("url", "", "address the tools call (default OHS_PUBLIC_URL)")
	case "tenant create":
		name = fs.String("name", "", "tenant name")
	case "tenant list":
	case "key create", "key list":
		tenantFlag = fs.String("tenant", "", "tenant id")
	case "key revoke":
		tenantFlag = fs.String("tenant", "", "tenant id")
		key = fs.String("key", "", "API key, or the prefix key list shows")
	default:
		return usageErr(adminUsage)
	}
	if err := fs.Parse(rest[2:]); err != nil || fs.NArg() != 0 {
		return usageErr(adminUsage)
	}
	var do func(context.Context, *tenant.Store) error
	// record writes an audit event for the commands that change a tenant's
	// credentials; pool is set before do runs.
	var pool *pgxpool.Pool
	record := func(ctx context.Context, tenantID uuid.UUID, action string, detail map[string]any) error {
		return audit.Record(ctx, pool, audit.Event{Tenant: tenantID, ActorType: audit.Admin, ActorID: "ohs admin",
			Action: action, Detail: detail})
	}
	switch {
	case cmd == "tenant create":
		n := strings.TrimSpace(*name)
		if n == "" {
			return usageErr(adminUsage)
		}
		do = func(ctx context.Context, s *tenant.Store) error {
			t, k, err := s.CreateTenant(ctx, n)
			if err != nil {
				return err
			}
			if err := record(ctx, t.ID, audit.TenantCreate, map[string]any{"key_prefix": shortKey(k)}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "tenant %s created (id %s)\nAPI key, shown only this once, store it now:\n%s\n", t.Name, t.ID, k)
			return err
		}
	case cmd == "personal setup":
		if !personPattern.MatchString(*person) {
			return usageErr("--person takes 1 to 64 letters, digits, '.', '_' or '-' (" + adminUsage + ")")
		}
		u := *publicURL
		if u == "" {
			u = os.Getenv("OHS_PUBLIC_URL")
		}
		if u == "" {
			u = "http://localhost:8080"
		}
		if err := config.CheckPublicURL(u); err != nil {
			return fmt.Errorf("--url: %w", err)
		}
		do = func(ctx context.Context, s *tenant.Store) error {
			return personalSetup(ctx, s, *person, strings.TrimRight(u, "/"), out, record)
		}
	case cmd == "tenant list":
		do = func(ctx context.Context, s *tenant.Store) error { return listTenants(ctx, s, out) }
	default: // key create, list, revoke
		if *tenantFlag == "" || (key != nil && *key == "") {
			return usageErr(adminUsage)
		}
		id, err := uuid.Parse(*tenantFlag)
		if err != nil {
			return fmt.Errorf("--tenant %q is not a tenant id (ohs admin tenant list shows them)", *tenantFlag)
		}
		k := ""
		if key != nil {
			k = *key
		}
		do = keyCommand(rest[1], id, k, out, record)
	}
	url, err := databaseURL()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if pool, err = db.Open(ctx, url); err != nil {
		return err
	}
	defer pool.Close()
	return do(ctx, tenant.New(pool))
}

// personalSetup makes a one person install ready for the recipes: the
// personal tenant, a user for person, and a fresh API key. It prints them as
// an env file, so `ohs admin personal setup > ~/.ohs.env` is the whole step.
// It is safe to run again: the tenant and the user are reused, and only a new
// key is added (the key is never stored in clear, so an old one cannot be
// shown again).
func personalSetup(ctx context.Context, s *tenant.Store, person, publicURL string, out io.Writer,
	record func(context.Context, uuid.UUID, string, map[string]any) error) error {
	t, key, err := s.EnsureTenant(ctx, personalTenant)
	if err != nil {
		return err
	}
	action := audit.TenantCreate
	if key == "" {
		if key, err = s.CreateKey(ctx, t.ID); err != nil {
			return err
		}
		action = audit.KeyCreate
	}
	if err := record(ctx, t.ID, action, map[string]any{"key_prefix": shortKey(key)}); err != nil {
		return err
	}
	u, created, err := s.EnsureUser(ctx, t.ID, person)
	if errors.Is(err, tenant.ErrConflict) {
		return fmt.Errorf("the user %q is being deleted: wait for the deletion to finish, or pick another --person", person)
	}
	if err != nil {
		return err
	}
	state := "found"
	if created {
		state = "created"
	}
	_, err = fmt.Fprintf(out, `# Open Health Sync, personal setup for %q (user %s, tenant %s %s).
# The key below is new and shown only this once. Keep this file private (chmod 600).
# Earlier keys keep working until revoked: ohs admin key list --tenant %s
OHS_URL=%s
OHS_KEY=%s
OHS_USER=%s
`, person, state, t.Name, t.ID, t.ID, publicURL, key, u.ID)
	return err
}

func keyCommand(action string, id uuid.UUID, key string, out io.Writer,
	record func(context.Context, uuid.UUID, string, map[string]any) error) func(context.Context, *tenant.Store) error {
	return func(ctx context.Context, s *tenant.Store) error {
		switch action {
		case "create":
			k, err := s.CreateKey(ctx, id)
			if errors.Is(err, tenant.ErrNotFound) {
				return fmt.Errorf("no tenant with id %s", id)
			}
			if err != nil {
				return err
			}
			if err := record(ctx, id, audit.KeyCreate, map[string]any{"key_prefix": shortKey(k)}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "API key for tenant %s, shown only this once, store it now:\n%s\n", id, k)
			return err
		case "list":
			keys, err := s.ListKeys(ctx, id)
			if errors.Is(err, tenant.ErrNotFound) {
				return fmt.Errorf("no tenant with id %s", id)
			}
			if err != nil {
				return err
			}
			return printKeys(out, keys)
		default: // revoke
			err := s.RevokeKeyByPrefix(ctx, id, key)
			switch {
			case errors.Is(err, tenant.ErrNotFound):
				return fmt.Errorf("no key %s for tenant %s (ohs admin key list --tenant %s shows them)", shortKey(key), id, id)
			case errors.Is(err, tenant.ErrAmbiguousPrefix):
				return fmt.Errorf("prefix %q matches several keys: pass the whole key", key)
			case err != nil:
				return err
			}
			if err := record(ctx, id, audit.KeyRevoke, map[string]any{"key_prefix": shortKey(key)}); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "key %s revoked\n", shortKey(key)); err != nil {
				return err
			}
			keys, err := s.ListKeys(ctx, id)
			if err != nil {
				return err
			}
			for _, k := range keys {
				if k.RevokedAt == nil {
					return nil
				}
			}
			_, err = fmt.Fprintf(out, "warning: tenant %s has no active API key left; ohs admin key create --tenant %s makes one\n", id, id)
			return err
		}
	}
}

// shortKey never echoes more of a secret than the prefix key list shows.
func shortKey(k string) string {
	if len(k) > 12 {
		return k[:12] + "..."
	}
	return k
}

func listTenants(ctx context.Context, s *tenant.Store, out io.Writer) error {
	ts, err := s.ListTenants(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tCREATED\tACTIVE KEYS")
	for _, t := range ts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", t.ID, t.Name, t.CreatedAt.UTC().Format(time.RFC3339), t.ActiveKeys)
	}
	return w.Flush()
}

func printKeys(out io.Writer, keys []tenant.APIKey) error {
	when := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return t.UTC().Format(time.RFC3339)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PREFIX\tCREATED\tLAST USED\tREVOKED")
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.Prefix, k.CreatedAt.UTC().Format(time.RFC3339), when(k.LastUsedAt), when(k.RevokedAt))
	}
	return w.Flush()
}

// runRekey implements `ohs admin rekey`: every sealed secret not yet under
// OHS_ENCRYPTION_KEY is re-sealed under it (docs/key-rotation.md). It prints
// counts and the rows no configured key opens, never a secret, and fails when
// there is such a row, which it leaves as it is.
func runRekey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("admin rekey", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	batch := fs.Int("batch", rekey.DefaultBatch, "rows read at once")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *batch < 1 {
		return usageErr(adminUsage)
	}
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err != nil {
		return err
	}
	box, err := crypto.NewKeyring(cfg.EncryptionKey, cfg.PreviousEncryptionKeys...)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	fmt.Fprintf(out, "rekey: sealing under OHS_ENCRYPTION_KEY, %d previous key(s) configured\n", box.PreviousKeys())
	counts, err := rekey.Run(ctx, pool, box, rekey.Options{
		Batch: *batch,
		OnFailure: func(f rekey.Failure) {
			fmt.Fprintf(out, "failed  %s %s: no configured key opens it, left as it is\n", f.Column, f.Row)
		},
	})
	var total rekey.Counts
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "COLUMN\tREWRITTEN\tALREADY CURRENT\tFAILED")
	for _, c := range counts {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\n", c.Column, c.Rewritten, c.Current, c.Failed)
		total.Rewritten += c.Rewritten
		total.Current += c.Current
		total.Failed += c.Failed
	}
	fmt.Fprintf(w, "total\t%d\t%d\t%d\n", total.Rewritten, total.Current, total.Failed)
	if ferr := w.Flush(); ferr != nil && err == nil {
		err = ferr
	}
	if err != nil {
		return err
	}
	if total.Failed > 0 {
		return fmt.Errorf("%d sealed value(s) open with no configured key and were left as they are: "+
			"add the key that sealed them to OHS_PREVIOUS_ENCRYPTION_KEYS and run again, "+
			"or reconnect those providers and rotate those webhook secrets", total.Failed)
	}
	return nil
}
