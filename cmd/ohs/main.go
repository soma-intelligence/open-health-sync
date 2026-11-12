// SPDX-License-Identifier: AGPL-3.0-only

// Command ohs is the single Open Health Sync binary.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
)

// Injected with -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return usageErr("usage: ohs <command>, one of up, serve, migrate, doctor, admin, mcp, import, seed, version: run `ohs help` for details")
	}
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "-help", "--help":
		return help(rest, out)
	case "--version", "-v":
		name, rest = "version", nil
	}
	if _, known := commandHelp[name]; known && wantsHelp(rest) {
		return help([]string{name}, out)
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch name {
	case "version":
		if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
			return usageErr("usage: ohs version")
		}
		_, err := fmt.Fprintf(out, "ohs %s (%s)\n", version, commit)
		return err
	case "migrate":
		return runMigrate(rest, out)
	case "serve":
		return runServe(rest, out)
	case "admin":
		return runAdmin(rest, out)
	case "doctor":
		return runDoctor(rest, out)
	case "up":
		return runUp(rest, out)
	case "mcp":
		return runMCP(rest, out)
	case "import":
		return runImport(rest, out)
	case "seed":
		return runSeed(rest, out)
	default:
		return usageErr(fmt.Sprintf("unknown command %q: run `ohs help` for the list", name))
	}
}

const migrateUsage = "usage: ohs migrate <up|down --all|version|latest|force <version> --yes>"

var errForceNeedsYes = usageErr("migrate force rewrites the recorded schema version without running any migration; " +
	"check that the schema really is at that version (docs/self-hosting.md, \"Recover from a failed migration\"), " +
	"then run `ohs migrate force <version> --yes` to confirm")

var errDownNeedsAll = usageErr("migrate down reverts every migration and drops all Open Health Sync data " +
	"(schemas ohs_core, ohs_sync, ohs_data); run `ohs migrate down --all` to confirm")

func runMigrate(rest []string, out io.Writer) error {
	// Flags may come before or after the action: parse the whole list,
	// collecting positional arguments as we go.
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	all := fs.Bool("all", false, "confirm that down reverts every migration")
	yes := fs.Bool("yes", false, "confirm that force rewrites the recorded schema version")
	var args []string
	for {
		if err := fs.Parse(rest); err != nil {
			return usageErr(fmt.Sprintf("%v (%s)", err, migrateUsage))
		}
		if fs.NArg() == 0 {
			break
		}
		args = append(args, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(args) == 0 {
		return usageErr(migrateUsage)
	}
	action := args[0]
	if action == "force" {
		return runMigrateForce(args[1:], *yes, out)
	}
	if len(args) != 1 {
		return usageErr(migrateUsage)
	}
	if action != "up" && action != "down" && action != "version" && action != "latest" {
		return usageErr(migrateUsage)
	}
	if action == "latest" {
		// The binary's own migrations: no database needed.
		v, err := db.LatestVersion()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "latest schema version %d\n", v)
		return err
	}
	if action == "down" && !*all {
		return errDownNeedsAll
	}
	url, err := databaseURL()
	if err != nil {
		return err
	}
	if action == "version" {
		v, dirty, err := db.Version(url)
		if err != nil {
			return err
		}
		suffix := ""
		if dirty {
			suffix = " (dirty)"
		}
		_, err = fmt.Fprintf(out, "schema version %d%s\n", v, suffix)
		return err
	}
	if err := db.Migrate(url, action); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "migrate %s: ok\n", action)
	return err
}

// runMigrateForce implements `ohs migrate force <version> --yes`: it records
// version as the schema version and clears the dirty mark a killed migration
// leaves, running no migration. db.Force refuses a schema that is not dirty
// and a version that is neither the dirty one nor next to it.
func runMigrateForce(args []string, yes bool, out io.Writer) error {
	if len(args) != 1 {
		return usageErr(migrateUsage)
	}
	target, err := strconv.ParseUint(args[0], 10, 31)
	if err != nil {
		return usageErr(fmt.Sprintf("migrate force: %q is not a schema version (%s)", args[0], migrateUsage))
	}
	if !yes {
		return errForceNeedsYes
	}
	url, err := databaseURL()
	if err != nil {
		return err
	}
	before, dirty, err := db.Version(url)
	if err != nil {
		return err
	}
	if err := db.Force(url, uint(target)); err != nil {
		return err
	}
	was := ""
	if dirty {
		was = ", dirty"
	}
	_, err = fmt.Fprintf(out, "migrate force: schema version set to %d, clean (was %d%s); run `ohs migrate up` next\n",
		target, before, was)
	return err
}

// databaseURL reads OHS_DATABASE_URL through config.Load. migrate and admin
// need neither the encryption keys nor the settings only serve reads, so an
// error about OHS_ENCRYPTION_KEY, OHS_PREVIOUS_ENCRYPTION_KEYS, OHS_PUBLIC_URL
// or OHS_LOG_LEVEL falls back to the variable alone; any other configuration
// error is returned.
func databaseURL() (string, error) {
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err == nil {
		return cfg.DatabaseURL, nil
	}
	if !errors.Is(err, config.ErrEncryptionKey) && !errors.Is(err, config.ErrPreviousEncryptionKeys) &&
		!errors.Is(err, config.ErrPublicURL) && !errors.Is(err, config.ErrLogLevel) {
		return "", err
	}
	return os.Getenv("OHS_DATABASE_URL"), nil // Load checked it is set
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ohs:", err)
		os.Exit(exitCode(err))
	}
}
