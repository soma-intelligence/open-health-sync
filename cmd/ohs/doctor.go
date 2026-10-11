// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"io"

	_ "github.com/soma-intelligence/open-health-sync/connectors/all"
	"github.com/soma-intelligence/open-health-sync/internal/app"
)

// runDoctor implements `ohs doctor [--quick]`.
func runDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	quick := fs.Bool("quick", false, "check the database and raw store only")
	if err := fs.Parse(args); err != nil {
		return usageErr("usage: ohs doctor [--quick]")
	}
	if fs.NArg() != 0 {
		return usageErr("usage: ohs doctor [--quick]")
	}
	return app.Doctor(context.Background(), out, app.DoctorOptions{Quick: *quick})
}
