// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"io"
)

// usageError is a command line the binary does not accept. main exits 2 for
// it, 1 for any other failure.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usageErr(msg string) error { return usageError{msg: msg} }

func exitCode(err error) int {
	var ue usageError
	if errors.As(err, &ue) {
		return 2
	}
	return 1
}

const topUsage = `Open Health Sync, a self-hosted health data aggregator.

Usage: ohs <command> [flags]

Commands:
  up        apply database migrations, then serve (the container entrypoint)
  serve     run the roles listed in OHS_ROLES (api, collect, etl)
  migrate   apply or inspect the database schema: up, version, latest, down --all
  doctor    check the configuration, database, raw store and connectors
  admin     create tenants, create, list and revoke their API keys, personal setup,
            re-seal stored secrets after an encryption key rotation
  mcp       serve MCP on stdin and stdout for a local agent (Claude Desktop, Claude Code)
  import    load an existing history from a file export: apple-health
  seed      fill an instance with synthetic demo data: seed --demo
  version   print the version and commit
  help      print this help, or the help of one command: ohs help <command>

Configuration comes from OHS_* environment variables, documented in
.env.example and docs/self-hosting.md.

Exit status: 0 on success, 1 on failure (a failed doctor check included),
2 on a command line error.
`

// commandHelp is the text `ohs <command> -h` and `ohs help <command>` print.
var commandHelp = map[string]string{
	"version": "usage: ohs version\n\nPrints the version and the commit the binary was built from.\n",
	"up": `usage: ohs up

Applies the pending database migrations (ohs migrate up), then runs ohs serve.
This is the container entrypoint. Needs OHS_DATABASE_URL and OHS_ENCRYPTION_KEY.
`,
	"serve": `usage: ohs serve

Runs the roles listed in OHS_ROLES (default api,collect,etl) until SIGINT or
SIGTERM. Needs OHS_DATABASE_URL and OHS_ENCRYPTION_KEY, and a database already
migrated (ohs migrate up, or use ohs up). Logs are JSON on stderr at
OHS_LOG_LEVEL (default info).
`,
	"migrate": `usage: ohs migrate <up|version|latest|down --all>

  up          apply every pending migration (safe to run on each start)
  version     print the schema version of the database
  latest      print the schema version this binary migrates to (no database)
  down --all  revert every migration: drops all Open Health Sync data

Needs OHS_DATABASE_URL only (latest needs nothing).
`,
	"doctor": `usage: ohs doctor [--quick]

Checks the configuration, the database, the raw store, OHS_PUBLIC_URL and the
keys of each connector, one "ok", "warn", "skip" or "fail" line per check, and
prints the OAuth callback URL to register for each enabled OAuth connector. It
counts the stored secrets still sealed with a previous or unknown key. Exits 1
when a check fails (a warning does not). With OHS_ENCRYPTION_KEY empty it prints a fresh key to
paste into .env.

  --quick   check the database and the raw store only (container healthcheck)
`,
	"admin": `usage: ohs admin <command>

  tenant create --name <name>    create a tenant and print its first API key
  tenant list                    list the tenants, with their ids
  key create --tenant <id>       add an API key to a tenant (rotation)
  key list --tenant <id>         list a tenant's keys by prefix, with last use
  key revoke --tenant <id> --key <key or prefix>
                                 revoke one key, named by the prefix key list
                                 shows or by the whole key (a whole user
                                 token is revoked the same way)
  personal setup [--person <name>] [--url <url>]
                                 ready a one person install: the "personal"
                                 tenant, a user (default "me") and a new API
                                 key, printed as OHS_URL, OHS_KEY and OHS_USER
                                 lines for the recipes; run it again for a new
                                 key, or with another --person for a second
                                 person of the household

  rekey [--batch <rows>]         re-seal every stored OAuth token and webhook
                                 secret under OHS_ENCRYPTION_KEY, after a key
                                 rotation (docs/key-rotation.md); prints counts
                                 and leaves a value no key opens as it is

A new key (ohs_sk_...) is printed once: store it, it cannot be shown again.
To rotate: key create, deploy the new key, key revoke the old one.
Needs OHS_DATABASE_URL; rekey also needs OHS_ENCRYPTION_KEY and
OHS_PREVIOUS_ENCRYPTION_KEYS.
`,
	"mcp": `usage: ohs mcp

Serves the read-only MCP tools of an Open Health Sync instance on stdin and
stdout, for a local agent such as Claude Desktop or Claude Code. It forwards
every call to the instance's /mcp endpoint and needs no database.

  OHS_MCP_URL   base URL of the instance (default OHS_PUBLIC_URL, then
                http://localhost:8080); https unless the host is local
  OHS_MCP_KEY   a tenant API key (ohs_sk_) or a user token with scope read
                (ohs_ut_)

Setup for each client: docs/mcp.md.
`,
	"import": `usage: ohs import apple-health <export.zip|export.xml> [flags]

Loads the history of an Apple Health export ("Export All Health Data" in the
Health app on the iPhone) into an instance, through the same upload API an
iOS app uses: records, workouts with their routes, ECGs. It reads the zip as
it is, in constant memory, and needs no database.

  --url URL         base URL of the instance (default OHS_URL); https unless
                    the host is local
  --token TOKEN     a user token (ohs_ut_) with the upload scope (default
                    OHS_TOKEN, which keeps it out of the shell history)
  --since DAY       keep samples from this local day on (YYYY-MM-DD)
  --tz ZONE         IANA zone (Europe/Paris) for samples the export gives no
                    HKTimeZone for; without it their days are UTC days
  --batch ID        batch id; the default is derived from the export, so
                    running the same import again uploads nothing twice
  --dry-run         convert and count, upload nothing
  --concurrency N   parts uploaded at once (default 4)
  --allow-http      accept a plain http URL off localhost

Prints counts only, never a health value or the token. Details:
docs/connectors/apple-health.md.
`,
	"seed": `usage: ohs seed --demo [flags]

Fills an instance with synthetic, plausible data for one user, so every read
route (samples, sleep, workouts, daily, FHIR, Open mHealth, exports, MCP) has
something to return without a wearable account. About 90 days from six
sources (oura, whoop, withings, fitbit, polar, apple_health) that overlap like
real devices. The data is made up: every record carries metadata
{"synthetic": true}. It goes through the ETL writer, so running the command
again adds nothing (content hash dedup).

  --demo              required: confirms that synthetic data is wanted
  --tenant ID         tenant to write into; without it, the tenant named
                      "demo" is used, created on the first run with an API
                      key printed once
  --days N            days of data, ending yesterday (default 90, max 365)
  --external-ref REF  external_ref of the user, created or reused
                      (default demo-user)
  --seed N            random seed: the same seed gives the same data
                      (default 1)

Prints the tenant id, the user id, the API key when one was created, and
curl calls to try (OHS_URL, default http://localhost:8080). Needs
OHS_DATABASE_URL. Details: docs/demo.md.
`,
}

// wantsHelp reports whether args ask for help (-h, -help, --help).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}

// help prints the top level help, or the help of the command args names.
func help(args []string, out io.Writer) error {
	if len(args) == 0 {
		_, err := io.WriteString(out, topUsage)
		return err
	}
	text, ok := commandHelp[args[0]]
	if !ok {
		return usageErr(fmt.Sprintf("unknown command %q: run `ohs help` for the list", args[0]))
	}
	_, err := io.WriteString(out, text)
	return err
}
