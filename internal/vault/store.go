// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/memlimit"
)

var (
	// ErrNoKeys is a user with no keys: erased, or not given keys yet. Reads
	// answer as for an absent user; writes are refused.
	ErrNoKeys = errors.New("vault: the user has no keys")
	// ErrCannotOpen is a process that may not unwrap keys (OHS_ROLES without
	// api or etl): it seals to public keys only.
	ErrCannotOpen = errors.New("vault: this process does not open keys")
)

// Querier reads one row; pgx.Tx and *pgxpool.Pool both fit.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options configure a Vault.
type Options struct {
	// CanOpen is true in a process that runs api or etl. Without it the
	// Vault never calls the KeyProvider: ForUser and EnsureUser refuse with
	// ErrCannotOpen, PublicKey still answers.
	CanOpen bool
	// CacheUsers bounds the users whose unwrapped keys stay in memory, and
	// the tenant keys as well (0: the default, LowMemoryCacheUsers on the
	// low-memory profile). CacheTTL is how long an unwrapped key stays (0:
	// config.DefaultVaultCacheTTL).
	CacheUsers int
	CacheTTL   time.Duration
	// Metrics counts seals, opens, cache lookups and provider calls. Nil
	// counts into a Metrics of the Vault's own.
	Metrics *Metrics
}

// Vault keeps the tenant and user keys: it creates them, unwraps them through
// the KeyProvider and holds the unwrapped ones in a cache, zeroed when they
// leave it. A tenant key is unwrapped at most once per tenant per TTL.
type Vault struct {
	pool     *pgxpool.Pool
	provider KeyProvider
	canOpen  bool
	metrics  *Metrics
	users    *cache[userRef, *userEntry]
	tenants  *cache[tenantRef, *tenantKey]
	// userLoads and tenantLoads merge concurrent misses of one user or
	// one tenant key, so a cold tenant costs one provider call.
	userLoads   flights[userRef, *userEntry]
	tenantLoads flights[tenantRef, *tenantKey]
	ttl         time.Duration
	// onListen, when set (tests), runs once Listen listens.
	onListen func()
}

type userRef struct{ tenant, user uuid.UUID }

// tenantRef names a tenant key by its row: the digest of the wrapped value
// makes a cached key match only the row it was unwrapped from.
type tenantRef struct {
	tenant  uuid.UUID
	version uint32
	wrapped [32]byte // SHA-256 of vault_keys.wrapped
}

// tenantKey is an unwrapped tenant key.
type tenantKey [32]byte

func (k *tenantKey) Zero() { clear(k[:]) }

// userEntry is every key version of one user, unwrapped.
type userEntry struct {
	keys   map[uint32]*UserKeys
	newest uint32
}

func (e *userEntry) Zero() {
	for _, k := range e.keys {
		k.Zero()
	}
}

// New returns a Vault over the key tables of pool.
func New(pool *pgxpool.Pool, p KeyProvider, o Options) *Vault {
	users := cacheUsersFor(o.CacheUsers, memlimit.Low())
	ttl := o.CacheTTL
	if ttl <= 0 {
		ttl = config.DefaultVaultCacheTTL
	}
	m := o.Metrics
	if m == nil {
		m = NewMetrics()
	}
	return &Vault{
		pool:     pool,
		provider: p,
		canOpen:  o.CanOpen,
		metrics:  m,
		users:    newCache[userRef, *userEntry](users, ttl, time.Now),
		tenants:  newCache[tenantRef, *tenantKey](users, ttl, time.Now),
		ttl:      ttl,
	}
}

// Metrics returns what the Vault counts into, for /metrics.
func (v *Vault) Metrics() *Metrics { return v.metrics }

// setClock replaces the clock of both caches (tests).
func (v *Vault) setClock(now func() time.Time) {
	v.users.now = now
	v.tenants.now = now
}

// keyAADLabel starts the AAD of every wrapped key.
const keyAADLabel = "ohs.vault.key.v1"

// keyAAD is the associated data of a wrapped key:
//
//	"ohs.vault.key.v1" || tenant_id (16) || user_id (16, zero for a tenant key) || version (uint32 BE)
//
// so a wrapped key moved to another tenant, user or version does not open.
func keyAAD(tenant, user uuid.UUID, version uint32) []byte {
	b := make([]byte, 0, len(keyAADLabel)+16+16+4)
	b = append(b, keyAADLabel...)
	b = append(b, tenant[:]...)
	b = append(b, user[:]...)
	return binary.BigEndian.AppendUint32(b, version)
}

// EnsureUser gives the user keys, in tx, unless they have some: version 1 of
// a fresh key set, wrapped by the newest tenant key. The tenant key is
// created first when the tenant has none. Two transactions racing on the same
// user or tenant leave one row each: the loser's insert waits for the
// winner's commit and then does nothing.
func (v *Vault) EnsureUser(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) error {
	if !v.canOpen {
		return ErrCannotOpen
	}
	var has bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id = $2)`,
		tenant, user).Scan(&has); err != nil {
		return fmt.Errorf("vault: user keys: %w", err)
	}
	if has {
		return nil
	}
	tk, tver, err := v.ensureTenantKey(ctx, tx, tenant)
	if err != nil {
		return err
	}
	defer tk.Zero()

	const version = 1
	uk, err := GenerateUserKeys(version)
	if err != nil {
		return err
	}
	defer uk.Zero()
	pub, err := uk.Public()
	if err != nil {
		return err
	}
	plain := uk.Marshal()
	defer clear(plain)
	// A user key is sealed as a row is, under the tenant key: the format
	// carries the tenant key version, and parent_version says it again.
	wrapped, err := sealRow((*[32]byte)(tk), tver, keyAAD(tenant, user, version), plain)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO ohs_core.vault_keys (tenant_id, user_id, version, wrapped, parent_version, kem_public)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, user_id, version) WHERE user_id IS NOT NULL DO NOTHING`,
		tenant, user, version, wrapped, strconv.FormatUint(uint64(tver), 10), pub.Marshal())
	if err != nil {
		return fmt.Errorf("vault: insert user key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return keyCreated(ctx, tx, tenant, &user, version)
}

// ensureTenantKey returns the newest tenant key, unwrapped, creating version
// 1 when the tenant has none. The caller zeroes it.
func (v *Vault) ensureTenantKey(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (*tenantKey, uint32, error) {
	for range 2 {
		var version uint32
		err := tx.QueryRow(ctx, `
			SELECT version FROM ohs_core.vault_keys
			WHERE tenant_id = $1 AND user_id IS NULL ORDER BY version DESC LIMIT 1`, tenant).Scan(&version)
		if err == nil {
			k, err := v.tenantKey(ctx, tx, tenant, version)
			return k, version, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, fmt.Errorf("vault: tenant key: %w", err)
		}
		k, created, err := v.createTenantKey(ctx, tx, tenant)
		if err != nil || created {
			return k, 1, err
		}
		// Another transaction created it first: read theirs.
	}
	return nil, 0, errors.New("vault: the tenant key was created and then vanished")
}

// createTenantKey inserts version 1 of the tenant's key and returns it, or
// reports false when another transaction inserted it first. The new key is
// not cached: the transaction may still roll back.
func (v *Vault) createTenantKey(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) (*tenantKey, bool, error) {
	const version = 1
	k := new(tenantKey)
	if _, err := rand.Read(k[:]); err != nil {
		return nil, false, fmt.Errorf("vault: generate tenant key: %w", err)
	}
	wrapped, rootVersion, err := v.wrap(ctx, k[:], keyAAD(tenant, uuid.Nil, version))
	if err != nil {
		k.Zero()
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ohs_core.vault_root (version, provider) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		rootVersion, v.provider.Name()); err != nil {
		k.Zero()
		return nil, false, fmt.Errorf("vault: record root key version: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO ohs_core.vault_keys (tenant_id, user_id, version, wrapped, parent_version)
		VALUES ($1, NULL, $2, $3, $4)
		ON CONFLICT (tenant_id, version) WHERE user_id IS NULL DO NOTHING`,
		tenant, version, wrapped, rootVersion)
	if err != nil {
		k.Zero()
		return nil, false, fmt.Errorf("vault: insert tenant key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		k.Zero()
		return nil, false, nil
	}
	if err := keyCreated(ctx, tx, tenant, nil, version); err != nil {
		k.Zero()
		return nil, false, err
	}
	return k, true, nil
}

// keyCreated records vault.key_created in tx: a tenant key when user is nil.
func keyCreated(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, user *uuid.UUID, version uint32) error {
	kind := "tenant"
	if user != nil {
		kind = "user"
	}
	err := audit.Record(ctx, tx, audit.Event{Tenant: tenant, User: user, ActorType: audit.System, ActorID: "vault",
		Action: audit.VaultKeyCreated, Detail: map[string]any{"key": kind, "version": version}})
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	return nil
}

// PublicKey returns the user's newest KEM public key and its version, read
// from db with no call to the provider: enough to seal an object for them.
func (v *Vault) PublicKey(ctx context.Context, db Querier, tenant, user uuid.UUID) (PublicKey, uint32, error) {
	var (
		raw     []byte
		version uint32
	)
	err := db.QueryRow(ctx, `
		SELECT kem_public, version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY version DESC LIMIT 1`, tenant, user).Scan(&raw, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicKey{}, 0, ErrNoKeys
	}
	if err != nil {
		return PublicKey{}, 0, fmt.Errorf("vault: public key: %w", err)
	}
	p, err := UnmarshalPublicKey(raw)
	if err != nil {
		return PublicKey{}, 0, err
	}
	return p, version, nil
}

// PublicKeyVersion is PublicKey for one key version of the user, read the
// same way: a sealed object's header names the version it was sealed to.
// A version the user does not have is ErrNoKeys.
func (v *Vault) PublicKeyVersion(ctx context.Context, db Querier, tenant, user uuid.UUID, version uint32) (PublicKey, error) {
	var raw []byte
	err := db.QueryRow(ctx, `
		SELECT kem_public FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id = $2 AND version = $3`, tenant, user, version).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicKey{}, ErrNoKeys
	}
	if err != nil {
		return PublicKey{}, fmt.Errorf("vault: public key: %w", err)
	}
	return UnmarshalPublicKey(raw)
}

// ForUser returns every key version of the user, from the cache or unwrapped
// now. All versions are loaded at once, so an open never needs the provider:
// an outage shows here, as an error wrapping ErrProviderUnavailable.
func (v *Vault) ForUser(ctx context.Context, tenant, user uuid.UUID) (*UserVault, error) {
	if !v.canOpen {
		return nil, ErrCannotOpen
	}
	ref := userRef{tenant, user}
	var u *UserVault
	if v.users.get(ref, func(e *userEntry) { u = v.userVault(ref, e) }) {
		v.metrics.cacheResult(true)
		return u, nil
	}
	v.metrics.cacheResult(false)
	// The epoch is read before the flight starts, so a shred committed
	// while it loads keeps its result out of the cache.
	epoch := v.users.epoch()
	// The loaded entry is shared by the callers of the flight and never
	// zeroed by the cache, which holds a clone of it. Each caller gets a
	// copy of its own (userVault); the flight's entry is left to the GC.
	e, err := v.userLoads.do(ctx, ref, func(ctx context.Context) (*userEntry, error) {
		e, err := v.loadUser(ctx, ref)
		if err != nil {
			return nil, err
		}
		v.users.put(ref, e.clone(), epoch)
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	return v.userVault(ref, e), nil
}

func (e *userEntry) clone() *userEntry {
	c := &userEntry{keys: make(map[uint32]*UserKeys, len(e.keys)), newest: e.newest}
	for ver, k := range e.keys {
		kc := *k
		c.keys[ver] = &kc
	}
	return c
}

// userVault copies e for one caller: the cache may zero e when it evicts it,
// and a request still sealing with it must not see its keys turn to zero.
func (v *Vault) userVault(ref userRef, e *userEntry) *UserVault {
	keys := make(map[uint32]*UserKeys, len(e.keys))
	for ver, k := range e.keys {
		c := *k
		keys[ver] = &c
	}
	return &UserVault{tenant: ref.tenant, user: ref.user, keys: keys, newest: keys[e.newest], metrics: v.metrics}
}

func (v *Vault) loadUser(ctx context.Context, ref userRef) (*userEntry, error) {
	type row struct {
		version uint32
		wrapped []byte
		parent  string
	}
	rows, err := v.pool.Query(ctx, `
		SELECT version, wrapped, parent_version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY version`, ref.tenant, ref.user)
	if err != nil {
		return nil, fmt.Errorf("vault: user keys: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.version, &x.wrapped, &x.parent)
	})
	if err != nil {
		return nil, fmt.Errorf("vault: user keys: %w", err)
	}
	if len(found) == 0 {
		return nil, ErrNoKeys
	}
	e := &userEntry{keys: make(map[uint32]*UserKeys, len(found))}
	for _, r := range found {
		uk, err := v.openUserKey(ctx, ref, r.version, r.wrapped, r.parent)
		if err != nil {
			e.Zero()
			return nil, err
		}
		e.keys[r.version] = uk
		e.newest = max(e.newest, r.version)
	}
	return e, nil
}

func (v *Vault) openUserKey(ctx context.Context, ref userRef, version uint32, wrapped []byte, parent string) (*UserKeys, error) {
	tver, err := strconv.ParseUint(parent, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("vault: user key %d names tenant key %q", version, parent)
	}
	tk, err := v.tenantKey(ctx, v.pool, ref.tenant, uint32(tver))
	if err != nil {
		return nil, err
	}
	defer tk.Zero()
	plain, err := openRow(func(ver uint32) (*[32]byte, error) {
		if ver != uint32(tver) {
			return nil, ErrOpen
		}
		return (*[32]byte)(tk), nil
	}, keyAAD(ref.tenant, ref.user, version), wrapped)
	if err != nil {
		v.metrics.openFailure(failKey)
		return nil, fmt.Errorf("vault: user key %d: %w", version, err)
	}
	defer clear(plain)
	uk, err := UnmarshalUserKeys(plain)
	if err != nil || uk.Version != version {
		v.metrics.openFailure(failKey)
		return nil, fmt.Errorf("vault: user key %d: %w", version, ErrOpen)
	}
	return uk, nil
}

// tenantKey returns a copy of the tenant key of that version, from the cache
// or unwrapped by the provider now. The caller zeroes it. The row is read
// through db every time, so the cache answers only for the row stored now;
// a key read through a transaction (db is not the pool) is cached only when
// the pool reads the same row, since that transaction may still roll back.
func (v *Vault) tenantKey(ctx context.Context, db Querier, tenant uuid.UUID, version uint32) (*tenantKey, error) {
	var (
		wrapped []byte
		parent  string
	)
	err := db.QueryRow(ctx, `
		SELECT wrapped, parent_version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id IS NULL AND version = $2`, tenant, version).Scan(&wrapped, &parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("vault: tenant key %d is missing", version)
	}
	if err != nil {
		return nil, fmt.Errorf("vault: tenant key: %w", err)
	}
	ref := tenantRef{tenant: tenant, version: version, wrapped: sha256.Sum256(wrapped)}
	k := new(tenantKey)
	if v.tenants.get(ref, func(c *tenantKey) { *k = *c }) {
		return k, nil
	}
	committed, err := v.tenantRowCommitted(ctx, db, ref, wrapped)
	if err != nil {
		return nil, err
	}
	epoch := v.tenants.epoch()
	// The flight's key is shared by its callers and not owned by the cache,
	// which holds a copy. Each caller copies it; the flight's own copy is
	// left to the GC.
	shared, err := v.tenantLoads.do(ctx, ref, func(ctx context.Context) (*tenantKey, error) {
		plain, err := v.unwrap(ctx, wrapped, keyAAD(tenant, uuid.Nil, version), parent)
		if err != nil {
			return nil, fmt.Errorf("vault: tenant key %d: %w", version, err)
		}
		defer clear(plain)
		if len(plain) != len(tenantKey{}) {
			return nil, fmt.Errorf("vault: tenant key %d is %d bytes", version, len(plain))
		}
		got := new(tenantKey)
		copy(got[:], plain)
		if committed {
			cached := *got
			v.tenants.put(ref, &cached, epoch)
		}
		return got, nil
	})
	if err != nil {
		return nil, err
	}
	*k = *shared
	return k, nil
}

// tenantRowCommitted reports whether the tenant key row read through db is
// committed: always when db is the pool, otherwise when the pool reads the
// same wrapped value (one query per cache miss). A row the transaction
// created, or replaced, is not.
func (v *Vault) tenantRowCommitted(ctx context.Context, db Querier, ref tenantRef, wrapped []byte) (bool, error) {
	if p, ok := db.(*pgxpool.Pool); ok && p == v.pool {
		return true, nil
	}
	var committed []byte
	err := v.pool.QueryRow(ctx, `
		SELECT wrapped FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id IS NULL AND version = $2`, ref.tenant, ref.version).Scan(&committed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("vault: tenant key: %w", err)
	}
	return sha256.Sum256(committed) == ref.wrapped, nil
}

// EnsureTenant gives the tenant a key, in tx, unless it has one. Committing
// it before the tenant's users get keys lets their transactions share one
// unwrap of it through the cache.
func (v *Vault) EnsureTenant(ctx context.Context, tx pgx.Tx, tenant uuid.UUID) error {
	if !v.canOpen {
		return ErrCannotOpen
	}
	var has bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NULL)`,
		tenant).Scan(&has); err != nil {
		return fmt.Errorf("vault: tenant key: %w", err)
	}
	if has {
		return nil
	}
	k, _, err := v.createTenantKey(ctx, tx, tenant)
	if k != nil {
		k.Zero()
	}
	return err
}

func (v *Vault) wrap(ctx context.Context, plain, aad []byte) ([]byte, string, error) {
	start := time.Now()
	w, ver, err := v.provider.Wrap(ctx, plain, aad)
	v.metrics.providerTime(time.Since(start))
	if err != nil {
		return nil, "", fmt.Errorf("vault: wrap with %s: %w", v.provider.Name(), err)
	}
	return w, ver, nil
}

func (v *Vault) unwrap(ctx context.Context, wrapped, aad []byte, version string) ([]byte, error) {
	start := time.Now()
	plain, err := v.provider.Unwrap(ctx, wrapped, aad, version)
	v.metrics.providerTime(time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("unwrap with %s: %w", v.provider.Name(), err)
	}
	return plain, nil
}

// Shred deletes the user's keys in tx and, once tx commits, has every Vault
// listening on the database drop them from its cache. From that commit on,
// nothing of the user's that is still stored opens.
func (v *Vault) Shred(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id = $2`, tenant, user); err != nil {
		return fmt.Errorf("vault: shred: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, userPayload(tenant, user)); err != nil {
		return fmt.Errorf("vault: shred: notify: %w", err)
	}
	// Drop this process's copy now. Before the commit this holds only while
	// Listen runs: a ForUser between here and the commit may cache the keys
	// again, and the NOTIFY delivered at commit evicts them once more.
	v.evictUser(userRef{tenant, user})
	return nil
}

// evictUser drops the user's keys and any load of them in flight, so the
// next ForUser reads the key rows again.
func (v *Vault) evictUser(ref userRef) {
	v.users.remove(ref)
	v.userLoads.forget(ref)
}

// purgeUsers is evictUser for every user.
func (v *Vault) purgeUsers() {
	v.users.purge()
	v.userLoads.forgetAll()
}

// Ready checks that the provider opens keys: it unwraps the newest tenant
// key, through the cache, so a provider outage fails it only once that key
// has left the cache. A process that cannot open, or an instance with no
// tenant key yet, is ready.
func (v *Vault) Ready(ctx context.Context) error {
	if !v.canOpen {
		return nil
	}
	var (
		tenant  uuid.UUID
		version uint32
	)
	err := v.pool.QueryRow(ctx, `
		SELECT tenant_id, version FROM ohs_core.vault_keys
		WHERE user_id IS NULL ORDER BY created_at DESC, version DESC LIMIT 1`).Scan(&tenant, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("vault: ready: %w", err)
	}
	k, err := v.tenantKey(ctx, v.pool, tenant, version)
	if err != nil {
		return err
	}
	k.Zero()
	return nil
}
