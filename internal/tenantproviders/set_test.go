// SPDX-License-Identifier: AGPL-3.0-only

package tenantproviders

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
)

// memStore is a Store in memory. failOff makes Off fail.
type memStore struct {
	mu      sync.Mutex
	rows    map[Key]Setting
	events  []audit.Event
	failOff error
}

func newMem() *memStore { return &memStore{rows: map[Key]Setting{}} }

func (m *memStore) Off(context.Context) ([]Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOff != nil {
		return nil, m.failOff
	}
	var out []Key
	for k, s := range m.rows {
		if !s.Enabled {
			out = append(out, k)
		}
	}
	return out, nil
}

func (m *memStore) List(_ context.Context, tenant uuid.UUID) ([]Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Setting
	for k, s := range m.rows {
		if k.Tenant == tenant {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memStore) Put(_ context.Context, s Setting, ev *audit.Event) (Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.UpdatedAt = time.Now().UTC()
	m.rows[Key{Tenant: s.Tenant, Provider: s.Provider}] = s
	if ev != nil {
		m.events = append(m.events, *ev)
	}
	return s, nil
}

// set writes a row behind the Set's back, as another process would.
func (m *memStore) set(tenant uuid.UUID, slug string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[Key{Tenant: tenant, Provider: slug}] = Setting{Tenant: tenant, Provider: slug, Enabled: enabled}
}

func TestNilSetAllowsEverything(t *testing.T) {
	var s *Set
	assert.True(t, s.Allowed(uuid.New(), "oura"))
}

func TestAllowedFollowsTheSwitches(t *testing.T) {
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	m := newMem()
	m.set(a, "oura", false)
	m.set(a, "whoop", true)
	s := New(ctx, m)

	assert.False(t, s.Allowed(a, "oura"), "turned off for a")
	assert.True(t, s.Allowed(a, "whoop"), "a row that says on")
	assert.True(t, s.Allowed(a, "withings"), "no row is on")
	assert.True(t, s.Allowed(b, "oura"), "another tenant keeps it")

	// A change saved here applies at once and is audited with the change.
	ev := audit.Event{ActorType: audit.Admin, ActorID: "api_key:k1", Action: audit.TenantProvider}
	st, err := s.Update(ctx, b, "oura", false, "k1", &ev)
	require.NoError(t, err)
	assert.False(t, st.Enabled)
	assert.Equal(t, "k1", st.UpdatedBy)
	assert.False(t, s.Allowed(b, "oura"))
	require.Len(t, m.events, 1)
	_, err = s.Update(ctx, a, "oura", true, "k1", nil)
	require.NoError(t, err)
	assert.True(t, s.Allowed(a, "oura"), "turned back on")

	// What another process saved shows after a reload.
	m.set(a, "whoop", false)
	assert.True(t, s.Allowed(a, "whoop"), "not reloaded yet")
	require.NoError(t, s.Reload(ctx))
	assert.False(t, s.Allowed(a, "whoop"))

	got, err := s.Tenant(ctx, a)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.True(t, got["oura"].Enabled)
	assert.False(t, got["whoop"].Enabled)
}

// A store that cannot be read leaves every provider on at start and the
// last good switches afterwards; a change saved meanwhile still applies.
func TestUnreadableStore(t *testing.T) {
	ctx := context.Background()
	a := uuid.New()
	m := newMem()
	m.set(a, "oura", false)
	m.failOff = errors.New("database down")
	s := New(ctx, m)
	assert.True(t, s.Allowed(a, "oura"), "nothing read yet: on")

	m.failOff = nil
	require.NoError(t, s.Reload(ctx))
	assert.False(t, s.Allowed(a, "oura"))

	m.failOff = errors.New("database down")
	require.Error(t, s.Reload(ctx))
	assert.False(t, s.Allowed(a, "oura"), "the last good switches stay")
	_, err := s.Update(ctx, a, "whoop", false, "k1", nil)
	require.NoError(t, err)
	assert.False(t, s.Allowed(a, "whoop"), "the change applies although the reload failed")
	assert.False(t, s.Allowed(a, "oura"))
	_, err = s.Update(ctx, a, "oura", true, "k1", nil)
	require.NoError(t, err)
	assert.True(t, s.Allowed(a, "oura"))
}

func TestRunReloads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := uuid.New()
	m := newMem()
	s := New(ctx, m)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, 5*time.Millisecond) }()
	m.set(a, "oura", false)
	require.Eventually(t, func() bool { return !s.Allowed(a, "oura") }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}
