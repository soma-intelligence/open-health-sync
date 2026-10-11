// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
)

const publicURL = "https://ohs.test"

func (e *env) bundle(path, key string) fhir.Bundle {
	e.t.Helper()
	r := e.do("GET", path, key, nil)
	require.Equal(e.t, http.StatusOK, r.code, "%s: %s", path, r.body)
	assert.Equal(e.t, "application/fhir+json", r.header.Get("Content-Type"))
	var b fhir.Bundle
	r.json(e.t, &b)
	assert.Equal(e.t, "Bundle", b.ResourceType)
	assert.Equal(e.t, "searchset", b.Type)
	require.NotEmpty(e.t, b.Link)
	assert.Equal(e.t, "self", b.Link[0].Relation)
	assert.Equal(e.t, publicURL+path, b.Link[0].URL)
	return b
}

func next(b fhir.Bundle) string {
	for _, l := range b.Link {
		if l.Relation == "next" {
			return l.URL
		}
	}
	return ""
}

func TestFHIRObservations(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL + "/" })
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'withings', 'blood_pressure_systolic', 118, 'mmHg', '2026-03-01T07:00:00Z', 'bp-1')`, e.tenantA, u)
	base := "/v1/users/" + u.String() + "/fhir/Observation"

	// One page of heart rate, then the rest through the next links.
	b := e.bundle(base+"?type=heart_rate&limit=2", e.keyA)
	require.Len(t, b.Entry, 2)
	o := b.Entry[0].Resource
	assert.True(t, strings.HasPrefix(o.ID, fhir.ObservationIDPrefix), o.ID)
	assert.Equal(t, publicURL+base+"/"+o.ID, b.Entry[0].FullURL)
	assert.Equal(t, "final", o.Status)
	assert.Equal(t, "Patient/"+u.String(), o.Subject.Reference)
	assert.Equal(t, "2026-03-01T08:00:00Z", o.EffectiveDateTime)
	assert.Contains(t, o.Code.Coding, fhir.Coding{System: fhir.LOINC, Code: "8867-4"}, "the catalog's LOINC code")
	assert.Contains(t, o.Code.Coding, fhir.Coding{System: fhir.QuantityTypeSystem, Code: "heart_rate"})
	assert.Contains(t, o.Meta.Profile, "http://hl7.org/fhir/StructureDefinition/heartrate")
	assert.Equal(t, fhir.CategoryVitalSigns, o.Category[0].Coding[0].Code)
	require.NotNil(t, o.ValueQuantity)
	assert.Equal(t, 60.0, *o.ValueQuantity.Value)
	assert.Equal(t, "/min", o.ValueQuantity.Code)
	assert.Equal(t, fhir.Namespace+"/provider/apple_health", o.Meta.Source)
	assert.Equal(t, []fhir.Coding{{System: fhir.SourceSystem, Code: "apple"}}, o.Meta.Tag)
	assert.Equal(t, &fhir.Reference{Display: "watch-1"}, o.Device)
	assert.NotEmpty(t, o.Issued)

	values := []float64{*b.Entry[0].Resource.ValueQuantity.Value, *b.Entry[1].Resource.ValueQuantity.Value}
	for link := next(b); link != ""; {
		require.True(t, strings.HasPrefix(link, publicURL+"/"), link)
		nu, err := url.Parse(link)
		require.NoError(t, err)
		pg := e.bundle(nu.RequestURI(), e.keyA)
		for _, en := range pg.Entry {
			values = append(values, *en.Resource.ValueQuantity.Value)
		}
		link = next(pg)
	}
	assert.Equal(t, []float64{60, 61, 62, 63, 64}, values)

	// The fullUrl reads the same Observation back.
	r := e.do("GET", strings.TrimPrefix(b.Entry[0].FullURL, publicURL), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "application/fhir+json", r.header.Get("Content-Type"))
	var one fhir.Observation
	r.json(t, &one)
	assert.Equal(t, o, one, "a re-read gives the same resource and id")

	// Every type, in time order: blood pressure (07:00), then the 08:00 ones.
	all := e.bundle(base, e.keyA)
	require.Len(t, all.Entry, 7)
	assert.Empty(t, next(all))
	bp := all.Entry[0].Resource
	assert.Equal(t, []fhir.Coding{{System: fhir.QuantityTypeSystem, Code: "blood_pressure_systolic"}}, bp.Code.Coding,
		"R4 allows 8480-6 only inside the blood pressure panel")
	assert.Equal(t, fhir.CategoryVitalSigns, bp.Category[0].Coding[0].Code)

	// The code token.
	assert.Len(t, e.bundle(base+"?code=http://loinc.org%7C8867-4", e.keyA).Entry, 5)
	assert.Len(t, e.bundle(base+"?code="+url.QueryEscape(fhir.QuantityTypeSystem+"|step_count"), e.keyA).Entry, 1)
	assert.Empty(t, e.bundle(base+"?code=http://loinc.org%7C8480-6", e.keyA).Entry)
	assert.Empty(t, e.bundle(base+"?code=http://loinc.org%7C0000-0", e.keyA).Entry)
	assert.Len(t, e.bundle(base+"?start=2026-03-01T09:00:00Z&end=2026-03-01T11:00:00Z", e.keyA).Entry, 2)

	for path, want := range map[string]int{
		base + "?type=heartrate":                     http.StatusBadRequest,
		base + "?code=8867-4":                        http.StatusBadRequest,
		base + "?cursor=bm9wZQ":                      http.StatusBadRequest,
		base + "/qs-999999999":                       http.StatusNotFound,
		base + "/" + strings.TrimPrefix(o.ID, "qs-"): http.StatusNotFound,
	} {
		r := e.do("GET", path, e.keyA, nil)
		assert.Equal(t, want, r.code, "%s: %s", path, r.body)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"), path)
	}
}

func TestFHIRTenantIsolation(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	base := "/v1/users/" + u.String() + "/fhir/Observation"
	b := e.bundle(base+"?type=heart_rate&limit=1", e.keyA)
	require.Len(t, b.Entry, 1)
	id := b.Entry[0].Resource.ID

	// Another tenant: the user and the Observation are absent.
	for _, p := range []string{base, base + "/" + id, base + "?code=http://loinc.org%7C8480-6"} {
		r := e.do("GET", p, e.keyB, nil)
		assert.Equal(t, http.StatusNotFound, r.code, p)
		assert.NotContains(t, string(r.body), "heart_rate")
	}
	// Tenant B's own user cannot reach tenant A's row by id.
	ub := e.user(e.tenantB, "b-1")
	r := e.do("GET", "/v1/users/"+ub.String()+"/fhir/Observation/"+id, e.keyB, nil)
	assert.Equal(t, http.StatusNotFound, r.code)
	assert.Empty(t, e.bundle("/v1/users/"+ub.String()+"/fhir/Observation", e.keyB).Entry)

	// A user token is not a tenant key.
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, u, []string{"read"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, e.do("GET", base, tok, nil).code)

	// A user being deleted reads as absent.
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/"+id, e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"?code=http://loinc.org%7C8480-6", e.keyA, nil).code)
}
