// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sealedBy seals one row for the user under v, so a test can tell later
// whether another Vault still opens what the user wrote.
func (e *storeEnv) sealedBy(v *Vault, user uuid.UUID) []byte {
	e.t.Helper()
	uv, err := v.ForUser(context.Background(), e.tenant, user)
	require.NoError(e.t, err)
	sealed, err := uv.SealRow(Schema["heartbeat_samples"], []byte("id"), []any{812.5})
	require.NoError(e.t, err)
	return sealed
}

func (e *storeEnv) opens(v *Vault, user uuid.UUID, sealed []byte) error {
	uv, err := v.ForUser(context.Background(), e.tenant, user)
	if err != nil {
		return err
	}
	vals, err := uv.OpenRow(Schema["heartbeat_samples"], []byte("id"), sealed)
	if err == nil {
		require.Equal(e.t, []any{812.5}, vals)
	}
	return err
}

// After a root rotation everything opens with the new root key alone, and a
// copy of the key table taken before it (an old backup) does not: destroying
// the old root key is what puts it out of reach.
func TestRotateRootRewrapsEveryTenantKey(t *testing.T) {
	e := newStoreEnv(t)
	ctx := context.Background()
	oldRoot, err := newLocalProvider(rootKey(1), nil)
	require.NoError(t, err)
	v := e.vault(oldRoot, Options{CanOpen: true})
	u := e.user("u-1")
	require.NoError(t, e.ensure(v, u))
	sealed := e.sealedBy(v, u)

	// What a dump taken now holds for the tenant key.
	var dumped []byte
	var dumpedVersion string
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT wrapped, parent_version FROM ohs_core.vault_keys
		WHERE tenant_id = $1 AND user_id IS NULL`, e.tenant).Scan(&dumped, &dumpedVersion))

	// The operator configures the new key and keeps the old one as previous.
	both, err := newLocalProvider(rootKey(2), [][]byte{rootKey(1)})
	require.NoError(t, err)
	r, err := RotateRoot(ctx, e.vault(both, Options{CanOpen: true}), e.pool, both)
	require.NoError(t, err)
	require.Equal(t, 1, r.Rewrapped)
	newVersion, err := LocalRootVersion(rootKey(2))
	require.NoError(t, err)
	require.Equal(t, newVersion, r.Version)

	// The old key removed from the settings: the live database opens.
	newOnly, err := newLocalProvider(rootKey(2), nil)
	require.NoError(t, err)
	fresh := e.vault(newOnly, Options{CanOpen: true})
	require.NoError(t, fresh.Ready(ctx))
	require.NoError(t, e.opens(fresh, u, sealed))
	// The dump does not: its tenant key is wrapped by the root key that is gone.
	_, err = newOnly.Unwrap(ctx, dumped, keyAAD(e.tenant, uuid.Nil, 1), dumpedVersion)
	require.Error(t, err)
	// And a process still on the old root key no longer opens the live one.
	require.Error(t, e.vault(oldRoot, Options{CanOpen: true}).Ready(ctx))

	assert.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_root WHERE version = $1 AND retired_at IS NULL`, newVersion))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_root WHERE version = $1 AND retired_at IS NOT NULL`, dumpedVersion))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.audit_events WHERE tenant_id = $1 AND action = 'vault.key_rotated'
		AND detail->>'key' = 'root'`, e.tenant))

	// Again with the same settings: nothing to rewrap.
	r, err = RotateRoot(ctx, e.vault(both, Options{CanOpen: true}), e.pool, both)
	require.NoError(t, err)
	require.Equal(t, RootRotation{Unchanged: 1, Version: newVersion}, r)

	// A process that cannot open keys cannot rotate them.
	_, err = RotateRoot(ctx, e.vault(nil, Options{}), e.pool, both)
	require.ErrorIs(t, err, ErrCannotOpen)
}

// A tenant rotation rewraps the user keys under a new tenant key: the data
// opens as before, in this process and in one that starts after.
func TestRotateTenantRewrapsItsUserKeys(t *testing.T) {
	e := newStoreEnv(t)
	ctx := context.Background()
	p := newCountingProvider(t)
	v := e.vault(p, Options{CanOpen: true})
	u1, u2 := e.user("u-1"), e.user("u-2")
	require.NoError(t, e.ensure(v, u1))
	require.NoError(t, e.ensure(v, u2))
	s1, s2 := e.sealedBy(v, u1), e.sealedBy(v, u2)

	n, err := RotateTenant(ctx, v, e.pool, e.tenant)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	assert.Equal(t, 2, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NOT NULL AND parent_version = '2'`, e.tenant))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NULL AND version = 1 AND retired_at IS NOT NULL`, e.tenant))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE tenant_id = $1 AND user_id IS NULL AND version = 2 AND retired_at IS NULL`, e.tenant))

	fresh := e.vault(newCountingProvider(t), Options{CanOpen: true})
	require.NoError(t, fresh.Ready(ctx))
	require.NoError(t, e.opens(fresh, u1, s1))
	require.NoError(t, e.opens(fresh, u2, s2))
	require.NoError(t, e.opens(v, u1, s1))

	// A user created after the rotation is wrapped by the new tenant key.
	u3 := e.user("u-3")
	require.NoError(t, e.ensure(fresh, u3))
	assert.Equal(t, 1, e.count(`SELECT count(*) FROM ohs_core.vault_keys WHERE user_id = $1 AND parent_version = '2'`, u3))

	_, err = RotateTenant(ctx, v, e.pool, uuid.New())
	require.ErrorIs(t, err, ErrNoKeys)
}

// A user rotation adds a version: new seals carry it, the old version still
// opens what it sealed, the keyed hashes do not change, and another process
// drops its cached keys on the notification.
func TestRotateUserAddsAVersion(t *testing.T) {
	e := newStoreEnv(t)
	ctx := context.Background()
	v := e.vault(newCountingProvider(t), Options{CanOpen: true})
	other := e.vault(newCountingProvider(t), Options{CanOpen: true})
	listening := make(chan struct{})
	other.onListen = func() { close(listening) }
	lctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = other.Listen(lctx) }()
	<-listening

	u := e.user("u-1")
	require.NoError(t, e.ensure(v, u))
	before, err := v.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	_, err = other.ForUser(ctx, e.tenant, u) // cached by the other process
	require.NoError(t, err)
	old := e.sealedBy(v, u)
	pubBefore, _, err := v.PublicKey(ctx, e.pool, e.tenant, u)
	require.NoError(t, err)

	version, err := RotateUser(ctx, v, e.pool, e.tenant, u)
	require.NoError(t, err)
	require.EqualValues(t, 2, version)

	after, err := v.ForUser(ctx, e.tenant, u)
	require.NoError(t, err)
	require.EqualValues(t, 2, after.Version())
	fresh := e.sealedBy(v, u)
	require.EqualValues(t, []byte{0, 0, 0, 2}, fresh[1:5], "a new seal carries version 2")
	require.EqualValues(t, []byte{0, 0, 0, 1}, old[1:5])
	require.NoError(t, e.opens(v, u, old), "version 1 still opens")
	require.Equal(t, before.ContentHash([]byte("x")), after.ContentHash([]byte("x")), "the keyed hashes are the same")
	require.Equal(t, before.SourceKey("Watch"), after.SourceKey("Watch"))
	k1, err := after.Keys(1)
	require.NoError(t, err)
	k2, err := after.Keys(2)
	require.NoError(t, err)
	require.NotEqual(t, k1.Data, k2.Data, "the data key is new")
	require.NotEqual(t, k1.X25519, k2.X25519, "the KEM keys are new")
	pubAfter, pubVersion, err := v.PublicKey(ctx, e.pool, e.tenant, u)
	require.NoError(t, err)
	require.EqualValues(t, 2, pubVersion)
	require.NotEqual(t, pubBefore.Marshal(), pubAfter.Marshal(), "raw objects are sealed to the new public key")

	// The other process seals under version 2 once the notification lands.
	require.Eventually(t, func() bool {
		uv, err := other.ForUser(ctx, e.tenant, u)
		return err == nil && uv.Version() == 2
	}, 5*time.Second, 10*time.Millisecond)

	_, err = RotateUser(ctx, v, e.pool, e.tenant, uuid.New())
	require.ErrorIs(t, err, ErrNoKeys)
	_, err = RotateUser(ctx, e.vault(nil, Options{}), e.pool, e.tenant, u)
	require.ErrorIs(t, err, ErrCannotOpen)
}
