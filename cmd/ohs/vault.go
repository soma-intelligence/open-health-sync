// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

const vaultUsage = "usage: ohs vault status [--check] | seal [--batch <rows>] [--user <id>] [--dry-run] | " +
	"rotate root | rotate tenant <tenant id> | rotate user <tenant id> <user id> | reseal [--batch <rows>] [--user <id>]"

// runVault implements `ohs vault`: what is still stored clear, sealing it,
// and key rotation (docs/specs/vault.md).
func runVault(rest []string, out io.Writer) error {
	if len(rest) == 0 {
		return usageErr(vaultUsage)
	}
	// Ctrl-C stops between two batches: what is done stays done.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch rest[0] {
	case "status":
		return runVaultStatus(ctx, rest[1:], out)
	case "seal":
		return runVaultSeal(ctx, rest[1:], out, false)
	case "reseal":
		return runVaultSeal(ctx, rest[1:], out, true)
	case "rotate":
		return runVaultRotate(ctx, rest[1:], out)
	}
	return usageErr(vaultUsage)
}

// errVaultClear is `ohs vault status --check` on an instance that still
// stores something clear.
var errVaultClear = errors.New("vault: health data is still stored clear; run `ohs vault seal`")

// runVaultStatus prints what is still stored clear. It reads only and needs
// no key.
func runVaultStatus(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("vault status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "exit 1 when something is still stored clear")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageErr(vaultUsage)
	}
	url, err := databaseURL()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	st, err := vault.Status(ctx, pool)
	if err != nil {
		return err
	}
	if st.Sealed() {
		_, err := fmt.Fprintln(out, "vault: nothing is stored clear")
		return err
	}
	fmt.Fprintf(out, "vault: %d rows and %d raw objects are still stored clear\n", st.ClearRows(), st.RawObjects)
	for _, t := range sortedKeys(st.Tables) {
		if st.Tables[t] == 0 {
			continue
		}
		if slices.Contains(vault.PerPoint, t) {
			// Sealed or not, a per-point row is left until it is in its
			// series blob.
			fmt.Fprintf(out, "  %-20s %d (per-point rows to move into series blobs)\n", t, st.Tables[t])
			continue
		}
		fmt.Fprintf(out, "  %-20s %d\n", t, st.Tables[t])
	}
	if st.RawObjects > 0 {
		fmt.Fprintf(out, "  %-20s %d\n", "raw objects", st.RawObjects)
	}
	fmt.Fprintln(out, "run `ohs vault seal` to seal them")
	if *check {
		return errVaultClear
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// vaultEnv is what the commands that open keys work with.
type vaultEnv struct {
	cfg      config.Config
	pool     *pgxpool.Pool
	provider vault.KeyProvider
	vault    *vault.Vault
}

// openVault loads the configuration of the server and opens its database and
// its key provider. from, when set, changes the settings the provider that
// opens keys is built from (rotate root).
func openVault(ctx context.Context, from func(config.Vault) (config.Vault, error)) (*vaultEnv, error) {
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err != nil {
		return nil, err
	}
	settings := cfg.Vault
	if from != nil {
		if settings, err = from(settings); err != nil {
			return nil, err
		}
	}
	provider, err := vault.OpenProvider(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("OHS_VAULT_KEY_PROVIDER: %w", err)
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	v := vault.New(pool, provider, vault.Options{CanOpen: true, CacheUsers: cfg.Vault.CacheUsers, CacheTTL: cfg.Vault.CacheTTL})
	if err := v.Ready(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("vault: the key provider does not open the tenant keys "+
			"(is this the root key of the server? after a root key change, run `ohs vault rotate root`): %w", err)
	}
	return &vaultEnv{cfg: cfg, pool: pool, provider: provider, vault: v}, nil
}

// userFlags collects repeated --user flags.
type userFlags []uuid.UUID

func (u *userFlags) String() string { return fmt.Sprint([]uuid.UUID(*u)) }

func (u *userFlags) Set(s string) error {
	id, err := uuid.Parse(s)
	if err != nil {
		return fmt.Errorf("%q is not a user id", s)
	}
	*u = append(*u, id)
	return nil
}

// runVaultSeal implements `ohs vault seal` and `ohs vault reseal`. Both work
// in batches and may be stopped: running the command again goes on.
func runVaultSeal(ctx context.Context, args []string, out io.Writer, reseal bool) error {
	name := "seal"
	if reseal {
		name = "reseal"
	}
	fs := flag.NewFlagSet("vault "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	batch := fs.Int("batch", vault.DefaultSealBatch, "rows converted in one transaction")
	var users userFlags
	fs.Var(&users, "user", "only this user (may be repeated)")
	dry := fs.Bool("dry-run", false, "change nothing, print what is left to do")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *batch < 1 {
		return usageErr(vaultUsage)
	}
	env, err := openVault(ctx, nil)
	if err != nil {
		return err
	}
	defer env.pool.Close()
	raw, err := rawstore.Open(env.cfg.RawStore)
	if err != nil {
		return fmt.Errorf("OHS_RAW_STORE: %w", err)
	}
	last := time.Now()
	o := vault.SealOptions{
		Rows: etl.Sealer{}, Batch: *batch, Users: users, DryRun: *dry,
		Progress: func(r vault.SealReport) {
			// A line now and then, so a long run shows it is alive.
			if time.Since(last) < 10*time.Second {
				return
			}
			last = time.Now()
			fmt.Fprintf(out, "%s: %d rows, %d raw objects, %d hourly buckets so far\n", name, r.TotalRows(), r.RawObjects, r.Hours)
		},
	}
	run := vault.Seal
	if reseal {
		run = vault.Reseal
	}
	report, err := run(ctx, env.vault, env.pool, raw, o)
	printSealReport(out, name, report, *dry, reseal)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s stopped; run `ohs vault %s` again to go on", name, name)
		}
		return fmt.Errorf("%w (what was converted stays; run `ohs vault %s` again once this is fixed)", err, name)
	}
	if reseal || *dry {
		return nil
	}
	st, err := vault.Status(ctx, env.pool)
	if err != nil {
		return err
	}
	if st.Sealed() {
		_, err = fmt.Fprintln(out, "vault: nothing is stored clear")
		return err
	}
	_, err = fmt.Fprintf(out, "vault: %d rows and %d raw objects are still stored clear (users being deleted, or raw objects the store no longer holds): see `ohs vault status`\n",
		st.ClearRows(), st.RawObjects)
	return err
}

func printSealReport(out io.Writer, name string, r vault.SealReport, dry, reseal bool) {
	var b strings.Builder
	switch {
	case dry && reseal:
		fmt.Fprintf(&b, "reseal --dry-run: %d users still have data under an older key version\n", r.Users)
	case dry:
		fmt.Fprintf(&b, "seal --dry-run: %d rows and %d raw objects to seal\n", r.TotalRows(), r.RawObjects)
	default:
		fmt.Fprintf(&b, "%s: %d users, %d rows, %d raw objects", name, r.Users, r.TotalRows(), r.RawObjects)
		if !reseal {
			fmt.Fprintf(&b, ", %d hourly buckets rebuilt", r.Hours)
		}
		if reseal {
			fmt.Fprintf(&b, ", %d old key versions retired", r.KeysRetired)
		}
		b.WriteString("\n")
	}
	for _, t := range sortedKeys(r.Rows) {
		fmt.Fprintf(&b, "  %-20s %d\n", t, r.Rows[t])
	}
	if r.Duplicates > 0 {
		fmt.Fprintf(&b, "  %d rows removed: the same record was stored twice (the older copy stays)\n", r.Duplicates)
	}
	if r.RawMissing > 0 {
		fmt.Fprintf(&b, "  %d registered raw objects are not in the raw store and were left as they are\n", r.RawMissing)
	}
	_, _ = io.WriteString(out, b.String())
}

// runVaultRotate implements `ohs vault rotate root|tenant|user`.
func runVaultRotate(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usageErr(vaultUsage)
	}
	ids := make([]uuid.UUID, 0, 2)
	for _, a := range args[1:] {
		id, err := uuid.Parse(a)
		if err != nil {
			return usageErr(fmt.Sprintf("%q is not an id (%s)", a, vaultUsage))
		}
		ids = append(ids, id)
	}
	switch {
	case args[0] == "root" && len(ids) == 0:
		return rotateRoot(ctx, out)
	case args[0] == "tenant" && len(ids) == 1:
		env, err := openVault(ctx, nil)
		if err != nil {
			return err
		}
		defer env.pool.Close()
		n, err := vault.RotateTenant(ctx, env.vault, env.pool, ids[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "vault: tenant %s has a new key; %d user keys rewrapped under it. No data was rewritten.\n", ids[0], n)
		return err
	case args[0] == "user" && len(ids) == 2:
		env, err := openVault(ctx, nil)
		if err != nil {
			return err
		}
		defer env.pool.Close()
		version, err := vault.RotateUser(ctx, env.vault, env.pool, ids[0], ids[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "vault: user %s now seals under key version %d. What is stored stays under the older versions "+
			"until `ohs vault reseal --user %s` rewrites it.\n", ids[1], version, ids[1])
		return err
	}
	return usageErr(vaultUsage)
}

// withDerivedRoots adds, to the previous root keys of a local provider with a
// root key of its own, the keys derived from OHS_ENCRYPTION_KEY and from the
// previous encryption keys: what wrapped the tenant keys before the operator
// set OHS_VAULT_ROOT_KEY. Only `ohs vault rotate root` opens with them, so the
// first move to a root key of its own needs no key copied by hand.
func withDerivedRoots(cfg config.Config) func(config.Vault) (config.Vault, error) {
	return func(v config.Vault) (config.Vault, error) {
		if v.Provider != "local" || v.RootDerived {
			return v, nil
		}
		known := append([][]byte{v.RootKey}, v.RootKeyPrevious...)
		previous := append([][]byte(nil), v.RootKeyPrevious...)
		for _, enc := range append([][]byte{cfg.EncryptionKey}, cfg.PreviousEncryptionKeys...) {
			d, err := config.DeriveVaultRootKey(enc)
			if err != nil {
				return v, err
			}
			if !containsKey(known, d) {
				known, previous = append(known, d), append(previous, d)
			}
		}
		v.RootKeyPrevious = previous
		return v, nil
	}
}

func containsKey(keys [][]byte, k []byte) bool {
	for _, x := range keys {
		if bytes.Equal(x, k) {
			return true
		}
	}
	return false
}

// rotateRoot rewraps every tenant key under the root key the configuration
// names now. The old root key must still be configured: OHS_VAULT_ROOT_KEY_PREVIOUS
// for a local key (the key derived from OHS_ENCRYPTION_KEY is tried without
// being listed), the older key version for a KMS.
func rotateRoot(ctx context.Context, out io.Writer) error {
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err != nil {
		return err
	}
	env, err := openVault(ctx, withDerivedRoots(cfg))
	if err != nil {
		return err
	}
	defer env.pool.Close()
	to, err := vault.OpenProvider(ctx, cfg.Vault)
	if err != nil {
		return fmt.Errorf("OHS_VAULT_KEY_PROVIDER: %w", err)
	}
	r, err := vault.RotateRoot(ctx, env.vault, env.pool, to)
	if err != nil {
		return err
	}
	if r.Rewrapped == 0 {
		_, err = fmt.Fprintf(out, "vault: nothing to do: the %d tenant keys are already wrapped by the configured root key (%s)\n",
			r.Unchanged, to.Name())
		return err
	}
	_, err = fmt.Fprintf(out, "vault: %d tenant keys rewrapped under the root key of the %s provider (%d already were). No data was rewritten.\n"+
		"Restart every ohs process with these settings now: a running one keeps the old root key and fails once its key cache expires.\n"+
		"Backups taken before this still open with the old root key: destroy it once they have expired, or to make them unreadable.\n",
		r.Rewrapped, to.Name(), r.Unchanged)
	return err
}

// rekeyVaultRoot is the vault's part of `ohs admin rekey`. When the root key
// is derived from OHS_ENCRYPTION_KEY, rotating that key changes the root key
// too: the tenant keys are rewrapped under the new derived key here, so the
// old encryption key can leave OHS_PREVIOUS_ENCRYPTION_KEYS without taking
// the health data with it. With a root key of its own, or a KMS, the vault
// does not depend on OHS_ENCRYPTION_KEY and nothing is done.
func rekeyVaultRoot(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, out io.Writer) error {
	if cfg.Vault.Provider != "local" || !cfg.Vault.RootDerived {
		return nil
	}
	provider, err := vault.OpenProvider(ctx, cfg.Vault)
	if err != nil {
		return fmt.Errorf("OHS_VAULT_KEY_PROVIDER: %w", err)
	}
	v := vault.New(pool, provider, vault.Options{CanOpen: true})
	r, err := vault.RotateRoot(ctx, v, pool, provider)
	if err != nil {
		return fmt.Errorf("vault: the root key is derived from OHS_ENCRYPTION_KEY and the tenant keys could not be rewrapped "+
			"under the new one; keep the old key in OHS_PREVIOUS_ENCRYPTION_KEYS until this passes: %w", err)
	}
	_, err = fmt.Fprintf(out, "vault: root key derived from OHS_ENCRYPTION_KEY: %d tenant keys rewrapped under the new one, %d already were\n",
		r.Rewrapped, r.Unchanged)
	return err
}

// doctorVault adds the vault checks to `ohs doctor`: the key provider opens
// (for a KMS: the key is reachable with these credentials) and unwraps the
// tenant keys, no tenant key is left under a previous root key, and nothing
// is still stored clear. It prints one line per finding in the doctor's
// format and reports whether a check failed. A configuration or a database
// the doctor already reported on is not reported again.
func doctorVault(ctx context.Context, out io.Writer) (failed bool) {
	cfg, err := config.Load(os.LookupEnv, os.Environ())
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	name := "vault key provider (" + cfg.Vault.Provider + ")"
	provider, err := vault.OpenProvider(ctx, cfg.Vault)
	if err != nil {
		fmt.Fprintf(out, "fail  %s: %v\n", name, err)
		return true
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return false
	}
	defer pool.Close()
	var tenantKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.vault_keys WHERE user_id IS NULL`).Scan(&tenantKeys); err != nil {
		fmt.Fprintf(out, "skip  %s: the database has no vault tables yet (run ohs migrate up)\n", name)
		return false
	}
	v := vault.New(pool, provider, vault.Options{CanOpen: true})
	if err := v.Ready(ctx); err != nil {
		fmt.Fprintf(out, "fail  %s: does not open the tenant keys (wrong root key, or the key provider is unreachable): %v\n", name, err)
		return true
	}
	if tenantKeys == 0 {
		fmt.Fprintf(out, "ok    %s (wraps and unwraps; no tenant key stored yet)\n", name)
	} else {
		fmt.Fprintf(out, "ok    %s (opens the tenant keys)\n", name)
	}
	if cfg.Vault.Provider == "local" {
		active, err := vault.LocalRootVersion(cfg.Vault.RootKey)
		var stale int
		if err == nil {
			err = pool.QueryRow(ctx, `SELECT count(*) FROM ohs_core.vault_keys WHERE user_id IS NULL AND parent_version <> $1`, active).Scan(&stale)
		}
		if err == nil && stale > 0 {
			fmt.Fprintf(out, "warn  vault: %d tenant keys are wrapped by a previous root key: run `ohs vault rotate root` "+
				"(or `ohs admin rekey` when the root key is derived from OHS_ENCRYPTION_KEY) before removing that key\n", stale)
		}
	}
	st, err := vault.Status(ctx, pool)
	switch {
	case err != nil:
		fmt.Fprintf(out, "skip  vault: could not count what is still stored clear (%v); run `ohs vault status`\n", err)
	case st.Sealed():
		fmt.Fprintln(out, "ok    vault (nothing is stored clear)")
	default:
		fmt.Fprintf(out, "warn  vault: %d rows and %d raw objects are still stored clear: run `ohs vault seal`\n", st.ClearRows(), st.RawObjects)
	}
	return false
}

// errUserNeedsVault refuses to create a user without keys on an instance
// that seals: nothing could be stored for them until a server restart.
var errUserNeedsVault = errors.New("this instance holds vault keys, so a user needs keys of their own: " +
	"run this command with the configuration of the server (OHS_ENCRYPTION_KEY and the OHS_VAULT_* settings)")

// userVault opens the instance's vault for a command that creates a user,
// so the user gets keys with their row as the API gives them. Without an
// encryption key configured it returns nil on an instance that holds no vault
// key yet (the server gives every user keys when it first starts) and an
// error on one that does.
func userVault(ctx context.Context, pool *pgxpool.Pool) (*vault.Vault, error) {
	v, err := seedVault(ctx, pool)
	if errors.Is(err, errSeedNeedsVault) {
		return nil, errUserNeedsVault
	}
	return v, err
}

// ensureUserKeys gives the user keys when the store has a vault and the user
// has none.
func ensureUserKeys(ctx context.Context, s *tenant.Store, tenantID, userID uuid.UUID) error {
	v := s.Vault()
	if v == nil {
		return nil
	}
	tx, err := s.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := v.EnsureUser(ctx, tx, tenantID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
