// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
)

const serveUsage = "usage: ohs serve [--allow-newer-schema]"

// checkSchema is db.CheckSchema, replaced in tests.
var checkSchema = db.CheckSchema

// runServe runs the roles of OHS_ROLES until SIGINT or SIGTERM.
func runServe(rest []string, _ io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowNewer := fs.Bool("allow-newer-schema", false, "start on a database migrated by a newer release")
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
		return usageErr(serveUsage)
	}
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err != nil {
		return err
	}
	// Every role refuses a database that is not at this binary's schema
	// version: behind, the queries would fail one by one on missing tables
	// and columns; ahead, a newer release may have changed what they mean.
	if err := checkSchema(context.Background(), cfg.DatabaseURL); err != nil {
		if !*allowNewer || !errors.Is(err, db.ErrSchemaAhead) {
			return fmt.Errorf("serve (ohs %s): %w", version, err)
		}
		fmt.Fprintf(os.Stderr, "ohs: warning: --allow-newer-schema: %v\n", err)
	}
	mcp.Version = version
	app.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, cfg)
}
