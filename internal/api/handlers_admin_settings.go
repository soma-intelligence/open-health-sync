// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

// adminSettingJSON is one runtime setting as the console shows it. Value of a
// secret is "set" or null; a secret's value is never served.
type adminSettingJSON struct {
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
}

type adminSettingsJSON struct {
	Revision int64              `json:"revision"`
	Data     []adminSettingJSON `json:"data"`
}

// adminSettingsPatchJSON answers a PATCH that went through.
type adminSettingsPatchJSON struct {
	Revision        int64    `json:"revision"`
	Changed         []string `json:"changed"`
	RestartRequired []string `json:"restart_required"`
}

// adminProcessJSON is one process that reported within the last two minutes.
type adminProcessJSON struct {
	ID            string    `json:"id"`
	Host          string    `json:"host"`
	Roles         []string  `json:"roles"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	SeenAt        time.Time `json:"seen_at"`
	StartRevision int64     `json:"start_revision"`
	LiveRevision  int64     `json:"live_revision"`
	Skipped       []string  `json:"skipped"`
	ReloadError   *string   `json:"reload_error"`
	Behind        bool      `json:"behind"`
	Stale         bool      `json:"stale"`
}

type adminProcessesJSON struct {
	Revision int64              `json:"revision"`
	Data     []adminProcessJSON `json:"data"`
}

// mountAdminSettings mounts the four routes behind admin, the admin key
// middleware of mountAdmin.
func (s *server) mountAdminSettings(admin func(string, http.HandlerFunc)) {
	admin("GET /v1/admin/settings", s.listAdminSettings)
	admin("PATCH /v1/admin/settings", s.patchAdminSettings)
	admin("GET /v1/admin/settings/export", s.exportAdminSettings)
	admin("GET /v1/admin/processes", s.listAdminProcesses)
}

func toAdminSetting(e settings.Entry) adminSettingJSON {
	st := e.Setting
	out := adminSettingJSON{
		Name: st.Name, Label: st.Label, Section: st.Section, Help: st.Help, Type: string(st.Type),
		Default: st.Default, Secret: st.Secret, Apply: string(st.Apply), Source: string(e.Source),
		Value: e.Value, StoredValue: e.StoredValue,
	}
	if st.Type == config.TypeInt {
		out.Min, out.Max = st.Min, st.Max
	}
	if st.Type == config.TypeEnum {
		out.Values = append([]string{}, st.Values...)
	}
	if st.DefaultNote != "" {
		n := st.DefaultNote
		out.DefaultNote = &n
	}
	if e.Invalid != "" {
		m := e.Invalid
		out.Invalid = &m
	}
	return out
}

func toAdminProcess(p settings.Process) adminProcessJSON {
	out := adminProcessJSON{
		ID: p.ID.String(), Host: p.Host, Roles: append([]string{}, p.Roles...), Version: p.Version,
		StartedAt: p.StartedAt.UTC(), SeenAt: p.SeenAt.UTC(), StartRevision: p.StartRevision, LiveRevision: p.LiveRevision,
		Skipped: append([]string{}, p.Skipped...), Behind: p.Behind, Stale: p.Stale,
	}
	if p.ReloadError != "" {
		m := p.ReloadError
		out.ReloadError = &m
	}
	return out
}

// etagOf is the strong entity tag of a settings revision.
func etagOf(revision int64) string { return `"` + strconv.FormatInt(revision, 10) + `"` }

// parseIfMatch reads one entity tag, `"5"` or the weak `W/"5"` (the gzip
// middleware weakens ETags). A bare number, `*`, a list and anything that is
// not a revision are refused.
func parseIfMatch(h string) (revision int64, ok bool) {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "W/")
	if len(h) < 3 || h[0] != '"' || h[len(h)-1] != '"' {
		return 0, false
	}
	n, err := strconv.ParseInt(h[1:len(h)-1], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (s *server) listAdminSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.Settings.View(r.Context())
	if err != nil {
		internalError(w, r, "api: view settings", err)
		return
	}
	out := adminSettingsJSON{Revision: v.Revision, Data: make([]adminSettingJSON, 0, len(v.Entries))}
	for _, e := range v.Entries {
		out.Data = append(out.Data, toAdminSetting(e))
	}
	w.Header().Set("ETag", etagOf(v.Revision))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *server) patchAdminSettings(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	h := r.Header.Get("If-Match")
	if strings.TrimSpace(h) == "" {
		problem(w, http.StatusPreconditionRequired, "send If-Match with the revision you read: GET /v1/admin/settings gives it as ETag")
		return
	}
	rev, ok := parseIfMatch(h)
	if !ok {
		badRequest(w, `If-Match must be one revision, "<revision>" or W/"<revision>"`)
		return
	}
	var req map[string]*string
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if len(req) == 0 {
		badRequest(w, "the body changes nothing: give at least one setting, a string or null")
		return
	}
	by, _ := uuid.Parse(p.KeyID) // a key id is a uuid; anything else is left unnamed
	res, err := s.d.Settings.Patch(r.Context(), rev, req, by)
	if err != nil {
		patchError(w, r, err, func() (int64, error) {
			v, err := s.d.Settings.View(r.Context())
			return v.Revision, err
		})
		return
	}
	auditNote(r, "settings", res.Changed)
	auditNote(r, "revision", res.Revision)
	w.Header().Set("ETag", etagOf(res.Revision))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, adminSettingsPatchJSON{Revision: res.Revision, Changed: res.Changed, RestartRequired: res.RestartRequired})
}

// patchError answers an error of settings.Live.Patch. current reads the
// revision a 412 names. The text of an error that is not one of the three
// kinds is logged by internalError, never sent.
func patchError(w http.ResponseWriter, r *http.Request, err error, current func() (int64, error)) {
	var locked *settings.LockedError
	var invalid *settings.InvalidError
	switch {
	case errors.As(err, &locked):
		writeSettingsProblem(w, http.StatusConflict, "these settings are not the console's to change", locked.Settings)
	case errors.As(err, &invalid):
		writeSettingsProblem(w, http.StatusUnprocessableEntity, "these settings were refused", invalid.Settings)
	case errors.Is(err, settings.ErrRevisionMoved):
		rev, cerr := current()
		if cerr != nil {
			internalError(w, r, "api: settings revision after a conflict", cerr)
			return
		}
		w.Header().Set("ETag", etagOf(rev))
		problem(w, http.StatusPreconditionFailed, "the settings changed since you read them: read them again, then retry")
	default:
		internalError(w, r, "api: patch settings", err)
	}
}

func (s *server) exportAdminSettings(w http.ResponseWriter, r *http.Request) {
	b, err := s.d.Settings.Export(r.Context())
	if err != nil {
		internalError(w, r, "api: export settings", err)
		return
	}
	writeSettingsExport(w, b)
}

func writeSettingsExport(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ohs.env"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *server) listAdminProcesses(w http.ResponseWriter, r *http.Request) {
	// The pool, not Live.Processes: that one needs the Postgres store.
	ps, rev, err := settings.ListProcesses(r.Context(), s.d.Pool)
	if err != nil {
		internalError(w, r, "api: list processes", err)
		return
	}
	out := adminProcessesJSON{Revision: rev, Data: make([]adminProcessJSON, 0, len(ps))}
	for _, p := range ps {
		out.Data = append(out.Data, toAdminProcess(p))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
