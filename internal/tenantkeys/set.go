// SPDX-License-Identifier: AGPL-3.0-only

// Package tenantkeys holds the provider keys a tenant brings for itself
// (ohs_core.tenant_provider_keys, docs/admin-api.md). While a tenant has keys
// for a provider, its users connect through the tenant's own OAuth
// application; every other tenant keeps the instance's keys. A connection
// records the keys it was made with, and its refreshes, pulls, notifications
// and revocation keep using them.
//
// Each set of keys runs in its own connector, made with
// connector.Instancer. The lookups answer from a snapshot in memory, without
// I/O: a change saved in this process applies at once, and Run reloads what
// other processes saved.
package tenantkeys

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
)

// ReloadEvery is how often Run reads the keys again.
const ReloadEvery = 30 * time.Second

// Statuses of a tenant's keys.
const (
	// StatusActive: the keys run; new connections of the tenant use them.
	StatusActive = "active"
	// StatusError: the keys do not open with the configured encryption
	// keys, or the connector refused them. The tenant's new connections are
	// refused rather than sent to the instance's keys.
	StatusError = "error"
)

// Errors of Put and Delete.
var (
	ErrUnknown       = errors.New("tenantkeys: unknown provider")
	ErrUnsupported   = errors.New("tenantkeys: this provider does not take tenant keys")
	ErrUnknownTenant = errors.New("tenantkeys: unknown tenant")
	errNoStore       = errors.New("tenantkeys: no store")
)

// appKeys are the keys that name the OAuth application, its account or
// region: keys that differ in one of them belong to another application,
// whose server does not refresh the grants of the first.
var appKeys = []string{"CLIENT_ID", "REGION", "ENVIRONMENT"}

// SameApp reports whether keys b name the same OAuth application as a.
func SameApp(a, b config.ProviderKeys) bool {
	for _, f := range appKeys {
		if a[f] != b[f] {
			return false
		}
	}
	return true
}

// Key names one provider of one tenant.
type Key struct {
	Tenant   uuid.UUID
	Provider string
}

// State is how one tenant's keys for one provider stand in this process.
type State struct {
	Keys
	// Connector runs the keys; nil unless Status is StatusActive.
	Connector connector.Connector
	Status    string
	// Error says why the status is StatusError.
	Error string
}

// Candidate is one tenant's connector of a provider, for a notification
// that names no tenant.
type Candidate struct {
	Tenant    uuid.UUID
	ID        uuid.UUID
	Connector connector.Connector
}

// Options build a Set.
type Options struct {
	// Connectors are the registered connectors; nil means connector.All().
	Connectors []connector.Connector
	// Store holds the keys. Nil holds none; Put and Delete then fail.
	Store Store
}

type snapshot struct {
	byKey  map[Key]State
	byID   map[uuid.UUID]State
	bySlug map[string][]Candidate
	slugs  []string
}

// running is one connector made for a set of keys.
type running struct {
	c    connector.Connector
	keys config.ProviderKeys
}

// Set holds every tenant's keys. Its reads are lock free; a nil Set holds
// none.
type Set struct {
	all   map[string]connector.Connector
	store Store

	mu  sync.Mutex // serializes loads and writes
	run map[uuid.UUID]running
	cur atomic.Pointer[snapshot]
}

// New reads the keys from the store. When they cannot be read the error is
// logged and Run retries; meanwhile no tenant has keys, and a tenant's new
// connection is started on the instance's keys.
func New(ctx context.Context, o Options) *Set {
	all := o.Connectors
	if all == nil {
		all = connector.All()
	}
	s := &Set{all: map[string]connector.Connector{}, store: o.Store, run: map[uuid.UUID]running{}}
	for _, c := range all {
		s.all[c.Slug()] = c
	}
	s.cur.Store(&snapshot{byKey: map[Key]State{}, byID: map[uuid.UUID]State{}, bySlug: map[string][]Candidate{}})
	if s.store != nil {
		if err := s.Reload(ctx); err != nil {
			slog.Error("tenant keys: the tenants' provider keys cannot be read, every tenant runs on the instance's keys until the next reload",
				"error", err)
		}
	}
	return s
}

// Supports reports whether slug takes tenant keys, and its required and
// optional keys.
func (s *Set) Supports(slug string) (required, optional []string, ok bool) {
	if s == nil {
		return nil, nil, false
	}
	c, found := s.all[slug]
	if !found {
		return nil, nil, false
	}
	return supports(c)
}

func supports(c connector.Connector) (required, optional []string, ok bool) {
	k, keyed := c.(connector.KeyedConnector)
	_, inst := c.(connector.Instancer)
	_, cfg := c.(connector.Configurable)
	_, oauth := connector.AsOAuth(c)
	if !keyed || !inst || !cfg || !oauth {
		return nil, nil, false
	}
	if o, ok := c.(connector.OptionalKeyed); ok {
		optional = o.OptionalKeys()
	}
	return k.RequiredKeys(), optional, true
}

// Template returns the registered connector of slug, to ask what the
// provider can do. It runs the instance's keys, if any: never use it for a
// tenant's provider calls.
func (s *Set) Template(slug string) (connector.Connector, bool) {
	if s == nil {
		return nil, false
	}
	c, ok := s.all[slug]
	return c, ok
}

// Known reports whether slug is a registered connector.
func (s *Set) Known(slug string) bool {
	if s == nil {
		return false
	}
	_, ok := s.all[slug]
	return ok
}

// Has reports whether the tenant set keys for slug, whether they run or
// not. A tenant with keys that do not run gets no fallback to the
// instance's keys: its users would consent to an application it did not
// choose.
func (s *Set) Has(tenant uuid.UUID, slug string) bool {
	if s == nil {
		return false
	}
	_, ok := s.cur.Load().byKey[Key{tenant, slug}]
	return ok
}

// Current returns the connector running the tenant's keys for slug and the
// keys' ID, when the tenant has keys that run. It does no I/O.
func (s *Set) Current(tenant uuid.UUID, slug string) (connector.Connector, uuid.UUID, bool) {
	if s == nil {
		return nil, uuid.Nil, false
	}
	st, ok := s.cur.Load().byKey[Key{tenant, slug}]
	if !ok || st.Connector == nil {
		return nil, uuid.Nil, false
	}
	return st.Connector, st.ID, true
}

// Get returns the connector of the keys id, while the tenant still has them
// for slug and they run. It does no I/O.
func (s *Set) Get(tenant uuid.UUID, slug string, id uuid.UUID) (connector.Connector, bool) {
	if s == nil {
		return nil, false
	}
	st, ok := s.cur.Load().byID[id]
	if !ok || st.Tenant != tenant || st.Provider != slug || st.Connector == nil {
		return nil, false
	}
	return st.Connector, true
}

// Candidates lists the running connectors of slug, one per tenant with keys,
// ordered by tenant.
func (s *Set) Candidates(slug string) []Candidate {
	if s == nil {
		return nil
	}
	return s.cur.Load().bySlug[slug]
}

// Any returns one running tenant connector of slug, to ask what the
// provider can do when the instance has no keys for it.
func (s *Set) Any(slug string) (connector.Connector, bool) {
	cs := s.Candidates(slug)
	if len(cs) == 0 {
		return nil, false
	}
	return cs[0].Connector, true
}

// Slugs lists the providers some tenant runs keys for, sorted.
func (s *Set) Slugs() []string {
	if s == nil {
		return nil
	}
	return s.cur.Load().slugs
}

// Tenant returns the states of one tenant's keys, by provider, from the
// snapshot.
func (s *Set) Tenant(tenant uuid.UUID) map[string]State {
	out := map[string]State{}
	if s == nil {
		return out
	}
	for k, st := range s.cur.Load().byKey {
		if k.Tenant == tenant {
			out[k.Provider] = st
		}
	}
	return out
}

// Lookup reads the tenant's keys for slug from the store, with their status
// in this process.
func (s *Set) Lookup(ctx context.Context, tenant uuid.UUID, slug string) (State, bool, error) {
	if s == nil || s.store == nil {
		return State{}, false, nil
	}
	k, ok, err := s.store.Get(ctx, tenant, slug)
	if err != nil || !ok {
		return State{}, false, err
	}
	if st, live := s.cur.Load().byKey[Key{tenant, slug}]; live && st.ID == k.ID && st.UpdatedAt.Equal(k.UpdatedAt) {
		return st, true, nil
	}
	// Saved by another process since the last reload.
	st := State{Keys: k, Status: StatusActive}
	if k.OpenErr != nil {
		st.Status, st.Error = StatusError, k.OpenErr.Error()
	}
	return st, true, nil
}

// Put validates keys for the tenant's slug and stores them, replacing any
// the tenant had, then applies them in this process. It returns the new
// state and how many connections were set needs_reauth because the keys
// name another application than the ones they replace. ev, when set, is
// recorded on the tenant's audit log with the change. by is the id of the
// admin key making it.
func (s *Set) Put(ctx context.Context, tenant uuid.UUID, slug string, keys map[string]string, by string, ev *audit.Event) (State, int, error) {
	if s == nil || s.store == nil {
		return State{}, 0, errNoStore
	}
	c, found := s.all[slug]
	if !found {
		return State{}, 0, ErrUnknown
	}
	required, optional, ok := supports(c)
	if !ok {
		return State{}, 0, ErrUnsupported
	}
	clean, err := validate(c, slug, required, optional, keys)
	if err != nil {
		return State{}, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, retired, err := s.store.Put(ctx, Keys{Tenant: tenant, Provider: slug, Keys: clean, UpdatedBy: by},
		func(old config.ProviderKeys) bool { return SameApp(old, clean) }, ev)
	if err != nil {
		return State{}, 0, err
	}
	s.reloadAfter(ctx, func(snap map[Key]Keys) { snap[Key{tenant, slug}] = stored })
	st := s.cur.Load().byKey[Key{tenant, slug}]
	return st, retired, nil
}

// Delete removes the tenant's keys for slug and applies it: the tenant's new
// connections go back to the instance's keys, and the connections made with
// the removed keys are set needs_reauth (their count is returned). found is
// false when the tenant had none.
func (s *Set) Delete(ctx context.Context, tenant uuid.UUID, slug string, ev *audit.Event) (State, int, bool, error) {
	if s == nil || s.store == nil {
		return State{}, 0, false, errNoStore
	}
	if _, found := s.all[slug]; !found {
		return State{}, 0, false, ErrUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, retired, found, err := s.store.Delete(ctx, tenant, slug, ev)
	if err != nil || !found {
		return State{}, 0, found, err
	}
	s.reloadAfter(ctx, func(snap map[Key]Keys) { delete(snap, Key{tenant, slug}) })
	return State{Keys: prev}, retired, true, nil
}

// validate checks the keys of a Put: only the provider's keys, every
// required one, values a provider could issue, and the connector's own
// Configure on a fresh connector. It returns them trimmed, empty ones left
// out.
func validate(c connector.Connector, slug string, required, optional []string, keys map[string]string) (config.ProviderKeys, error) {
	allowed := append(slices.Clone(required), optional...)
	clean := config.ProviderKeys{}
	for _, f := range slices.Sorted(maps.Keys(keys)) {
		if !slices.Contains(allowed, f) {
			return nil, &providerset.FieldError{Msg: fmt.Sprintf("%s takes no key %q: its keys are %s",
				slug, f, strings.Join(allowed, ", "))}
		}
		v := strings.TrimSpace(keys[f])
		if v == "" {
			continue
		}
		if err := providerset.CheckValue(f, v); err != nil {
			return nil, err
		}
		clean[f] = v
	}
	var missing []string
	for _, f := range required {
		if clean[f] == "" {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return nil, &providerset.FieldError{Msg: fmt.Sprintf("keys must include %s", strings.Join(missing, " and "))}
	}
	if !c.Enabled(clean) {
		return nil, &providerset.RefusedError{Msg: slug + " does not run with these keys"}
	}
	trial := c.(connector.Instancer).NewInstance()
	if err := trial.(connector.Configurable).Configure(clean); err != nil {
		return nil, &providerset.RefusedError{Msg: err.Error()}
	}
	return clean, nil
}

// Reload reads the store again and applies it.
func (s *Set) Reload(ctx context.Context) error {
	if s == nil || s.store == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	s.apply(rows)
	return nil
}

// reloadAfter reads the store after a change of this process; when it
// cannot, it applies change to the current snapshot alone, the next reload
// bringing the others. s.mu is held.
func (s *Set) reloadAfter(ctx context.Context, change func(map[Key]Keys)) {
	rows, err := s.store.List(ctx)
	if err == nil {
		s.apply(rows)
		return
	}
	slog.Warn("tenant keys: reload after a change failed, applying the change alone", "error", err)
	cur := map[Key]Keys{}
	for k, st := range s.cur.Load().byKey {
		cur[k] = st.Keys
	}
	change(cur)
	list := make([]Keys, 0, len(cur))
	for _, k := range cur {
		list = append(list, k)
	}
	s.apply(list)
}

// Run reloads every interval (ReloadEvery when 0) until ctx ends. A failed
// reload is logged and the last good snapshot stays.
func (s *Set) Run(ctx context.Context, every time.Duration) error {
	if s == nil || s.store == nil {
		<-ctx.Done()
		return nil
	}
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
				slog.Warn("tenant keys: reload failed, keeping the current keys", "error", err)
			}
		}
	}
}

// apply builds and publishes the snapshot of rows, reusing the connector of
// keys that did not change and configuring the others. s.mu is held.
func (s *Set) apply(rows []Keys) {
	snap := &snapshot{byKey: map[Key]State{}, byID: map[uuid.UUID]State{}, bySlug: map[string][]Candidate{}}
	next := map[uuid.UUID]running{}
	slices.SortFunc(rows, func(a, b Keys) int {
		if c := strings.Compare(a.Tenant.String(), b.Tenant.String()); c != 0 {
			return c
		}
		return strings.Compare(a.Provider, b.Provider)
	})
	for _, k := range rows {
		st := State{Keys: k, Status: StatusError}
		switch tmpl, found := s.all[k.Provider]; {
		case !found:
			st.Error = "this provider is not registered in this build"
		case k.OpenErr != nil:
			st.Error = k.OpenErr.Error()
		default:
			c, err := s.connector(tmpl, k)
			if err != nil {
				st.Error = err.Error()
				slog.Warn("tenant keys: refused by the connector", "tenant_id", k.Tenant, "provider", k.Provider,
					"error", err)
				break
			}
			next[k.ID] = running{c: c, keys: maps.Clone(k.Keys)}
			st.Connector, st.Status = c, StatusActive
			snap.bySlug[k.Provider] = append(snap.bySlug[k.Provider], Candidate{Tenant: k.Tenant, ID: k.ID, Connector: c})
			if !slices.Contains(snap.slugs, k.Provider) {
				snap.slugs = append(snap.slugs, k.Provider)
			}
		}
		snap.byKey[Key{k.Tenant, k.Provider}] = st
		snap.byID[k.ID] = st
	}
	slices.Sort(snap.slugs)
	s.run = next
	s.cur.Store(snap)
}

// connector returns the connector of k: the running one when its keys did
// not change, the running one configured with them when they did (a secret
// rotated), else a new one. s.mu is held.
func (s *Set) connector(tmpl connector.Connector, k Keys) (connector.Connector, error) {
	if _, _, ok := supports(tmpl); !ok {
		return nil, errors.New("this provider does not take tenant keys")
	}
	if r, ok := s.run[k.ID]; ok {
		if maps.Equal(r.keys, k.Keys) {
			return r.c, nil
		}
		if err := r.c.(connector.Configurable).Configure(k.Keys); err != nil {
			return nil, err
		}
		return r.c, nil
	}
	c := tmpl.(connector.Instancer).NewInstance()
	if err := c.(connector.Configurable).Configure(k.Keys); err != nil {
		return nil, err
	}
	return c, nil
}
