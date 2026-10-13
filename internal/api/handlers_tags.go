// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/timetag"
)

type createTagRequest struct {
	Kind     string  `json:"kind"`
	Label    *string `json:"label"`
	StartAt  string  `json:"start_at"`
	EndAt    *string `json:"end_at"`
	Timezone *string `json:"timezone"`
	Note     *string `json:"note"`
	Source   string  `json:"source"`
}

// nullable is a string member of a PATCH body: absent, null or a value.
type nullable struct {
	set bool
	v   *string
}

func (n *nullable) UnmarshalJSON(b []byte) error {
	n.set = true
	if string(b) == "null" {
		n.v = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.v = &s
	return nil
}

func (n nullable) change() timetag.Change[string] {
	return timetag.Change[string]{Set: n.set, Value: n.v}
}

// timeChange parses a nullable time member.
func (n nullable) timeChange(name string) (timetag.Change[time.Time], error) {
	c := timetag.Change[time.Time]{Set: n.set}
	if n.v != nil {
		t, err := parseTime(*n.v)
		if err != nil {
			return c, errors.New(name + ": " + err.Error())
		}
		c.Value = &t
	}
	return c, nil
}

type updateTagRequest struct {
	Kind     nullable `json:"kind"`
	Label    nullable `json:"label"`
	StartAt  nullable `json:"start_at"`
	EndAt    nullable `json:"end_at"`
	Timezone nullable `json:"timezone"`
	Note     nullable `json:"note"`
}

func (s *server) mountTags(mux *http.ServeMux) {
	// The stored response of a create names the user, so it is deleted with them.
	mux.Handle("POST /v1/users/{id}/tags", auth.Middleware(s.lookup, auth.TenantKey)(s.idempotent(http.HandlerFunc(s.createTag))))
	s.tenantRoute(mux, "GET /v1/users/{id}/tags", s.listTags)
	s.tenantRoute(mux, "GET /v1/users/{id}/tags/{tag_id}", s.getTag)
	s.tenantRoute(mux, "PATCH /v1/users/{id}/tags/{tag_id}", s.updateTag)
	s.tenantRoute(mux, "DELETE /v1/users/{id}/tags/{tag_id}", s.deleteTag)
}

// pathTag parses {id} and {tag_id}. A malformed tag id is a tag that does
// not exist.
func pathTag(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, uuid.UUID, bool) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return p, uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("tag_id"))
	if err != nil {
		notFound(w, "tag")
		return p, uuid.Nil, uuid.Nil, false
	}
	return p, user, id, true
}

func (s *server) createTag(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	var req createTagRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	f := timetag.Fields{Kind: req.Kind, Label: req.Label, Timezone: req.Timezone, Note: req.Note, Source: req.Source}
	if req.StartAt == "" {
		badRequest(w, "start_at is required")
		return
	}
	var err error
	if f.StartAt, err = parseTime(req.StartAt); err != nil {
		badRequest(w, "start_at: "+err.Error())
		return
	}
	if req.EndAt != nil {
		end, err := parseTime(*req.EndAt)
		if err != nil {
			badRequest(w, "end_at: "+err.Error())
			return
		}
		f.EndAt = &end
	}
	t, err := timetag.Create(r.Context(), s.d.Pool, p.TenantID, user, f)
	if !tagErr(w, r, err, "api: create tag") {
		return
	}
	tenant.SetIdempotencyUser(r.Context(), user)
	w.Header().Set("Location", "/v1/users/"+user.String()+"/tags/"+t.ID.String())
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) listTags(w http.ResponseWriter, r *http.Request) {
	p, user, ok := pathUser(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	q := timetag.Query{Kind: qs.Get("kind"), Cursor: qs.Get("cursor")}
	var err error
	if q.Limit, err = parseLimit(r); err != nil {
		badRequest(w, err.Error())
		return
	}
	for name, dst := range map[string]*time.Time{"start": &q.Start, "end": &q.End} {
		if v := qs.Get(name); v != "" {
			if *dst, err = parseTime(v); err != nil {
				badRequest(w, name+": "+err.Error())
				return
			}
		}
	}
	pg, err := timetag.List(r.Context(), s.d.Pool, p.TenantID, user, q)
	if !tagErr(w, r, err, "api: list tags") {
		return
	}
	writeJSON(w, http.StatusOK, newPage(pg.Records, pg.NextCursor))
}

func (s *server) getTag(w http.ResponseWriter, r *http.Request) {
	p, user, id, ok := pathTag(w, r)
	if !ok {
		return
	}
	t, err := timetag.Get(r.Context(), s.d.Pool, p.TenantID, user, id)
	if !tagErr(w, r, err, "api: get tag") {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) updateTag(w http.ResponseWriter, r *http.Request) {
	p, user, id, ok := pathTag(w, r)
	if !ok {
		return
	}
	var req updateTagRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	patch := timetag.Patch{Kind: req.Kind.change(), Label: req.Label.change(),
		Timezone: req.Timezone.change(), Note: req.Note.change()}
	var err error
	if patch.StartAt, err = req.StartAt.timeChange("start_at"); err != nil {
		badRequest(w, err.Error())
		return
	}
	if patch.EndAt, err = req.EndAt.timeChange("end_at"); err != nil {
		badRequest(w, err.Error())
		return
	}
	t, err := timetag.Update(r.Context(), s.d.Pool, p.TenantID, user, id, patch)
	if !tagErr(w, r, err, "api: update tag") {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTag(w http.ResponseWriter, r *http.Request) {
	p, user, id, ok := pathTag(w, r)
	if !ok {
		return
	}
	if !tagErr(w, r, timetag.Delete(r.Context(), s.d.Pool, p.TenantID, user, id), "api: delete tag") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// tagErr writes the problem for err and returns false; it returns true when
// err is nil. A refusal names the field, never the value sent.
func tagErr(w http.ResponseWriter, r *http.Request, err error, msg string) bool {
	var ie *timetag.InvalidError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ie):
		badRequest(w, ie.Msg)
	case errors.Is(err, timetag.ErrInvalidCursor):
		badRequest(w, "the cursor is not valid")
	case errors.Is(err, timetag.ErrUserNotFound):
		notFound(w, "user")
	case errors.Is(err, timetag.ErrNotFound):
		notFound(w, "tag")
	default:
		internalError(w, r, msg, err)
	}
	return false
}
