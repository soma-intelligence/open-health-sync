// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// memStore is an in memory Store with the revision rules of the real one.
type memStore struct {
	rev     int64
	rows    map[string]Row
	applies []Change
}

func (m *memStore) Revision(context.Context) (int64, error) { return m.rev, nil }
func (m *memStore) Load(context.Context) (State, error) {
	rows := make(map[string]Row, len(m.rows))
	for k, v := range m.rows {
		rows[k] = v
	}
	return State{Revision: m.rev, Rows: rows}, nil
}
func (m *memStore) Apply(_ context.Context, ifRev int64, ch Change) (int64, error) {
	m.applies = append(m.applies, ch)
	if ifRev != m.rev {
		return 0, ErrRevisionMoved
	}
	for n, v := range ch.Put {
		s, _ := config.LookupSetting(n)
		m.rows[n] = Row{Name: n, Value: v, Sealed: s.Secret}
	}
	for _, n := range ch.Delete {
		delete(m.rows, n)
	}
	m.rev++
	return m.rev, nil
}

func manageLive(t *testing.T, extra map[string]string, rows map[string]Row) (*Live, *memStore) {
	t.Helper()
	kv := map[string]string{"OHS_DATABASE_URL": "postgres://x", "OHS_ENCRYPTION_KEY": goodKeyInternal}
	var environ []string
	for k, v := range extra {
		kv[k] = v
	}
	for k, v := range kv {
		environ = append(environ, k+"="+v)
	}
	env := func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	cfg, err := config.Load(env, environ)
	require.NoError(t, err)
	if rows == nil {
		rows = map[string]Row{}
	}
	st := &memStore{rows: rows}
	return NewLive(LiveOptions{Store: st, Env: env, Environ: environ, Config: cfg}), st
}

func entryOf(t *testing.T, v View, name string) Entry {
	t.Helper()
	for _, e := range v.Entries {
		if e.Setting.Name == name {
			return e
		}
	}
	t.Fatalf("no entry %s", name)
	return Entry{}
}

func sp(s string) *string { return &s }

func TestViewSources(t *testing.T) {
	l, _ := manageLive(t, map[string]string{"OHS_LOG_LEVEL": "debug", "OHS_RETENTION_DAYS": ""}, map[string]Row{
		"OHS_LOG_LEVEL":      {Name: "OHS_LOG_LEVEL", Value: "warn"},
		"OHS_RETENTION_DAYS": {Name: "OHS_RETENTION_DAYS", Value: "30"},
		"OHS_METRICS_TOKEN":  {Name: "OHS_METRICS_TOKEN", Value: "0123456789abcdefXYZ", Sealed: true},
		"OHS_BACKFILL_DAYS":  {Name: "OHS_BACKFILL_DAYS", Value: "abc"},
		"OHS_GONE":           {Name: "OHS_GONE", Value: "1"},
	})
	v, err := l.View(context.Background())
	require.NoError(t, err)
	require.Len(t, v.Entries, len(config.Registry()))
	for i, s := range config.Registry() {
		require.Equal(t, s.Name, v.Entries[i].Setting.Name, "registry order")
	}

	e := entryOf(t, v, "OHS_LOG_LEVEL")
	require.Equal(t, SourceEnv, e.Source)
	require.Equal(t, "debug", *e.Value)
	require.Equal(t, "warn", *e.StoredValue)

	e = entryOf(t, v, "OHS_RETENTION_DAYS")
	require.Equal(t, SourceConsole, e.Source)
	require.Equal(t, "30", *e.Value)
	require.Nil(t, e.StoredValue)

	e = entryOf(t, v, "OHS_METRICS_TOKEN")
	require.Equal(t, SourceConsole, e.Source)
	require.Equal(t, "set", *e.Value)

	e = entryOf(t, v, "OHS_BACKFILL_DAYS")
	require.Equal(t, SourceDefault, e.Source)
	require.NotEmpty(t, e.Invalid)
	require.Equal(t, e.Setting.Default, *e.Value)

	e = entryOf(t, v, "OHS_WEBHOOK_PAUSE_AFTER")
	require.Equal(t, SourceDefault, e.Source)
	require.Equal(t, "5", *e.Value)
	require.Empty(t, e.Invalid)

	l2, _ := manageLive(t, nil, nil)
	v2, err := l2.View(context.Background())
	require.NoError(t, err)
	require.Nil(t, entryOf(t, v2, "OHS_METRICS_TOKEN").Value)
}

func TestViewEnvSecret(t *testing.T) {
	l, _ := manageLive(t, map[string]string{"OHS_METRICS_TOKEN": "0123456789abcdefENV"}, map[string]Row{
		"OHS_METRICS_TOKEN": {Name: "OHS_METRICS_TOKEN", Value: "0123456789abcdefSTORED", Sealed: true},
	})
	v, err := l.View(context.Background())
	require.NoError(t, err)
	e := entryOf(t, v, "OHS_METRICS_TOKEN")
	require.Equal(t, SourceEnv, e.Source)
	require.Equal(t, "set", *e.Value)
	require.Equal(t, "set", *e.StoredValue)
}

func TestPatchLocked(t *testing.T) {
	l, st := manageLive(t, map[string]string{"OHS_LOG_LEVEL": "info"}, nil)
	_, err := l.Patch(context.Background(), 0, map[string]*string{"OHS_LISTEN": sp(":9"), "OHS_LOG_LEVEL": sp("debug")}, uuid.New())
	var le *LockedError
	require.ErrorAs(t, err, &le)
	require.Len(t, le.Settings, 2)
	byName := map[string]string{}
	for _, s := range le.Settings {
		byName[s.Name] = s.Message
	}
	require.True(t, strings.HasPrefix(byName["OHS_LISTEN"], "bootstrap:"))
	require.True(t, strings.HasPrefix(byName["OHS_LOG_LEVEL"], "env:"))
	require.Empty(t, st.applies)
}

func TestPatchInvalid(t *testing.T) {
	ctx := context.Background()
	l, st := manageLive(t, nil, map[string]Row{
		"OHS_WEBHOOK_PER_TENANT": {Name: "OHS_WEBHOOK_PER_TENANT", Value: "4"},
	})
	_, err := l.Patch(ctx, 0, map[string]*string{
		"OHS_WEBHOOK_PAUSE_AFTER": sp("abc"), "OHS_GONE": sp("1"), "OHS_RETENTION_DAYS": sp(""),
	}, uuid.New())
	var ie *InvalidError
	require.ErrorAs(t, err, &ie)
	require.Len(t, ie.Settings, 3)
	for _, s := range ie.Settings {
		if s.Name == "OHS_RETENTION_DAYS" {
			require.Contains(t, s.Message, "send null")
		}
	}

	_, err = l.Patch(ctx, 0, map[string]*string{"OHS_WEBHOOK_CONCURRENCY": sp("2")}, uuid.New())
	require.ErrorAs(t, err, &ie)
	require.Len(t, ie.Settings, 1)
	require.Equal(t, "OHS_WEBHOOK_PER_TENANT", ie.Settings[0].Name)

	for _, bad := range []string{"short", "0123456789abc\ndef", strings.Repeat("x", 5000)} {
		_, err = l.Patch(ctx, 0, map[string]*string{"OHS_METRICS_TOKEN": sp(bad)}, uuid.New())
		require.ErrorAs(t, err, &ie)
		require.Len(t, ie.Settings, 1)
		require.NotContains(t, ie.Settings[0].Message, bad)
	}
	require.Empty(t, st.applies)
}

func TestPatchUnknownRowRemoval(t *testing.T) {
	l, st := manageLive(t, nil, map[string]Row{"OHS_GONE": {Name: "OHS_GONE", Value: "1"}})
	res, err := l.Patch(context.Background(), 0, map[string]*string{"OHS_GONE": nil}, uuid.New())
	require.NoError(t, err)
	require.Equal(t, []string{"OHS_GONE"}, res.Changed)
	require.Empty(t, res.RestartRequired)
	require.EqualValues(t, 1, res.Revision)
	require.NotContains(t, st.rows, "OHS_GONE")

	_, err = l.Patch(context.Background(), 1, map[string]*string{"OHS_NEVER": nil}, uuid.New())
	var ie *InvalidError
	require.ErrorAs(t, err, &ie)
}

func TestPatchRestartRequired(t *testing.T) {
	l, st := manageLive(t, nil, nil)
	require.Equal(t, zerolog.InfoLevel, l.LogLevel())
	res, err := l.Patch(context.Background(), 0, map[string]*string{
		"OHS_LOG_LEVEL": sp("debug"), "OHS_INGEST_CONCURRENCY": sp("4")}, uuid.New())
	require.NoError(t, err)
	require.Equal(t, []string{"OHS_INGEST_CONCURRENCY", "OHS_LOG_LEVEL"}, res.Changed)
	require.Equal(t, []string{"OHS_INGEST_CONCURRENCY"}, res.RestartRequired)
	require.True(t, st.applies[0].Restart)
	require.Equal(t, zerolog.DebugLevel, l.LogLevel())
	require.EqualValues(t, 1, res.Revision)
}

func TestPatchNoChange(t *testing.T) {
	l, st := manageLive(t, nil, map[string]Row{
		"OHS_LOG_LEVEL":     {Name: "OHS_LOG_LEVEL", Value: "warn"},
		"OHS_METRICS_TOKEN": {Name: "OHS_METRICS_TOKEN", Value: "0123456789abcdefXYZ", Sealed: true},
	})
	st.rev = 3
	res, err := l.Patch(context.Background(), 3, map[string]*string{
		"OHS_LOG_LEVEL": sp("warn"), "OHS_METRICS_TOKEN": sp("0123456789abcdefXYZ"), "OHS_RETENTION_DAYS": nil}, uuid.New())
	require.NoError(t, err)
	require.EqualValues(t, 3, res.Revision)
	require.NotNil(t, res.Changed)
	require.Empty(t, res.Changed)
	require.NotNil(t, res.RestartRequired)
	require.Empty(t, res.RestartRequired)
	require.Empty(t, st.applies)

	_, err = l.Patch(context.Background(), 2, map[string]*string{"OHS_LOG_LEVEL": sp("warn")}, uuid.New())
	require.True(t, errors.Is(err, ErrRevisionMoved), "a no-op body still checks the revision")
}

func TestExportFormat(t *testing.T) {
	l, _ := manageLive(t, nil, map[string]Row{
		"OHS_LOG_LEVEL":     {Name: "OHS_LOG_LEVEL", Value: "warn"},
		"OHS_METRICS_TOKEN": {Name: "OHS_METRICS_TOKEN", Value: "0123456789abcdefXYZ", Sealed: true},
	})
	out, err := l.Export(context.Background())
	require.NoError(t, err)
	text := string(out)
	require.True(t, strings.HasPrefix(text, "# Open Health Sync settings, revision 0\n"))
	require.NotContains(t, text, "0123456789abcdefXYZ")
	require.Contains(t, text, "# Log level (source console, applies live)\nOHS_LOG_LEVEL=warn\n")
	require.Contains(t, text, "OHS_DATABASE_URL=\n", "a bootstrap secret set in the environment is not exported")
	require.NotContains(t, text, "postgres://x")
	require.Equal(t, `"a b#c \"q\"\\"`, quoteEnv(`a b#c "q"\`))
	require.Equal(t, "plain", quoteEnv("plain"))
	require.Equal(t, `"it's"`, quoteEnv("it's"))
	require.Contains(t, text, "# set, value not exported\nOHS_METRICS_TOKEN=\n")
	require.Contains(t, text, "\n# Server\n")
	require.Contains(t, text, "# Retention\n")
	require.False(t, strings.Contains(text, "\r"))
}

func TestPatchStillSkippedPutIsRefused(t *testing.T) {
	l, st := manageLive(t, map[string]string{"OHS_WEBHOOK_CONCURRENCY": "2"}, map[string]Row{
		"OHS_WEBHOOK_PER_TENANT": {Name: "OHS_WEBHOOK_PER_TENANT", Value: "9"},
	})
	_, err := l.Patch(context.Background(), 0, map[string]*string{"OHS_WEBHOOK_PER_TENANT": sp("8")}, uuid.New())
	var ie *InvalidError
	require.ErrorAs(t, err, &ie)
	require.Len(t, ie.Settings, 1)
	require.Equal(t, "OHS_WEBHOOK_PER_TENANT", ie.Settings[0].Name)
	require.Empty(t, st.applies)
}

func TestViewEnvSetUnopenableStored(t *testing.T) {
	l, _ := manageLive(t, map[string]string{"OHS_LOG_LEVEL": "debug", "OHS_METRICS_TOKEN": "0123456789abcdefENV"}, map[string]Row{
		"OHS_LOG_LEVEL":     {Name: "OHS_LOG_LEVEL", OpenErr: errors.New("x")},
		"OHS_METRICS_TOKEN": {Name: "OHS_METRICS_TOKEN", Sealed: true, OpenErr: errors.New("x")},
	})
	v, err := l.View(context.Background())
	require.NoError(t, err)
	require.Nil(t, entryOf(t, v, "OHS_LOG_LEVEL").StoredValue)
	require.Equal(t, "set", *entryOf(t, v, "OHS_METRICS_TOKEN").StoredValue)
}

func TestPatchResentSkippedValueIsRefused(t *testing.T) {
	l, st := manageLive(t, map[string]string{"OHS_WEBHOOK_CONCURRENCY": "2"}, map[string]Row{
		"OHS_WEBHOOK_PER_TENANT": {Name: "OHS_WEBHOOK_PER_TENANT", Value: "9"},
	})
	_, err := l.Patch(context.Background(), 0, map[string]*string{"OHS_WEBHOOK_PER_TENANT": sp("9")}, uuid.New())
	var ie *InvalidError
	require.ErrorAs(t, err, &ie, "the same value the rows already skip is not unchanged")
	require.Len(t, ie.Settings, 1)
	require.Equal(t, "OHS_WEBHOOK_PER_TENANT", ie.Settings[0].Name)
	require.Empty(t, st.applies)
}

func TestSafeMessageNeverQuotesASecret(t *testing.T) {
	const value = `pa"ss\word`
	err := fmt.Errorf("OHS_X: bad value %q", value)
	secret := config.Setting{Name: "OHS_X", Secret: true}
	got := SafeMessage(secret, err)
	require.Equal(t, "OHS_X: invalid value", got)
	require.NotContains(t, got, "pa")
	plain := config.Setting{Name: "OHS_X"}
	require.Equal(t, err.Error(), SafeMessage(plain, err))
}
