// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/fhir"
)

// ecgT0 is the start of the first invented recording.
var ecgT0 = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

// ecgVolt is the invented voltage of sample i, in microvolts.
func ecgVolt(i int) float64 { return float64(i%50)*1.5 - 20 }

// seedECG inserts one invented recording of n samples at hz, stored as the
// writer stores them (sample time = start + offset), and returns its id.
func (e *env) seedECG(tn, u uuid.UUID, provider string, start time.Time, class, symptoms int, hz float64, n int) int64 {
	e.t.Helper()
	ctx := context.Background()
	var id int64
	end := start.Add(time.Duration(float64(n) / hz * float64(time.Second)))
	require.NoError(e.t, e.pool.QueryRow(ctx, `INSERT INTO ohs_data.ecg_recordings
		(tenant_id, user_id, provider, source_slug, start_date, end_date, classification, symptoms_status,
		 average_heart_rate, sampling_frequency, number_of_measurements, content_hash, metadata)
		VALUES ($1, $2, $3, $3, $4, $5, $6, $7, 64, $8, $9, $10, '{"timezone":"Europe/Paris"}') RETURNING id`,
		tn, u, provider, start, end, class, symptoms, hz, n, fmt.Sprintf("ecg-%s-%d", provider, start.Unix())).Scan(&id))
	// Inserted in reverse to check the read orders them.
	e.exec(`INSERT INTO ohs_data.ecg_samples
		(tenant_id, user_id, ecg_id, ts, time_since_sample_start, voltage_uv, content_hash)
		SELECT $1, $2, $3::bigint, $4::timestamptz + make_interval(secs => g / $5::float8), g / $5::float8,
			(g % 50) * 1.5 - 20, 'ecg-' || $3::text || '-' || g::text
		FROM generate_series($6::int - 1, 0, -1) AS g`, tn, u, id, start, hz, n)
	return id
}

type ecgJSON struct {
	ID                   int64            `json:"id"`
	StartDate            time.Time        `json:"start_date"`
	EndDate              *time.Time       `json:"end_date"`
	Classification       *string          `json:"classification"`
	ClassificationValue  int              `json:"classification_value"`
	SymptomsStatus       *string          `json:"symptoms_status"`
	AverageHeartRate     *float64         `json:"average_heart_rate"`
	SamplingFrequencyHz  float64          `json:"sampling_frequency_hz"`
	NumberOfMeasurements int              `json:"number_of_measurements"`
	Metadata             json.RawMessage  `json:"metadata"`
	Provider             string           `json:"provider"`
	Source               *string          `json:"source"`
	Samples              *read.ECGSamples `json:"samples"`
}

func TestECGRead(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	first := e.seedECG(e.tenantA, u, "apple_health", ecgT0, 1, 1, 512, 300)
	second := e.seedECG(e.tenantA, u, "apple_health", ecgT0.Add(24*time.Hour), 9, 2, 512, 40)
	third := e.seedECG(e.tenantA, u, "withings", ecgT0.Add(48*time.Hour), 0, 0, 300, 25)
	// The same tenant's other user, and another tenant's user.
	sibling := e.user(e.tenantA, "a-2")
	theirs := e.seedECG(e.tenantA, sibling, "apple_health", ecgT0, 2, 0, 512, 10)
	ub := e.user(e.tenantB, "b-1")
	e.seedECG(e.tenantB, ub, "apple_health", ecgT0, 2, 0, 512, 10)
	base := "/v1/users/" + u.String() + "/ecg"

	// The list: metadata only, by start date.
	r := e.do("GET", base, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.NotContains(t, string(r.body), "voltages_uv", "no samples in the list")
	var pg page[ecgJSON]
	r.json(t, &pg)
	require.Len(t, pg.Data, 3)
	assert.Nil(t, pg.NextCursor)
	assert.Equal(t, []int64{first, second, third}, []int64{pg.Data[0].ID, pg.Data[1].ID, pg.Data[2].ID})
	a := pg.Data[0]
	assert.Equal(t, ecgT0, a.StartDate)
	assert.Equal(t, "sinus_rhythm", *a.Classification)
	assert.Equal(t, 1, a.ClassificationValue)
	assert.Equal(t, "none", *a.SymptomsStatus)
	assert.Equal(t, 64.0, *a.AverageHeartRate)
	assert.Equal(t, 512.0, a.SamplingFrequencyHz)
	assert.Equal(t, 300, a.NumberOfMeasurements)
	assert.JSONEq(t, `{"timezone":"Europe/Paris"}`, string(a.Metadata))
	assert.Equal(t, "apple_health", a.Provider)
	assert.Nil(t, a.Samples)
	assert.Nil(t, pg.Data[1].Classification, "a value outside HealthKit's has no label")
	assert.Equal(t, 9, pg.Data[1].ClassificationValue)
	assert.Equal(t, "present", *pg.Data[1].SymptomsStatus)
	assert.Equal(t, "negative", *pg.Data[2].Classification, "the Withings vocabulary")

	// Paging and the window.
	assert.Equal(t, []int64{first, second, third}, e.collectIDs(base+"?"))
	var listed []int64
	for link := base + "?limit=1"; link != ""; {
		var p page[ecgJSON]
		e.do("GET", link, e.keyA, nil).json(t, &p)
		for _, d := range p.Data {
			listed = append(listed, d.ID)
		}
		link = ""
		if p.NextCursor != nil {
			link = base + "?limit=1&cursor=" + url.QueryEscape(*p.NextCursor)
		}
	}
	assert.Equal(t, []int64{first, second, third}, listed)
	pg = page[ecgJSON]{}
	e.do("GET", base+"?start=2026-03-02&end=2026-03-03", e.keyA, nil).json(t, &pg)
	require.Len(t, pg.Data, 1)
	assert.Equal(t, second, pg.Data[0].ID)

	// One recording with its samples, in offset order.
	r = e.do("GET", fmt.Sprintf("%s/%d", base, first), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var one ecgJSON
	r.json(t, &one)
	assert.Equal(t, first, one.ID)
	require.NotNil(t, one.Samples)
	assert.Equal(t, 300, one.Samples.Count)
	assert.False(t, one.Samples.Truncated)
	require.Len(t, one.Samples.OffsetsS, 300)
	require.Len(t, one.Samples.VoltagesUV, 300)
	for i := range 300 {
		assert.InDelta(t, float64(i)/512, one.Samples.OffsetsS[i], 1e-9)
		assert.Equal(t, ecgVolt(i), one.Samples.VoltagesUV[i])
	}

	// The cap: the first samples, marked truncated.
	defer func(v int) { read.MaxECGSamples = v }(read.MaxECGSamples)
	read.MaxECGSamples = 100
	one = ecgJSON{}
	e.do("GET", fmt.Sprintf("%s/%d", base, first), e.keyA, nil).json(t, &one)
	assert.Equal(t, 100, one.Samples.Count)
	assert.True(t, one.Samples.Truncated)
	assert.Len(t, one.Samples.VoltagesUV, 100)
	assert.InDelta(t, 99.0/512, one.Samples.OffsetsS[99], 1e-9)
	one = ecgJSON{}
	e.do("GET", fmt.Sprintf("%s/%d", base, third), e.keyA, nil).json(t, &one)
	assert.False(t, one.Samples.Truncated, "25 samples fit")
	read.MaxECGSamples = 100000

	// Not the user's, not the tenant's, not a recording: 404 alike.
	for path, key := range map[string]string{
		fmt.Sprintf("%s/%d", base, theirs):            e.keyA,
		fmt.Sprintf("%s/%d", base, first):             e.keyB,
		fmt.Sprintf("/v1/users/%s/ecg/%d", ub, first): e.keyB,
		base + "/999999999":                           e.keyA,
		base + "/x":                                   e.keyA,
		base:                                          e.keyB,
		"/v1/users/00000000-0000-4000-8000-000000000000/ecg":                        e.keyA,
		fmt.Sprintf("/v1/users/00000000-0000-4000-8000-000000000000/ecg/%d", first): e.keyA,
	} {
		r := e.do("GET", path, key, nil)
		assert.Equal(t, http.StatusNotFound, r.code, "%s: %s", path, r.body)
		assert.NotContains(t, string(r.body), "voltages_uv", path)
	}
	assert.Equal(t, http.StatusBadRequest, e.do("GET", base+"?cursor=bm9wZQ", e.keyA, nil).code)

	// A user token is not a tenant key.
	tok, _, err := e.store.MintUserToken(context.Background(), e.tenantA, u, []string{"read"}, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, e.do("GET", base, tok, nil).code)

	// Every read is in the audit log, as a data read of the user.
	r = e.do("GET", "/v1/audit?action=data.read&user_id="+u.String()+"&limit=1000", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var log auditPage
	r.json(t, &log)
	routes := map[string]int{}
	for _, en := range log.Data {
		assert.Equal(t, audit.DataRead, en.Action)
		routes[en.Route]++
	}
	assert.Positive(t, routes["GET /v1/users/{id}/ecg"])
	assert.Positive(t, routes["GET /v1/users/{id}/ecg/{ecg_id}"])
}

func TestECGFHIR(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.PublicURL = publicURL })
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	// Between the 08:00 samples and the 09:00 ones, and on the second day.
	early := e.seedECG(e.tenantA, u, "apple_health", ecgT0.Add(30*time.Minute), 1, 1, 512, 64)
	late := e.seedECG(e.tenantA, u, "withings", ecgT0.Add(26*time.Hour), 2, 0, 300, 30)
	base := "/v1/users/" + u.String() + "/fhir"
	obs := base + "/Observation"
	q := url.QueryEscape

	all := e.walk(obs, e.keyA)
	assert.Equal(t, []string{"sleep", "daily", "qs", "qs", "workout", "ecg", "qs", "qs", "qs", "qs", "nutrition", "daily", "ecg"}, idPrefixes(all))
	o := all[5]
	assert.Equal(t, "ecg-"+strconv.FormatInt(early, 10), o.ID)
	assert.Equal(t, []fhir.Coding{{System: fhir.LOINC, Code: "11524-6"}, {System: fhir.RecordTypeSystem, Code: fhir.RecordECG}}, o.Code.Coding)
	assert.Empty(t, o.Category)
	assert.Equal(t, "Patient/"+u.String(), o.Subject.Reference)
	assert.Equal(t, &fhir.Period{Start: "2026-03-01T08:30:00Z", End: "2026-03-01T08:30:00.125Z"}, o.EffectivePeriod)
	require.Len(t, o.Component, 4)
	sd := o.Component[0].ValueSampledData
	require.NotNil(t, sd)
	assert.Equal(t, 1.953125, sd.Period)
	assert.Equal(t, 1, sd.Dimensions)
	data := strings.Fields(sd.Data)
	require.Len(t, data, 64)
	for i, v := range data {
		assert.Equal(t, strconv.FormatFloat(ecgVolt(i), 'f', -1, 64), v, i)
	}
	assert.Equal(t, "sinus_rhythm", o.Component[2].ValueCodeableConcept.Coding[0].Code)
	w := all[12]
	assert.Equal(t, "ecg-"+strconv.FormatInt(late, 10), w.ID)
	assert.Equal(t, fhir.AFibClassificationSystem, w.Component[2].ValueCodeableConcept.Coding[0].System)
	assert.Equal(t, "inconclusive", w.Component[2].ValueCodeableConcept.Coding[0].Code)
	assert.InDelta(t, 3.3333, w.Component[0].ValueSampledData.Period, 0.0001)

	// Read back by id, the same resource.
	r := e.do("GET", obs+"/"+o.ID, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	var back fhir.Observation
	r.json(t, &back)
	assert.Equal(t, o, back)

	// The filters.
	both := []string{o.ID, w.ID}
	for query, want := range map[string][]string{
		"?code=http://loinc.org%7C11524-6":                                       both,
		"?code=" + q(fhir.RecordTypeSystem+"|ecg_recording"):                     both,
		"?code=http://loinc.org%7C11524-6&start=2026-03-02":                      {w.ID},
		"?code=http://loinc.org%7C11524-6&category=vital-signs":                  nil,
		"?category=activity&start=2026-03-01T08:30:00Z&end=2026-03-01T08:31:00Z": nil,
	} {
		got := ids(e.walk(obs+query, e.keyA))
		if want == nil {
			assert.Empty(t, got, query)
			continue
		}
		assert.Equal(t, want, got, query)
	}
	assert.NotContains(t, idPrefixes(e.walk(obs+"?type=heart_rate", e.keyA)), "ecg")
	assert.NotContains(t, idPrefixes(e.walk(obs+"?category=vital-signs", e.keyA)), "ecg", "no category is claimed")
	// The waveform code is in a component, not Observation.code; and MDC is
	// not a system the search takes.
	assert.Equal(t, http.StatusBadRequest, e.do("GET", obs+"?code="+q(fhir.MDC+"|131328"), e.keyA, nil).code)

	// Every page size gives the same list.
	for limit := 1; limit <= len(all)+1; limit++ {
		assert.Equal(t, ids(all), ids(e.walk(obs+"?limit="+strconv.Itoa(limit), e.keyA)), "limit %d", limit)
	}

	// The ECG cap of a page: a page ends at its first ECG, and the walk
	// still lists every Observation once.
	defer func(v int) { read.MaxECGPerPage = v }(read.MaxECGPerPage)
	read.MaxECGPerPage = 1
	b := e.bundle(obs+"?code=http://loinc.org%7C11524-6", e.keyA)
	require.Len(t, b.Entry, 1)
	assert.NotEmpty(t, next(b))
	assert.Equal(t, ids(all), ids(e.walk(obs+"?limit=100", e.keyA)))
	read.MaxECGPerPage = 100

	// Provenance of the ECG Observation, and $everything holds it.
	r = e.do("GET", base+"/Provenance/prov-"+o.ID, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Contains(t, string(r.body), `"Observation/`+o.ID+`"`)
	var got []string
	for link := base + "/Patient/" + u.String() + "/$everything?limit=5"; link != ""; {
		eb := e.rawBundle(link, e.keyA)
		for _, en := range eb.Entry {
			var tr typed
			require.NoError(t, json.Unmarshal(en.Resource, &tr))
			if tr.ResourceType == "Observation" {
				got = append(got, tr.ID)
			}
		}
		if link = next(fhir.Bundle{Link: eb.Link}); link != "" {
			link = relative(t, link)
		}
	}
	assert.Equal(t, ids(all), got)

	// Not the user's: a 404.
	ub := e.user(e.tenantB, "b-1")
	assert.Equal(t, http.StatusNotFound, e.do("GET", obs+"/"+o.ID, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/v1/users/"+ub.String()+"/fhir/Observation/"+o.ID, e.keyB, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", obs+"/ecg-999999999", e.keyA, nil).code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", obs+"/ecg-x", e.keyA, nil).code)
}

func TestECGExportAndArchive(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	first := e.seedECG(e.tenantA, u, "apple_health", ecgT0, 1, 1, 512, 20)
	e.seedECG(e.tenantA, u, "withings", ecgT0.Add(24*time.Hour), 0, 0, 300, 10)
	base := "/v1/users/" + u.String()

	// The export: the list's members, no samples.
	r := e.do("GET", base+"/exports/ecg", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	rows, err := csv.NewReader(bytes.NewReader(r.body)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 1+2)
	assert.Equal(t, []string{"id", "start_date", "end_date", "classification", "classification_value"}, rows[0][:5])
	assert.NotContains(t, rows[0], "samples")
	assert.Equal(t, strconv.FormatInt(first, 10), rows[1][0])
	assert.Equal(t, "sinus_rhythm", rows[1][3])
	r = e.do("GET", base+"/exports/ecg?format=parquet", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	f, err := parquet.OpenFile(bytes.NewReader(r.body), int64(len(r.body)))
	require.NoError(t, err)
	assert.EqualValues(t, 2, f.NumRows())

	// The archive: each recording with its samples, as the REST read.
	r = e.do("GET", base+"/archive", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	files := openArchive(t, r.body)
	lines := ndjsonLines(t, files["canonical/ecg.ndjson"])
	require.Len(t, lines, 2)
	var rest map[string]any
	e.do("GET", fmt.Sprintf("%s/ecg/%d", base, first), e.keyA, nil).json(t, &rest)
	assert.Equal(t, rest, lines[0])
	assert.Len(t, lines[1]["samples"].(map[string]any)["voltages_uv"], 10)
	var m struct {
		Files []struct {
			Path    string `json:"path"`
			Records int    `json:"records"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(files["manifest.json"], &m))
	counts := map[string]int{}
	for _, fl := range m.Files {
		counts[fl.Path] = fl.Records
	}
	assert.Equal(t, 2, counts["canonical/ecg.ndjson"])
	assert.Equal(t, 13, counts["fhir/bundle.json"], "eleven of e.seed and the two recordings")
	assert.Contains(t, string(files["fhir/bundle.json"]), `"valueSampledData"`)
}
