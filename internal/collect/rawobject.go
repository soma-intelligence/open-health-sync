// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/deletion"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
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
	// captured is when the data was fetched from the provider (the start of
	// the pull's fetch). Zero for an upload or a notification body, which
	// count from when the row is created.
	captured time.Time
}

// rawMatch is a registered raw object found by key or by content hash.
type rawMatch struct {
	key string
	// otherBytes: the key matched but holds other bytes.
	otherBytes bool
}

// findRaw looks for a registered raw object of this user with this key or
// these bytes: b's hash, or the plain SHA-256 a row registered before the
// vault carries. A row with the same key is preferred over one with the same
// hash. found is false when neither exists.
func findRaw(ctx context.Context, pool *pgxpool.Pool, tenant, user uuid.UUID, key string, b rawBody) (rawMatch, bool, error) {
	var gotKey, gotHash string
	err := pool.QueryRow(ctx, `
		SELECT key, content_hash FROM ohs_sync.raw_objects
		WHERE tenant_id = $1 AND user_id = $2 AND (key = $3 OR content_hash IN ($4, $5))
		ORDER BY (key = $3) DESC
		LIMIT 1`, tenant, user, key, b.hash, b.legacyHash).Scan(&gotKey, &gotHash)
	if errors.Is(err, pgx.ErrNoRows) {
		// The key is globally unique and embeds tenant and user, so no row of
		// another owner can hold it.
		return rawMatch{}, false, nil
	}
	if err != nil {
		return rawMatch{}, false, fmt.Errorf("look up raw object: %w", err)
	}
	same := gotHash == b.hash || gotHash == b.legacyHash
	return rawMatch{key: gotKey, otherBytes: gotKey == key && !same}, true, nil
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

	// created_at is the start of this transaction, which can be long after
	// the data was fetched: a pull says when its fetch started.
	var captured *time.Time
	if !o.captured.IsZero() {
		captured = &o.captured
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO ohs_sync.raw_objects
			(tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type, captured_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT DO NOTHING
		RETURNING id`,
		o.tenant, o.user, o.provider, o.key, o.hash, o.size, o.batch, o.part, o.dataType, captured).Scan(&id)
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

// rawBody is one raw file before it is written: the gzip bytes, and the
// content_hash that names them for their owner. With a vault the hash is the
// owner's RawDigest of the bytes and the file is written sealed to their
// public key; without one (pub nil) it is the plain SHA-256 and the bytes are
// written as they are.
type rawBody struct {
	gz   []byte
	hash string
	// legacyHash is the plain SHA-256: the hash of the same bytes
	// registered before the vault (equal to hash without one).
	legacyHash string
	digest     [32]byte
	pub        *vault.PublicKey
	version    uint32
	// digestAt is RawDigest of gz under another key version of the owner,
	// for an object stored before their key was rotated.
	digestAt func(ctx context.Context, version uint32) ([32]byte, error)
}

// newRawBody hashes gz for its owner. It reads only the owner's public key,
// so a collect-only process seals as well as one that opens keys. A user
// with no keys gets nothing written in clear: errInactive when the user is
// being deleted (their keys go first), an error otherwise.
func newRawBody(ctx context.Context, pool *pgxpool.Pool, v *vault.Vault, tenant, user uuid.UUID, gz []byte) (rawBody, error) {
	sum := sha256.Sum256(gz)
	legacy := hex.EncodeToString(sum[:])
	if v == nil {
		return rawBody{gz: gz, hash: legacy, legacyHash: legacy}, nil
	}
	pub, version, err := v.PublicKey(ctx, pool, tenant, user)
	if errors.Is(err, vault.ErrNoKeys) {
		if err := userActive(ctx, pool, tenant, user); err != nil {
			return rawBody{}, err
		}
		return rawBody{}, fmt.Errorf("collect: %w", err)
	}
	if err != nil {
		return rawBody{}, err
	}
	digest := vault.RawDigest(pub, gz)
	return rawBody{
		gz: gz, hash: hex.EncodeToString(digest[:]), legacyHash: legacy, digest: digest, pub: &pub, version: version,
		digestAt: func(ctx context.Context, at uint32) ([32]byte, error) {
			p, err := v.PublicKeyVersion(ctx, pool, tenant, user, at)
			if err != nil {
				return [32]byte{}, err
			}
			return vault.RawDigest(p, gz), nil
		},
	}, nil
}

// stored returns the bytes written to the raw store: the sealed object, or
// the gzip bytes as they are without a vault. One call is one KEM operation.
func (b rawBody) stored() ([]byte, error) {
	if b.pub == nil {
		return b.gz, nil
	}
	var out bytes.Buffer
	out.Grow(len(b.gz) + len(b.gz)/256 + 4096)
	if err := vault.SealObject(&out, *b.pub, b.version, b.digest, bytes.NewReader(b.gz)); err != nil {
		return nil, fmt.Errorf("seal raw object: %w", err)
	}
	return out.Bytes(), nil
}

// putRaw writes b under key and returns the size of what the key holds.
// rawstore.ErrExists means "maybe stored" (an earlier attempt whose
// registration failed, or a concurrent one): the stored object is read back
// and compared (sameRaw). Equal is success; different is errOtherBytes; gone
// is retried once. The object is sealed once, so a retry writes the same
// bytes.
func putRaw(ctx context.Context, rs rawstore.Store, key string, b rawBody) (int, error) {
	data, err := b.stored()
	if err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := rs.Put(ctx, key, bytes.NewReader(data))
		if err == nil {
			return len(data), nil
		}
		if !errors.Is(err, rawstore.ErrExists) {
			return 0, fmt.Errorf("put raw object: %w", err)
		}
		same, legacy, err := sameRaw(ctx, rs, key, b)
		if errors.Is(err, rawstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !same {
			return 0, errOtherBytes
		}
		if legacy {
			return len(b.gz), nil
		}
		// Every seal of one body has the same size: the header and the
		// chunk overheads depend only on its length.
		return len(data), nil
	}
	return 0, errors.New("put raw object: key reported existing but is not readable")
}

// sameRaw reports whether the object stored under key holds b, and whether
// it is a legacy one. A sealed object is compared by the digest in its
// header, read without keys and computed with the public key of the version
// the header names: its bytes differ on every write. A legacy object (gzip,
// stored before the vault or without one) is compared byte for byte, as
// before. A sealed object with a damaged header holds other bytes.
func sameRaw(ctx context.Context, rs rawstore.Store, key string, b rawBody) (same, legacy bool, err error) {
	rc, err := rs.Get(ctx, key)
	if err != nil {
		if errors.Is(err, rawstore.ErrNotFound) {
			return false, false, err
		}
		return false, false, fmt.Errorf("read back raw object: %w", err)
	}
	defer rc.Close()
	// bufio's default buffer holds the whole header, so ReadObjectHeader
	// only peeks and the legacy comparison still sees every byte.
	br := bufio.NewReader(rc)
	h, sealed, err := vault.ReadObjectHeader(br)
	if errors.Is(err, vault.ErrOpen) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read back raw object: %w", err)
	}
	if sealed {
		if b.pub == nil {
			return false, false, nil
		}
		want := b.digest
		if h.Version != b.version {
			// Sealed before a key rotation: the digest names the key of
			// its own version. A version the user no longer has holds
			// other bytes.
			if want, err = b.digestAt(ctx, h.Version); errors.Is(err, vault.ErrNoKeys) {
				return false, false, nil
			} else if err != nil {
				return false, false, fmt.Errorf("read back raw object: %w", err)
			}
		}
		return h.Digest == want, false, nil
	}
	stored, err := io.ReadAll(io.LimitReader(br, int64(len(b.gz))+1))
	if err != nil {
		return false, false, fmt.Errorf("read back raw object: %w", err)
	}
	return bytes.Equal(stored, b.gz), true, nil
}
