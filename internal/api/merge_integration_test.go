// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api"
)

// mergeDay is the day every record of the merge tests falls on.
var mergeDay = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

func at(h, m int) time.Time {
	return mergeDay.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// sample inserts one quantity sample and returns its id. stream is the
// source_name inside the provider (a phone or a watch).
func (e *env) sample(u uuid.UUID, provider, slug, stream, qt string, v float64, start, end time.Time) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.quantity_samples
		(tenant_id, user_id, provider, source_slug, source_name, quantity_type, value, start_date, end_date, content_hash)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10) RETURNING id`,
		e.tenantA, u, provider, slug, stream, qt, v, start, end, uuid.NewString()).Scan(&id))
	return id
}

func (e *env) sleepSession(u uuid.UUID, provider string, start, end time.Time) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.sleep_sessions
		(tenant_id, user_id, provider, source_slug, bedtime_start, bedtime_end, content_hash)
		VALUES ($1, $2, $3, $3, $4, $5, $6) RETURNING id`, e.tenantA, u, provider, start, end, uuid.NewString()).Scan(&id))
	return id
}

func (e *env) workout(u uuid.UUID, provider string, start, end time.Time) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.workouts
		(tenant_id, user_id, provider, source_slug, activity_type, start_date, end_date, content_hash)
		VALUES ($1, $2, $3, $3, 'running', $4, $5, $6) RETURNING id`, e.tenantA, u, provider, start, end, uuid.NewString()).Scan(&id))
	return id
}

// daily inserts one provider's summary of mergeDay with the given figures.
func (e *env) daily(u uuid.UUID, provider string, figures map[string]any) int64 {
	e.t.Helper()
	cols := []string{"tenant_id", "user_id", "provider", "source_slug", "summary_date", "content_hash"}
	args := []any{e.tenantA, u, provider, provider, mergeDay.Format(time.DateOnly), uuid.NewString()}
	for c, v := range figures {
		cols = append(cols, c)
		args = append(args, v)
	}
	ph := make([]string, len(args))
	for i := range ph {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	var id int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(), `INSERT INTO ohs_data.daily_summaries (`+
		strings.Join(cols, ", ")+`) VALUES (`+strings.Join(ph, ", ")+`) RETURNING id`, args...).Scan(&id))
	return id
}

// ids lists the ids of one page of path.
func (e *env) ids(path string) []int64 {
	e.t.Helper()
	r := e.do("GET", path, e.keyA, nil)
	require.Equal(e.t, http.StatusOK, r.code, string(r.body))
	var pg page[struct {
		ID int64 `json:"id"`
	}]
	r.json(e.t, &pg)
	out := []int64{}
	for _, d := range pg.Data {
		out = append(out, d.ID)
	}
	return out
}

type mergedDay struct {
	ID             int64             `json:"id"`
	Date           string            `json:"date"`
	StepCount      *int              `json:"step_count"`
	ReadinessScore *float64          `json:"readiness_score"`
	StrainScore    *float64          `json:"strain_score"`
	RestingHR      *float64          `json:"resting_hr"`
	Provider       string            `json:"provider"`
	Sources        map[string]string `json:"sources"`
}

func (e *env) mergedDaily(u uuid.UUID) mergedDay {
	e.t.Helper()
	r := e.do("GET", "/v1/users/"+u.String()+"/daily", e.keyA, nil)
	require.Equal(e.t, http.StatusOK, r.code, string(r.body))
	var pg page[mergedDay]
	r.json(e.t, &pg)
	require.Len(e.t, pg.Data, 1, "one summary per day")
	return pg.Data[0]
}

func TestMergedSamplesRankOverlappingSources(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "m-1")
	base := "/v1/users/" + u.String()

	// Steps: the watch and the phone count the same walk, a ring has a day total.
	watch := e.sample(u, "apple_health", "apple_health", "Apple Watch", "step_count", 500, at(9, 0), at(9, 10))
	phoneSame := e.sample(u, "apple_health", "apple_health", "iPhone", "step_count", 480, at(9, 2), at(9, 12))
	phoneAlone := e.sample(u, "apple_health", "apple_health", "iPhone", "step_count", 300, at(12, 0), at(12, 10))
	ringDay := e.sample(u, "oura", "oura", "", "step_count", 9000, mergeDay, mergeDay.Add(24*time.Hour))

	all := e.ids(base + "/samples?type=step_count&view=all")
	assert.ElementsMatch(t, []int64{watch, phoneSame, phoneAlone, ringDay}, all)
	assert.Equal(t, []int64{watch, phoneAlone}, e.ids(base+"/samples?type=step_count"),
		"the phone's copy of the walk and the ring's day total are left out; the phone alone stays")

	// Heart rate: a reading of the better source within 5 minutes covers one of a worse source.
	hrWatch := e.sample(u, "apple_health", "apple_health", "Apple Watch", "heart_rate", 62, at(10, 0), at(10, 0))
	e.sample(u, "oura", "oura", "", "heart_rate", 61, at(10, 3), at(10, 3))
	e.sample(u, "oura", "oura", "", "heart_rate", 58, at(3, 0), at(3, 0))
	// The same sample arriving directly and through an aggregator is one:
	// same source, so the tie goes to the provider name.
	hrRing := e.sample(u, "aggregator", "oura", "", "heart_rate", 58, at(3, 0), at(3, 0))
	assert.Equal(t, []int64{hrRing, hrWatch}, e.ids(base+"/samples?type=heart_rate"))
	assert.Len(t, e.ids(base+"/samples?type=heart_rate&view=all"), 4)

	// A user's own order flips the steps.
	r := e.do("PUT", base+"/source-priorities", e.keyA, map[string]any{"priorities": map[string]any{"step_count": []string{"Oura"}}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.Equal(t, []int64{ringDay}, e.ids(base+"/samples?type=step_count"))
	assert.Equal(t, []int64{hrRing, hrWatch}, e.ids(base+"/samples?type=heart_rate"), "other metrics keep the defaults")

	r = e.do("GET", base+"/samples?type=step_count&view=raw", e.keyA, nil)
	assert.Equal(t, http.StatusBadRequest, r.code)
}

func TestMergedSleepAndWorkouts(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "m-2")
	base := "/v1/users/" + u.String()

	ring := e.sleepSession(u, "oura", at(-1, 0), at(7, 0))
	e.sleepSession(u, "apple_health", at(-1, 30), at(6, 30))
	nap := e.sleepSession(u, "apple_health", at(14, 0), at(14, 30))
	assert.Equal(t, []int64{ring, nap}, e.ids(base+"/sleep"), "the watch's night is the ring's; its nap stays")
	assert.Len(t, e.ids(base+"/sleep?view=all"), 3)

	// Two apps behind Apple Health recorded the same night: the watch's
	// session stays, the phone's is covered. Each app's own nap stays.
	u2 := e.user(e.tenantA, "m-2b")
	base2 := "/v1/users/" + u2.String()
	watch := e.sleepSession(u2, "apple_health", at(-1, 0), at(7, 0))
	phone := e.sleepSession(u2, "apple_health", at(-1, 15), at(6, 45))
	e.exec(`UPDATE ohs_data.sleep_sessions SET metadata = '{"sourceName":"My Apple Watch"}' WHERE id = $1`, watch)
	e.exec(`UPDATE ohs_data.sleep_sessions SET metadata = '{"sourceName":"My iPhone"}' WHERE id = $1`, phone)
	assert.Equal(t, []int64{watch}, e.ids(base2+"/sleep"), "one night, two apps")
	assert.Len(t, e.ids(base2+"/sleep?view=all"), 2)

	run := e.workout(u, "polar", at(8, 0), at(9, 0))
	e.workout(u, "strava", at(8, 1), at(9, 2))
	walk := e.workout(u, "strava", at(18, 0), at(18, 40))
	assert.Equal(t, []int64{run, walk}, e.ids(base+"/workouts"))
}

func TestMergedDailyTakesEachFigureFromItsBestSource(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "m-3")
	base := "/v1/users/" + u.String()

	apple := e.daily(u, "apple_health", map[string]any{"step_count": 10000})
	oura := e.daily(u, "oura", map[string]any{"step_count": 8000, "readiness_score": 80})
	whoop := e.daily(u, "whoop", map[string]any{"strain_score": 12.5, "resting_hr": 50})
	withings := e.daily(u, "withings", map[string]any{"step_count": 9000})

	d := e.mergedDaily(u)
	assert.Equal(t, mergeDay.Format(time.DateOnly), d.Date)
	assert.Equal(t, apple, d.ID, "the day's best ranked row under '*' leads")
	assert.Equal(t, 10000, *d.StepCount)
	assert.Equal(t, 80.0, *d.ReadinessScore)
	assert.Equal(t, 12.5, *d.StrainScore)
	assert.Equal(t, 50.0, *d.RestingHR)
	assert.Equal(t, map[string]string{"step_count": "apple_health", "readiness_score": "oura",
		"strain_score": "whoop", "resting_hr": "whoop"}, d.Sources)
	assert.ElementsMatch(t, []int64{apple, oura, whoop, withings}, e.ids(base+"/daily?view=all"))

	r := e.do("PUT", base+"/source-priorities", e.keyA, map[string]any{"priorities": map[string]any{"activity": []string{"withings"}}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	d = e.mergedDaily(u)
	assert.Equal(t, 9000, *d.StepCount)
	assert.Equal(t, "withings", d.Sources["step_count"])

	// FHIR: a row stays when it owns a figure, and shows only the figures it owns.
	fhirIDs := func(q string) []string {
		r := e.do("GET", base+"/fhir/Observation?category=activity"+q, e.keyA, nil)
		require.Equal(t, http.StatusOK, r.code, string(r.body))
		var b struct {
			Entry []struct {
				Resource struct {
					ID string `json:"id"`
				} `json:"resource"`
			} `json:"entry"`
		}
		r.json(t, &b)
		var out []string
		for _, en := range b.Entry {
			out = append(out, en.Resource.ID)
		}
		return out
	}
	id := func(n int64) string { return fmt.Sprintf("daily-%d", n) }
	assert.ElementsMatch(t, []string{id(apple), id(oura), id(whoop), id(withings)}, fhirIDs("&view=all"))
	assert.ElementsMatch(t, []string{id(oura), id(whoop), id(withings)}, fhirIDs(""),
		"apple's only figure, steps, is withings' now")
	r = e.do("GET", base+"/fhir/Observation/"+id(oura), e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	assert.NotContains(t, string(r.body), "8000", "oura's steps are withings'")
	r = e.do("GET", base+"/fhir/Observation/"+id(oura)+"?view=all", e.keyA, nil)
	assert.Contains(t, string(r.body), "8000")
}

func TestSourcePrioritiesRoute(t *testing.T) {
	e := newEnv(t)
	u := e.user(e.tenantA, "m-4")
	path := "/v1/users/" + u.String() + "/source-priorities"

	var got struct {
		Priorities map[string][]string `json:"priorities"`
		Defaults   map[string][]string `json:"defaults"`
	}
	r := e.do("GET", path, e.keyA, nil)
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &got)
	assert.Empty(t, got.Priorities)
	assert.Equal(t, "oura", got.Defaults["sleep"][0])

	r = e.do("PUT", path, e.keyA, map[string]any{"priorities": map[string]any{"*": []string{"WHOOP", "oura"}, "sleep": []string{"withings"}}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	r.json(t, &got)
	assert.Equal(t, map[string][]string{"*": {"whoop", "oura"}, "sleep": {"withings"}}, got.Priorities)

	for name, body := range map[string]any{
		"no priorities": map[string]any{},
		"bad key":       map[string]any{"priorities": map[string]any{"Heart Rate": []string{"oura"}}},
		"duplicate":     map[string]any{"priorities": map[string]any{"sleep": []string{"oura", "oura"}}},
		"empty order":   map[string]any{"priorities": map[string]any{"sleep": []string{}}},
	} {
		r := e.do("PUT", path, e.keyA, body)
		assert.Equal(t, http.StatusBadRequest, r.code, name)
	}

	r = e.do("PUT", path, e.keyA, map[string]any{"priorities": map[string]any{}})
	require.Equal(t, http.StatusOK, r.code, string(r.body))
	got.Priorities = nil
	r.json(t, &got)
	assert.Empty(t, got.Priorities, "an empty object goes back to the defaults")

	// Another tenant's user is not found, and nothing is written for it.
	r = e.do("PUT", path, e.keyB, map[string]any{"priorities": map[string]any{"*": []string{"oura"}}})
	assert.Equal(t, http.StatusNotFound, r.code)
	r = e.do("GET", path, e.keyB, nil)
	assert.Equal(t, http.StatusNotFound, r.code)
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_sync.source_priorities`).Scan(&n))
	assert.Zero(t, n)
}

// staticScorer stands in for a quality layer.
type staticScorer struct {
	scores map[string]map[string]float64
	err    error
}

func (s staticScorer) SourceScores(context.Context, uuid.UUID, uuid.UUID) (map[string]map[string]float64, error) {
	return s.scores, s.err
}

func TestScorerRanksUnorderedMetrics(t *testing.T) {
	scorer := &staticScorer{scores: map[string]map[string]float64{"heart_rate": {"oura": 0.9, "apple_health": 0.2}}}
	e := newEnv(t, func(d *api.Deps) { d.Scorer = scorer })
	u := e.user(e.tenantA, "m-5")
	base := "/v1/users/" + u.String()
	e.sample(u, "apple_health", "apple_health", "Apple Watch", "heart_rate", 62, at(10, 0), at(10, 0))
	ring := e.sample(u, "oura", "oura", "", "heart_rate", 61, at(10, 3), at(10, 3))
	assert.Equal(t, []int64{ring}, e.ids(base+"/samples?type=heart_rate"), "the scores put the ring first")

	scorer.err = errors.New("down")
	assert.Len(t, e.ids(base+"/samples?type=heart_rate"), 1, "a failing scorer falls back to the orders")
}
