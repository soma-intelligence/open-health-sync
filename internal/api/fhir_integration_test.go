// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/api/read"
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

// walk follows the next links from path and returns every Observation.
func (e *env) walk(path, key string) []fhir.Observation {
	e.t.Helper()
	var out []fhir.Observation
	for link := path; link != ""; {
		b := e.bundle(link, key)
		for _, en := range b.Entry {
			out = append(out, en.Resource)
		}
		if link = next(b); link != "" {
			require.True(e.t, strings.HasPrefix(link, publicURL+"/"), link)
			nu, err := url.Parse(link)
			require.NoError(e.t, err)
			link = nu.RequestURI()
		}
	}
	return out
}

func ids(obs []fhir.Observation) []string {
	out := make([]string, 0, len(obs))
	for _, o := range obs {
		out = append(out, o.ID)
	}
	return out
}

func idPrefixes(obs []fhir.Observation) []string {
	out := make([]string, 0, len(obs))
	for _, o := range obs {
		p, _, _ := strings.Cut(o.ID, "-")
		out = append(out, p)
	}
	return out
}

func TestFHIRObservations(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL + "/" })
	u := e.user(e.tenantA, "a-1")
	sleepID, workoutID := e.seed(e.tenantA, u)
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
	assert.Equal(t, &fhir.Reference{Reference: "Device/" + fhir.DeviceID(u, read.Device{Provider: "apple_health", Source: "apple", DeviceID: "watch-1"}), Display: "watch-1"}, o.Device)
	assert.NotEmpty(t, o.Issued)

	var values []float64
	for _, hr := range e.walk(base+"?type=heart_rate&limit=2", e.keyA) {
		values = append(values, *hr.ValueQuantity.Value)
	}
	assert.Equal(t, []float64{60, 61, 62, 63, 64}, values)

	// Every record, in one list ordered by time: the sleep session (bedtime
	// the evening before), the first day (midnight UTC), blood pressure
	// (07:00), the 08:00 heart rate and steps, the workout (08:00, after the
	// samples of the same time), the rest of the heart rate, lunch (12:00),
	// the second day.
	all := e.bundle(base, e.keyA)
	assert.Empty(t, next(all))
	var obs []fhir.Observation
	for _, en := range all.Entry {
		obs = append(obs, en.Resource)
	}
	assert.Equal(t, []string{"sleep", "daily", "qs", "qs", "qs", "workout", "qs", "qs", "qs", "qs", "nutrition", "daily"}, idPrefixes(obs))
	bp := obs[2]
	assert.Equal(t, []fhir.Coding{{System: fhir.QuantityTypeSystem, Code: "blood_pressure_systolic"}}, bp.Code.Coding,
		"an unpaired half: R4 allows 8480-6 only inside the blood pressure panel")
	assert.Equal(t, fhir.CategoryVitalSigns, bp.Category[0].Coding[0].Code)

	sl := obs[0]
	assert.Equal(t, "sleep-"+strconv.FormatInt(sleepID, 10), sl.ID)
	assert.Equal(t, fhir.LOINCSleepDuration, sl.Code.Coding[0].Code)
	assert.Equal(t, &fhir.Period{Start: "2026-02-28T22:00:00Z", End: "2026-03-01T08:00:00Z"}, sl.EffectivePeriod)
	assert.Equal(t, 25200.0, *sl.ValueQuantity.Value)
	assert.Equal(t, "s", sl.ValueQuantity.Code)

	wo := obs[5]
	assert.Equal(t, "workout-"+strconv.FormatInt(workoutID, 10), wo.ID)
	assert.Equal(t, fhir.LOINCExerciseDuration, wo.Code.Coding[0].Code)
	assert.Equal(t, "running workout", wo.Code.Text)
	assert.Equal(t, fhir.CategoryActivity, wo.Category[0].Coding[0].Code)
	assert.Equal(t, 1800.0, *wo.ValueQuantity.Value)
	require.Len(t, wo.Component, 2)
	assert.Equal(t, "running", wo.Component[0].ValueCodeableConcept.Coding[0].Code)
	assert.Equal(t, 5000.0, *wo.Component[1].ValueQuantity.Value)

	day := obs[1]
	assert.Equal(t, &fhir.Period{Start: "2026-03-01T00:00:00Z", End: "2026-03-02T00:00:00Z"}, day.EffectivePeriod,
		"no timezone metadata: the UTC day")
	require.Len(t, day.Component, 1)
	assert.Equal(t, "41950-7", day.Component[0].Code.Coding[0].Code)
	assert.Equal(t, 9000.0, *day.Component[0].ValueQuantity.Value)

	meal := obs[10]
	assert.Equal(t, []fhir.Coding{{System: fhir.RecordTypeSystem, Code: fhir.RecordNutritionLog}}, meal.Code.Coding)
	assert.Empty(t, meal.Category)
	assert.Equal(t, "2026-03-01T12:00:00Z", meal.EffectiveDateTime, "no end: an instant")
	require.Len(t, meal.Component, 2, "the meal type and the energy; a name the nutrient table does not know is left out")
	assert.Equal(t, "lunch", meal.Component[0].ValueCodeableConcept.Coding[0].Code)
	assert.Equal(t, []fhir.Coding{{System: fhir.LOINC, Code: "9052-2"}, {System: fhir.ComponentSystem, Code: "energy_kcal"}},
		meal.Component[1].Code.Coding)
	assert.Equal(t, 650.0, *meal.Component[1].ValueQuantity.Value)

	// Every page size gives the same list, each Observation once.
	for limit := 1; limit <= len(obs)+1; limit++ {
		assert.Equal(t, ids(obs), ids(e.walk(base+"?limit="+strconv.Itoa(limit), e.keyA)), "limit %d", limit)
	}

	// Each fullUrl reads the same Observation back.
	for _, en := range all.Entry {
		r := e.do("GET", strings.TrimPrefix(en.FullURL, publicURL), e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		assert.Equal(t, "application/fhir+json", r.header.Get("Content-Type"))
		var one fhir.Observation
		r.json(t, &one)
		assert.Equal(t, en.Resource, one, "a re-read gives the same resource and id")
	}

	// The code, category and type filters, alone and together.
	q := func(s string) string { return url.QueryEscape(s) }
	for query, want := range map[string]int{
		"?code=http://loinc.org%7C8867-4":                                    5,
		"?code=" + q(fhir.QuantityTypeSystem+"|step_count"):                  1,
		"?code=http://loinc.org%7C8480-6":                                    0, // no pair here
		"?code=http://loinc.org%7C0000-0":                                    0,
		"?code=http://loinc.org%7C93832-4":                                   1,
		"?code=http://loinc.org%7C55411-3":                                   1,
		"?code=" + q(fhir.RecordTypeSystem+"|daily_summary"):                 2,
		"?code=" + q(fhir.RecordTypeSystem+"|nutrition_log"):                 1,
		"?code=" + q(fhir.RecordTypeSystem+"|unknown"):                       0,
		"?category=activity":                                                 4, // steps, the workout, two days
		"?category=" + q(fhir.ObservationCategory+"|activity"):               4,
		"?category=vital-signs":                                              6,
		"?category=laboratory":                                               0,
		"?category=survey":                                                   0,
		"?type=heart_rate&category=activity":                                 0,
		"?type=heart_rate&code=http://loinc.org%7C8867-4":                    5,
		"?category=activity&code=http://loinc.org%7C55411-3":                 1,
		"?start=2026-03-01T09:00:00Z&end=2026-03-01T11:00:00Z":               2,
		"?start=2026-03-01T08:00:00Z&end=2026-03-01T08:00:01Z":               3,
		"?start=2026-03-02&category=activity":                                1,
		"?start=2026-03-01T12:00:00Z&end=2026-03-01T13:00:00Z":               2, // heart rate and lunch
		"?type=heart_rate&code=" + q(fhir.RecordTypeSystem+"|nutrition_log"): 0,
	} {
		assert.Len(t, e.walk(base+query, e.keyA), want, query)
	}

	for path, want := range map[string]int{
		base + "?type=heartrate":                        http.StatusBadRequest,
		base + "?code=8867-4":                           http.StatusBadRequest,
		base + "?category=vitals":                       http.StatusBadRequest,
		base + "?cursor=bm9wZQ":                         http.StatusBadRequest,
		base + "/qs-999999999":                          http.StatusNotFound,
		base + "/sleep-999999999":                       http.StatusNotFound,
		base + "/nutrition-999999999":                   http.StatusNotFound,
		base + "/bp-" + strings.TrimPrefix(o.ID, "qs-"): http.StatusNotFound, // heart rate is no panel
		base + "/" + strings.TrimPrefix(o.ID, "qs-"):    http.StatusNotFound,
	} {
		r := e.do("GET", path, e.keyA, nil)
		assert.Equal(t, want, r.code, "%s: %s", path, r.body)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"), path)
	}
}

// bpSample inserts one blood pressure half and returns its id.
func (e *env) bpSample(tn, u uuid.UUID, provider, typ string, v float64, start time.Time, end *time.Time, source, device *string) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, source_slug, device_id, quantity_type, value, unit, start_date, end_date, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'mmHg', $8, $9, $10) RETURNING id`,
		tn, u, provider, source, device, typ, v, start, end, uuid.NewString()).Scan(&id))
	return id
}

func TestFHIRBloodPressurePanels(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	const sys, dia = "blood_pressure_systolic", "blood_pressure_diastolic"
	t0 := time.Date(2026, 3, 5, 7, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	str := func(s string) *string { return &s }
	tp := func(t time.Time) *time.Time { return &t }
	src, cuff := str("withings"), str("cuff-1")

	// 07:00 a pair, and a second diastolic reading with the same key that
	// stays single (the lowest id of each type pairs).
	pairSys := e.bpSample(e.tenantA, u, "withings", sys, 120, at(0), nil, src, cuff)
	pairDia := e.bpSample(e.tenantA, u, "withings", dia, 80, at(0), nil, src, cuff)
	extraDia := e.bpSample(e.tenantA, u, "withings", dia, 82, at(0), nil, src, cuff)
	// 08:00 another device, 09:00 another end, 10:00 another provider: no pair.
	otherDeviceSys := e.bpSample(e.tenantA, u, "withings", sys, 130, at(1), nil, src, cuff)
	e.bpSample(e.tenantA, u, "withings", dia, 85, at(1), nil, src, str("cuff-2"))
	e.bpSample(e.tenantA, u, "withings", sys, 125, at(2), tp(at(2).Add(time.Minute)), src, cuff)
	e.bpSample(e.tenantA, u, "withings", dia, 79, at(2), nil, src, cuff)
	e.bpSample(e.tenantA, u, "apple_health", sys, 140, at(3), nil, src, cuff)
	e.bpSample(e.tenantA, u, "withings", dia, 90, at(3), nil, src, cuff)
	// 11:00 a pair with unknown source and device on both halves.
	nullSys := e.bpSample(e.tenantA, u, "withings", sys, 110, at(4), nil, nil, nil)
	e.bpSample(e.tenantA, u, "withings", dia, 70, at(4), nil, nil, nil)
	// Tenant B's user with the same times: never paired with tenant A's.
	ub := e.user(e.tenantB, "b-1")
	e.bpSample(e.tenantB, ub, "withings", dia, 60, at(4), nil, nil, nil)

	base := "/v1/users/" + u.String() + "/fhir/Observation"
	panelID := "bp-" + strconv.FormatInt(pairSys, 10)
	nullID := "bp-" + strconv.FormatInt(nullSys, 10)

	for _, code := range []string{"85354-9", "8480-6", "8462-4"} {
		panels := e.walk(base+"?code=http://loinc.org%7C"+code, e.keyA)
		assert.Equal(t, []string{panelID, nullID}, ids(panels), code)
	}
	panels := e.walk(base+"?code="+url.QueryEscape(fhir.RecordTypeSystem+"|blood_pressure"), e.keyA)
	require.Len(t, panels, 2)
	p := panels[0]
	assert.Equal(t, []string{"http://hl7.org/fhir/StructureDefinition/vitalsigns", "http://hl7.org/fhir/StructureDefinition/bp"}, p.Meta.Profile)
	assert.Nil(t, p.ValueQuantity)
	require.Len(t, p.Component, 2)
	assert.Equal(t, 120.0, *p.Component[0].ValueQuantity.Value)
	assert.Equal(t, 80.0, *p.Component[1].ValueQuantity.Value)
	assert.Equal(t, &fhir.Reference{Reference: "Device/" + fhir.DeviceID(u, read.Device{Provider: "withings", Source: "withings", DeviceID: "cuff-1"}), Display: "cuff-1"}, p.Device)
	assert.Equal(t, 70.0, *panels[1].Component[1].ValueQuantity.Value)

	// Everything: 12 samples of tenant A less the two absorbed halves.
	all := e.walk(base, e.keyA)
	require.Len(t, all, 9)
	assert.Equal(t, panelID, all[0].ID)
	assert.Equal(t, "qs-"+strconv.FormatInt(extraDia, 10), all[1].ID)
	assert.NotContains(t, ids(all), "qs-"+strconv.FormatInt(pairDia, 10))
	for limit := 1; limit <= 10; limit++ {
		assert.Equal(t, ids(all), ids(e.walk(base+"?limit="+strconv.Itoa(limit), e.keyA)), "limit %d", limit)
	}

	// A type keeps the panels that hold a half of it.
	assert.Len(t, e.walk(base+"?type="+sys, e.keyA), 5)
	assert.Len(t, e.walk(base+"?type="+dia, e.keyA), 6)
	assert.Len(t, e.walk(base+"?category=vital-signs", e.keyA), 9)
	assert.Len(t, e.walk(base+"?type="+dia+"&code=http://loinc.org%7C8480-6", e.keyA), 2)
	assert.Empty(t, e.walk(base+"?category=activity&code=http://loinc.org%7C85354-9", e.keyA))
	assert.Len(t, e.walk(base+"?start=2026-03-05T07:00:00Z&end=2026-03-05T08:00:00Z", e.keyA), 2)

	// Reads: the panel by its id; its halves are no Observations of their
	// own, and a single sample is no panel.
	for path, want := range map[string]int{
		base + "/" + panelID:                                  http.StatusOK,
		base + "/qs-" + strconv.FormatInt(pairSys, 10):        http.StatusNotFound,
		base + "/qs-" + strconv.FormatInt(pairDia, 10):        http.StatusNotFound,
		base + "/bp-" + strconv.FormatInt(pairDia, 10):        http.StatusNotFound,
		base + "/bp-" + strconv.FormatInt(otherDeviceSys, 10): http.StatusNotFound,
		base + "/qs-" + strconv.FormatInt(otherDeviceSys, 10): http.StatusOK,
		base + "/qs-" + strconv.FormatInt(extraDia, 10):       http.StatusOK,
	} {
		assert.Equal(t, want, e.do("GET", path, e.keyA, nil).code, path)
	}
	r := e.do("GET", base+"/"+panelID, e.keyA, nil)
	var one fhir.Observation
	r.json(t, &one)
	assert.Equal(t, p, one)
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"/"+panelID, e.keyB, nil).code)
	assert.Len(t, e.walk("/v1/users/"+ub.String()+"/fhir/Observation", e.keyB), 1)
}

func TestFHIRTenantIsolation(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	sleepID, _ := e.seed(e.tenantA, u)
	base := "/v1/users/" + u.String() + "/fhir/Observation"
	b := e.bundle(base+"?type=heart_rate&limit=1", e.keyA)
	require.Len(t, b.Entry, 1)
	id := b.Entry[0].Resource.ID
	sleep := "sleep-" + strconv.FormatInt(sleepID, 10)

	// Another tenant: the user and the Observations are absent.
	for _, p := range []string{base, base + "/" + id, base + "/" + sleep, base + "?code=http://loinc.org%7C8480-6"} {
		r := e.do("GET", p, e.keyB, nil)
		assert.Equal(t, http.StatusNotFound, r.code, p)
		assert.NotContains(t, string(r.body), "heart_rate")
	}
	// Tenant B's own user cannot reach tenant A's rows by id.
	ub := e.user(e.tenantB, "b-1")
	for _, oid := range []string{id, sleep} {
		r := e.do("GET", "/v1/users/"+ub.String()+"/fhir/Observation/"+oid, e.keyB, nil)
		assert.Equal(t, http.StatusNotFound, r.code)
	}
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
