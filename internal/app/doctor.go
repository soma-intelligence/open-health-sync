// SPDX-License-Identifier: AGPL-3.0-only

// Package app wires Open Health Sync roles together and hosts the operator
// diagnostics (doctor).
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/rekey"
)

// DoctorOptions tunes Doctor.
type DoctorOptions struct {
	// Quick runs the database and raw store checks only (container healthcheck).
	Quick bool
	// Lookup reads one variable; defaults to os.LookupEnv.
	Lookup func(string) (string, bool)
	// Environ is the full KEY=value list; defaults to os.Environ.
	Environ []string
	// Enabled lists enabled connectors; defaults to connector.Enabled.
	Enabled func(map[string]config.ProviderKeys) []connector.Connector
	// All lists every registered connector; defaults to connector.All.
	All func() []connector.Connector
	// Timeout bounds each network check; defaults to 5 seconds.
	Timeout time.Duration
}

// ErrDoctorFailed is returned when at least one check failed.
var ErrDoctorFailed = errors.New("doctor: one or more checks failed")

// NewEncryptionKey returns a fresh base64 encoded 32 byte key.
func NewEncryptionKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Doctor runs the configuration checks and writes one "ok", "warn", "skip" or
// "fail" line per check to out. It returns ErrDoctorFailed when any check
// failed (a warning does not count). Output never contains secrets other than
// a newly generated encryption key, printed on purpose so the operator can
// paste it.
func Doctor(ctx context.Context, out io.Writer, o DoctorOptions) error {
	if o.Lookup == nil {
		o.Lookup = os.LookupEnv
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.Enabled == nil {
		o.Enabled = connector.Enabled
	}
	if o.All == nil {
		o.All = connector.All
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	failed := false
	report := func(name string, err error, detail string) {
		if err != nil {
			failed = true
			fmt.Fprintf(out, "fail  %s: %v\n", name, err)
			return
		}
		if detail != "" {
			fmt.Fprintf(out, "ok    %s (%s)\n", name, detail)
			return
		}
		fmt.Fprintf(out, "ok    %s\n", name)
	}
	get := func(name, def string) string {
		if v, ok := o.Lookup(name); ok && v != "" {
			return v
		}
		return def
	}

	cfg, cfgErr := config.Load(o.Lookup, o.Environ)
	if cfgErr != nil && errors.Is(cfgErr, config.ErrEncryptionKey) && get("OHS_ENCRYPTION_KEY", "") == "" {
		key, err := NewEncryptionKey()
		if err != nil {
			report("config", err, "")
		} else {
			failed = true
			fmt.Fprintf(out, "fail  config: OHS_ENCRYPTION_KEY is empty. Paste this generated key into your .env:\nOHS_ENCRYPTION_KEY=%s\n", key)
		}
	} else {
		report("config", cfgErr, "")
	}

	// Database and raw store use the raw variables so they are still checked
	// when another part of the configuration is invalid.
	dbURL := get("OHS_DATABASE_URL", "")
	dbErr := checkDB(ctx, dbURL, o.Timeout)
	report("database", dbErr, "")
	report("raw store", checkRawStore(ctx, get("OHS_RAW_STORE", "file:///var/lib/ohs/raw"), o.Timeout, !o.Quick), "")

	if !o.Quick {
		public := get("OHS_PUBLIC_URL", "http://localhost:8080")
		report("public url", config.CheckPublicURL(public), public)
		if cfgErr == nil {
			doctorConnectors(out, report, o, cfg, strings.TrimRight(public, "/"))
			if dbErr == nil {
				doctorKeys(ctx, out, report, cfg, dbURL, o.Timeout)
			}
		}
	}
	if failed {
		return ErrDoctorFailed
	}
	return nil
}

func doctorConnectors(out io.Writer, report func(string, error, string), o DoctorOptions, cfg config.Config, public string) {
	enabled := o.Enabled(cfg.Providers)
	on := map[string]bool{}
	for _, c := range enabled {
		on[c.Slug()] = true
	}
	// A keyed connector with some but not all keys set is a mistake worth a
	// failure; one with none is listed as disabled, so the operator sees it
	// exists.
	for _, c := range o.All() {
		k, ok := c.(connector.KeyedConnector)
		if !ok || on[c.Slug()] {
			continue
		}
		var set, missing []string
		for _, f := range k.RequiredKeys() {
			name := fmt.Sprintf("OHS_%s_%s", strings.ToUpper(c.Slug()), f)
			if cfg.Providers[c.Slug()][f] != "" {
				set = append(set, name)
			} else {
				missing = append(missing, name)
			}
		}
		switch {
		case len(set) > 0 && len(missing) > 0:
			report("connector "+c.Slug(), fmt.Errorf("partially configured, missing %s", strings.Join(missing, ", ")), "")
		case len(set) == 0:
			fmt.Fprintf(out, "skip  connector %s: disabled (no keys)\n", c.Slug())
		}
	}
	if len(enabled) == 0 {
		report("connectors", nil, "none enabled")
		return
	}
	for _, c := range enabled {
		slug := c.Slug()
		var missing []string
		if k, ok := c.(connector.KeyedConnector); ok {
			for _, f := range k.RequiredKeys() {
				if cfg.Providers[slug][f] == "" {
					missing = append(missing, fmt.Sprintf("OHS_%s_%s", strings.ToUpper(slug), f))
				}
			}
		}
		if len(missing) > 0 {
			report("connector "+slug, fmt.Errorf("missing %s", strings.Join(missing, ", ")), "")
			continue
		}
		report("connector "+slug, nil, "")
		if _, ok := connector.AsOAuth(c); ok {
			fmt.Fprintf(out, "      %s callback: %s/oauth/%s/callback\n", slug, public, slug)
		}
	}
}

// doctorKeys counts the stored secrets by the key that seals them. None of
// it fails the doctor: a value under a previous key still opens, and one no
// configured key opens is a past loss, only repaired by connecting again.
func doctorKeys(ctx context.Context, out io.Writer, report func(string, error, string), cfg config.Config, dbURL string, timeout time.Duration) {
	const name = "encryption keys"
	box, err := crypto.NewKeyring(cfg.EncryptionKey, cfg.PreviousEncryptionKeys...)
	if err != nil {
		report(name, err, "")
		return
	}
	// Every value is decrypted: allow more than one network round trip.
	ctx, cancel := context.WithTimeout(ctx, 6*timeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, db.ConnURL(dbURL))
	if err != nil {
		report(name, fmt.Errorf("cannot connect: %s", redact(err, dbURL)), "")
		return
	}
	defer conn.Close(context.Background())
	survey, err := rekey.Inspect(ctx, conn, box)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
		fmt.Fprintf(out, "skip  %s: the database is not migrated yet (ohs migrate up)\n", name)
		return
	}
	if err != nil {
		report(name, fmt.Errorf("cannot read the sealed values: %s", redact(err, dbURL)), "")
		return
	}
	var total rekey.Survey
	for _, s := range survey {
		total.Current += s.Current
		total.Legacy += s.Legacy
		total.Previous += s.Previous
		total.Unknown += s.Unknown
	}
	if total.Previous > 0 {
		fmt.Fprintf(out, "warn  %s: %d sealed value(s) still use a previous key: run `ohs admin rekey`\n", name, total.Previous)
	}
	if total.Unknown > 0 {
		fmt.Fprintf(out, "warn  %s: %d sealed value(s) open with no configured key: add the key that sealed them to "+
			"OHS_PREVIOUS_ENCRYPTION_KEYS, or connect those providers again and rotate those webhook secrets\n", name, total.Unknown)
	}
	if box.PreviousKeys() > 0 && total.Previous == 0 {
		fmt.Fprintf(out, "warn  %s: no sealed value uses a previous key any more: "+
			"you can remove OHS_PREVIOUS_ENCRYPTION_KEYS once every process runs with the new key\n", name)
	}
	if total.Previous == 0 && total.Unknown == 0 {
		detail := fmt.Sprintf("%d sealed value(s), all under the active key", total.Current+total.Legacy)
		if total.Legacy > 0 {
			detail += fmt.Sprintf("; %d without a key id, which `ohs admin rekey` adds", total.Legacy)
		}
		report(name, nil, detail)
	}
}

func checkDB(ctx context.Context, dbURL string, timeout time.Duration) error {
	if dbURL == "" {
		return errors.New("OHS_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, db.ConnURL(dbURL))
	if err != nil {
		return fmt.Errorf("cannot connect: %s", redact(err, dbURL))
	}
	defer conn.Close(context.Background())
	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("query failed: %s", redact(err, dbURL))
	}
	return nil
}

// redact strips the connection URL (it holds the password) out of an error.
func redact(err error, dbURL string) string {
	s := err.Error()
	if dbURL != "" {
		s = strings.ReplaceAll(s, dbURL, "<url>")
	}
	if u, perr := url.Parse(dbURL); perr == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			s = strings.ReplaceAll(s, pw, "***")
		}
	}
	return s
}

// checkRawStore opens and lists the store; with write it also writes and
// deletes a probe object (not in --quick: the healthcheck runs every 10 s).
func checkRawStore(ctx context.Context, rawURL string, timeout time.Duration, write bool) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st, err := rawstore.Open(rawURL)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("open: %w%s", err, writeHint(rawURL))
		}
		return fmt.Errorf("open: %w", err)
	}
	// A walk of an unused prefix proves the store is reachable without
	// reading or writing any data.
	if err := st.Walk(ctx, "raw/.ohs-doctor/", func([]string) error { return nil }); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if write {
		if err := rawstore.CheckWritable(ctx, st); err != nil {
			return fmt.Errorf("%w%s", err, writeHint(rawURL))
		}
	}
	return nil
}

// writeHint points at the usual cause of an unwritable folder: the image
// runs as the distroless nonroot user (uid 65532), and a bind mounted host
// folder is owned by someone else.
func writeHint(rawURL string) string {
	if strings.HasPrefix(rawURL, "file:") {
		return " (the container runs as uid 65532: a bind mounted folder needs `chown -R 65532:65532 <folder>` on the host)"
	}
	return " (check the bucket exists and the credentials may write to it)"
}
