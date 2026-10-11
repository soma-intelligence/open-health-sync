// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

const adminUsage = "usage: ohs admin tenant create --name <name>"

// runAdmin handles `ohs admin tenant create --name <name>`: it creates a
// tenant and prints its first API key, once. The key is never stored in clear
// and cannot be shown again.
func runAdmin(rest []string, out io.Writer) error {
	if len(rest) < 2 || rest[0] != "tenant" || rest[1] != "create" {
		return usageErr(adminUsage)
	}
	fs := flag.NewFlagSet("admin tenant create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "tenant name")
	if err := fs.Parse(rest[2:]); err != nil || fs.NArg() != 0 || strings.TrimSpace(*name) == "" {
		return usageErr(adminUsage)
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
	t, key, err := tenant.New(pool).CreateTenant(ctx, strings.TrimSpace(*name))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "tenant %s created (id %s)\nAPI key, shown only this once, store it now:\n%s\n", t.Name, t.ID, key)
	return err
}
