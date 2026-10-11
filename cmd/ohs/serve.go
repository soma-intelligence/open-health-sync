// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/soma-intelligence/open-health-sync/internal/app"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/mcp"
)

const serveUsage = "usage: ohs serve"

// runServe runs the roles of OHS_ROLES until SIGINT or SIGTERM.
func runServe(rest []string, _ io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
		return usageErr(serveUsage)
	}
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err != nil {
		return err
	}
	mcp.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, cfg)
}
