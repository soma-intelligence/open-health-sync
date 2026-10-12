// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// tenantRouter is the router over a pool that never connects, with every
// bearer secret accepted as a tenant key: enough for the refusals answered
// before any query.
func tenantRouter(t *testing.T) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	lookup := func(context.Context, string) (auth.Principal, error) {
		return auth.Principal{TenantID: uuid.MustParse("00000000-0000-4000-8000-000000000001")}, nil
	}
	return NewRouter(Deps{Pool: pool, Store: tenant.New(pool), Lookup: lookup})
}

func TestFHIRRefusalsBeforeAnyQuery(t *testing.T) {
	h := tenantRouter(t)
	base := "/v1/users/3f0c2f8e-8b8a-4c55-9f5e-2b0f3a1d9c11/fhir/Observation"
	for path, want := range map[string]int{
		base + "?code=8867-4":                                               http.StatusBadRequest,
		base + "?code=http://snomed.info/sct|364075005":                     http.StatusBadRequest,
		base + "?code=http://loinc.org|8867-4,9279-1":                       http.StatusBadRequest,
		base + "?code=http://loinc.org|8867-4&code=http://loinc.org|9279-1": http.StatusBadRequest,
		base + "?code=http://loinc.org%7C8480-6&cursor=bm9wZQ":              http.StatusBadRequest,
		base + "?limit=5000":                                                http.StatusBadRequest,
		base + "?start=yesterday":                                           http.StatusBadRequest,
		base + "?category=vitals":                                           http.StatusBadRequest,
		base + "?category=http://snomed.info/sct|activity":                  http.StatusBadRequest,
		base + "?cursor=MjAyNi0wMy0wMVQwODowMDowMFp8OXwx":                   http.StatusBadRequest,
		base + "/bp-x":                          http.StatusNotFound,
		base + "/sleep-0":                       http.StatusNotFound,
		base + "/workout--1":                    http.StatusNotFound,
		base + "/daily-":                        http.StatusNotFound,
		base + "/qs-x":                          http.StatusNotFound,
		base + "/1":                             http.StatusNotFound,
		"/v1/users/not-a-uuid/fhir/Observation": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+auth.PrefixSecretKey+"invented")
		h.ServeHTTP(rec, req)
		assert.Equal(t, want, rec.Code, "%s: %s", path, rec.Body)
		assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"), path)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", base, nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", base, nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the tenant key is required")
}

func TestParseFHIRSearch(t *testing.T) {
	cases := []struct {
		query string
		want  fhirSearch
		bad   bool
	}{
		{query: ""},
		{query: "code=http://loinc.org|8867-4", want: fhirSearch{codeSystem: fhir.LOINC, code: "8867-4"}},
		{query: "code=http://loinc.org|85354-9", want: fhirSearch{codeSystem: fhir.LOINC, code: "85354-9"}},
		{query: "code=" + fhir.QuantityTypeSystem + "|heart_rate", want: fhirSearch{codeSystem: fhir.QuantityTypeSystem, code: "heart_rate"}},
		{query: "code=" + fhir.RecordTypeSystem + "|workout", want: fhirSearch{codeSystem: fhir.RecordTypeSystem, code: "workout"}},
		{query: "category=vital-signs", want: fhirSearch{category: "vital-signs"}},
		{query: "category=" + fhir.ObservationCategory + "|activity", want: fhirSearch{category: "activity"}},
		{query: "category=|laboratory", want: fhirSearch{category: "laboratory"}},
		{query: "category=survey", want: fhirSearch{category: "survey"}},
		{query: "code=8867-4", bad: true},
		{query: "code=", bad: true},
		{query: "code=|8867-4", bad: true},
		{query: "code=http://loinc.org|", bad: true},
		{query: "code=http://example.org/x|1", bad: true},
		{query: "code=a|1&code=b|2", bad: true},
		{query: "category=vitals", bad: true},
		{query: "category=", bad: true},
		{query: "category=http://example.org/c|activity", bad: true},
		{query: "category=activity,laboratory", bad: true},
	}
	for _, c := range cases {
		qs, err := url.ParseQuery(c.query)
		require.NoError(t, err)
		got, msg := parseFHIRSearch(qs)
		if c.bad {
			assert.NotEmpty(t, msg, c.query)
			continue
		}
		assert.Empty(t, msg, c.query)
		assert.Equal(t, c.want, got, c.query)
	}
}

func TestPublicBase(t *testing.T) {
	s := &server{}
	r := httptest.NewRequest("GET", "/v1/users", nil)
	r.Host = "ohs.internal:8080"
	assert.Equal(t, "http://ohs.internal:8080", s.publicBase(r))
	r.TLS = &tls.ConnectionState{}
	assert.Equal(t, "https://ohs.internal:8080", s.publicBase(r))
	s.d.PublicURL = "https://health.example.net/"
	assert.Equal(t, "https://health.example.net", s.publicBase(r), "OHS_PUBLIC_URL wins, without its trailing slash")
}

func TestWriteFHIRKeepsAmpersands(t *testing.T) {
	rec := httptest.NewRecorder()
	writeFHIR(rec, fhir.SearchSet("http://h/fhir", "http://h/fhir/Observation?a=1&b=2", "", nil))
	assert.Equal(t, fhirJSON, rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"url":"http://h/fhir/Observation?a=1&b=2"`)
	var b map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
	assert.Equal(t, "searchset", b["type"])
	assert.NotContains(t, b, "entry", "an empty page has no entry array")
}
