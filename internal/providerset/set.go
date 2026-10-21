// SPDX-License-Identifier: AGPL-3.0-only

// Package providerset holds the connectors enabled on this instance and what
// the console saved for them (docs/console.md). A provider runs with its
// OHS_<PROVIDER>_* environment variables when any is set, else with the keys
// saved in the console; the console's switch turns either off. The set is
// swapped atomically: a change saved in this process applies at once, and
// Run reloads what other processes saved.
package providerset

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
	"unicode"
	"unicode/utf8"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// ReloadEvery is how often Run reads the settings again.
const ReloadEvery = 30 * time.Second

// MaxValue bounds one key's value.
const MaxValue = 4096

// Statuses of a provider.
const (
	// StatusActive: enabled, users connect and sync.
	StatusActive = "active"
	// StatusOff: turned off from the console. Connections are kept.
	StatusOff = "off"
	// StatusNotConfigured: a required key is missing.
	StatusNotConfigured = "not_configured"
	// StatusError: the connector refused its keys, or the saved keys do not
	// open with the configured encryption keys.
	StatusError = "error"
	// StatusBuiltIn: needs no key and is always on (device sources).
	StatusBuiltIn = "built_in"
)

// Where a provider's keys come from.
const (
	SourceEnv     = "env"
	SourceConsole = "console"
)

// State is how one registered connector stands on this instance.
type State struct {
	Slug      string
	Connector connector.Connector
	Status    string
	// Source is SourceEnv or SourceConsole, empty when no key is set.
	Source string
	// Keys are the keys it runs with, secrets included: never serve their
	// values (connector.SecretKey says which may be shown).
	Keys               config.ProviderKeys
	Required, Optional []string
	// Enabled is the console's switch: true when the console saved nothing.
	Enabled bool
	// Error says why the status is StatusError.
	Error     string
	UpdatedBy string
	UpdatedAt *time.Time
}

// Errors of Update and Forget.
var (
	ErrUnknown = errors.New("providerset: unknown provider")
	ErrBuiltIn = errors.New("providerset: this provider needs no key and is always on")
	// ErrEnvKeys refuses a change of keys of a provider whose keys come from
	// its environment variables.
	ErrEnvKeys = errors.New("providerset: the keys of this provider come from its environment variables")
)

// FieldError is a change that names an unknown key or holds a bad value.
type FieldError struct{ Msg string }

func (e *FieldError) Error() string { return e.Msg }

// RefusedError is a change the provider cannot run with: a required key is
// missing, or the connector refused the keys.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

// Change is what the console asks of one provider. A nil value in Keys, or
// an empty one, removes that key; a key not named is kept.
type Change struct {
	Keys    map[string]*string
	Enabled *bool
}

// Options build a Set.
type Options struct {
	// Connectors are the registered connectors; nil means connector.All().
	Connectors []connector.Connector
	// Env are the OHS_<PROVIDER>_* values of the process.
	Env map[string]config.ProviderKeys
	// Store holds what the console saved. Nil runs on the environment only;
	// Update and Forget then fail.
	Store Store
}

type snapshot struct {
	enabled []connector.Connector
	bySlug  map[string]connector.Connector
	slugs   []string
	states  []State
}

// Set is the set of enabled connectors. Its reads are lock free.
type Set struct {
	all   []connector.Connector
	env   map[string]config.ProviderKeys
	store Store

	mu      sync.Mutex // serializes loads and writes
	applied map[string]config.ProviderKeys
	cur     atomic.Pointer[snapshot]
}

// New configures the connectors from the environment, then applies what the
// store holds. A connector that refuses its environment keys stops startup,
// as a wrong environment variable always has; settings that cannot be read
// are logged and retried by Run, the environment applying meanwhile.
func New(ctx context.Context, o Options) (*Set, error) {
	all := o.Connectors
	if all == nil {
		all = connector.All()
	}
	all = slices.Clone(all)
	slices.SortFunc(all, func(a, b connector.Connector) int { return strings.Compare(a.Slug(), b.Slug()) })
	s := &Set{all: all, env: o.Env, store: o.Store, applied: map[string]config.ProviderKeys{}}
	if s.env == nil {
		s.env = map[string]config.ProviderKeys{}
	}
	warnPartial(all, s.env)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apply(nil)
	for _, st := range s.cur.Load().states {
		if st.Source == SourceEnv && st.Status == StatusError {
			return nil, fmt.Errorf("provider %s: configuration refused: %s", st.Slug, st.Error)
		}
	}
	if s.store != nil {
		rows, err := s.load(ctx)
		if err != nil {
			slog.Error("providers: the settings saved in the console cannot be read, running on the environment only",
				"error", err)
		} else {
			s.apply(rows)
		}
	}
	slog.Info("providers enabled", "providers", strings.Join(s.cur.Load().slugs, ","))
	return s, nil
}

// warnPartial logs a provider given some of its required keys but not all,
// naming the missing variables, never a value.
func warnPartial(all []connector.Connector, env map[string]config.ProviderKeys) {
	for _, c := range all {
		k, ok := c.(connector.KeyedConnector)
		if !ok {
			continue
		}
		missing := missingKeys(k.RequiredKeys(), env[c.Slug()])
		if len(missing) > 0 && len(missing) < len(k.RequiredKeys()) {
			for i, f := range missing {
				missing[i] = EnvName(c.Slug(), f)
			}
			slog.Warn("provider partially configured, left disabled", "provider", c.Slug(),
				"missing", strings.Join(missing, ","))
		}
	}
}

// EnvName is the environment variable of one key of a provider.
func EnvName(slug, field string) string { return "OHS_" + strings.ToUpper(slug) + "_" + field }

// List returns the enabled connectors, sorted by slug.
func (s *Set) List() []connector.Connector { return s.cur.Load().enabled }

// Slugs returns the slugs of the enabled connectors, sorted.
func (s *Set) Slugs() []string { return s.cur.Load().slugs }

// Lookup only answers for enabled connectors.
func (s *Set) Lookup(slug string) (connector.Connector, bool) {
	c, ok := s.cur.Load().bySlug[slug]
	return c, ok
}

// States returns the state of every registered connector, sorted by slug.
func (s *Set) States() []State { return s.cur.Load().states }

// State returns the state of one registered connector.
func (s *Set) State(slug string) (State, bool) {
	for _, st := range s.cur.Load().states {
		if st.Slug == slug {
			return st, true
		}
	}
	return State{}, false
}

// Reload reads the store again and applies what changed.
func (s *Set) Reload(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load(ctx)
	if err != nil {
		return err
	}
	s.apply(rows)
	return nil
}

// Run reloads every interval (ReloadEvery when 0) until ctx ends. A failed
// reload is logged and the last good set stays.
func (s *Set) Run(ctx context.Context, every time.Duration) error {
	if s.store == nil {
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
				slog.Warn("providers: reload failed, keeping the current set", "error", err)
			}
		}
	}
}

// Update validates and stores a change for one keyed provider, then applies
// it. Keys are checked by the connector's own Configure before anything is
// stored: a refused change leaves the provider as it was.
func (s *Set) Update(ctx context.Context, slug string, ch Change, by string) (State, error) {
	if s.store == nil {
		return State{}, errNoStore
	}
	c, k, err := s.keyed(slug)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load(ctx)
	if err != nil {
		return State{}, err
	}
	prev, saved := rows[slug]
	next := Settings{Slug: slug, Enabled: true, UpdatedBy: by}
	if saved {
		next.Enabled = prev.Enabled
		if prev.openErr == nil {
			next.Keys = maps.Clone(prev.Keys)
		}
	}
	env := anySet(s.env[slug])
	if ch.Keys != nil {
		if env {
			return State{}, ErrEnvKeys
		}
		allowed := append(slices.Clone(k.RequiredKeys()), optionalKeys(c)...)
		if next.Keys == nil {
			next.Keys = config.ProviderKeys{}
		}
		for _, f := range sortedKeys(ch.Keys) {
			if !slices.Contains(allowed, f) {
				return State{}, &FieldError{Msg: fmt.Sprintf("%s takes no key %q: its keys are %s",
					slug, f, strings.Join(allowed, ", "))}
			}
			v := ch.Keys[f]
			if v == nil || strings.TrimSpace(*v) == "" {
				delete(next.Keys, f)
				continue
			}
			val := strings.TrimSpace(*v)
			if err := CheckValue(f, val); err != nil {
				return State{}, err
			}
			next.Keys[f] = val
		}
	}
	if ch.Enabled != nil {
		next.Enabled = *ch.Enabled
	}
	keys := next.Keys
	if env {
		keys = s.env[slug]
	}
	prevApplied, hadApplied := s.applied[slug]
	if next.Enabled {
		if missing := missingKeys(k.RequiredKeys(), keys); len(missing) > 0 {
			return State{}, &RefusedError{Msg: fmt.Sprintf("%s needs %s before it can be turned on",
				slug, strings.Join(missing, " and "))}
		}
		if cfg, ok := c.(connector.Configurable); ok && !maps.Equal(prevApplied, keys) {
			if err := cfg.Configure(keys); err != nil {
				return State{}, &RefusedError{Msg: err.Error()}
			}
			s.applied[slug] = maps.Clone(keys)
		}
	}
	if err := s.store.Put(ctx, next); err != nil {
		// The connector may run with the new keys already: give it the
		// old ones back.
		if cfg, ok := c.(connector.Configurable); ok && hadApplied && !maps.Equal(prevApplied, s.applied[slug]) {
			if cerr := cfg.Configure(prevApplied); cerr == nil {
				s.applied[slug] = prevApplied
			}
		}
		return State{}, err
	}
	if fresh, err := s.load(ctx); err == nil {
		rows = fresh
	} else {
		next.UpdatedAt = time.Now().UTC()
		rows[slug] = row{Settings: next}
	}
	s.apply(rows)
	st, _ := s.State(slug)
	return st, nil
}

// Forget removes what the console saved for a keyed provider (its keys and
// its switch) and applies the result. It reports whether there was a row.
func (s *Set) Forget(ctx context.Context, slug string) (State, bool, error) {
	if s.store == nil {
		return State{}, false, errNoStore
	}
	if _, _, err := s.keyed(slug); err != nil {
		return State{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found, err := s.store.Delete(ctx, slug)
	if err != nil {
		return State{}, false, err
	}
	rows, err := s.load(ctx)
	if err != nil {
		return State{}, false, err
	}
	s.apply(rows)
	st, _ := s.State(slug)
	return st, found, nil
}

func (s *Set) keyed(slug string) (connector.Connector, connector.KeyedConnector, error) {
	for _, c := range s.all {
		if c.Slug() != slug {
			continue
		}
		k, ok := c.(connector.KeyedConnector)
		if !ok {
			return nil, nil, ErrBuiltIn
		}
		return c, k, nil
	}
	return nil, nil, ErrUnknown
}

// row is one stored setting and whether its keys opened.
type row struct {
	Settings
	openErr error
}

// load reads the store by slug. A row whose keys do not open is kept with
// its error, so only that provider is affected.
func (s *Set) load(ctx context.Context) (map[string]row, error) {
	list, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]row, len(list))
	for _, st := range list {
		out[st.Slug] = row{Settings: st, openErr: st.OpenErr}
	}
	return out, nil
}

// apply builds and publishes the snapshot for rows (nil: the environment
// only), configuring each connector whose keys changed. s.mu is held.
func (s *Set) apply(rows map[string]row) {
	snap := &snapshot{bySlug: map[string]connector.Connector{}}
	for _, c := range s.all {
		st := s.state(c, rows)
		if st.Status == StatusActive || st.Status == StatusBuiltIn {
			snap.enabled = append(snap.enabled, c)
			snap.bySlug[st.Slug] = c
			snap.slugs = append(snap.slugs, st.Slug)
		}
		snap.states = append(snap.states, st)
	}
	if old := s.cur.Load(); old != nil && !slices.Equal(old.slugs, snap.slugs) {
		slog.Info("providers enabled", "providers", strings.Join(snap.slugs, ","))
	}
	s.cur.Store(snap)
}

// state works out one connector's state, configuring it when its keys
// changed since the last call.
func (s *Set) state(c connector.Connector, rows map[string]row) State {
	slug := c.Slug()
	st := State{Slug: slug, Connector: c, Enabled: true}
	r, saved := rows[slug]
	if saved {
		st.Enabled = r.Enabled
		st.UpdatedBy = r.UpdatedBy
		t := r.UpdatedAt
		st.UpdatedAt = &t
	}
	k, keyed := c.(connector.KeyedConnector)
	if !keyed {
		// Device sources need no operator key and ignore the switch. A
		// connector that declares no keys but is enabled by its environment
		// variables is configured with them, as at startup before.
		st.Enabled = true
		st.Status = StatusNotConfigured
		env := s.env[slug]
		if anySet(env) {
			st.Keys, st.Source = env, SourceEnv
		}
		if _, device := connector.AsDevice(c); device || c.Enabled(env) {
			st.Status = StatusBuiltIn
			if anySet(env) {
				if err := s.configure(c, env); err != nil {
					st.Status, st.Error = StatusError, err.Error()
				}
			}
		}
		return st
	}
	st.Required = k.RequiredKeys()
	st.Optional = optionalKeys(c)
	switch {
	case anySet(s.env[slug]):
		st.Keys, st.Source = s.env[slug], SourceEnv
	case saved && r.openErr != nil:
		st.Source = SourceConsole
		st.Status, st.Error = StatusError, r.openErr.Error()
		return st
	case saved && len(r.Keys) > 0:
		st.Keys, st.Source = r.Keys, SourceConsole
	}
	switch {
	case !st.Enabled:
		st.Status = StatusOff
	case len(missingKeys(st.Required, st.Keys)) > 0 || !c.Enabled(st.Keys):
		st.Status = StatusNotConfigured
	default:
		if err := s.configure(c, st.Keys); err != nil {
			st.Status, st.Error = StatusError, err.Error()
		} else {
			st.Status = StatusActive
		}
	}
	return st
}

// configure hands c its keys when they changed since the last success.
func (s *Set) configure(c connector.Connector, keys config.ProviderKeys) error {
	cfg, ok := c.(connector.Configurable)
	if !ok {
		return nil
	}
	if prev, done := s.applied[c.Slug()]; done && maps.Equal(prev, keys) {
		return nil
	}
	if err := cfg.Configure(keys); err != nil {
		return err
	}
	s.applied[c.Slug()] = maps.Clone(keys)
	return nil
}

func optionalKeys(c connector.Connector) []string {
	if o, ok := c.(connector.OptionalKeyed); ok {
		return o.OptionalKeys()
	}
	return nil
}

func anySet(keys config.ProviderKeys) bool {
	for _, v := range keys {
		if v != "" {
			return true
		}
	}
	return false
}

func missingKeys(required []string, keys config.ProviderKeys) []string {
	var out []string
	for _, f := range required {
		if keys[f] == "" {
			out = append(out, f)
		}
	}
	return out
}

// CheckValue refuses a value no provider issues: too long, not UTF-8, or
// holding a control character.
func CheckValue(field, v string) error {
	switch {
	case len(v) > MaxValue:
		return &FieldError{Msg: fmt.Sprintf("%s is longer than %d bytes", field, MaxValue)}
	case !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl):
		return &FieldError{Msg: field + " holds a character no key has (a control character or invalid UTF-8)"}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
