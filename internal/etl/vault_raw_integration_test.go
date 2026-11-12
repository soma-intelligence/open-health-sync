// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// withVault gives the env's users keys as they get them in production, and
// returns the Vault the jobs then take.
func (e *env) withVault(users ...uuid.UUID) *vault.Vault {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	for _, u := range users {
		require.NoError(e.t, e.v.EnsureUser(ctx, tx, e.tenant, u))
	}
	require.NoError(e.t, tx.Commit(ctx))
	return e.v
}

// uploadSealed is upload with the object sealed to the user's public key,
// as collect writes it.
func (e *env) uploadSealed(v *vault.Vault, user uuid.UUID, provider, dataType string, lines ...string) queue.ETLJob {
	e.t.Helper()
	return e.uploadSealedAs(v, user, provider, dataType, nil, lines...)
}

// uploadSealedAs is uploadSealed with the header's digest taken over
// digestOf instead of the body when it is not nil: an object that opens but
// is not what its digest says.
func (e *env) uploadSealedAs(v *vault.Vault, user uuid.UUID, provider, dataType string, digestOf []byte, lines ...string) queue.ETLJob {
	e.t.Helper()
	ctx := context.Background()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	for _, l := range lines {
		_, _ = zw.Write([]byte(l + "\n"))
	}
	require.NoError(e.t, zw.Close())
	pub, version, err := v.PublicKey(ctx, e.pool, e.tenant, user)
	require.NoError(e.t, err)
	digest := vault.RawDigest(pub, gz.Bytes())
	if digestOf != nil {
		digest = vault.RawDigest(pub, digestOf)
	}
	var sealed bytes.Buffer
	require.NoError(e.t, vault.SealObject(&sealed, pub, version, digest, bytes.NewReader(gz.Bytes())))

	batch := fmt.Sprintf("b%d", batchSeq.Add(1))
	key := rawstore.Key(e.tenant, user, provider, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), batch, 0)
	require.NoError(e.t, e.rs.Put(ctx, key, bytes.NewReader(sealed.Bytes())))
	e.exec(`
		INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8)`,
		e.tenant, user, provider, key, hex.EncodeToString(digest[:]), sealed.Len(), batch, dataType)
	return queue.ETLJob{TenantID: e.tenant, UserID: user, Provider: provider, Key: key, Tier: queue.TierStandard}
}

// A sealed object is processed as its legacy gzip twin is: same counts, same
// rows, and a legacy object still reads with the vault on.
func TestProcess_SealedRawObjectAsLegacy(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	other := e.newUser(e.tenant)
	v := e.withVault(e.user, other)
	at := recent()
	lines := []string{
		q("heart_rate", 62, "bpm", at),
		q("heart_rate", 64, "count/min", at.Add(time.Minute)),
		`not json`,
		line(map[string]any{"fail": "bad record"}),
	}
	opts := e.opts()
	opts.Vault = v

	sealed, err := ProcessWith(context.Background(), e.pool, e.rs, e.uploadSealed(v, e.user, "apple_health", "heart_rate", lines...), opts)
	require.NoError(t, err)
	legacy, err := ProcessWith(context.Background(), e.pool, e.rs, e.upload(e.tenant, other, "apple_health", "heart_rate", lines...), opts)
	require.NoError(t, err)

	assert.Equal(t, TableCount{Written: 2}, sealed["quantity_samples"])
	assert.Equal(t, legacy, sealed)
	assert.Equal(t, 2, e.countFor("quantity_samples", e.tenant, e.user))
	assert.Equal(t, e.countFor("failed_records", e.tenant, other), e.countFor("failed_records", e.tenant, e.user))
}

// Without the vault a sealed object is a job error: nothing is written and
// the object is not taken for an empty one.
func TestProcess_SealedRawObjectWithoutVaultFails(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	v := e.withVault(e.user)
	job := e.uploadSealed(v, e.user, "apple_health", "heart_rate", q("heart_rate", 62, "bpm", recent()))

	_, err := ProcessWith(context.Background(), e.pool, e.rs, job, e.clearOpts())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sealed")
	assert.Zero(t, e.count("quantity_samples"))
	assert.Zero(t, e.count("failed_records"))
}

// A sealed object whose body does not match its digest fails the job, with
// nothing written.
func TestProcess_SealedRawObjectWrongDigestFails(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	v := e.withVault(e.user)
	job := e.uploadSealedAs(v, e.user, "apple_health", "heart_rate", []byte("other"), q("heart_rate", 62, "bpm", recent()))
	opts := e.opts()
	opts.Vault = v

	_, err := ProcessWith(context.Background(), e.pool, e.rs, job, opts)
	require.ErrorIs(t, err, vault.ErrOpen)
	assert.Contains(t, err.Error(), "digest")
	assert.Zero(t, e.count("quantity_samples"))
	assert.Zero(t, e.count("failed_records"))
}
