// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

type tagJSON struct {
	ID        uuid.UUID  `json:"id"`
	Kind      string     `json:"kind"`
	Label     *string    `json:"label"`
	StartAt   time.Time  `json:"start_at"`
	EndAt     *time.Time `json:"end_at"`
	Timezone  *string    `json:"timezone"`
	Note      *string    `json:"note"`
	Source    string     `json:"source"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (e *env) tag(user uuid.UUID, body string) tagJSON {
	e.t.Helper()
	r := e.do("POST", "/v1/users/"+user.String()+"/tags", e.keyA, body)
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
	var tg tagJSON
	r.json(e.t, &tg)
	return tg
}

func TestTagsCRUD(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "tagged")
	base := "/v1/users/" + u.String() + "/tags"

	r := e.do("POST", base, e.keyA, `{"kind":"travel","label":"Trip to Tokyo","start_at":"2026-04-10T07:00:00+02:00",
		"timezone":"Asia/Tokyo","note":"invented note","source":"my-app"}`)
	require.Equal(t, http.StatusCreated, r.code, string(r.body))
	var created tagJSON
	r.json(t, &created)
	assert.Equal(t, base+"/"+created.ID.String(), r.header.Get("Location"))
	assert.Equal(t, "travel", created.Kind)
	assert.Equal(t, time.Date(2026, 4, 10, 5, 0, 0, 0, time.UTC), created.StartAt)
	assert.Equal(t, "UTC", created.StartAt.Location().String())
	assert.Nil(t, created.EndAt, "ongoing")
	assert.Equal(t, "Asia/Tokyo", *created.Timezone)
	assert.Equal(t, "my-app", created.Source)

	g := e.do("GET", base+"/"+created.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, g.code, string(g.body))
	var got tagJSON
	g.json(t, &got)
	assert.Equal(t, created, got)

	// Close the ongoing tag; clear the note; absent members stay.
	p := e.do("PATCH", base+"/"+created.ID.String(), e.keyA, `{"end_at":"2026-04-20","note":null}`)
	require.Equal(t, http.StatusOK, p.code, string(p.body))
	var closed tagJSON
	p.json(t, &closed)
	require.NotNil(t, closed.EndAt)
	assert.Equal(t, time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC), *closed.EndAt)
	assert.Nil(t, closed.Note)
	assert.Equal(t, "Trip to Tokyo", *closed.Label)
	assert.Equal(t, "my-app", closed.Source)
	assert.False(t, closed.UpdatedAt.Before(created.UpdatedAt))

	// Reopen it.
	p = e.do("PATCH", base+"/"+created.ID.String(), e.keyA, `{"end_at":null}`)
	require.Equal(t, http.StatusOK, p.code, string(p.body))
	p.json(t, &closed)
	assert.Nil(t, closed.EndAt)

	// A patch is checked as a whole.
	p = e.do("PATCH", base+"/"+created.ID.String(), e.keyA, `{"end_at":"2026-04-01T00:00:00Z"}`)
	assert.Equal(t, http.StatusBadRequest, p.code, string(p.body))
	assert.Contains(t, string(p.body), "end_at must be after start_at")
	for _, body := range []string{`{"kind":null}`, `{"start_at":null}`, `{"kind":"flu"}`, `{"source":"x"}`,
		`{"timezone":"Nowhere/City"}`, `{"start_at":"yesterday"}`, `{"label":3}`} {
		p = e.do("PATCH", base+"/"+created.ID.String(), e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, p.code, body)
		assert.Equal(t, "application/problem+json", p.header.Get("Content-Type"), body)
	}

	d := e.do("DELETE", base+"/"+created.ID.String(), e.keyA, nil)
	assert.Equal(t, http.StatusNoContent, d.code, string(d.body))
	for _, m := range []string{"GET", "DELETE"} {
		assert.Equal(t, http.StatusNotFound, e.do(m, base+"/"+created.ID.String(), e.keyA, nil).code, m)
	}
	assert.Equal(t, http.StatusNotFound, e.do("PATCH", base+"/"+created.ID.String(), e.keyA, `{"label":"x"}`).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/not-a-uuid", e.keyA, nil).code)
}

func TestTagValidation(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "tagged")
	base := "/v1/users/" + u.String() + "/tags"
	for body, want := range map[string]string{
		`{"start_at":"2026-04-10"}`:                                                             "kind is required",
		`{"kind":"flu","start_at":"2026-04-10"}`:                                                "kind must be one of",
		`{"kind":"illness"}`:                                                                    "start_at is required",
		`{"kind":"illness","start_at":"soon"}`:                                                  "start_at: not an RFC 3339 time",
		`{"kind":"illness","start_at":"2026-04-10","end_at":"2026-04-10"}`:                      "end_at must be after start_at",
		`{"kind":"illness","start_at":"2026-04-10","timezone":"Local"}`:                         "timezone must be an IANA",
		`{"kind":"illness","start_at":"2026-04-10","source":"a b"}`:                             "source must be",
		`{"kind":"illness","start_at":"2026-04-10","user_id":"x"}`:                              "unknown field",
		`{"kind":"illness","start_at":"2026-04-10","label":"` + strings.Repeat("x", 101) + `"}`: "label is at most 100",
	} {
		r := e.do("POST", base, e.keyA, body)
		require.Equal(t, http.StatusBadRequest, r.code, body)
		var pb problem
		r.json(t, &pb)
		assert.Contains(t, pb.Detail, want, body)
	}
	// A note never comes back in a refusal.
	r := e.do("POST", base, e.keyA, `{"kind":"illness","start_at":"2026-04-10","note":"`+strings.Repeat("n", 2001)+`"}`)
	require.Equal(t, http.StatusBadRequest, r.code)
	assert.NotContains(t, string(r.body), "nnnn")

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_data.time_tags`).Scan(&n))
	assert.Zero(t, n)
}

func TestTagsOverlapFilterAndPaging(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "tagged")
	base := "/v1/users/" + u.String() + "/tags"
	before := e.tag(u, `{"kind":"illness","start_at":"2026-03-01","end_at":"2026-03-05"}`)
	across := e.tag(u, `{"kind":"travel","start_at":"2026-03-08","end_at":"2026-03-12"}`)
	inside := e.tag(u, `{"kind":"alcohol","start_at":"2026-03-10T20:00:00Z","end_at":"2026-03-11T02:00:00Z"}`)
	edge := e.tag(u, `{"kind":"jet_lag","start_at":"2026-03-15"}`) // ongoing, starts at the window's end
	ongoing := e.tag(u, `{"kind":"medication","start_at":"2026-02-01"}`)

	ids := func(path string) []uuid.UUID {
		t.Helper()
		r := e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var pg page[tagJSON]
		r.json(t, &pg)
		out := []uuid.UUID{}
		for _, tg := range pg.Data {
			out = append(out, tg.ID)
		}
		return out
	}
	// By start time.
	assert.Equal(t, []uuid.UUID{ongoing.ID, before.ID, across.ID, inside.ID, edge.ID}, ids(base))
	// [2026-03-05, 2026-03-15): a tag ending at the start and one starting at
	// the end touch the window without overlapping it; the ongoing tag does.
	assert.Equal(t, []uuid.UUID{ongoing.ID, across.ID, inside.ID}, ids(base+"?start=2026-03-05&end=2026-03-15"))
	assert.Equal(t, []uuid.UUID{ongoing.ID, across.ID, inside.ID, edge.ID}, ids(base+"?start=2026-03-11T01:00:00Z"))
	assert.Equal(t, []uuid.UUID{ongoing.ID, before.ID}, ids(base+"?end=2026-03-08"))
	assert.Equal(t, []uuid.UUID{across.ID}, ids(base+"?kind=travel&start=2026-03-01"))
	assert.Empty(t, ids(base+"?start=2026-03-09&end=2026-03-09"), "an empty window: nothing overlaps it")

	// Paging, with a filter.
	var all []uuid.UUID
	path := base + "?start=2026-03-01&limit=2"
	for {
		r := e.do("GET", path, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var pg page[tagJSON]
		r.json(t, &pg)
		for _, tg := range pg.Data {
			all = append(all, tg.ID)
		}
		if pg.NextCursor == nil {
			break
		}
		require.Len(t, pg.Data, 2)
		path = base + "?start=2026-03-01&limit=2&cursor=" + *pg.NextCursor
	}
	assert.Equal(t, []uuid.UUID{ongoing.ID, before.ID, across.ID, inside.ID, edge.ID}, all)

	for _, q := range []string{"?kind=flu", "?limit=0", "?limit=1001", "?cursor=zzz", "?start=2026-03-02&end=2026-03-01", "?start=x"} {
		r := e.do("GET", base+q, e.keyA, nil)
		assert.Equal(t, http.StatusBadRequest, r.code, q)
	}
}

func TestTagsTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := e.user(e.tenantA, "a")
	b := e.user(e.tenantB, "b")
	tg := e.tag(a, `{"kind":"injury","start_at":"2026-03-01","note":"invented"}`)
	pathA := "/v1/users/" + a.String() + "/tags"

	// Tenant B with tenant A's user: the user does not exist.
	for _, m := range []string{"GET", "POST"} {
		r := e.do(m, pathA, e.keyB, `{"kind":"other","start_at":"2026-03-01"}`)
		assert.Equal(t, http.StatusNotFound, r.code, m)
		assert.Contains(t, string(r.body), "user not found")
	}
	for _, m := range []string{"GET", "PATCH", "DELETE"} {
		r := e.do(m, pathA+"/"+tg.ID.String(), e.keyB, `{"label":"x"}`)
		assert.Equal(t, http.StatusNotFound, r.code, m)
	}
	// Tenant B's own user with tenant A's tag id: the tag does not exist.
	pathB := "/v1/users/" + b.String() + "/tags/" + tg.ID.String()
	for _, m := range []string{"GET", "PATCH", "DELETE"} {
		r := e.do(m, pathB, e.keyB, `{"label":"x"}`)
		assert.Equal(t, http.StatusNotFound, r.code, m)
		assert.Contains(t, string(r.body), "tag not found", m)
	}
	r := e.do("GET", "/v1/users/"+b.String()+"/tags", e.keyB, nil)
	require.Equal(t, http.StatusOK, r.code)
	assert.JSONEq(t, `{"data":[],"next_cursor":null}`, string(r.body))

	// The tag is untouched.
	g := e.do("GET", pathA+"/"+tg.ID.String(), e.keyA, nil)
	require.Equal(t, http.StatusOK, g.code)
	var got tagJSON
	g.json(t, &got)
	assert.Equal(t, tg, got)

	// A user token cannot call the tenant routes.
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, a, []string{"read"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, e.do("GET", pathA, tok, nil).code)
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", pathA, "", nil).code)

	// A user being deleted has no tags to read or write.
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, a)
	assert.Equal(t, http.StatusNotFound, e.do("GET", pathA, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", pathA+"/"+tg.ID.String(), e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("POST", pathA, e.keyA, `{"kind":"other","start_at":"2026-03-01"}`).code)
	assert.Equal(t, http.StatusNotFound, e.do("DELETE", pathA+"/"+tg.ID.String(), e.keyA, nil).code)
}

func TestCreateTagIdempotency(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "tagged")
	base := "/v1/users/" + u.String() + "/tags"
	body := `{"kind":"vaccination","start_at":"2026-05-02T10:00:00Z","end_at":"2026-05-02T10:15:00Z"}`
	r1 := e.do("POST", base, e.keyA, body, "Idempotency-Key", "tag-1")
	require.Equal(t, http.StatusCreated, r1.code, string(r1.body))
	r2 := e.do("POST", base, e.keyA, body, "Idempotency-Key", "tag-1")
	require.Equal(t, http.StatusCreated, r2.code)
	assert.Equal(t, "true", r2.header.Get("Idempotent-Replayed"))
	assert.JSONEq(t, string(r1.body), string(r2.body))
	r3 := e.do("POST", base, e.keyA, `{"kind":"other","start_at":"2026-05-02"}`, "Idempotency-Key", "tag-1")
	assert.Equal(t, http.StatusUnprocessableEntity, r3.code)

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_data.time_tags WHERE user_id = $1`, u).Scan(&n))
	assert.Equal(t, 1, n, "the retry created nothing")
	var stored *uuid.UUID
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT user_id FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = 'tag-1'`, e.tenantA).Scan(&stored))
	require.NotNil(t, stored, "the stored response is deleted with the user")
	assert.Equal(t, u, *stored)
}

func TestTagWebhookEvents(t *testing.T) {
	e := webhookEnv(t, false)
	u := e.user(e.tenantA, "tagged")
	r := e.do("POST", "/v1/webhooks", e.keyA, map[string]any{"url": "https://app.example.com/hooks",
		"event_types": []string{webhook.TagCreated, webhook.TagUpdated, webhook.TagDeleted}})
	require.Equal(t, http.StatusCreated, r.code, string(r.body))

	tg := e.tag(u, `{"kind":"illness","start_at":"2026-03-01","note":"invented fever note"}`)
	path := fmt.Sprintf("/v1/users/%s/tags/%s", u, tg.ID)
	require.Equal(t, http.StatusOK, e.do("PATCH", path, e.keyA, `{"end_at":"2026-03-04"}`).code)
	require.Equal(t, http.StatusOK, e.do("PATCH", path, e.keyA, `{}`).code, "a patch that changes nothing sends nothing")
	require.Equal(t, http.StatusNoContent, e.do("DELETE", path, e.keyA, nil).code)

	rows, err := e.pool.Query(context.Background(), `SELECT event_type, payload FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 ORDER BY id`, e.tenantA)
	require.NoError(t, err)
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ string
		var payload []byte
		require.NoError(t, rows.Scan(&typ, &payload))
		types = append(types, typ)
		var p struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(payload, &p))
		assert.Equal(t, tg.ID.String(), p.Data["tag_id"])
		assert.Equal(t, u.String(), p.Data["user_id"])
		assert.Equal(t, "tagged", p.Data["external_ref"])
		assert.NotContains(t, string(payload), "fever", "an event never carries the tag's text")
		assert.NotContains(t, string(payload), "illness")
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{webhook.TagCreated, webhook.TagUpdated, webhook.TagDeleted}, types)
}
