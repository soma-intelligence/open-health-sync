// SPDX-License-Identifier: AGPL-3.0-only

// Package tenantproviders holds the providers an instance admin turned off
// for one tenant (ohs_core.tenant_providers, docs/admin-api.md). A provider is
// on for a tenant until a row says otherwise. Allowed answers from a snapshot
// in memory, without I/O, so every request and pull can ask: a change saved
// in this process applies at once, and Run reloads what other processes
// saved.
package tenantproviders

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

// ReloadEvery is how often Run reads the switches again.
const ReloadEvery = 30 * time.Second

// ErrUnknownTenant is a change for a tenant that does not exist.
var ErrUnknownTenant = errors.New("tenantproviders: unknown tenant")

// Key names one provider of one tenant.
type Key struct {
	Tenant   uuid.UUID
	Provider string
}

// Setting is the stored switch of one provider for one tenant.
type Setting struct {
	Tenant   uuid.UUID
	Provider string
	Enabled  bool
	// UpdatedBy is the id of the admin key that saved it.
	UpdatedBy string
	UpdatedAt time.Time
}

// Store is the table. The interface lets the Set be tested without a
// database.
type Store interface {
	// Off lists the providers turned off, for every tenant.
	Off(ctx context.Context) ([]Key, error)
	// List returns the rows of one tenant.
	List(ctx context.Context, tenant uuid.UUID) ([]Setting, error)
	// Put stores one row, replacing the previous one, and returns it as
	// stored. ev, when set, is recorded in the audit log in the same
	// transaction. An unknown tenant is ErrUnknownTenant.
	Put(ctx context.Context, s Setting, ev *audit.Event) (Setting, error)
}

// Set answers whether a tenant may use a provider. Its reads are lock free;
// a nil Set allows everything.
type Set struct {
	store Store
	mu    sync.Mutex // serializes loads and writes
	off   atomic.Pointer[map[Key]struct{}]
}

// New reads the switches from store. When they cannot be read the error is
// logged and every provider is on until Run reloads them, as the provider
// set runs on the environment until the console's settings can be read.
func New(ctx context.Context, store Store) *Set {
	s := &Set{store: store}
	s.off.Store(&map[Key]struct{}{})
	if err := s.Reload(ctx); err != nil {
		slog.Error("tenant providers: the switches cannot be read, every provider is on until the next reload",
			"error", err)
	}
	return s
}

// Allowed reports whether the tenant may use the provider: true unless an
// instance admin turned it off for that tenant. It does no I/O.
func (s *Set) Allowed(tenant uuid.UUID, slug string) bool {
	if s == nil {
		return true
	}
	_, off := (*s.off.Load())[Key{Tenant: tenant, Provider: slug}]
	return !off
}

// Reload reads the switches again and applies them.
func (s *Set) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(ctx)
}

// load reads the providers turned off and publishes them. s.mu is held.
func (s *Set) load(ctx context.Context) error {
	keys, err := s.store.Off(ctx)
	if err != nil {
		return err
	}
	next := make(map[Key]struct{}, len(keys))
	for _, k := range keys {
		next[k] = struct{}{}
	}
	s.off.Store(&next)
	return nil
}

// Run reloads every interval (ReloadEvery when 0) until ctx ends. A failed
// reload is logged and the last good switches stay.
func (s *Set) Run(ctx context.Context, every time.Duration) error {
	if every <= 0 {
		every = ReloadEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := s.Reload(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("tenant providers: reload failed, keeping the current switches", "error", err)
			}
		}
	}
}

// Tenant returns the stored switches of one tenant by provider. A provider
// with no row is on.
func (s *Set) Tenant(ctx context.Context, tenant uuid.UUID) (map[string]Setting, error) {
	rows, err := s.store.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Setting, len(rows))
	for _, r := range rows {
		out[r.Provider] = r
	}
	return out, nil
}

// Update stores the switch of one provider for one tenant, with ev in the
// same transaction when set, and applies it in this process at once. by is
// the id of the admin key making the change.
func (s *Set) Update(ctx context.Context, tenant uuid.UUID, slug string, enabled bool, by string, ev *audit.Event) (Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.store.Put(ctx, Setting{Tenant: tenant, Provider: slug, Enabled: enabled, UpdatedBy: by}, ev)
	if err != nil {
		return Setting{}, err
	}
	if err := s.load(ctx); err != nil {
		// The change is stored: apply it here even though the others could
		// not be read again; the next reload brings them.
		slog.Warn("tenant providers: reload after a change failed, applying the change alone", "error", err)
		next := maps.Clone(*s.off.Load())
		k := Key{Tenant: tenant, Provider: slug}
		if enabled {
			delete(next, k)
		} else {
			next[k] = struct{}{}
		}
		s.off.Store(&next)
	}
	return st, nil
}
