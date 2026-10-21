// SPDX-License-Identifier: AGPL-3.0-only

package providerset

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// keyed is a connector that needs CLIENT_ID and CLIENT_SECRET, reads REGION
// and refuses a REGION other than us or eu.
type keyed struct {
	slug string
	mu   sync.Mutex
	got  []config.ProviderKeys // every accepted Configure
}

func (k *keyed) Slug() string                                     { return k.slug }
func (k *keyed) Enabled(cfg connector.Config) bool                { return cfg["CLIENT_ID"] != "" }
func (k *keyed) Map(connector.RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }
func (k *keyed) RequiredKeys() []string                           { return []string{"CLIENT_ID", "CLIENT_SECRET"} }
func (k *keyed) OptionalKeys() []string                           { return []string{"REGION"} }
func (k *keyed) Configure(cfg connector.Config) error {
	if r := cfg["REGION"]; r != "" && r != "us" && r != "eu" {
		return errors.New(k.slug + ": REGION must be us or eu")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.got = append(k.got, maps.Clone(cfg))
	return nil
}

func (k *keyed) configured() []config.ProviderKeys {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.got)
}

// device needs no key.
type device struct{}

func (device) Slug() string                                                         { return "phone" }
func (device) Enabled(connector.Config) bool                                        { return false }
func (device) Map(connector.RawRecord) (model.MapResult, error)                     { return model.MapResult{}, nil }
func (device) ParseUpload(connector.UploadMeta, io.Reader, connector.RawSink) error { return nil }

// keyless is enabled by its CLIENT_ID but declares no keys (no
// KeyedConnector), and refuses CLIENT_ID "refuse".
type keyless struct{ got []config.ProviderKeys }

func (k *keyless) Slug() string                                     { return "loose" }
func (k *keyless) Enabled(cfg connector.Config) bool                { return cfg["CLIENT_ID"] != "" }
func (k *keyless) Map(connector.RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }
func (k *keyless) Configure(cfg connector.Config) error {
	if cfg["CLIENT_ID"] == "refuse" {
		return errors.New("loose: refused")
	}
	k.got = append(k.got, maps.Clone(cfg))
	return nil
}

// memStore is a Store in memory.
type memStore struct {
	mu   sync.Mutex
	rows map[string]Settings
	fail error
}

func newMem() *memStore { return &memStore{rows: map[string]Settings{}} }

func (m *memStore) List(context.Context) ([]Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	var out []Settings
	for _, slug := range sortedKeys(m.rows) {
		r := m.rows[slug]
		r.Keys = maps.Clone(r.Keys)
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) Put(_ context.Context, s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	s.Keys = maps.Clone(s.Keys)
	s.UpdatedAt = time.Now().UTC()
	m.rows[s.Slug] = s
	return nil
}

func (m *memStore) Delete(_ context.Context, slug string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[slug]
	delete(m.rows, slug)
	return ok, nil
}

func str(s string) *string { return &s }
func on(b bool) *bool      { return &b }

func slugs(cs []connector.Connector) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Slug())
	}
	return out
}

func TestEnvironmentOnly(t *testing.T) {
	a, b := &keyed{slug: "alpha"}, &keyed{slug: "beta"}
	s, err := New(context.Background(), Options{
		Connectors: []connector.Connector{b, a, device{}},
		Env:        map[string]config.ProviderKeys{"alpha": {"CLIENT_ID": "id", "CLIENT_SECRET": "sec"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "phone"}, s.Slugs())
	require.Equal(t, []string{"alpha", "phone"}, slugs(s.List()))
	_, ok := s.Lookup("beta")
	require.False(t, ok)
	c, ok := s.Lookup("alpha")
	require.True(t, ok)
	require.Same(t, a, c)
	require.Len(t, a.configured(), 1)

	st, ok := s.State("alpha")
	require.True(t, ok)
	require.Equal(t, StatusActive, st.Status)
	require.Equal(t, SourceEnv, st.Source)
	require.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET"}, st.Required)
	require.Equal(t, []string{"REGION"}, st.Optional)
	st, _ = s.State("beta")
	require.Equal(t, StatusNotConfigured, st.Status)
	require.Empty(t, st.Source)
	st, _ = s.State("phone")
	require.Equal(t, StatusBuiltIn, st.Status)

	// Without a store, the writes fail and Reload is a no-op.
	_, err = s.Update(context.Background(), "beta", Change{Enabled: on(true)}, "k")
	require.Error(t, err)
	require.NoError(t, s.Reload(context.Background()))
}

func TestEnvironmentRefusedStopsStartup(t *testing.T) {
	_, err := New(context.Background(), Options{
		Connectors: []connector.Connector{&keyed{slug: "alpha"}},
		Env:        map[string]config.ProviderKeys{"alpha": {"CLIENT_ID": "id", "CLIENT_SECRET": "s", "REGION": "mars"}},
	})
	require.ErrorContains(t, err, "provider alpha: configuration refused: alpha: REGION must be us or eu")
}

// A connector without declared keys runs, configured, when its environment
// enables it, as it did before the console; a refusal stops startup.
func TestKeylessFromEnvironment(t *testing.T) {
	k := &keyless{}
	s, err := New(context.Background(), Options{
		Connectors: []connector.Connector{k},
		Env:        map[string]config.ProviderKeys{"loose": {"CLIENT_ID": "id"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"loose"}, s.Slugs())
	require.Equal(t, []config.ProviderKeys{{"CLIENT_ID": "id"}}, k.got)
	st, _ := s.State("loose")
	require.Equal(t, SourceEnv, st.Source)

	s, err = New(context.Background(), Options{Connectors: []connector.Connector{&keyless{}}})
	require.NoError(t, err)
	require.Empty(t, s.Slugs(), "not enabled without its variables")

	_, err = New(context.Background(), Options{
		Connectors: []connector.Connector{&keyless{}},
		Env:        map[string]config.ProviderKeys{"loose": {"CLIENT_ID": "refuse"}},
	})
	require.ErrorContains(t, err, "provider loose: configuration refused: loose: refused")
}

func TestConsoleKeys(t *testing.T) {
	ctx := context.Background()
	a := &keyed{slug: "alpha"}
	store := newMem()
	s, err := New(ctx, Options{Connectors: []connector.Connector{a, device{}}, Store: store})
	require.NoError(t, err)
	require.Equal(t, []string{"phone"}, s.Slugs())

	// Turning it on without its keys is refused, nothing is stored.
	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"CLIENT_ID": str("id")}}, "key-1")
	var refused *RefusedError
	require.ErrorAs(t, err, &refused)
	require.Contains(t, refused.Msg, "CLIENT_SECRET")
	require.Empty(t, store.rows)

	// A key it does not take, or a bad value, is a field error.
	var fe *FieldError
	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"PASSWORD": str("x")}}, "key-1")
	require.ErrorAs(t, err, &fe)
	require.Contains(t, fe.Msg, "CLIENT_ID, CLIENT_SECRET, REGION")
	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"CLIENT_ID": str("a\nb"), "CLIENT_SECRET": str("s")}}, "key-1")
	require.ErrorAs(t, err, &fe)

	// The connector's own check runs before anything is stored.
	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{
		"CLIENT_ID": str("id"), "CLIENT_SECRET": str("sec"), "REGION": str("mars")}}, "key-1")
	require.ErrorAs(t, err, &refused)
	require.Equal(t, "alpha: REGION must be us or eu", refused.Msg)
	require.Empty(t, store.rows)
	require.Empty(t, a.configured())

	st, err := s.Update(ctx, "alpha", Change{Keys: map[string]*string{
		"CLIENT_ID": str(" id "), "CLIENT_SECRET": str("sec"), "REGION": str("eu")}}, "key-1")
	require.NoError(t, err)
	require.Equal(t, StatusActive, st.Status)
	require.Equal(t, SourceConsole, st.Source)
	require.Equal(t, "key-1", st.UpdatedBy)
	require.NotNil(t, st.UpdatedAt)
	require.Equal(t, config.ProviderKeys{"CLIENT_ID": "id", "CLIENT_SECRET": "sec", "REGION": "eu"}, st.Keys, "values are trimmed")
	require.Equal(t, []string{"alpha", "phone"}, s.Slugs())
	require.Len(t, a.configured(), 1)

	// A key not named is kept; a null one is removed.
	st, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"REGION": nil}}, "key-2")
	require.NoError(t, err)
	require.Equal(t, config.ProviderKeys{"CLIENT_ID": "id", "CLIENT_SECRET": "sec"}, st.Keys)
	require.Len(t, a.configured(), 2)

	// Off keeps the keys, takes it out of the set; on puts it back without
	// configuring it again.
	st, err = s.Update(ctx, "alpha", Change{Enabled: on(false)}, "key-2")
	require.NoError(t, err)
	require.Equal(t, StatusOff, st.Status)
	require.False(t, st.Enabled)
	_, ok := s.Lookup("alpha")
	require.False(t, ok)
	st, err = s.Update(ctx, "alpha", Change{Enabled: on(true)}, "key-2")
	require.NoError(t, err)
	require.Equal(t, StatusActive, st.Status)
	require.Len(t, a.configured(), 2)

	// Device sources need no key.
	_, err = s.Update(ctx, "phone", Change{Enabled: on(false)}, "key-2")
	require.ErrorIs(t, err, ErrBuiltIn)
	_, err = s.Update(ctx, "nope", Change{Enabled: on(false)}, "key-2")
	require.ErrorIs(t, err, ErrUnknown)

	// Forget removes the row: no keys left, not configured.
	st, found, err := s.Forget(ctx, "alpha")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, StatusNotConfigured, st.Status)
	require.Equal(t, []string{"phone"}, s.Slugs())
	_, found, err = s.Forget(ctx, "alpha")
	require.NoError(t, err)
	require.False(t, found)
}

func TestEnvironmentWins(t *testing.T) {
	ctx := context.Background()
	a := &keyed{slug: "alpha"}
	store := newMem()
	store.rows["alpha"] = Settings{Slug: "alpha", Enabled: true,
		Keys: config.ProviderKeys{"CLIENT_ID": "console", "CLIENT_SECRET": "console"}}
	s, err := New(ctx, Options{
		Connectors: []connector.Connector{a},
		Env:        map[string]config.ProviderKeys{"alpha": {"CLIENT_ID": "env", "CLIENT_SECRET": "env"}},
		Store:      store,
	})
	require.NoError(t, err)
	st, _ := s.State("alpha")
	require.Equal(t, SourceEnv, st.Source)
	require.Equal(t, "env", st.Keys["CLIENT_ID"])

	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"CLIENT_ID": str("x")}}, "k")
	require.ErrorIs(t, err, ErrEnvKeys)

	// The switch still applies to environment keys.
	st, err = s.Update(ctx, "alpha", Change{Enabled: on(false)}, "k")
	require.NoError(t, err)
	require.Equal(t, StatusOff, st.Status)
	require.Equal(t, SourceEnv, st.Source)
	_, ok := s.Lookup("alpha")
	require.False(t, ok)
	for _, cfg := range a.configured() {
		require.Equal(t, "env", cfg["CLIENT_ID"], "the console keys never reach the connector")
	}
}

// Another process saves a change: Reload picks it up and configures the
// connector once; a store that fails keeps the last set.
func TestReload(t *testing.T) {
	ctx := context.Background()
	a := &keyed{slug: "alpha"}
	store := newMem()
	s, err := New(ctx, Options{Connectors: []connector.Connector{a}, Store: store})
	require.NoError(t, err)
	require.Empty(t, s.Slugs())

	require.NoError(t, store.Put(ctx, Settings{Slug: "alpha", Enabled: true,
		Keys: config.ProviderKeys{"CLIENT_ID": "id", "CLIENT_SECRET": "sec"}}))
	require.NoError(t, s.Reload(ctx))
	require.Equal(t, []string{"alpha"}, s.Slugs())
	require.NoError(t, s.Reload(ctx))
	require.Len(t, a.configured(), 1, "unchanged keys are not configured again")

	// Keys another process saved that this connector refuses: error, out of
	// the set.
	require.NoError(t, store.Put(ctx, Settings{Slug: "alpha", Enabled: true,
		Keys: config.ProviderKeys{"CLIENT_ID": "id", "CLIENT_SECRET": "sec", "REGION": "mars"}}))
	require.NoError(t, s.Reload(ctx))
	st, _ := s.State("alpha")
	require.Equal(t, StatusError, st.Status)
	require.Contains(t, st.Error, "REGION")
	require.Empty(t, s.Slugs())

	// Keys that do not open: error, the other providers unaffected.
	require.NoError(t, store.Put(ctx, Settings{Slug: "alpha", Enabled: true,
		Keys: config.ProviderKeys{"CLIENT_ID": "id", "CLIENT_SECRET": "sec"}}))
	require.NoError(t, s.Reload(ctx))
	require.Equal(t, []string{"alpha"}, s.Slugs())
	store.mu.Lock()
	r := store.rows["alpha"]
	r.Keys, r.OpenErr = nil, errors.New("does not open")
	store.rows["alpha"] = r
	store.mu.Unlock()
	require.NoError(t, s.Reload(ctx))
	st, _ = s.State("alpha")
	require.Equal(t, StatusError, st.Status)
	require.Equal(t, "does not open", st.Error)

	store.fail = errors.New("db down")
	require.Error(t, s.Reload(ctx))
	st, _ = s.State("alpha")
	require.Equal(t, StatusError, st.Status, "the last set stays")
}

func TestStoreUnreadableAtStart(t *testing.T) {
	store := newMem()
	store.fail = errors.New("relation does not exist")
	s, err := New(context.Background(), Options{
		Connectors: []connector.Connector{&keyed{slug: "alpha"}},
		Env:        map[string]config.ProviderKeys{"alpha": {"CLIENT_ID": "id", "CLIENT_SECRET": "sec"}},
		Store:      store,
	})
	require.NoError(t, err, "the environment still applies")
	require.Equal(t, []string{"alpha"}, s.Slugs())
}

// A failed save leaves the connector on its previous keys.
func TestUpdateStoreFailure(t *testing.T) {
	ctx := context.Background()
	a := &keyed{slug: "alpha"}
	store := newMem()
	s, err := New(ctx, Options{Connectors: []connector.Connector{a}, Store: store})
	require.NoError(t, err)
	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"CLIENT_ID": str("one"), "CLIENT_SECRET": str("s")}}, "k")
	require.NoError(t, err)

	s.store = &failingPut{memStore: store}
	_, err = s.Update(ctx, "alpha", Change{Keys: map[string]*string{"CLIENT_ID": str("two")}}, "k")
	require.Error(t, err)
	got := a.configured()
	require.Equal(t, "one", got[len(got)-1]["CLIENT_ID"])
	st, _ := s.State("alpha")
	require.Equal(t, "one", st.Keys["CLIENT_ID"])
}

type failingPut struct{ *memStore }

func (f *failingPut) Put(context.Context, Settings) error { return errors.New("disk full") }

func TestRunStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, Options{Connectors: []connector.Connector{&keyed{slug: "alpha"}}, Store: newMem()})
	require.NoError(t, err)
	done := make(chan error)
	go func() { done <- s.Run(ctx, time.Millisecond) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

func TestEnvName(t *testing.T) {
	require.Equal(t, "OHS_INTERVALS_ICU_CLIENT_ID", EnvName("intervals_icu", "CLIENT_ID"))
}
