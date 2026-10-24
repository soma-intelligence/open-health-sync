// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/providerset"
)

// problemOf decodes the problem details a helper wrote.
func problemOf(t *testing.T, w *httptest.ResponseRecorder) (status int, detail string) {
	t.Helper()
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	var p struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
	assert.Equal(t, w.Code, p.Status)
	return p.Status, p.Detail
}

func TestNewPageEnvelope(t *testing.T) {
	b, err := json.Marshal(newPage[int](nil, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":[],"next_cursor":null}`, string(b), "an empty last page")

	b, err = json.Marshal(newPage([]int{1, 2}, "c2"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":[1,2],"next_cursor":"c2"}`, string(b))
}

func TestParseLimitBounds(t *testing.T) {
	for q, want := range map[string]int{"": 0, "limit=1": 1, "limit=1000": read.MaxLimit} {
		n, err := parseLimit(httptest.NewRequest("GET", "/?"+q, nil))
		require.NoError(t, err, q)
		assert.Equal(t, want, n, q)
	}
	for _, q := range []string{"limit=0", "limit=-3", "limit=1001", "limit=ten", "limit=2.5"} {
		_, err := parseLimit(httptest.NewRequest("GET", "/?"+q, nil))
		assert.EqualError(t, err, "limit must be an integer between 1 and 1000", q)
	}
}

func TestParseTimeInReadsDatesInTheZone(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	got, err := parseTimeIn("2026-10-20", paris)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC), got.UTC(), "midnight in Paris, summer time")

	got, err = parseTimeIn("2026-10-20T08:00:00+02:00", paris)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC), got, "a full time ignores the zone")

	_, err = parseTimeIn("20/10/2026", paris)
	assert.Error(t, err)
}

func TestParseReadQuery(t *testing.T) {
	q, err := parseReadQuery(httptest.NewRequest("GET",
		"/?type=heart_rate&start=2026-10-01&end=2026-10-02T12:00:00Z&cursor=abc&limit=50&view=all", nil))
	require.NoError(t, err)
	assert.Equal(t, read.Query{
		Type: "heart_rate", Cursor: "abc", Limit: 50, View: read.ViewAll,
		Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}, q)

	q, err = parseReadQuery(httptest.NewRequest("GET", "/", nil))
	require.NoError(t, err)
	assert.Equal(t, read.ViewMerged, q.View, "merged is the default view")
	assert.True(t, q.Start.IsZero() && q.End.IsZero())

	for query, want := range map[string]string{
		"view=both":       "view must be merged or all",
		"limit=0":         "limit must be an integer",
		"start=yesterday": "start: not an RFC 3339 time",
		"end=2026-13-01":  "end: not an RFC 3339 time",
	} {
		_, err := parseReadQuery(httptest.NewRequest("GET", "/?"+query, nil))
		require.Error(t, err, query)
		assert.Contains(t, err.Error(), want, query)
	}
}

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	decode := func(raw string, allowEmpty bool) (body, bool, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		var v body
		ok := decodeJSON(w, httptest.NewRequest("POST", "/", strings.NewReader(raw)), &v, allowEmpty)
		return v, ok, w
	}

	v, ok, _ := decode(`{"name":"a","count":2}`, false)
	assert.True(t, ok)
	assert.Equal(t, body{Name: "a", Count: 2}, v)

	_, ok, w := decode(``, true)
	assert.True(t, ok, "an empty body is allowed when asked")
	assert.Equal(t, 0, w.Body.Len(), "and writes nothing")

	for _, tc := range []struct{ raw, detail string }{
		{``, "unexpected end of input"},
		{`{"name":`, "unexpected end of input"},
		{`{"count":"two"}`, "field count has the wrong type"},
		{`{"other":1}`, `unknown field "other"`},
		{`{"name":"a"} {"name":"b"}`, "more than one JSON value"},
		{`{"name":"a\u0000b"}`, "NUL character"},
		{`{"count":1,"name":"\u0000"}`, `field "name" holds a NUL character`},
	} {
		_, ok, w := decode(tc.raw, false)
		assert.False(t, ok, tc.raw)
		status, detail := problemOf(t, w)
		assert.Equal(t, http.StatusBadRequest, status, tc.raw)
		assert.Contains(t, detail, tc.detail, tc.raw)
	}

	v, ok, _ = decode(`{"name":"a\\u0000b"}`, false)
	assert.True(t, ok, "an escaped backslash before u0000 is not a NUL")
	assert.Equal(t, `a\u0000b`, v.Name)

	_, ok, w = decode(`{"name":"`+strings.Repeat("x", maxJSONBody)+`"}`, false)
	assert.False(t, ok)
	status, detail := problemOf(t, w)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status)
	assert.Equal(t, "the request body is too large", detail)
}

func TestPathTenantID(t *testing.T) {
	id := uuid.New()
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("tenant_id", id.String())
	got, ok := pathTenantID(httptest.NewRecorder(), r)
	assert.True(t, ok)
	assert.Equal(t, id, got)

	r.SetPathValue("tenant_id", "team-blue")
	w := httptest.NewRecorder()
	_, ok = pathTenantID(w, r)
	assert.False(t, ok)
	status, detail := problemOf(t, w)
	assert.Equal(t, http.StatusNotFound, status, "a malformed id reads as an unknown tenant")
	assert.Equal(t, "tenant not found", detail)
}

func TestClientIDHintNeverShowsTheWholeID(t *testing.T) {
	assert.Nil(t, clientIDHint(""))
	for id, want := range map[string]string{
		"x":                    "x...",
		"ab12":                 "ab...",
		"123456789012":         "12...",
		"1234567890123":        "1234...0123",
		"8f2c4e6a0b1d3f5791ab": "8f2c...91ab",
		"clé-événement-àéîõü":  "clé-...éîõü", // runes, not bytes
	} {
		got := clientIDHint(id)
		require.NotNil(t, got, id)
		assert.Equal(t, want, *got, id)
		assert.NotEqual(t, id, *got, id)
	}
}

func TestCheckAdminTenantName(t *testing.T) {
	for name, want := range map[string]string{
		"Team Blue":                             "",
		strings.Repeat("é", maxAdminTenantName): "",
		"":                                      "name is required",
		strings.Repeat("a", maxAdminTenantName+1): "name is at most 100 characters",
		"Team\tBlue":     "name must not contain control characters",
		"Team\u0000Blue": "name must not contain control characters",
	} {
		assert.Equal(t, want, checkAdminTenantName(name), "%q", name)
	}
}

func TestProviderErrStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		detail string
	}{
		{providerset.ErrUnknown, http.StatusNotFound, "provider not found"},
		{fmt.Errorf("save: %w", providerset.ErrBuiltIn), http.StatusConflict, "needs no key"},
		{providerset.ErrEnvKeys, http.StatusConflict, "OHS_ environment variables"},
		{&providerset.FieldError{Msg: "unknown key REGION"}, http.StatusBadRequest, "unknown key REGION"},
		{&providerset.RefusedError{Msg: "CLIENT_ID is not a UUID"}, http.StatusUnprocessableEntity, "CLIENT_ID is not a UUID"},
		{errors.New("connection refused"), http.StatusInternalServerError, "an internal error occurred"},
	} {
		w := httptest.NewRecorder()
		assert.False(t, providerErr(w, httptest.NewRequest("PATCH", "/", nil), tc.err), "%v", tc.err)
		status, detail := problemOf(t, w)
		assert.Equal(t, tc.status, status, "%v", tc.err)
		assert.Contains(t, detail, tc.detail, "%v", tc.err)
		assert.NotContains(t, detail, "connection refused", "the cause of a 500 is never sent")
	}

	w := httptest.NewRecorder()
	assert.True(t, providerErr(w, httptest.NewRequest("PATCH", "/", nil), nil))
	assert.Equal(t, 0, w.Body.Len(), "no error writes nothing")
}

func TestNULFreeNamesTheMember(t *testing.T) {
	for raw, want := range map[string]string{
		`{"a":"x\u0000"}`:                           "a",
		`{"a":{"b":1},"c":["ok","\u0000"]}`:         "c",
		`{"a":[{"b":"\u0000"}]}`:                    "b",
		`{"a\u0000":1}`:                             "a",
		`["\u0000"]`:                                "",
		`{"a":{"b":{"c":2}},"d":null,"e":"\u0000"}`: "e",
	} {
		name, ok := nulFree([]byte(raw))
		assert.False(t, ok, raw)
		assert.Equal(t, want, name, raw)
	}
	for _, raw := range []string{`{"a":"x"}`, `{"a":"\\u0000"}`, `{"u0000":1}`} {
		_, ok := nulFree([]byte(raw))
		assert.True(t, ok, raw)
	}
}
