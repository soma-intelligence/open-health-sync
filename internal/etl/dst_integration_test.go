// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// appleShapedSteps maps one Apple Health shaped quantity line into a canonical
// step sample, carrying HKTimeZone as the canonical "timezone" metadata key.
func appleShapedSteps(r connector.RawRecord) (model.MapResult, error) {
	start, err := time.Parse(time.RFC3339, r.Fields["startDate"].(string))
	if err != nil {
		return model.MapResult{}, err
	}
	end, err := time.Parse(time.RFC3339, r.Fields["endDate"].(string))
	if err != nil {
		return model.MapResult{}, err
	}
	md := map[string]any{}
	if m, ok := r.Fields["metadata"].(map[string]any); ok {
		if tz, ok := m["HKTimeZone"].(string); ok {
			md["timezone"] = tz
		}
	}
	v := r.Fields["value"].(float64)
	return model.MapResult{QuantitySamples: []model.QuantitySample{{
		UserID: r.UserID, Provider: r.Provider, QuantityType: model.QTStepCount,
		Value: v, Unit: r.Fields["unit"].(string), StartDate: start.UTC(), EndDate: end.UTC(),
		SourceName: "Watch", Metadata: md,
		ContentHash: model.ComputeContentHash(map[string]string{
			"user_id": r.UserID, "provider": r.Provider, "type": model.QTStepCount,
			"start": start.UTC().Format(time.RFC3339Nano), "value": model.CanonicalFloat(v),
		}),
	}}}, nil
}

// afterFixture is a validation clock past the fixture, which lies in the
// future of the day this was written.
func afterFixture() time.Time { return time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC) }

func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type daySteps struct {
	day   string
	steps int
}

func (e *env) dailySteps(provider string) []daySteps {
	e.t.Helper()
	rows, err := e.read().Query(context.Background(), `
		SELECT to_char(summary_date, 'YYYY-MM-DD'), step_count FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 ORDER BY summary_date`,
		e.tenant, e.user, provider)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []daySteps
	for rows.Next() {
		var d daySteps
		require.NoError(e.t, rows.Scan(&d.day, &d.steps))
		out = append(out, d)
	}
	require.NoError(e.t, rows.Err())
	return out
}

// TestDSTDayBoundary: step samples every 30 minutes from 2026-10-31T23:30 to
// 2026-11-02T01:30 America/Los_Angeles, across the fall-back transition, so
// 2026-11-01 is a 25 hour local day. The daily summaries are the sums per LOCAL
// day (1, 50 and 4 samples of 10 steps), and processing the object a second
// time changes no row count.
func TestDSTDayBoundary(t *testing.T) {
	e := newEnv(t)
	e.now = afterFixture
	e.register("apple_health", appleShapedSteps)
	lines := fixtureLines(t, "dst_steps.ndjson")
	require.Len(t, lines, 55)
	job := e.upload(e.tenant, e.user, "apple_health", "step_count", lines...)

	want := []daySteps{{"2026-10-31", 10}, {"2026-11-01", 500}, {"2026-11-02", 40}}

	e.process(job)
	assert.Equal(t, want, e.dailySteps("apple_health"))
	samples, summaries := e.count("quantity_samples"), e.count("daily_summaries")
	assert.Equal(t, 55, samples)
	assert.Equal(t, 3, summaries)

	e.process(job)
	assert.Equal(t, want, e.dailySteps("apple_health"))
	assert.Equal(t, samples, e.count("quantity_samples"))
	assert.Equal(t, summaries, e.count("daily_summaries"))
}

// A later upload for the same day recomputes the derived summary (latest wins).
func TestDerivedSummaryIsRecomputed(t *testing.T) {
	e := newEnv(t)
	e.now = afterFixture
	e.register("apple_health", appleShapedSteps)
	lines := fixtureLines(t, "dst_steps.ndjson")
	e.process(e.upload(e.tenant, e.user, "apple_health", "step_count", lines[:10]...))
	assert.Equal(t, []daySteps{{"2026-10-31", 10}, {"2026-11-01", 90}}, e.dailySteps("apple_health"))
	e.process(e.upload(e.tenant, e.user, "apple_health", "step_count", lines[10:]...))
	assert.Equal(t, []daySteps{{"2026-10-31", 10}, {"2026-11-01", 500}, {"2026-11-02", 40}}, e.dailySteps("apple_health"))
	assert.Equal(t, 3, e.count("daily_summaries"))
}

// A provider that sends its own daily summary for a day keeps it: no derived
// row is written for that day, and one written earlier is removed.
func TestDerivedSummaryYieldsToProviderSummary(t *testing.T) {
	e := newEnv(t)
	e.now = afterFixture
	e.register("apple_health", appleShapedSteps)
	lines := fixtureLines(t, "dst_steps.ndjson")
	e.process(e.upload(e.tenant, e.user, "apple_health", "step_count", lines...))

	own := 4321
	e.register("dev", func(r connector.RawRecord) (model.MapResult, error) {
		mr, err := appleShapedSteps(r)
		mr.DailySummaries = []model.DailySummary{{
			UserID: r.UserID, Provider: "dev", SummaryDate: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			StepCount: &own, ContentHash: "dev-day",
		}}
		return mr, err
	})
	// The same samples through a provider that ships its own summary for 11-01.
	e.process(e.upload(e.tenant, e.user, "dev", "step_count", lines[1:3]...))
	assert.Equal(t, []daySteps{{"2026-11-01", own}}, e.dailySteps("dev"))

	// And a provider summary arriving after a derived one replaces it.
	e.register("late", func(r connector.RawRecord) (model.MapResult, error) {
		if _, ok := r.Fields["summary"]; ok {
			return model.MapResult{DailySummaries: []model.DailySummary{{
				UserID: r.UserID, Provider: "late", SummaryDate: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
				StepCount: &own, ContentHash: "late-day",
			}}}, nil
		}
		return appleShapedSteps(r)
	})
	e.process(e.upload(e.tenant, e.user, "late", "step_count", lines[1:3]...))
	assert.Equal(t, []daySteps{{"2026-11-01", 20}}, e.dailySteps("late"))
	e.process(e.upload(e.tenant, e.user, "late", "daily", `{"summary":true}`))
	assert.Equal(t, []daySteps{{"2026-11-01", own}}, e.dailySteps("late"))
}

// Two jobs carrying parts of one batch for the same days, processed at once:
// the derived summary must equal the total of both, not the last job's view.
func TestDerivedSummaryConcurrentJobs(t *testing.T) {
	e := newEnv(t)
	e.now = afterFixture
	e.register("apple_health", appleShapedSteps)
	lines := fixtureLines(t, "dst_steps.ndjson")
	for round := range 3 {
		// A fresh user per round so every round starts empty.
		e.user = e.newUser(e.tenant)
		a := e.upload(e.tenant, e.user, "apple_health", "step_count", lines[:28]...)
		b := e.upload(e.tenant, e.user, "apple_health", "step_count", lines[28:]...)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, job := range []queue.ETLJob{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = ProcessWith(context.Background(), e.pool, e.rs, job, e.opts())
			}()
		}
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		assert.Equal(t, []daySteps{{"2026-10-31", 10}, {"2026-11-01", 500}, {"2026-11-02", 40}},
			e.dailySteps("apple_health"), "round %d", round)
	}
}

// Two sources of one provider count the same steps: the day keeps the larger
// source total, not their sum.
func TestDerivedSummaryTakesTheLargestSource(t *testing.T) {
	e := newEnv(t)
	e.register("apple_health", quantityMap)
	at := time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	step := func(v float64, src string, m int) string {
		return line(map[string]any{"type": "step_count", "value": v, "unit": "count",
			"start": at.Add(time.Duration(m) * time.Minute).Format(time.RFC3339), "source": src, "tz": "America/Los_Angeles"})
	}
	e.process(e.upload(e.tenant, e.user, "apple_health", "step_count",
		step(600, "com.apple.health.phone", 0), step(400, "com.apple.health.phone", 30),
		step(700, "com.apple.health.watch", 0), step(500, "com.apple.health.watch", 30),
	))
	assert.Equal(t, []daySteps{{"2026-09-30", 1200}}, e.dailySteps("apple_health"))
}
