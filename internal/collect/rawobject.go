// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// rawObject is one raw file to register: the raw_objects row and its ETL job.
type rawObject struct {
	tenant, user uuid.UUID
	provider     string
	key, hash    string
	size         int
	batch        string
	part         int
	dataType     string
	tier         string
}

// rawMatch is a registered raw object found by key or by content hash.
type rawMatch struct {
	key string
	// otherBytes: the key matched but holds other bytes.
	otherBytes bool
}

// findRaw looks for a registered raw object of this user with this key or
// these bytes. A row with the same key is preferred over one with the same
// hash. found is false when neither exists.
func findRaw(ctx context.Context, pool *pgxpool.Pool, tenant, user uuid.UUID, key, hash string) (rawMatch, bool, error) {
	var gotKey, gotHash string
	err := pool.QueryRow(ctx, `
		SELECT key, content_hash FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 AND (key = $3 OR content_hash = $4)
		ORDER BY (key = $3) DESC
		LIMIT 1`, tenant, user, key, hash).Scan(&gotKey, &gotHash)
	if errors.Is(err, pgx.ErrNoRows) {
		// The key is globally unique and embeds tenant and user, so no row of
		// another owner can hold it.
		return rawMatch{}, false, nil
	}
	if err != nil {
		return rawMatch{}, false, fmt.Errorf("look up raw object: %w", err)
	}
	return rawMatch{key: gotKey, otherBytes: gotKey == key && gotHash != hash}, true, nil
}

// lockActiveUser takes a share lock on the end user row and returns
// errInactive when the user is gone or being deleted. Held until tx ends, it
// keeps a deletion from starting while the caller writes the user's rows.
func lockActiveUser(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		tenant, user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return errInactive
	}
	if err != nil {
		return fmt.Errorf("lock user: %w", err)
	}
	return nil
}

// userActive is the unlocked check done before a raw file is written, so a
// user being deleted gets no new file (registerRaw re-checks under the lock).
func userActive(ctx context.Context, pool *pgxpool.Pool, tenant, user uuid.UUID) error {
	var status string
	err := pool.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2`,
		tenant, user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return errInactive
	}
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}
	return nil
}

// errErasing means the user's data from this provider is being erased
// (a provider deletion job is unfinished): nothing of it is stored meanwhile.
var errErasing = errors.New("collect: this provider's data is being deleted for this user")

// registerRaw inserts the raw_objects row and its ETL job in one transaction
// (the orphan scanner relies on both or neither). It returns false, with
// nothing written, when a row with this key or these bytes already exists,
// errInactive when the user is gone or being deleted, and errErasing while
// the user's data from this provider is being erased. The caller removes the
// file it wrote on either error, so none outlives the erasure's raw sweep.
func registerRaw(ctx context.Context, pool *pgxpool.Pool, o rawObject) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockActiveUser(ctx, tx, o.tenant, o.user); err != nil {
		return false, err
	}
	// The erasure job takes the user row FOR UPDATE: under this share lock
	// it either has not started its delete (and is pending, seen here) or
	// has committed.
	if erasing, err := deletion.Pending(ctx, tx, o.tenant, o.user, o.provider); err != nil {
		return false, fmt.Errorf("look up provider deletion: %w", err)
	} else if erasing {
		return false, errErasing
	}

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO ohs_sync.raw_objects
			(tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		o.tenant, o.user, o.provider, o.key, o.hash, o.size, o.batch, o.part, o.dataType).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert raw object: %w", err)
	}
	if err := queue.EnqueueETL(ctx, tx, queue.ETLJob{
		TenantID: o.tenant, UserID: o.user, Provider: o.provider, Key: o.key, Tier: o.tier,
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		// The file stays: raw is never rewritten, and the daily file scan
		// reports it as unregistered.
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

var errOtherBytes = errors.New("collect: key holds other bytes")

// putRaw writes body under key. rawstore.ErrExists means "maybe stored" (an
// earlier attempt whose registration failed, or a concurrent one): the stored
// bytes are read back and compared. Equal is success; different is
// errOtherBytes; gone is retried once.
func putRaw(ctx context.Context, rs rawstore.Store, key string, body []byte) error {
	for attempt := 0; attempt < 2; attempt++ {
		err := rs.Put(ctx, key, bytes.NewReader(body))
		if err == nil {
			return nil
		}
		if !errors.Is(err, rawstore.ErrExists) {
			return fmt.Errorf("put raw object: %w", err)
		}
		same, err := sameBytes(ctx, rs, key, body)
		if errors.Is(err, rawstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !same {
			return errOtherBytes
		}
		return nil
	}
	return errors.New("put raw object: key reported existing but is not readable")
}

func sameBytes(ctx context.Context, rs rawstore.Store, key string, body []byte) (bool, error) {
	rc, err := rs.Get(ctx, key)
	if err != nil {
		if errors.Is(err, rawstore.ErrNotFound) {
			return false, err
		}
		return false, fmt.Errorf("read back raw object: %w", err)
	}
	defer rc.Close()
	stored, err := io.ReadAll(io.LimitReader(rc, int64(len(body))+1))
	if err != nil {
		return false, fmt.Errorf("read back raw object: %w", err)
	}
	return bytes.Equal(stored, body), nil
}
