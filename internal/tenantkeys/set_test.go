// SPDX-License-Identifier: AGPL-3.0-only

package tenantkeys

import (
	"context"
	"errors"
	"io"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
)

// app is an OAuth connector that takes tenant keys: CLIENT_ID and
// CLIENT_SECRET, an optional REGION of us or eu.
type app struct {
	mu   sync.Mutex
	keys connector.Config
	made *int // NewInstance calls, shared by every instance
}

func (a *app) Slug() string                                     { return "alpha" }
func (a *app) Enabled(cfg connector.Config) bool                { return cfg["CLIENT_ID"] != "" }
func (a *app) Map(connector.RawRecord) (model.MapResult, error) { return model.MapResult{}, nil }
func (a *app) RequiredKeys() []string                           { return []string{"CLIENT_ID", "CLIENT_SECRET"} }
func (a *app) OptionalKeys() []string                           { return []string{"REGION"} }
func (a *app) Configure(cfg connector.Config) error {
	if r := cfg["REGION"]; r != "" && r != "us" && r != "eu" {
		return errors.New("alpha: REGION must be us or eu")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys = maps.Clone(cfg)
	return nil
}
func (a *app) NewInstance() connector.Connector { *a.made++; return &app{made: a.made} }
func (a *app) secret() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keys["CLIENT_SECRET"]
}
func (a *app) AuthorizeURL(string, string) (string, error) { return "", nil }
func (a *app) Exchange(context.Context, string, string) (connector.Tokens, error) {
	return connector.Tokens{}, nil
}
func (a *app) Refresh(context.Context, connector.Tokens) (connector.Tokens, error) {
	return connector.Tokens{}, nil
}
func (a *app) Fetch(context.Context, connector.Connection, time.Time, time.Time, connector.RawSink) error {
	return nil
}

// device takes no keys.
type device struct{}

func (device) Slug() string                                                         { return "phone" }
func (device) Enabled(connector.Config) bool                                        { return false }
func (device) Map(connector.RawRecord) (model.MapResult, error)                     { return model.MapResult{}, nil }
func (device) ParseUpload(connector.UploadMeta, io.Reader, connector.RawSink) error { return nil }

// memStore is a Store in memory. retired records the keys ids retired.
type memStore struct {
	mu      sync.Mutex
	rows    map[Key]Keys
	retired []uuid.UUID
	fail    error
}

func newMem() *memStore { return &memStore{rows: map[Key]Keys{}} }

func (m *memStore) List(context.Context) ([]Keys, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	var out []Keys
	for _, k := range m.rows {
		k.Keys = maps.Clone(k.Keys)
		out = append(out, k)
	}
	return out, nil
}

func (m *memStore) Get(_ context.Context, tenant uuid.UUID, provider string) (Keys, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.rows[Key{tenant, provider}]
	return k, ok, nil
}

func (m *memStore) Put(_ context.Context, k Keys, same func(config.ProviderKeys) bool, _ *audit.Event) (Keys, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, found := m.rows[Key{k.Tenant, k.Provider}]
	k.ID, k.CreatedAt = uuid.New(), time.Now().UTC()
	retired := 0
	if found {
		k.CreatedAt = prev.CreatedAt
		if prev.OpenErr == nil && same(prev.Keys) {
			k.ID = prev.ID
		} else {
			m.retired = append(m.retired, prev.ID)
			retired = 1
		}
	}
	k.UpdatedAt = time.Now().UTC()
	k.Keys = maps.Clone(k.Keys)
	m.rows[Key{k.Tenant, k.Provider}] = k
	return k, retired, nil
}

func (m *memStore) Delete(_ context.Context, tenant uuid.UUID, provider string, _ *audit.Event) (Keys, int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, found := m.rows[Key{tenant, provider}]
	if !found {
		return Keys{}, 0, false, nil
	}
	delete(m.rows, Key{tenant, provider})
	m.retired = append(m.retired, prev.ID)
	return prev, 1, true, nil
}

func newSet(t *testing.T, store Store) (*Set, *app) {
	t.Helper()
	made := 0
	tmpl := &app{made: &made}
	return New(context.Background(), Options{Connectors: []connector.Connector{tmpl, device{}}, Store: store}), tmpl
}

func keys(id, secret string) map[string]string {
	return map[string]string{"CLIENT_ID": id, "CLIENT_SECRET": secret}
}

func TestSupports(t *testing.T) {
	s, _ := newSet(t, newMem())
	req, opt, ok := s.Supports("alpha")
	assert.True(t, ok)
	assert.Equal(t, []string{"CLIENT_ID", "CLIENT_SECRET"}, req)
	assert.Equal(t, []string{"REGION"}, opt)
	_, _, ok = s.Supports("phone")
	assert.False(t, ok, "a device source takes no keys")
	_, _, ok = s.Supports("nope")
	assert.False(t, ok)
	assert.True(t, s.Known("phone"))
	assert.False(t, s.Known("nope"))
}

func TestPutRefusesBadKeys(t *testing.T) {
	ctx := context.Background()
	store := newMem()
	s, _ := newSet(t, store)
	tenant := uuid.New()
	var field *providerset.FieldError
	var refused *providerset.RefusedError

	_, _, err := s.Put(ctx, tenant, "alpha", map[string]string{"CLIENT_ID": "x"}, "k", nil)
	require.ErrorAs(t, err, &field)
	assert.Equal(t, "keys must include CLIENT_SECRET", field.Msg)
	_, _, err = s.Put(ctx, tenant, "alpha", map[string]string{"CLIENT_ID": "x", "CLIENT_SECRET": "y", "OTHER": "z"}, "k", nil)
	require.ErrorAs(t, err, &field)
	assert.Contains(t, field.Msg, `takes no key "OTHER"`)
	_, _, err = s.Put(ctx, tenant, "alpha", map[string]string{"CLIENT_ID": "x", "CLIENT_SECRET": "y\x01"}, "k", nil)
	require.ErrorAs(t, err, &field)
	_, _, err = s.Put(ctx, tenant, "alpha", map[string]string{"CLIENT_ID": "x", "CLIENT_SECRET": "y", "REGION": "mars"}, "k", nil)
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "alpha: REGION must be us or eu", refused.Msg)
	_, _, err = s.Put(ctx, tenant, "phone", keys("x", "y"), "k", nil)
	assert.ErrorIs(t, err, ErrUnsupported)
	_, _, err = s.Put(ctx, tenant, "nope", keys("x", "y"), "k", nil)
	assert.ErrorIs(t, err, ErrUnknown)
	assert.Empty(t, store.rows, "nothing refused is stored")
	assert.False(t, s.Has(tenant, "alpha"))
}

func TestPutRunsTheTenantsOwnConnector(t *testing.T) {
	ctx := context.Background()
	store := newMem()
	s, tmpl := newSet(t, store)
	blue, green := uuid.New(), uuid.New()

	st, retired, err := s.Put(ctx, blue, "alpha", map[string]string{"CLIENT_ID": " blue-app ", "CLIENT_SECRET": "s1", "REGION": ""}, "key-1", nil)
	require.NoError(t, err)
	assert.Zero(t, retired)
	assert.Equal(t, StatusActive, st.Status)
	assert.Equal(t, config.ProviderKeys{"CLIENT_ID": "blue-app", "CLIENT_SECRET": "s1"}, st.Keys.Keys, "trimmed, empty left out")
	assert.Equal(t, "key-1", st.UpdatedBy)

	c, id, ok := s.Current(blue, "alpha")
	require.True(t, ok)
	assert.Equal(t, st.ID, id)
	assert.NotSame(t, tmpl, c, "its own connector, not the instance's")
	assert.Equal(t, "s1", c.(*app).secret())
	assert.Empty(t, tmpl.secret(), "the instance's connector is untouched")
	got, ok := s.Get(blue, "alpha", id)
	require.True(t, ok)
	assert.Same(t, c, got)
	_, ok = s.Get(green, "alpha", id)
	assert.False(t, ok, "another tenant's keys id")
	_, _, ok = s.Current(green, "alpha")
	assert.False(t, ok)
	assert.Equal(t, []string{"alpha"}, s.Slugs())
	require.Len(t, s.Candidates("alpha"), 1)
	assert.Equal(t, blue, s.Candidates("alpha")[0].Tenant)
	assert.Contains(t, s.Tenant(blue), "alpha")
	assert.Empty(t, s.Tenant(green))

	// A secret rotated: same id, same connector, new secret.
	made := *tmpl.made
	st2, retired, err := s.Put(ctx, blue, "alpha", keys("blue-app", "s2"), "key-1", nil)
	require.NoError(t, err)
	assert.Zero(t, retired)
	assert.Equal(t, id, st2.ID)
	c2, _, _ := s.Current(blue, "alpha")
	assert.Same(t, c, c2)
	assert.Equal(t, "s2", c2.(*app).secret())
	assert.Equal(t, made+1, *tmpl.made, "only the trial connector of the validation was made")

	// Another application: new id, new connector, the old id retired.
	st3, retired, err := s.Put(ctx, blue, "alpha", keys("blue-app-2", "s2"), "key-1", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, retired)
	assert.NotEqual(t, id, st3.ID)
	assert.Equal(t, []uuid.UUID{id}, store.retired)
	_, ok = s.Get(blue, "alpha", id)
	assert.False(t, ok, "the old keys are gone")
	c3, _, _ := s.Current(blue, "alpha")
	assert.NotSame(t, c, c3)

	// Removed.
	_, retired, found, err := s.Delete(ctx, blue, "alpha", nil)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 1, retired)
	assert.False(t, s.Has(blue, "alpha"))
	assert.Empty(t, s.Slugs())
	_, _, found, err = s.Delete(ctx, blue, "alpha", nil)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestSameApp(t *testing.T) {
	a := config.ProviderKeys{"CLIENT_ID": "x", "CLIENT_SECRET": "1"}
	assert.True(t, SameApp(a, config.ProviderKeys{"CLIENT_ID": "x", "CLIENT_SECRET": "2"}))
	assert.False(t, SameApp(a, config.ProviderKeys{"CLIENT_ID": "y", "CLIENT_SECRET": "1"}))
	assert.False(t, SameApp(a, config.ProviderKeys{"CLIENT_ID": "x", "CLIENT_SECRET": "1", "REGION": "eu"}))
}

// Another process saves keys: Reload brings them, and keys that do not open
// leave the tenant without the provider.
func TestReloadAndKeysThatDoNotOpen(t *testing.T) {
	ctx := context.Background()
	store := newMem()
	writer, _ := newSet(t, store)
	reader, _ := newSet(t, store)
	blue := uuid.New()
	_, _, err := writer.Put(ctx, blue, "alpha", keys("blue-app", "s1"), "k", nil)
	require.NoError(t, err)
	assert.False(t, reader.Has(blue, "alpha"))
	require.NoError(t, reader.Reload(ctx))
	_, _, ok := reader.Current(blue, "alpha")
	assert.True(t, ok)

	store.mu.Lock()
	k := store.rows[Key{blue, "alpha"}]
	k.Keys, k.OpenErr = nil, errNotOpen
	store.rows[Key{blue, "alpha"}] = k
	store.mu.Unlock()
	require.NoError(t, reader.Reload(ctx))
	assert.True(t, reader.Has(blue, "alpha"))
	_, _, ok = reader.Current(blue, "alpha")
	assert.False(t, ok)
	assert.Equal(t, StatusError, reader.Tenant(blue)["alpha"].Status)
	st, found, err := reader.Lookup(ctx, blue, "alpha")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, StatusError, st.Status)
	assert.Contains(t, st.Error, "OHS_ENCRYPTION_KEY")

	// A failed reload keeps the last snapshot.
	store.fail = errors.New("database down")
	assert.Error(t, reader.Reload(ctx))
	assert.True(t, reader.Has(blue, "alpha"))
}

func TestNilSetHoldsNothing(t *testing.T) {
	var s *Set
	tenant := uuid.New()
	assert.False(t, s.Has(tenant, "alpha"))
	_, _, ok := s.Current(tenant, "alpha")
	assert.False(t, ok)
	_, ok = s.Get(tenant, "alpha", uuid.New())
	assert.False(t, ok)
	assert.Empty(t, s.Candidates("alpha"))
	assert.Empty(t, s.Slugs())
	assert.Empty(t, s.Tenant(tenant))
	_, ok = s.Any("alpha")
	assert.False(t, ok)
	assert.NoError(t, s.Reload(context.Background()))
}
