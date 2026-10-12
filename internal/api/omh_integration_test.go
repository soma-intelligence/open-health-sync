// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/omh"
)

type omhPage struct {
	Data       []omh.DataPoint `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

// omhWalk follows next_cursor from path and returns every data point.
func (e *env) omhWalk(path string) []omh.DataPoint {
	e.t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var out []omh.DataPoint
	for cursor := ""; ; {
		p := path
		if cursor != "" {
			p += sep + "cursor=" + cursor
		}
		r := e.do("GET", p, e.keyA, nil)
		require.Equal(e.t, http.StatusOK, r.code, "%s: %s", p, r.body)
		assert.Equal(e.t, "application/json", r.header.Get("Content-Type"))
		var pg omhPage
		r.json(e.t, &pg)
		out = append(out, pg.Data...)
		if pg.NextCursor == nil {
			return out
		}
		cursor = *pg.NextCursor
	}
}

func schemaNames(ds []omh.DataPoint) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Header.SchemaID.Name)
	}
	return out
}

func TestOMHDataPoints(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	sleepID, workoutID := e.seed(e.tenantA, u)
	// A blood pressure pair at 07:00, and an HRV sample (no Open mHealth
	// schema) at 07:30.
	t7 := time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC)
	sys := e.bpSample(e.tenantA, u, "withings", "blood_pressure_systolic", 120, t7, nil, nil, nil)
	e.bpSample(e.tenantA, u, "withings", "blood_pressure_diastolic", 80, t7, nil, nil, nil)
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		VALUES ($1, $2, 'oura', 'hrv_sdnn', 42, 'ms', $3, 'hrv-1')`, e.tenantA, u, t7.Add(30*time.Minute))
	base := "/v1/users/" + u.String() + "/omh/data-points"

	// In time order: the sleep session, the blood pressure pair, the 08:00
	// heart rate and steps, the workout, the rest of the heart rate. No daily
	// summary and no HRV.
	all := e.omhWalk(base)
	assert.Equal(t, []string{"sleep-episode", "blood-pressure", "heart-rate", "step-count", "physical-activity",
		"heart-rate", "heart-rate", "heart-rate", "heart-rate"}, schemaNames(all))

	sl := all[0]
	assert.Equal(t, "sleep-"+strconv.FormatInt(sleepID, 10), sl.Header.ID)
	assert.Equal(t, omh.SchemaID{Namespace: "omh", Name: "sleep-episode", Version: "1.1"}, sl.Header.SchemaID)
	assert.Equal(t, u.String(), sl.Header.UserID)
	assert.Equal(t, "apple_health", sl.Header.AcquisitionProvenance.SourceName)
	assert.Equal(t, map[string]any{"value": 25200.0, "unit": "sec"}, sl.Body["total_sleep_time"])

	bp := all[1]
	assert.Equal(t, "bp-"+strconv.FormatInt(sys, 10), bp.Header.ID)
	assert.Equal(t, map[string]any{"value": 120.0, "unit": "mmHg"}, bp.Body["systolic_blood_pressure"])
	assert.Equal(t, map[string]any{"value": 80.0, "unit": "mmHg"}, bp.Body["diastolic_blood_pressure"])
	assert.Equal(t, map[string]any{"date_time": "2026-03-01T07:00:00Z"}, bp.Body["effective_time_frame"])

	hr := all[2]
	assert.Equal(t, map[string]any{"value": 60.0, "unit": "beats/min"}, hr.Body["heart_rate"])
	assert.Equal(t, map[string]any{"date_time": "2026-03-01T08:00:00Z"}, hr.Body["effective_time_frame"])

	wo := all[4]
	assert.Equal(t, "workout-"+strconv.FormatInt(workoutID, 10), wo.Header.ID)
	assert.Equal(t, "running", wo.Body["activity_name"])
	assert.Equal(t, map[string]any{"value": 5000.0, "unit": "m"}, wo.Body["distance"])

	// Every page size gives the same list.
	for limit := 1; limit <= len(all)+2; limit++ {
		got := e.omhWalk(base + "?limit=" + strconv.Itoa(limit))
		assert.Equal(t, schemaNames(all), schemaNames(got), "limit %d", limit)
	}

	// One schema, bare or with its namespace and version, and a window.
	for query, want := range map[string]int{
		"?schema=heart-rate":                                   5,
		"?schema=omh:heart-rate":                               5,
		"?schema=omh:heart-rate:2.0":                           5,
		"?schema=blood-pressure":                               1,
		"?schema=sleep-episode":                                1,
		"?schema=physical-activity":                            1,
		"?schema=step-count":                                   1,
		"?schema=body-weight":                                  0,
		"?start=2026-03-01T09:00:00Z&end=2026-03-01T11:00:00Z": 2,
	} {
		assert.Len(t, e.omhWalk(base+query), want, query)
	}

	for path, want := range map[string]int{
		base + "?schema=heart_rate":                                      http.StatusBadRequest,
		base + "?schema=omh:heart-rate:3.0":                              http.StatusBadRequest,
		base + "?schema=a&schema=b":                                      http.StatusBadRequest,
		base + "?type=heart_rate":                                        http.StatusBadRequest,
		base + "?cursor=bm9wZQ":                                          http.StatusBadRequest,
		base + "?limit=5000":                                             http.StatusBadRequest,
		"/v1/users/00000000-0000-4000-8000-000000000000/omh/data-points": http.StatusNotFound,
	} {
		r := e.do("GET", path, e.keyA, nil)
		assert.Equal(t, want, r.code, "%s: %s", path, r.body)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"), path)
	}

	// Another tenant's key does not see the user.
	assert.Equal(t, http.StatusNotFound, e.do("GET", base, e.keyB, nil).code)
}

func TestExports(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.seed(e.tenantA, u)
	base := "/v1/users/" + u.String() + "/exports/"

	// CSV, the default: a header line, then every sample of every type in
	// start order.
	r := e.do("GET", base+"samples", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "text/csv; charset=utf-8", r.header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="samples-`+u.String()+`.csv"`, r.header.Get("Content-Disposition"))
	rows, err := csv.NewReader(bytes.NewReader(r.body)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 1+6, "five heart rates and the steps")
	assert.Equal(t, []string{"id", "type", "value", "unit", "start_date"}, rows[0][:5])
	col := map[string]int{}
	for i, name := range rows[0] {
		col[name] = i
	}
	var types []string
	for _, row := range rows[1:] {
		types = append(types, row[col["type"]])
	}
	assert.ElementsMatch(t, []string{"heart_rate", "heart_rate", "heart_rate", "heart_rate", "heart_rate", "step_count"}, types)
	assert.Equal(t, "2026-03-01T08:00:00Z", rows[1][col["start_date"]])
	assert.Equal(t, `{"timezone":"Europe/Paris"}`, rows[1][col["metadata"]])

	// One type and a window.
	r = e.do("GET", base+"samples?type=heart_rate&start=2026-03-01T09:00:00Z&end=2026-03-01T11:00:00Z", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	rows, err = csv.NewReader(bytes.NewReader(r.body)).ReadAll()
	require.NoError(t, err)
	assert.Len(t, rows, 1+2)

	// Parquet: the same rows, typed.
	r = e.do("GET", base+"samples?format=parquet", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, "application/vnd.apache.parquet", r.header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="samples-`+u.String()+`.parquet"`, r.header.Get("Content-Disposition"))
	f, err := parquet.OpenFile(bytes.NewReader(r.body), int64(len(r.body)))
	require.NoError(t, err)
	assert.EqualValues(t, 6, f.NumRows())

	// Every list, in both formats; an empty one is a header line or an
	// empty Parquet file.
	for _, name := range []string{"samples", "category-samples", "sleep", "workouts", "nutrition", "daily"} {
		for _, format := range []string{"csv", "parquet"} {
			r := e.do("GET", base+name+"?format="+format, e.keyA, nil)
			assert.Equal(t, http.StatusOK, r.code, "%s %s: %s", name, format, r.body)
		}
	}
	other := e.user(e.tenantA, "a-2")
	r = e.do("GET", "/v1/users/"+other.String()+"/exports/sleep", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, 1, strings.Count(string(r.body), "\n"), "only the header line")
	r = e.do("GET", "/v1/users/"+other.String()+"/exports/sleep?format=parquet", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	f, err = parquet.OpenFile(bytes.NewReader(r.body), int64(len(r.body)))
	require.NoError(t, err)
	assert.EqualValues(t, 0, f.NumRows())

	for path, want := range map[string]int{
		base + "samples?format=xlsx":     http.StatusBadRequest,
		base + "samples?limit=10":        http.StatusBadRequest,
		base + "samples?cursor=abc":      http.StatusBadRequest,
		base + "samples?type=heartrate":  http.StatusBadRequest,
		base + "samples?start=yesterday": http.StatusBadRequest,
		base + "routes":                  http.StatusNotFound,
		"/v1/users/00000000-0000-4000-8000-000000000000/exports/samples": http.StatusNotFound,
	} {
		r := e.do("GET", path, e.keyA, nil)
		assert.Equal(t, want, r.code, "%s: %s", path, r.body)
		assert.Equal(t, "application/problem+json", r.header.Get("Content-Type"), path)
	}
	assert.Equal(t, http.StatusNotFound, e.do("GET", base+"samples", e.keyB, nil).code, "another tenant")
}

// TestExportManyPages: an export reads past the largest page.
func TestExportManyPages(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.exec(`INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, quantity_type, value, unit, start_date, content_hash)
		SELECT $1, $2, 'apple_health', 'heart_rate', 60, 'bpm',
			'2026-03-01T00:00:00Z'::timestamptz + make_interval(secs => g), 'hr-' || g
		FROM generate_series(1, 2500) g`, e.tenantA, u)
	r := e.do("GET", "/v1/users/"+u.String()+"/exports/samples?format=csv", e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code)
	rows, err := csv.NewReader(bytes.NewReader(r.body)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 1+2500)
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		assert.False(t, seen[row[0]], "id %s twice", row[0])
		seen[row[0]] = true
	}
}
