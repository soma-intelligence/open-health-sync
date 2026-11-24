// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
)

func TestEveryLiveSettingIsInTheSnapshot(t *testing.T) {
	var want, got []string
	for _, s := range config.Registry() {
		if s.Apply == config.ApplyLive {
			want = append(want, s.Name)
		}
	}
	for n := range liveFields {
		got = append(got, n)
	}
	sort.Strings(want)
	sort.Strings(got)
	require.Equal(t, want, got)
}

type fakeStore struct {
	rev     int64
	rows    map[string]Row
	loads   int
	revErr  error
	loadErr error
}

func (f *fakeStore) Revision(context.Context) (int64, error) { return f.rev, f.revErr }
func (f *fakeStore) Load(context.Context) (State, error) {
	f.loads++
	if f.loadErr != nil {
		return State{}, f.loadErr
	}
	return State{Revision: f.rev, Rows: f.rows}, nil
}
func (f *fakeStore) Apply(context.Context, int64, Change) (int64, error) {
	return 0, errors.New("unused")
}

func liveEnv(t *testing.T, st Store) *Live {
	t.Helper()
	kv := map[string]string{"OHS_DATABASE_URL": "postgres://x", "OHS_ENCRYPTION_KEY": goodKeyInternal}
	environ := []string{"OHS_DATABASE_URL=postgres://x", "OHS_ENCRYPTION_KEY=" + goodKeyInternal}
	env := func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	cfg, err := config.Load(env, environ)
	require.NoError(t, err)
	return NewLive(LiveOptions{Store: st, Env: env, Environ: environ, Config: cfg})
}

const goodKeyInternal = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestLiveReloadSwaps(t *testing.T) {
	st := &fakeStore{}
	l := liveEnv(t, st)
	require.Equal(t, 5, l.WebhookPauseAfter())
	var calls1, calls2 int
	l.Subscribe(func(Snapshot) { calls1++ })
	l.Subscribe(func(s Snapshot) { calls2++; require.Equal(t, int64(1), s.Revision) })

	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 0, st.loads, "same revision does not load")

	st.rev = 1
	st.rows = map[string]Row{"OHS_WEBHOOK_PAUSE_AFTER": {Name: "OHS_WEBHOOK_PAUSE_AFTER", Value: "9"}}
	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 9, l.WebhookPauseAfter())
	require.Equal(t, int64(1), l.Status().LiveRevision)
	require.Equal(t, 1, calls1)
	require.Equal(t, 1, calls2)
	require.Equal(t, 1, st.loads)

	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 1, st.loads)
	require.Equal(t, 1, calls1)
}

func TestLiveReloadFailureKeepsLastGood(t *testing.T) {
	st := &fakeStore{rev: 1, rows: map[string]Row{"OHS_WEBHOOK_PAUSE_AFTER": {Value: "9"}}}
	l := liveEnv(t, st)
	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 9, l.WebhookPauseAfter())

	st.rev = 2
	st.loadErr = errors.New("db down")
	require.Error(t, l.Reload(context.Background()))
	require.Equal(t, 9, l.WebhookPauseAfter())
	require.Equal(t, int64(1), l.Status().LiveRevision)
	require.Contains(t, l.Status().ReloadError, "db down")

	st.loadErr = nil
	st.rows = map[string]Row{"OHS_WEBHOOK_PAUSE_AFTER": {Value: "7"}}
	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 7, l.WebhookPauseAfter())
	require.Empty(t, l.Status().ReloadError)
}

func TestLiveIgnoresRestartSettings(t *testing.T) {
	st := &fakeStore{}
	l := liveEnv(t, st)
	before := l.Snapshot()
	st.rev = 3
	st.rows = map[string]Row{"OHS_INGEST_CONCURRENCY": {Value: "7"}}
	require.NoError(t, l.Reload(context.Background()))
	after := l.Snapshot()
	require.Equal(t, int64(3), l.Status().LiveRevision)
	after.Revision = before.Revision
	require.Equal(t, before, after)
}

func TestLiveEnvSetValueNeverChanges(t *testing.T) {
	st := &fakeStore{rev: 1, rows: map[string]Row{"OHS_WEBHOOK_PAUSE_AFTER": {Value: "9"}}}
	kv := map[string]string{"OHS_DATABASE_URL": "postgres://x", "OHS_ENCRYPTION_KEY": goodKeyInternal, "OHS_WEBHOOK_PAUSE_AFTER": "4"}
	var environ []string
	for k, v := range kv {
		environ = append(environ, k+"="+v)
	}
	env := func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	cfg, err := config.Load(env, environ)
	require.NoError(t, err)
	l := NewLive(LiveOptions{Store: st, Env: env, Environ: environ, Config: cfg})
	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 4, l.WebhookPauseAfter())
}

func TestLiveRunReloads(t *testing.T) {
	st := &fakeStore{rev: 1, rows: map[string]Row{"OHS_WEBHOOK_PAUSE_AFTER": {Value: "9"}}}
	l := liveEnv(t, st)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- l.Run(ctx, 10*time.Millisecond) }()
	require.Eventually(t, func() bool { return l.WebhookPauseAfter() == 9 }, time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

func TestLiveSkipsBadStoredValues(t *testing.T) {
	buf := logtest.Capture(t, zerolog.DebugLevel)

	st := &fakeStore{rev: 4, rows: map[string]Row{
		"OHS_WEBHOOK_PAUSE_AFTER": {Value: "abc"},
		"OHS_METRICS_TOKEN":       {Value: "short-secret"},
		"OHS_NOT_A_SETTING":       {Value: "zzz-unknown-value"},
		"OHS_RETENTION_DAYS":      {OpenErr: errors.New("open failed: sealed-bytes")},
	}}
	l := liveEnv(t, st)
	require.NoError(t, l.Reload(context.Background()))
	require.Equal(t, 5, l.WebhookPauseAfter())
	require.Equal(t, 0, l.RetentionDays())
	s := l.Status()
	require.Equal(t, int64(4), s.LiveRevision)
	require.Empty(t, s.ReloadError)
	require.ElementsMatch(t, []string{"OHS_WEBHOOK_PAUSE_AFTER", "OHS_METRICS_TOKEN", "OHS_NOT_A_SETTING", "OHS_RETENTION_DAYS"}, s.Skipped)
	log := buf.String()
	for _, n := range s.Skipped {
		require.Contains(t, log, n)
	}
	for _, v := range []string{"short-secret", "zzz-unknown-value", "sealed-bytes"} {
		require.NotContains(t, log, v)
	}
}

func TestLiveSubscriberMayCallBack(t *testing.T) {
	st := &fakeStore{rev: 1}
	l := liveEnv(t, st)
	got := make(chan Status, 1)
	l.Subscribe(func(Snapshot) {
		l.Subscribe(func(Snapshot) {})
		got <- l.Status()
	})
	done := make(chan error, 1)
	go func() { done <- l.Reload(context.Background()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock")
	}
	require.Equal(t, int64(1), (<-got).LiveRevision)
}
