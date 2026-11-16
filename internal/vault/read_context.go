// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
)

// The read side (docs/specs/vault.md, "Data flow", step 3): SQL filters on
// clear columns and the store layer opens the rows. A request carries the
// Vault on its context (NewContext); each read takes a Reader for its user,
// which unwraps the user's keys on the first sealed row it meets, so a read
// of clear rows (no vault configured, or a user not converted yet) costs
// nothing more than before.

type contextKey struct{}

// NewContext returns ctx carrying v, for the reads made under it. A nil v
// leaves ctx as it is: the reads then take clear rows only and fail on a
// sealed one.
func NewContext(ctx context.Context, v *Vault) context.Context {
	if v == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, v)
}

// FromContext returns the Vault ctx carries, or nil.
func FromContext(ctx context.Context) *Vault {
	v, _ := ctx.Value(contextKey{}).(*Vault)
	return v
}

// ErrNoVault is a sealed row met by a read that carries no vault: the
// process has no key configuration, or the caller did not pass the Vault on.
// It is never an empty answer.
var ErrNoVault = errors.New("vault: a row is sealed and this read holds no vault")

// AuditOpenFailed is the audit action of a sealed row that did not open. Its
// detail names the table, never a value.
const AuditOpenFailed = "vault.open_failed"

// OpenError is a sealed row of Table that did not open. It wraps ErrOpen.
type OpenError struct{ Table string }

func (e *OpenError) Error() string { return "vault: a sealed row of " + e.Table + " does not open" }

// Unwrap makes errors.Is(err, ErrOpen) true.
func (e *OpenError) Unwrap() error { return ErrOpen }

// Reader opens the sealed rows of one user for one read. It is safe for
// concurrent use.
type Reader struct {
	v            *Vault
	tenant, user uuid.UUID
	// noKeys, when set, is added to the error of a user with no keys.
	noKeys error

	mu       sync.Mutex
	uv       *UserVault
	err      error
	loaded   bool
	reported bool
}

// ReaderFor returns the Reader of a user for the reads made under ctx.
func ReaderFor(ctx context.Context, tenant, user uuid.UUID) *Reader {
	return NewReader(FromContext(ctx), tenant, user)
}

// NewReader returns the Reader of a user over v; v may be nil (ErrNoVault on
// the first sealed row).
func NewReader(v *Vault, tenant, user uuid.UUID) *Reader {
	return &Reader{v: v, tenant: tenant, user: user}
}

// ReaderOf returns a Reader over keys the caller already holds (a job that
// took them for its writes).
func ReaderOf(uv *UserVault) *Reader {
	if uv == nil {
		return &Reader{}
	}
	return &Reader{tenant: uv.tenant, user: uv.user, uv: uv, loaded: true}
}

// UserVaultOf returns the handle of a user over key sets the caller already
// holds, the newest version first in use for new seals: for a tool or a test
// that has the keys without a Vault (the fuzz targets). It is nil without a
// key set.
func UserVaultOf(tenant, user uuid.UUID, keys ...*UserKeys) *UserVault {
	if len(keys) == 0 {
		return nil
	}
	uv := &UserVault{tenant: tenant, user: user, keys: make(map[uint32]*UserKeys, len(keys)), metrics: NewMetrics()}
	for _, k := range keys {
		uv.keys[k.Version] = k
		if uv.newest == nil || k.Version > uv.newest.Version {
			uv.newest = k
		}
	}
	return uv
}

// NoKeysAs makes the reader answer a user with no keys (ErrNoKeys: erased,
// or not given keys yet) with an error that is also as: a read layer passes
// its own "not found", so such a user reads as an absent one.
func (r *Reader) NoKeysAs(as error) *Reader {
	r.noKeys = as
	return r
}

// HasVault reports whether the reader can ask for keys at all. Without one,
// a writer of this user's rows writes them clear, as an instance with no
// vault configured does.
func (r *Reader) HasVault() bool { return r.v != nil || r.uv != nil }

// Keys returns the user's keys, unwrapped once per Reader: ErrNoVault
// without a vault, ErrNoKeys for a user with none (erased, or not given
// keys yet), ErrProviderUnavailable when the key provider is down and the
// cache is cold.
func (r *Reader) Keys(ctx context.Context) (*UserVault, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return r.uv, r.err
	}
	if r.v == nil {
		return nil, ErrNoVault
	}
	uv, err := r.v.ForUser(ctx, r.tenant, r.user)
	if err != nil && ctx.Err() != nil {
		// A cancelled request says nothing about the keys: ask again.
		return nil, err
	}
	r.uv, r.err, r.loaded = uv, err, true
	return uv, err
}

// Open opens the sealed value of the row of t whose identity is identity
// (RowIdentity). A value that does not open is an *OpenError: the caller
// fails its read, it never leaves the row out. The failure is counted
// (ohs_vault_open_failures_total) and recorded once per Reader as the audit
// event vault.open_failed, with the table and no value.
func (r *Reader) Open(ctx context.Context, t Table, identity, sealed []byte) ([]any, error) {
	uv, err := r.Keys(ctx)
	if err != nil {
		if r.noKeys != nil && errors.Is(err, ErrNoKeys) {
			return nil, fmt.Errorf("vault: open %s row: %w (%w)", t.Name, r.noKeys, err)
		}
		return nil, fmt.Errorf("vault: open %s row: %w", t.Name, err)
	}
	vals, err := uv.OpenRow(t, identity, sealed)
	if err != nil {
		r.openFailed(ctx, t.Name)
		return nil, &OpenError{Table: t.Name}
	}
	return vals, nil
}

// auditTimeout bounds the write of the audit event of a failed open.
const auditTimeout = 5 * time.Second

// openFailed records the audit event of a row that did not open, once per
// Reader. It is written on a connection of its own, so the failed read (and
// a transaction it ran in) cannot take it back.
func (r *Reader) openFailed(ctx context.Context, table string) {
	r.mu.Lock()
	done := r.reported
	r.reported = true
	r.mu.Unlock()
	if done || r.v == nil || r.v.pool == nil {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	user := r.user
	// The event goes with the 500 the read answers; a failure to record it
	// must not hide that answer.
	_ = audit.Record(actx, r.v.pool, audit.Event{Tenant: r.tenant, User: &user, ActorType: audit.System,
		ActorID: "vault", Action: AuditOpenFailed, RequestID: reqid.From(ctx), Detail: map[string]any{"table": table}})
}
