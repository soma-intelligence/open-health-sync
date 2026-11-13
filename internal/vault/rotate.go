// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// Key rotation (docs/specs/vault.md, "Rotation").
//
// A key is rotated by wrapping what it wraps under a new one: the root key
// rewraps the tenant keys, a tenant key rewraps the user keys of its tenant.
// Neither touches a row or an object. Only a user key seals data, so only
// its rotation leaves data under the old version, which Reseal rewrites.
//
// A replaced tenant key or user key version stays stored, marked retired:
// whatever a process that had not seen the rotation yet wrapped or sealed
// with it still opens. What takes a key out of reach is destroying the root
// key version that leads to it, in the key provider.

// rotated records vault.key_rotated in tx.
func rotated(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, user *uuid.UUID, detail map[string]any) error {
	err := audit.Record(ctx, tx, audit.Event{Tenant: tenant, User: user, ActorType: audit.System, ActorID: "vault",
		Action: AuditKeyRotated, Detail: detail})
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	return nil
}

// RootRotation is what RotateRoot did.
type RootRotation struct {
	// Rewrapped is the tenant keys now wrapped by the new root key version;
	// Unchanged the ones that already were.
	Rewrapped, Unchanged int
	// Version is the root key version that wraps the tenant keys now, empty
	// when the instance has no tenant key.
	Version string
}

// RotateRoot rewraps every tenant key under the root key of to, in one
// transaction: each is unwrapped by v's provider (which must still open the
// root key version that wraps it) and wrapped again by to. to may be v's own
// provider once its active root key changed (a local provider with the new
// key and the old one among its previous keys, a KMS key with a new version),
// or another provider altogether.
//
// Running processes keep the provider they started with: restart them with
// the new settings. Until then they serve from their key cache and fail once
// it expires. The previous root key version is not destroyed here; destroying
// it, in the provider, is what makes every backup older than the rotation
// unreadable.
func RotateRoot(ctx context.Context, v *Vault, pool *pgxpool.Pool, to KeyProvider) (RootRotation, error) {
	var out RootRotation
	if !v.canOpen {
		return out, ErrCannotOpen
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("vault: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	type row struct {
		tenant  uuid.UUID
		version uint32
		wrapped []byte
		parent  string
	}
	found, err := tx.Query(ctx, `SELECT tenant_id, version, wrapped, parent_version FROM ohs_core.vault_keys
		WHERE user_id IS NULL ORDER BY tenant_id, version FOR UPDATE`)
	if err != nil {
		return out, fmt.Errorf("vault: tenant keys: %w", err)
	}
	keys, err := pgx.CollectRows(found, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.tenant, &x.version, &x.wrapped, &x.parent)
	})
	if err != nil {
		return out, fmt.Errorf("vault: tenant keys: %w", err)
	}
	// A local key names its version by the key itself, so a tenant key
	// already under the active one needs nothing. A KMS may rotate the key
	// behind a name that stays: there every key is wrapped again.
	sameLocal := to.Name() == "local" && v.provider.Name() == "local"
	for _, k := range keys {
		aad := keyAAD(k.tenant, uuid.Nil, k.version)
		plain, err := v.unwrap(ctx, k.wrapped, aad, k.parent)
		if err != nil {
			return out, fmt.Errorf("vault: tenant key %d of %s: %w", k.version, k.tenant, err)
		}
		wrapped, version, err := to.Wrap(ctx, plain, aad)
		clear(plain)
		if err != nil {
			return out, fmt.Errorf("vault: wrap with %s: %w", to.Name(), err)
		}
		out.Version = version
		if sameLocal && version == k.parent {
			out.Unchanged++
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE ohs_core.vault_keys SET wrapped = $3, parent_version = $4
			WHERE tenant_id = $1 AND user_id IS NULL AND version = $2`, k.tenant, k.version, wrapped, version); err != nil {
			return out, fmt.Errorf("vault: rewrap tenant key: %w", err)
		}
		if err := rotated(ctx, tx, k.tenant, nil, map[string]any{
			"key": "root", "provider": to.Name(), "tenant_key": k.version, "from": k.parent, "to": version}); err != nil {
			return out, err
		}
		out.Rewrapped++
	}
	if out.Version != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO ohs_core.vault_root (version, provider) VALUES ($1, $2)
			ON CONFLICT (version) DO UPDATE SET retired_at = NULL, provider = EXCLUDED.provider`, out.Version, to.Name()); err != nil {
			return out, fmt.Errorf("vault: record root key version: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ohs_core.vault_root r SET retired_at = now()
		WHERE r.retired_at IS NULL AND NOT EXISTS (SELECT 1 FROM ohs_core.vault_keys k
		    WHERE k.user_id IS NULL AND k.parent_version = r.version)`); err != nil {
		return out, fmt.Errorf("vault: retire root key versions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("vault: commit: %w", err)
	}
	return out, nil
}

// RotateTenant gives the tenant a new key and rewraps every user key of the
// tenant under it, in one transaction. It returns the user keys rewrapped.
// The data is not touched, and the unwrapped user keys other processes hold
// stay valid. The old tenant key stays stored, retired: a user created by a
// process that had not seen the rotation is still wrapped by it.
func RotateTenant(ctx context.Context, v *Vault, pool *pgxpool.Pool, tenant uuid.UUID) (int, error) {
	if !v.canOpen {
		return 0, ErrCannotOpen
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("vault: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// The newest tenant key row is the lock: two rotations of one tenant
	// run one after the other.
	var current uint32
	err = tx.QueryRow(ctx, `SELECT version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id IS NULL ORDER BY version DESC LIMIT 1 FOR UPDATE`, tenant).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("vault: tenant %s: %w", tenant, ErrNoKeys)
	}
	if err != nil {
		return 0, fmt.Errorf("vault: tenant key: %w", err)
	}
	next := current + 1
	fresh := new(tenantKey)
	defer fresh.Zero()
	if _, err := rand.Read(fresh[:]); err != nil {
		return 0, fmt.Errorf("vault: generate tenant key: %w", err)
	}
	wrapped, rootVersion, err := v.wrap(ctx, fresh[:], keyAAD(tenant, uuid.Nil, next))
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ohs_core.vault_root (version, provider) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		rootVersion, v.provider.Name()); err != nil {
		return 0, fmt.Errorf("vault: record root key version: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ohs_core.vault_keys (tenant_id, user_id, version, wrapped, parent_version)
		VALUES ($1, NULL, $2, $3, $4)`, tenant, next, wrapped, rootVersion); err != nil {
		return 0, fmt.Errorf("vault: insert tenant key: %w", err)
	}

	type row struct {
		user    uuid.UUID
		version uint32
		wrapped []byte
		parent  string
	}
	found, err := tx.Query(ctx, `SELECT user_id, version, wrapped, parent_version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id IS NOT NULL ORDER BY user_id, version FOR UPDATE`, tenant)
	if err != nil {
		return 0, fmt.Errorf("vault: user keys: %w", err)
	}
	keys, err := pgx.CollectRows(found, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.user, &x.version, &x.wrapped, &x.parent)
	})
	if err != nil {
		return 0, fmt.Errorf("vault: user keys: %w", err)
	}
	// The old tenant keys, unwrapped once each.
	old := map[uint32]*tenantKey{}
	defer func() {
		for _, k := range old {
			k.Zero()
		}
	}()
	nextText := strconv.FormatUint(uint64(next), 10)
	for _, k := range keys {
		pv, err := strconv.ParseUint(k.parent, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("vault: user key %d names tenant key %q", k.version, k.parent)
		}
		tk, ok := old[uint32(pv)]
		if !ok {
			if tk, err = v.tenantKey(ctx, tx, tenant, uint32(pv)); err != nil {
				return 0, err
			}
			old[uint32(pv)] = tk
		}
		aad := keyAAD(tenant, k.user, k.version)
		plain, err := openRow(func(ver uint32) (*[32]byte, error) {
			if ver != uint32(pv) {
				return nil, ErrOpen
			}
			return (*[32]byte)(tk), nil
		}, aad, k.wrapped)
		if err != nil {
			v.metrics.openFailure(failKey)
			return 0, fmt.Errorf("vault: user key %d of %s: %w", k.version, k.user, err)
		}
		rewrapped, err := sealRow((*[32]byte)(fresh), next, aad, plain)
		clear(plain)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE ohs_core.vault_keys SET wrapped = $4, parent_version = $5
			WHERE tenant_id = $1 AND user_id = $2 AND version = $3`, tenant, k.user, k.version, rewrapped, nextText); err != nil {
			return 0, fmt.Errorf("vault: rewrap user key: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ohs_core.vault_keys SET retired_at = now()
		WHERE tenant_id = $1 AND user_id IS NULL AND version < $2 AND retired_at IS NULL`, tenant, next); err != nil {
		return 0, fmt.Errorf("vault: retire tenant keys: %w", err)
	}
	if err := rotated(ctx, tx, tenant, nil, map[string]any{
		"key": "tenant", "from": current, "to": next, "user_keys": len(keys)}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("vault: commit: %w", err)
	}
	return len(keys), nil
}

// RotateUser gives the user a new key version and returns it. New rows and
// raw objects are sealed under it from the commit on, in every process (the
// others drop the user from their cache on the notification). The older
// versions stay, and still open what they sealed, until Reseal has rewritten
// it and retires them.
//
// The new version has a new data key and new KEM keys. It keeps the index
// key: the dedup hashes, the source keys and the content MACs of the user are
// made with it, and a record must hash the same before and after, or it would
// be stored twice and its hourly buckets split. So a rotation after a leak
// puts new data out of reach of the leaked data and KEM keys, but someone who
// holds the leaked index key and a later copy of the database can still test
// guesses against the keyed hashes of that user.
//
// It takes the user row FOR UPDATE, so it waits for the jobs that write for
// the user and none seals under the old version after it commits.
func RotateUser(ctx context.Context, v *Vault, pool *pgxpool.Pool, tenant, user uuid.UUID) (uint32, error) {
	if !v.canOpen {
		return 0, ErrCannotOpen
	}
	ref := userRef{tenant, user}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("vault: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		tenant, user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return 0, fmt.Errorf("vault: user %s: %w", user, ErrNoKeys)
	}
	if err != nil {
		return 0, fmt.Errorf("vault: lock user: %w", err)
	}
	var (
		current uint32
		wrapped []byte
		parent  string
	)
	err = tx.QueryRow(ctx, `SELECT version, wrapped, parent_version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY version DESC LIMIT 1`, tenant, user).Scan(&current, &wrapped, &parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("vault: user %s: %w", user, ErrNoKeys)
	}
	if err != nil {
		return 0, fmt.Errorf("vault: user keys: %w", err)
	}
	old, err := v.openUserKey(ctx, ref, current, wrapped, parent)
	if err != nil {
		return 0, err
	}
	defer old.Zero()
	next := current + 1
	fresh, err := GenerateUserKeys(next)
	if err != nil {
		return 0, err
	}
	defer fresh.Zero()
	fresh.Index = old.Index
	pub, err := fresh.Public()
	if err != nil {
		return 0, err
	}
	tk, tver, err := v.ensureTenantKey(ctx, tx, tenant)
	if err != nil {
		return 0, err
	}
	defer tk.Zero()
	plain := fresh.Marshal()
	defer clear(plain)
	sealed, err := sealRow((*[32]byte)(tk), tver, keyAAD(tenant, user, next), plain)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ohs_core.vault_keys (tenant_id, user_id, version, wrapped, parent_version, kem_public)
		VALUES ($1, $2, $3, $4, $5, $6)`, tenant, user, next, sealed, strconv.FormatUint(uint64(tver), 10), pub.Marshal()); err != nil {
		return 0, fmt.Errorf("vault: insert user key: %w", err)
	}
	if err := rotated(ctx, tx, tenant, &user, map[string]any{"key": "user", "from": current, "to": next}); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, notifyChannel, userPayload(tenant, user)); err != nil {
		return 0, fmt.Errorf("vault: rotate: notify: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("vault: commit: %w", err)
	}
	v.evictUser(ref)
	return next, nil
}

// resealUsers selects the users who still have a key version, not retired,
// below their newest one. It is a condition on ohs_core.end_users u.
const resealUsers = `EXISTS (SELECT 1 FROM ohs_core.vault_keys k
	WHERE k.tenant_id = u.tenant_id AND k.user_id = u.id AND k.retired_at IS NULL
	  AND k.version < (SELECT max(m.version) FROM ohs_core.vault_keys m
	      WHERE m.tenant_id = u.tenant_id AND m.user_id = u.id))`

// Reseal rewrites, under the newest key version of each user, the rows and
// raw objects sealed under an older one, then retires the older versions of
// the users it finished. It works as Seal does: user by user, in batches,
// stopped anywhere and resumed by running it again. raw may be nil; the raw
// objects are then left as they are and no version is retired.
func Reseal(ctx context.Context, v *Vault, pool *pgxpool.Pool, raw rawstore.Store, o SealOptions) (SealReport, error) {
	s, err := newSealRun(v, pool, raw, o, "reseal")
	if err != nil {
		return SealReport{}, err
	}
	users, err := sealUsers(ctx, pool, o.Users, resealUsers)
	if err != nil {
		return SealReport{}, err
	}
	if o.DryRun {
		s.report.Users = len(users)
		return s.report, nil
	}
	tables := append(o.Rows.Tables(), HourlyTable)
	for _, u := range users {
		err := s.resealUser(ctx, u, tables)
		if errors.Is(err, errUserGone) {
			continue
		}
		if err != nil {
			return s.report, err
		}
	}
	return s.report, nil
}

func (s *sealRun) resealUser(ctx context.Context, u sealUser, tables []string) error {
	// The keys are read again: this process may hold the user's keys from
	// before the rotation.
	s.v.evictUser(userRef{u.tenant, u.user})
	uv, err := s.v.ForUser(ctx, u.tenant, u.user)
	if errors.Is(err, ErrNoKeys) {
		return errUserGone
	}
	if err != nil {
		return err
	}
	p := &userProgress{}
	s.report.Users++
	defer func() {
		if p.any() {
			_ = s.audit(context.WithoutCancel(ctx), u, p.detail(true))
		}
	}()
	err = s.tables(ctx, u, uv, p, tables, func(tx pgx.Tx, table string, from Cursor) (SealCount, error) {
		return s.o.Rows.Reseal(ctx, tx, uv, u.tenant, u.user, table, from, s.o.Batch)
	})
	if err != nil || s.raw == nil {
		return err
	}
	if err := s.resealRawObjects(ctx, u, uv, p); err != nil {
		return err
	}
	// Everything of the user now opens with the newest version alone.
	return inUserTx(ctx, s.pool, u, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ohs_core.vault_keys SET retired_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND version < $3 AND retired_at IS NULL`, u.tenant, u.user, uv.Version())
		if err != nil {
			return fmt.Errorf("vault: retire user keys: %w", err)
		}
		s.report.KeysRetired += int(tag.RowsAffected())
		return rotated(ctx, tx, u.tenant, &u.user, map[string]any{
			"key": "user", "retired_below": uv.Version(), "retired": tag.RowsAffected()})
	})
}

// rawVersion reads the header of a stored object: the key version it is
// sealed to, and false for an object that is not sealed.
func (s *sealRun) rawVersion(ctx context.Context, key string) (uint32, bool, error) {
	rc, err := s.raw.Get(ctx, key)
	if err != nil {
		return 0, false, err
	}
	defer rc.Close()
	head := make([]byte, objectHeaderSize)
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, false, err
	}
	h, sealed, err := parseObjectHeader(head[:n])
	return h.Version, sealed, err
}

// resealRawObjects rewrites the raw objects of u sealed to an older key
// version. One that Seal or Reseal wrote says its version in its name; any
// other is asked by its header.
func (s *sealRun) resealRawObjects(ctx context.Context, u sealUser, uv *UserVault, p *userProgress) error {
	newest := uv.Version()
	pub, err := uv.newest.Public()
	if err != nil {
		return err
	}
	var after int64
	for {
		rows, err := s.rawRows(ctx, u, after, "true")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			after = r.id
			if ver, ok := sealedKeyVersion(r.key); ok && ver == newest {
				continue
			}
			ver, sealed, err := s.rawVersion(ctx, r.key)
			if err != nil && !errors.Is(err, rawstore.ErrNotFound) {
				return fmt.Errorf("vault: reseal raw object %s: %w", r.key, err)
			}
			if err == nil && (!sealed || ver == newest) {
				// Not sealed is for Seal; the newest version needs nothing.
				continue
			}
			done, err := s.rewriteRaw(ctx, u, r, pub, newest, func(stored []byte) ([]byte, error) {
				_, body, err := OpenObject(bytes.NewReader(stored), uv.Keys)
				if err != nil {
					return nil, err
				}
				return io.ReadAll(body)
			})
			if err != nil {
				return fmt.Errorf("vault: reseal raw object %s: %w", r.key, err)
			}
			if done {
				s.report.RawObjects++
				p.objects++
				s.progress()
			}
		}
	}
}
