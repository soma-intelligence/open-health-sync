// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

// settingsEnv is the test router with the settings routes, over the pool of
// the test database, and the Live behind them. The environment holds only
// the two variables Load needs, so every other setting is the console's.
func settingsEnv(t *testing.T) (*env, *settings.Live) {
	t.Helper()
	var live *settings.Live
	e := newEnv(t, func(d *api.Deps) {
		keyB64 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
		kv := map[string]string{"OHS_DATABASE_URL": "postgres://unused/unused", "OHS_ENCRYPTION_KEY": keyB64}
		environ := []string{"OHS_DATABASE_URL=" + kv["OHS_DATABASE_URL"], "OHS_ENCRYPTION_KEY=" + keyB64}
		lookup := func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
		cfg, err := config.Load(lookup, environ)
		require.NoError(t, err)
		key, err := base64.StdEncoding.DecodeString(keyB64)
		require.NoError(t, err)
		box, err := crypto.NewBox(key)
		require.NoError(t, err)
		live = settings.NewLive(settings.LiveOptions{Store: settings.NewPG(d.Pool, box), Env: lookup, Environ: environ, Config: cfg})
		d.Settings = live
	})
	return e, live
}

type settingsBody struct {
	Revision int64 `json:"revision"`
	Data     []struct {
		Name        string   `json:"name"`
		Label       string   `json:"label"`
		Section     string   `json:"section"`
		Help        string   `json:"help"`
		Type        string   `json:"type"`
		Min         *int64   `json:"min"`
		Max         *int64   `json:"max"`
		Values      []string `json:"values"`
		Default     string   `json:"default"`
		DefaultNote *string  `json:"default_note"`
		Secret      bool     `json:"secret"`
		Apply       string   `json:"apply"`
		Source      string   `json:"source"`
		Value       *string  `json:"value"`
		StoredValue *string  `json:"stored_value"`
		Invalid     *string  `json:"invalid"`
	} `json:"data"`
}

type patchBody struct {
	Revision        int64    `json:"revision"`
	Changed         []string `json:"changed"`
	RestartRequired []string `json:"restart_required"`
}

type settingsProblemBody struct {
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Settings []struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"settings"`
}

func (e *env) settingsRevision(t *testing.T) int64 {
	t.Helper()
	r := e.do("GET", "/v1/admin/settings", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var b settingsBody
	r.json(t, &b)
	return b.Revision
}

func etag(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

func (e *env) patchSettings(t *testing.T, rev int64, body string, hdr ...string) resp {
	t.Helper()
	return e.do("PATCH", "/v1/admin/settings", e.keyA, body, append([]string{"If-Match", etag(rev), "Content-Type", "application/json"}, hdr...)...)
}

func TestAdminSettingsGet(t *testing.T) {
	e, _ := settingsEnv(t)

	r := e.do("GET", "/v1/admin/settings", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var b settingsBody
	r.json(t, &b)
	assert.Equal(t, etag(b.Revision), r.header.Get("ETag"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))

	reg := config.Registry()
	require.Len(t, b.Data, len(reg))
	for i, s := range reg {
		got := b.Data[i]
		assert.Equal(t, s.Name, got.Name, "registry order")
		assert.Equal(t, string(s.Type), got.Type)
		assert.Equal(t, string(s.Apply), got.Apply)
		assert.Equal(t, s.Secret, got.Secret)
		assert.Contains(t, []string{"env", "console", "default"}, got.Source)
		assert.Equal(t, s.Default, got.Default)
		if s.Type != config.TypeInt {
			assert.Nil(t, got.Min, s.Name)
			assert.Nil(t, got.Max, s.Name)
		}
		if s.Type != config.TypeEnum {
			assert.Nil(t, got.Values, s.Name)
		}
		if s.Secret {
			assert.Nil(t, got.Value, "a secret that is not set has no value: "+s.Name)
		}
	}
	var raw map[string]any
	r.json(t, &raw)
	first := raw["data"].([]any)[0].(map[string]any)
	assert.Len(t, first, 16, "the 16 fields of the contract")

	// Not the instance admin: 403.
	r = e.do("GET", "/v1/admin/settings", e.keyB, nil)
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Empty(t, r.header.Get("ETag"))
}

func TestAdminSettingsPatch(t *testing.T) {
	e, _ := settingsEnv(t)
	rev := e.settingsRevision(t)

	r := e.patchSettings(t, rev, `{"OHS_WEBHOOK_PAUSE_AFTER": "9"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var p patchBody
	r.json(t, &p)
	assert.Equal(t, rev+1, p.Revision)
	assert.Equal(t, []string{"OHS_WEBHOOK_PAUSE_AFTER"}, p.Changed)
	assert.Equal(t, []string{}, p.RestartRequired, "an empty list, not null")
	assert.Equal(t, etag(rev+1), r.header.Get("ETag"))
	assert.JSONEq(t, `[]`, string(mustSub(t, r.body, "restart_required")))

	// The old ETag is stale: 412 with the current one.
	r = e.patchSettings(t, rev, `{"OHS_WEBHOOK_PAUSE_AFTER": "8"}`)
	require.Equal(t, http.StatusPreconditionFailed, r.code, string(r.body))
	assert.Equal(t, etag(rev+1), r.header.Get("ETag"))

	// No If-Match: 428. `*`: 400.
	r = e.do("PATCH", "/v1/admin/settings", e.keyA, `{"OHS_WEBHOOK_PAUSE_AFTER": "8"}`)
	assert.Equal(t, http.StatusPreconditionRequired, r.code)
	r = e.do("PATCH", "/v1/admin/settings", e.keyA, `{"OHS_WEBHOOK_PAUSE_AFTER": "8"}`, "If-Match", "*")
	assert.Equal(t, http.StatusBadRequest, r.code)
	r = e.do("PATCH", "/v1/admin/settings", e.keyA, `{}`, "If-Match", etag(rev+1))
	assert.Equal(t, http.StatusBadRequest, r.code, "an empty object changes nothing")

	// A weak ETag passes.
	r = e.do("PATCH", "/v1/admin/settings", e.keyA, `{"OHS_WEBHOOK_PAUSE_AFTER": "7"}`, "If-Match", "W/"+etag(rev+1))
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	cur := e.settingsRevision(t)
	assert.Equal(t, rev+2, cur)

	// A body that changes no row answers the unchanged revision.
	r = e.patchSettings(t, cur, `{"OHS_WEBHOOK_PAUSE_AFTER": "7"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &p)
	assert.Equal(t, cur, p.Revision)
	assert.Empty(t, p.Changed)

	// The GET shows the saved value as the console's.
	r = e.do("GET", "/v1/admin/settings", e.keyA, nil)
	var b settingsBody
	r.json(t, &b)
	for _, s := range b.Data {
		if s.Name == "OHS_WEBHOOK_PAUSE_AFTER" {
			assert.Equal(t, "console", s.Source)
			require.NotNil(t, s.Value)
			assert.Equal(t, "7", *s.Value)
		}
	}

	// Not an admin: 403, nothing changes.
	r = e.do("PATCH", "/v1/admin/settings", e.keyB, `{"OHS_WEBHOOK_PAUSE_AFTER": "1"}`, "If-Match", etag(cur))
	assert.Equal(t, http.StatusForbidden, r.code)
	assert.Equal(t, cur, e.settingsRevision(t))
}

func mustSub(t *testing.T, body []byte, key string) json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m))
	return m[key]
}

// The gzip middleware weakens the ETag it passes on: the weak form of the
// ETag a gzipped GET returned is accepted as If-Match.
func TestAdminSettingsPatchAcceptsTheWeakETagOfAGzippedGet(t *testing.T) {
	e, _ := settingsEnv(t)
	r := e.do("GET", "/v1/admin/settings", e.keyA, nil, "Accept-Encoding", "gzip")
	require.Equal(t, http.StatusOK, r.code)
	require.Equal(t, "gzip", r.header.Get("Content-Encoding"), "the list is over 1 KiB: it is compressed")
	weak := r.header.Get("ETag")
	require.True(t, strings.HasPrefix(weak, `W/"`), weak)

	r = e.do("PATCH", "/v1/admin/settings", e.keyA, `{"OHS_WEBHOOK_PAUSE_AFTER": "9"}`, "If-Match", weak)
	assert.Equal(t, http.StatusOK, r.code, string(r.body))
}

func TestAdminSettingsConflictAndInvalid(t *testing.T) {
	e, _ := settingsEnv(t)
	rev := e.settingsRevision(t)

	r := e.patchSettings(t, rev, `{"OHS_LISTEN": ":1"}`)
	require.Equal(t, http.StatusConflict, r.code, string(r.body))
	var p settingsProblemBody
	r.json(t, &p)
	require.Len(t, p.Settings, 1)
	assert.Equal(t, "OHS_LISTEN", p.Settings[0].Name)
	assert.True(t, strings.HasPrefix(p.Settings[0].Message, "bootstrap:"), p.Settings[0].Message)
	assert.Equal(t, rev, e.settingsRevision(t))

	r = e.patchSettings(t, rev, `{"OHS_WEBHOOK_PAUSE_AFTER": "x", "OHS_RETENTION_DAYS": "-2"}`)
	require.Equal(t, http.StatusUnprocessableEntity, r.code, string(r.body))
	p = settingsProblemBody{}
	r.json(t, &p)
	require.Len(t, p.Settings, 2)
	names := []string{p.Settings[0].Name, p.Settings[1].Name}
	assert.ElementsMatch(t, []string{"OHS_WEBHOOK_PAUSE_AFTER", "OHS_RETENTION_DAYS"}, names)
	assert.Equal(t, rev, e.settingsRevision(t), "all or nothing")

	// A value that is not a string or null is a malformed body.
	r = e.patchSettings(t, rev, `{"OHS_X": 5}`)
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
	r = e.patchSettings(t, rev, `[1]`)
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
}

// logBuf is a log sink safe for concurrent writes.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// captureLogs sends the default logger to a buffer for the test.
func captureLogs(t *testing.T) *logBuf {
	t.Helper()
	buf := &logBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestAdminSettingsSecretsNeverServed(t *testing.T) {
	logs := captureLogs(t)
	e, _ := settingsEnv(t)
	const secret = "s3cr3t-metrics-token-0123456789"
	rev := e.settingsRevision(t)

	r := e.patchSettings(t, rev, `{"OHS_METRICS_TOKEN": "`+secret+`"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var p patchBody
	r.json(t, &p)
	assert.Equal(t, []string{"OHS_METRICS_TOKEN"}, p.RestartRequired)
	assert.NotContains(t, string(r.body), secret)

	r = e.do("GET", "/v1/admin/settings", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	assert.NotContains(t, string(r.body), secret)
	var b settingsBody
	r.json(t, &b)
	for _, s := range b.Data {
		if s.Name == "OHS_METRICS_TOKEN" {
			require.NotNil(t, s.Value)
			assert.Equal(t, "set", *s.Value)
		}
	}

	r = e.do("GET", "/v1/admin/settings/export", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	assert.NotContains(t, string(r.body), secret)
	assert.Contains(t, string(r.body), "# set, value not exported\nOHS_METRICS_TOKEN=\n")

	// A refused secret does not echo its value either.
	r = e.patchSettings(t, p.Revision, `{"OHS_METRICS_TOKEN": "short"}`)
	require.Equal(t, http.StatusUnprocessableEntity, r.code, string(r.body))
	assert.NotContains(t, string(r.body), "short\"")

	r = e.do("GET", "/v1/audit?action=config.write", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.NotContains(t, string(r.body), secret)
	var page struct {
		Data []struct {
			Status int            `json:"status"`
			Detail map[string]any `json:"detail"`
		} `json:"data"`
	}
	r.json(t, &page)
	var saved bool
	for _, ev := range page.Data {
		if ev.Status == http.StatusOK && ev.Detail["settings"] != nil {
			assert.Equal(t, []any{"OHS_METRICS_TOKEN"}, ev.Detail["settings"])
			assert.EqualValues(t, p.Revision, ev.Detail["revision"])
			saved = true
		}
	}
	assert.True(t, saved, "the save is in the audit log with the names that changed")

	assert.NotContains(t, logs.String(), secret)
}

func TestAdminSettingsExport(t *testing.T) {
	e, _ := settingsEnv(t)
	rev := e.settingsRevision(t)
	r := e.patchSettings(t, rev, `{"OHS_WEBHOOK_PAUSE_AFTER": "9"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))

	r = e.do("GET", "/v1/admin/settings/export", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "text/plain; charset=utf-8", r.header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="ohs.env"`, r.header.Get("Content-Disposition"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	body := string(r.body)
	assert.True(t, strings.HasPrefix(body, "# Open Health Sync settings, revision "+strconv.FormatInt(rev+1, 10)+"\n"), body)
	assert.Contains(t, body, "\nOHS_WEBHOOK_PAUSE_AFTER=9\n")

	r = e.do("GET", "/v1/admin/settings/export", e.keyB, nil)
	assert.Equal(t, http.StatusForbidden, r.code)
}

func TestAdminProcesses(t *testing.T) {
	e, live := settingsEnv(t)
	ctx := context.Background()

	r := e.do("GET", "/v1/admin/processes", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	var empty struct {
		Data []any `json:"data"`
	}
	r.json(t, &empty)
	assert.Equal(t, []any{}, empty.Data, "an array, not null")

	id := uuid.New()
	hb := settings.NewHeartbeatWriter(e.pool, live, settings.Heartbeat{
		ID: id, Host: "ohs-test", Roles: []string{"etl", "api"}, Version: "0.0.0-test", StartedAt: time.Now().Add(-time.Minute).UTC(),
	})
	require.NoError(t, hb.Beat(ctx))

	type procBody struct {
		Revision int64 `json:"revision"`
		Data     []struct {
			ID          string   `json:"id"`
			Host        string   `json:"host"`
			Roles       []string `json:"roles"`
			Skipped     []string `json:"skipped"`
			ReloadError *string  `json:"reload_error"`
			Behind      bool     `json:"behind"`
			Stale       bool     `json:"stale"`
		} `json:"data"`
	}
	var pb procBody
	r = e.do("GET", "/v1/admin/processes", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &pb)
	require.Len(t, pb.Data, 1)
	assert.Equal(t, id.String(), pb.Data[0].ID)
	assert.Equal(t, []string{"api", "etl"}, pb.Data[0].Roles)
	assert.Nil(t, pb.Data[0].ReloadError)
	assert.False(t, pb.Data[0].Behind)

	// A restart setting saved after the process started: it is behind.
	rev := e.settingsRevision(t)
	r = e.patchSettings(t, rev, `{"OHS_METRICS": "false"}`)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	pb = procBody{}
	r = e.do("GET", "/v1/admin/processes", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &pb)
	require.Len(t, pb.Data, 1)
	assert.True(t, pb.Data[0].Behind)
	assert.Equal(t, rev+1, pb.Revision)

	r = e.do("GET", "/v1/admin/processes", e.keyB, nil)
	assert.Equal(t, http.StatusForbidden, r.code)
}
