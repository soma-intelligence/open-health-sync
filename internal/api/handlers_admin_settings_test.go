// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

func TestParseIfMatch(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{`"5"`, 5, true},
		{`W/"5"`, 5, true},
		{` "0" `, 0, true},
		{`5`, 0, false},
		{`*`, 0, false},
		{`"x"`, 0, false},
		{`""`, 0, false},
		{`"-1"`, 0, false},
		{`"5", "6"`, 0, false},
		{`w/"5"`, 0, false},
		{``, 0, false},
	} {
		got, ok := parseIfMatch(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

func problemBody(t *testing.T, rec *httptest.ResponseRecorder) settingsProblem {
	t.Helper()
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	var p settingsProblem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	assert.Equal(t, rec.Code, p.Status)
	return p
}

func TestPatchErrorMapping(t *testing.T) {
	r := httptest.NewRequest("PATCH", "/v1/admin/settings", nil)
	current := func() (int64, error) { return 7, nil }

	rec := httptest.NewRecorder()
	patchError(rec, r, &settings.LockedError{Settings: []settings.SettingError{{Name: "OHS_LISTEN", Message: "bootstrap: set it in the environment"}}}, current)
	require.Equal(t, http.StatusConflict, rec.Code)
	p := problemBody(t, rec)
	assert.Equal(t, []settingErrorJSON{{Name: "OHS_LISTEN", Message: "bootstrap: set it in the environment"}}, p.Settings)

	rec = httptest.NewRecorder()
	patchError(rec, r, &settings.InvalidError{Settings: []settings.SettingError{{Name: "A", Message: "m1"}, {Name: "B", Message: "m2"}}}, current)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Len(t, problemBody(t, rec).Settings, 2)

	rec = httptest.NewRecorder()
	patchError(rec, r, settings.ErrRevisionMoved, current)
	require.Equal(t, http.StatusPreconditionFailed, rec.Code)
	assert.Equal(t, `"7"`, rec.Header().Get("ETag"))
	assert.Nil(t, problemBody(t, rec).Settings)

	// Anything else is a generic 500: the error text never reaches the body.
	rec = httptest.NewRecorder()
	patchError(rec, r, errors.New("boom: s3cret-value"), current)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "s3cret-value")
}

func TestAdminSettingJSONShape(t *testing.T) {
	reg := config.Registry()
	var intS, enumS, secretS config.Setting
	for _, s := range reg {
		switch {
		case s.Type == config.TypeInt && intS.Name == "":
			intS = s
		case s.Type == config.TypeEnum && enumS.Name == "":
			enumS = s
		case s.Secret && secretS.Name == "":
			secretS = s
		}
	}
	require.NotEmpty(t, intS.Name)
	require.NotEmpty(t, enumS.Name)
	require.NotEmpty(t, secretS.Name)
	set, stored := "set", "x"

	b, err := json.Marshal(toAdminSetting(settings.Entry{Setting: intS, Source: settings.SourceDefault, Value: &stored}))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	for _, k := range []string{"name", "label", "section", "help", "type", "min", "max", "values", "default", "default_note",
		"secret", "apply", "source", "value", "stored_value", "invalid"} {
		assert.Contains(t, m, k)
	}
	assert.Len(t, m, 16)
	assert.Nil(t, m["values"], "values is null for an int")
	assert.Nil(t, m["stored_value"])
	assert.Nil(t, m["invalid"])
	assert.Equal(t, "x", m["value"])

	b, _ = json.Marshal(toAdminSetting(settings.Entry{Setting: enumS, Source: settings.SourceConsole, Value: &stored}))
	m = map[string]any{}
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Nil(t, m["min"])
	assert.Nil(t, m["max"])
	assert.NotEmpty(t, m["values"])

	b, _ = json.Marshal(toAdminSetting(settings.Entry{Setting: secretS, Source: settings.SourceEnv, Value: &set, StoredValue: &set, Invalid: "bad"}))
	m = map[string]any{}
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "set", m["value"])
	assert.Equal(t, "set", m["stored_value"])
	assert.Equal(t, "bad", m["invalid"])
	assert.Equal(t, true, m["secret"])
}

func TestAdminProcessJSONShape(t *testing.T) {
	id := uuid.New()
	started := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	p := settings.Process{
		Heartbeat: settings.Heartbeat{ID: id, Host: "ohs-7f9c", Roles: []string{"api", "etl"}, Version: "0.4.0", StartedAt: started},
		SeenAt:    started.Add(5*time.Minute + 30*time.Second), StartRevision: 4, LiveRevision: 6, Behind: true,
	}
	b, err := json.Marshal(toAdminProcess(p))
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"`+id.String()+`","host":"ohs-7f9c","roles":["api","etl"],"version":"0.4.0",
"started_at":"2026-11-14T10:00:00Z","seen_at":"2026-11-14T10:05:30Z","start_revision":4,"live_revision":6,
"skipped":[],"reload_error":null,"behind":true,"stale":false}`, string(b))

	p.Skipped, p.ReloadError, p.Roles = []string{"OHS_X"}, "reload failed", nil
	b, _ = json.Marshal(toAdminProcess(p))
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "reload failed", m["reload_error"])
	assert.Equal(t, []any{"OHS_X"}, m["skipped"])
	assert.Equal(t, []any{}, m["roles"])
}

func TestAuditNoteIsMergedNotLeaked(t *testing.T) {
	r := httptest.NewRequest("PATCH", "/v1/admin/settings", nil)
	auditNote(r, "settings", []string{"X"}) // no notes map in the context: a no-op, no panic
	r = r.WithContext(withAuditNotes(r.Context()))
	auditNote(r, "settings", []string{"OHS_A"})
	auditNote(r, "revision", int64(3))
	assert.Equal(t, map[string]any{"settings": []string{"OHS_A"}, "revision": int64(3)}, auditNotesOf(r))
}

func TestExportDispositionHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSettingsExport(rec, []byte("# x\n"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="ohs.env"`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}
