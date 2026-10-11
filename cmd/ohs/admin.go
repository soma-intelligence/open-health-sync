// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

const adminUsage = "usage: ohs admin tenant create --name <name> | tenant list | " +
	"key create --tenant <id> | key list --tenant <id> | key revoke --tenant <id> --key <key or prefix>"

// runAdmin handles the tenant and API key commands. A key is printed once,
// when it is created: it is never stored in clear and cannot be shown again.
func runAdmin(rest []string, out io.Writer) error {
	if len(rest) < 2 {
		return usageErr(adminUsage)
	}
	// Each command has its own flags: a flag another command takes is refused.
	fs := flag.NewFlagSet("admin "+rest[0]+" "+rest[1], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var name, tenantFlag, key *string
	cmd := rest[0] + " " + rest[1]
	switch cmd {
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
			_, err = fmt.Fprintf(out, "tenant %s created (id %s)\nAPI key, shown only this once, store it now:\n%s\n", t.Name, t.ID, k)
			return err
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
		do = keyCommand(rest[1], id, k, out)
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
	return do(ctx, tenant.New(pool))
}

func keyCommand(action string, id uuid.UUID, key string, out io.Writer) func(context.Context, *tenant.Store) error {
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
