// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth/export"
)

const importUsage = "usage: ohs import apple-health <export.zip|export.xml> [--url URL] [--token TOKEN] " +
	"[--since YYYY-MM-DD] [--tz ZONE] [--batch ID] [--dry-run]: run `ohs import --help` for details"

var batchPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// runImport loads an existing history into an instance from a file export,
// over the same upload API a device uses. Today the one source is Apple
// Health's "Export All Health Data" archive.
func runImport(rest []string, out io.Writer) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	baseURL := fs.String("url", os.Getenv("OHS_URL"), "base URL of the instance")
	token := fs.String("token", os.Getenv("OHS_TOKEN"), "user token with the upload scope")
	since := fs.String("since", "", "keep samples from this local day on")
	zone := fs.String("tz", "", "IANA zone for samples without HKTimeZone")
	batch := fs.String("batch", "", "batch id (default: derived from the export)")
	dryRun := fs.Bool("dry-run", false, "convert and count, upload nothing")
	allowHTTP := fs.Bool("allow-http", false, "accept a plain http URL that is not localhost")
	concurrency := fs.Int("concurrency", 4, "parts uploaded at once")
	// Flags may come before or after the positional arguments.
	var args []string
	for {
		if err := fs.Parse(rest); err != nil {
			return usageErr(fmt.Sprintf("%v (%s)", err, importUsage))
		}
		if fs.NArg() == 0 {
			break
		}
		args = append(args, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(args) == 0 {
		return usageErr(importUsage)
	}
	if args[0] != "apple-health" {
		return usageErr(fmt.Sprintf("unknown source %q: the one source is apple-health (%s)", args[0], importUsage))
	}
	if len(args) != 2 {
		return usageErr("name one export.zip or export.xml (" + importUsage + ")")
	}
	opts := export.Options{Since: *since, Zone: *zone}
	if err := opts.Validate(); err != nil {
		return usageErr(err.Error())
	}
	if *concurrency < 1 || *concurrency > 16 {
		return usageErr("--concurrency must be from 1 to 16")
	}
	if *batch != "" && !batchPattern.MatchString(*batch) {
		return usageErr("--batch takes letters, digits, _ and -, at most 100")
	}
	var endpoint string
	if !*dryRun {
		var err error
		if endpoint, err = ingestEndpoint(*baseURL, *allowHTTP); err != nil {
			return err
		}
		if strings.TrimSpace(*token) == "" {
			return usageErr("--token (or OHS_TOKEN) is required: a user token (ohs_ut_) with the upload scope")
		}
	}

	e, err := export.Open(args[1])
	if err != nil {
		return err
	}
	defer e.Close()
	if *batch == "" {
		date, err := e.ExportDate()
		if err != nil {
			return err
		}
		size, err := e.XMLSize()
		if err != nil {
			return err
		}
		*batch = export.BatchID(date, size, opts)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var up *export.Uploader
	if *dryRun {
		fmt.Fprintf(out, "dry run: converting %s, nothing is uploaded\n", baseName(args[1]))
	} else {
		up = &export.Uploader{Endpoint: endpoint, Token: strings.TrimSpace(*token), Batch: *batch}
		fmt.Fprintf(out, "importing %s into %s, batch %s\n", baseName(args[1]), redacted(endpoint), *batch)
	}
	sum, err := export.Run(ctx, e, opts, up, *concurrency, progressPrinter(out, *dryRun))
	printSummary(out, sum, *dryRun)
	if err != nil {
		if up != nil && sum.Uploaded > 0 {
			return fmt.Errorf("%w (run the same command again to resume: parts already stored are answered as duplicates)", err)
		}
		return err
	}
	return nil
}

// baseName shows a path without its directories, which may name a person.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ingestEndpoint appends the ingest path to the base URL. Plain http is
// refused off localhost unless asked for: the token would cross the network
// in clear.
func ingestEndpoint(base string, allowHTTP bool) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", usageErr("--url (or OHS_URL) is required: the base URL of the instance, for example https://ohs.example.org")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", usageErr("--url must be an http or https URL")
	}
	if u.User != nil {
		return "", usageErr("--url must not hold a user or password: pass the token with --token")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) && !allowHTTP {
		return "", usageErr(fmt.Sprintf("--url %s sends the token in clear: use https, or --allow-http on a network you trust", u.Redacted()))
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/ingest/apple-health"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func redacted(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Redacted()
}

// progressPrinter prints the running totals at most every two seconds.
func progressPrinter(out io.Writer, dryRun bool) func(export.Summary) {
	var (
		mu   sync.Mutex
		last time.Time
	)
	return func(s export.Summary) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < 2*time.Second {
			return
		}
		last = time.Now()
		if dryRun {
			fmt.Fprintf(out, "  %d records read, %d parts\n", s.Records, s.Parts)
			return
		}
		fmt.Fprintf(out, "  %d records read, %d parts uploaded (%d already stored)\n", s.Records, s.Uploaded, s.Duplicates)
	}
}

// printSummary prints counts and type identifiers, never a health value.
func printSummary(out io.Writer, s export.Summary, dryRun bool) {
	row := func(label string, n int) { fmt.Fprintf(out, "  %-18s%d\n", label, n) }
	if dryRun {
		fmt.Fprintln(out, "dry run done:")
	} else {
		fmt.Fprintf(out, "done, batch %s:\n", s.Batch)
	}
	row("records read", s.Records)
	row("lines", s.Lines)
	row("route points", s.RoutePoints)
	row("parts", s.Parts)
	if !dryRun {
		row("uploaded", s.Uploaded)
		row("already stored", s.Duplicates)
	}
	row("before --since", s.BeforeSince)
	if s.NoRoute > 0 {
		row("route not found", s.NoRoute)
	}
	byType := func(title string, m map[string]int) {
		if len(m) == 0 {
			return
		}
		fmt.Fprintln(out, title)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "    %-60s %d\n", k, m[k])
		}
	}
	byType("  skipped, not stored by Open Health Sync:", s.Skipped)
	byType("  skipped, could not be read:", s.Invalid)
}
