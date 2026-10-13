// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

type cycleJSON struct {
	StartDate        string  `json:"start_date"`
	PeriodEndDate    string  `json:"period_end_date"`
	PeriodLengthDays int     `json:"period_length_days"`
	EndDate          *string `json:"end_date"`
	CycleLengthDays  *int    `json:"cycle_length_days"`
	Providers        []string
	FlowDays         []struct {
		Date      string   `json:"date"`
		Flow      string   `json:"flow"`
		Providers []string `json:"providers"`
	} `json:"flow_days"`
}

// category inserts one category sample with invented values.
func (e *env) category(tn, u uuid.UUID, provider, typ, label string, at time.Time, tz string) {
	e.t.Helper()
	md := "{}"
	if tz != "" {
		md = fmt.Sprintf(`{"timezone":%q}`, tz)
	}
	e.exec(`INSERT INTO ohs_data.category_samples
		(tenant_id, user_id, provider, category_type, value_label, start_date, end_date, content_hash, metadata)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $6, $7, $8::jsonb)`,
		tn, u, provider, typ, label, at, uuid.NewString(), md)
}

func noon(date string) time.Time {
	d, _ := time.Parse(time.DateOnly, date)
	return d.Add(12 * time.Hour)
}

// seedCycles writes three periods (2 Jan, 30 Jan, 27 Feb 2026) from two
// providers, with a none day, intermenstrual bleeding and a mindful session
// that must all be ignored.
func (e *env) seedCycles(tn, u uuid.UUID) {
	e.t.Helper()
	for _, d := range []string{"2026-01-02", "2026-01-03", "2026-01-04"} {
		e.category(tn, u, "apple_health", "menstrual_flow", "medium", noon(d), "Europe/Paris")
	}
	// 23:30 UTC on 1 January is 2 January in Paris: the same first day.
	e.category(tn, u, "health_connect", "menstrual_flow", "heavy", time.Date(2026, 1, 1, 23, 30, 0, 0, time.UTC), "Etc/GMT-1")
	e.category(tn, u, "apple_health", "menstrual_flow", "none", noon("2026-01-10"), "")
	e.category(tn, u, "apple_health", "intermenstrual_bleeding", "", noon("2026-01-15"), "")
	e.category(tn, u, "apple_health", "mindful_session", "done", noon("2026-01-16"), "")
	for _, d := range []string{"2026-01-30", "2026-01-31", "2026-02-03"} {
		e.category(tn, u, "apple_health", "menstrual_flow", "light", noon(d), "")
	}
	e.category(tn, u, "apple_health", "menstrual_flow", "", noon("2026-02-27"), "")
}

func (e *env) cycles(path string) page[cycleJSON] {
	e.t.Helper()
	r := e.do("GET", path, e.keyA, nil)
	require.Equal(e.t, http.StatusOK, r.code, "%s: %s", path, r.body)
	var p page[cycleJSON]
	r.json(e.t, &p)
	return p
}

func TestMenstrualCycles(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	e.seedCycles(e.tenantA, u)
	base := "/v1/users/" + u.String() + "/menstrual-cycles"

	p := e.cycles(base)
	assert.Nil(t, p.NextCursor)
	require.Len(t, p.Data, 3)
	c := p.Data[0]
	assert.Equal(t, "2026-01-02", c.StartDate)
	assert.Equal(t, "2026-01-04", c.PeriodEndDate)
	assert.Equal(t, 3, c.PeriodLengthDays)
	require.NotNil(t, c.EndDate)
	assert.Equal(t, "2026-01-29", *c.EndDate)
	require.NotNil(t, c.CycleLengthDays)
	assert.Equal(t, 28, *c.CycleLengthDays)
	assert.Equal(t, []string{"apple_health", "health_connect"}, c.Providers)
	require.Len(t, c.FlowDays, 3)
	assert.Equal(t, "heavy", c.FlowDays[0].Flow)
	assert.Equal(t, []string{"apple_health", "health_connect"}, c.FlowDays[0].Providers)

	// A gap of two days keeps one period.
	assert.Equal(t, "2026-02-03", p.Data[1].PeriodEndDate)
	assert.Equal(t, 5, p.Data[1].PeriodLengthDays)
	assert.Len(t, p.Data[1].FlowDays, 3)
	assert.Equal(t, 28, *p.Data[1].CycleLengthDays)
	// The latest cycle is open; a sample without a label is unspecified flow.
	assert.Nil(t, p.Data[2].EndDate)
	assert.Nil(t, p.Data[2].CycleLengthDays)
	assert.Equal(t, "unspecified", p.Data[2].FlowDays[0].Flow)

	// Paging, one cycle a page: the same cycles, each with its length.
	var paged []cycleJSON
	cursor := ""
	for range 5 {
		path := base + "?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		pg := e.cycles(path)
		paged = append(paged, pg.Data...)
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	assert.Equal(t, p.Data, paged)

	// The window selects by start day; the edge cycle keeps its length.
	w := e.cycles(base + "?start=2026-01-03&end=2026-02-01")
	require.Len(t, w.Data, 1)
	assert.Equal(t, "2026-01-30", w.Data[0].StartDate)
	assert.Equal(t, 28, *w.Data[0].CycleLengthDays)
	assert.Empty(t, e.cycles(base+"?start=2026-03-01").Data)

	// Derived at read time: removing a sample changes the answer at once.
	e.exec(`DELETE FROM ohs_data.category_samples WHERE tenant_id = $1 AND user_id = $2 AND start_date = $3`,
		e.tenantA, u, noon("2026-02-27"))
	p = e.cycles(base)
	require.Len(t, p.Data, 2)
	assert.Nil(t, p.Data[1].CycleLengthDays)

	// Refusals.
	for path, want := range map[string]int{
		base + "?limit=5000":                                  http.StatusBadRequest,
		base + "?cursor=%25%25":                               http.StatusBadRequest,
		base + "?start=2026-02-01&end=2026-01-01":             http.StatusBadRequest,
		"/v1/users/" + uuid.NewString() + "/menstrual-cycles": http.StatusNotFound,
	} {
		assert.Equal(t, want, e.do("GET", path, e.keyA, nil).code, path)
	}
	assert.Equal(t, http.StatusNotFound, e.do("GET", base, e.keyB, nil).code, "another tenant")
}

// A user of another tenant with flow on the same days never leaks in.
func TestMenstrualCyclesTenantIsolation(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	ub := e.user(e.tenantB, "b-1")
	e.category(e.tenantA, u, "apple_health", "menstrual_flow", "light", noon("2026-01-02"), "")
	e.seedCycles(e.tenantB, ub)
	p := e.cycles("/v1/users/" + u.String() + "/menstrual-cycles")
	require.Len(t, p.Data, 1)
	assert.Equal(t, []string{"apple_health"}, p.Data[0].Providers)
	assert.Len(t, p.Data[0].FlowDays, 1)
}

// Past MaxCycleScanSamples a page ends early with a cursor, and paging still
// returns every cycle once.
func TestMenstrualCyclesScanCap(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "a-1")
	for m := 1; m <= 6; m++ {
		e.category(e.tenantA, u, "apple_health", "menstrual_flow", "light", time.Date(2026, time.Month(m), 3, 12, 0, 0, 0, time.UTC), "")
	}
	base := "/v1/users/" + u.String() + "/menstrual-cycles"
	want := e.cycles(base).Data
	require.Len(t, want, 6)

	old := read.MaxCycleScanSamples
	read.MaxCycleScanSamples = 3
	t.Cleanup(func() { read.MaxCycleScanSamples = old })
	var got []cycleJSON
	cursor, pages := "", 0
	for range 10 {
		path := base
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		pg := e.cycles(path)
		got = append(got, pg.Data...)
		pages++
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	assert.Equal(t, want, got)
	assert.Greater(t, pages, 1, "the cap split the read")

	// One cycle needing more samples than the cap is refused, not cut.
	read.MaxCycleScanSamples = 1
	r := e.do("GET", base, e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code, string(r.body))
}
