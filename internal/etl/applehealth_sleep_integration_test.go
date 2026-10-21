// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// HealthKit sleep analysis values, as the SDK uploads them.
const (
	hkInBed = 0
	hkAwake = 2
	hkCore  = 3
	hkDeep  = 4
	hkREM   = 5
)

// sleepLine is one sleep analysis sample in the SDK's upload shape.
func sleepLine(value int, start, end string) string {
	return line(map[string]any{
		"categoryType": "HKCategoryTypeIdentifierSleepAnalysis",
		"value":        value,
		"startDate":    start,
		"endDate":      end,
		"metadata":     map[string]any{"HKTimeZone": "Europe/Paris"},
		"sourceName":   "Apple Watch",
		"device":       "Apple Watch",
	})
}

// Two nights and a nap of one person in Paris (UTC+1), filed by the SDK and
// the export converter under the local day each sample starts on. Each night
// starts in one part and ends in the next one.
var (
	night1InBed = sleepLine(hkInBed, "2026-01-10T21:30:00Z", "2026-01-11T05:45:00Z")
	night1      = []string{
		sleepLine(hkCore, "2026-01-10T21:45:00Z", "2026-01-10T22:45:00Z"), // 3600 s, Paris 22:45
		sleepLine(hkDeep, "2026-01-10T22:45:00Z", "2026-01-10T23:30:00Z"), // 2700 s, Paris 23:45
		sleepLine(hkREM, "2026-01-10T23:30:00Z", "2026-01-11T00:15:00Z"),  // 2700 s, Paris 00:30, next day
		sleepLine(hkAwake, "2026-01-11T00:15:00Z", "2026-01-11T00:25:00Z"),
		sleepLine(hkCore, "2026-01-11T00:25:00Z", "2026-01-11T03:25:00Z"), // 10800 s
		sleepLine(hkREM, "2026-01-11T03:25:00Z", "2026-01-11T04:25:00Z"),  // 3600 s
		sleepLine(hkCore, "2026-01-11T04:25:00Z", "2026-01-11T05:30:00Z"), // 3900 s
	}
	nap         = sleepLine(hkCore, "2026-01-11T13:00:00Z", "2026-01-11T13:40:00Z") // 2400 s
	night2InBed = sleepLine(hkInBed, "2026-01-11T22:00:00Z", "2026-01-12T06:00:00Z")
	night2      = []string{
		sleepLine(hkCore, "2026-01-11T22:10:00Z", "2026-01-11T23:00:00Z"), // 3000 s, Paris 23:10
		sleepLine(hkDeep, "2026-01-11T23:00:00Z", "2026-01-12T00:30:00Z"), // 5400 s, Paris 00:00, next day
		sleepLine(hkCore, "2026-01-12T00:30:00Z", "2026-01-12T05:50:00Z"), // 19200 s
	}
)

// dayParts files the nights as the SDK does: one part per local start day.
// Without inBed, the iPhone recorded no time in bed (a watch alone).
func dayParts(inBed bool) [][]string {
	opt := func(l string) []string {
		if inBed {
			return []string{l}
		}
		return nil
	}
	return [][]string{
		slices.Concat(opt(night1InBed), night1[:2]), // 2026-01-10
		slices.Concat(night1[2:], []string{nap}, opt(night2InBed), // 2026-01-11
			night2[:1]),
		night2[1:], // 2026-01-12
	}
}

type storedNight struct {
	start, end                     time.Time
	total, deep, rem, light, awake *int
	phases, stagePhases            int
}

// nights returns the stored sleep sessions, oldest first, with their phases.
func (e *env) nights() []storedNight {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `
		SELECT s.bedtime_start, s.bedtime_end, s.total_sleep_seconds, s.deep_sleep_seconds,
		       s.rem_sleep_seconds, s.light_sleep_seconds, s.awake_seconds,
		       (SELECT count(*) FROM ohs_data.sleep_phases p WHERE p.session_id = s.id),
		       (SELECT count(*) FROM ohs_data.sleep_phases p WHERE p.session_id = s.id AND p.phase <> 'in_bed')
		FROM ohs_data.sleep_sessions s
		WHERE s.tenant_id = $1 AND s.user_id = $2
		ORDER BY s.bedtime_start`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []storedNight
	for rows.Next() {
		var n storedNight
		require.NoError(e.t, rows.Scan(&n.start, &n.end, &n.total, &n.deep, &n.rem, &n.light, &n.awake, &n.phases, &n.stagePhases))
		out = append(out, n)
	}
	require.NoError(e.t, rows.Err())
	return out
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// assertFigures checks a night's figures in seconds.
func assertFigures(t *testing.T, n storedNight, total, deep, rem, light, awake int) {
	t.Helper()
	for _, f := range []struct {
		name string
		got  *int
		want int
	}{{"total", n.total, total}, {"deep", n.deep, deep}, {"rem", n.rem, rem}, {"light", n.light, light}, {"awake", n.awake, awake}} {
		if assert.NotNil(t, f.got, "%s of the night starting %s", f.name, n.start) {
			assert.Equal(t, f.want, *f.got, "%s of the night starting %s", f.name, n.start)
		}
	}
}

// A night recorded "in bed" takes its figures from the stages inside it, as
// the Health app's "time asleep" does.
func TestAppleHealthSleep_InBedNightGetsTotals(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed", slices.Concat([]string{night1InBed}, night1)...))

	got := e.nights()
	require.Len(t, got, 1)
	assert.True(t, got[0].start.Equal(utc("2026-01-10T21:30:00Z")))
	assertFigures(t, got[0], 27300, 2700, 6300, 18300, 600)
	assert.Equal(t, 8, got[0].phases, "the in bed phase and the seven stages")
}

// Each night lands as one session with all its stages, whichever order the
// day parts are processed in and however often, with or without the
// iPhone's time in bed. The nap between the nights is a session of its own.
func TestAppleHealthSleep_NightsAcrossDayParts(t *testing.T) {
	orders := map[string][]int{"in order": {0, 1, 2}, "reversed": {2, 1, 0}, "middle first": {1, 2, 0}}
	for _, inBed := range []bool{true, false} {
		for name, order := range orders {
			label := name + ", watch only"
			if inBed {
				label = name + ", with time in bed"
			}
			t.Run(label, func(t *testing.T) {
				e := newEnv(t)
				e.reg.Register(applehealth.New())
				parts := dayParts(inBed)
				for round := range 2 { // the second round re-processes every part
					for _, i := range order {
						e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed", parts[i]...))
					}
					got := e.nights()
					require.Len(t, got, 3, "round %d: two nights and a nap", round)

					night, napS, night2S := got[0], got[1], got[2]
					assertFigures(t, night, 27300, 2700, 6300, 18300, 600)
					assertFigures(t, napS, 2400, 0, 0, 2400, 0)
					assertFigures(t, night2S, 27600, 5400, 0, 22200, 0)
					assert.Equal(t, 7, night.stagePhases)
					assert.Equal(t, 1, napS.stagePhases)
					assert.Equal(t, 3, night2S.stagePhases)
					assert.True(t, napS.start.Equal(utc("2026-01-11T13:00:00Z")))
					if inBed {
						assert.True(t, night.start.Equal(utc("2026-01-10T21:30:00Z")))
						assert.True(t, night.end.Equal(utc("2026-01-11T05:45:00Z")))
						assert.True(t, night2S.start.Equal(utc("2026-01-11T22:00:00Z")))
					} else {
						assert.True(t, night.start.Equal(utc("2026-01-10T21:45:00Z")), "the first stage")
						assert.True(t, night.end.Equal(utc("2026-01-11T05:30:00Z")), "the last stage")
						assert.True(t, night2S.start.Equal(utc("2026-01-11T22:10:00Z")))
						assert.True(t, night2S.end.Equal(utc("2026-01-12T05:50:00Z")))
					}
					assert.Equal(t, 11+boolInt(inBed)*2, e.count("sleep_phases"), "no phase stored twice")
				}
			})
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// The day parts of one night, processed at the same time by several workers,
// still make one session per night.
func TestAppleHealthSleep_PartsProcessedAtOnce(t *testing.T) {
	for _, inBed := range []bool{true, false} {
		e := newEnv(t)
		e.reg.Register(applehealth.New())
		parts := dayParts(inBed)
		jobs := make([]queue.ETLJob, len(parts))
		for i, p := range parts {
			jobs[i] = e.upload(e.tenant, e.user, applehealth.Slug, "mixed", p...)
		}
		errs := make(chan error, len(jobs))
		var wg sync.WaitGroup
		for _, j := range jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := ProcessWith(context.Background(), e.pool, e.rs, j, e.opts())
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		got := e.nights()
		require.Len(t, got, 3, "time in bed: %v", inBed)
		assertFigures(t, got[0], 27300, 2700, 6300, 18300, 600)
		assertFigures(t, got[2], 27600, 5400, 0, 22200, 0)
	}
}

// repairSleepPhasesSQL and repairSleepSessionsSQL are the repair of
// docs/connectors/apple-health.md#sleep, with the two ids as parameters. Keep
// them in step.
const (
	repairSleepPhasesSQL = `DELETE FROM ohs_data.sleep_phases p USING ohs_data.sleep_sessions s
WHERE p.tenant_id = s.tenant_id AND p.user_id = s.user_id AND p.session_id = s.id
  AND s.provider = 'apple_health' AND s.tenant_id = $1 AND s.user_id = $2`
	repairSleepSessionsSQL = `DELETE FROM ohs_data.sleep_sessions
WHERE provider = 'apple_health' AND tenant_id = $1 AND user_id = $2`
)

// Nights stored wrong by an earlier version (stages under the next night's
// session, no figures) are rebuilt by the documented repair: delete the
// user's Apple Health sleep, then re-process the raw files.
func TestAppleHealthSleep_RepairRecipe(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	var jobs []queue.ETLJob
	for _, p := range dayParts(true) {
		jobs = append(jobs, e.upload(e.tenant, e.user, applehealth.Slug, "mixed", p...))
		e.process(jobs[len(jobs)-1])
	}
	// What 0.1.0 stored: the first night's stages under the second one.
	e.exec(`UPDATE ohs_data.sleep_phases SET session_id = (SELECT max(id) FROM ohs_data.sleep_sessions WHERE tenant_id = $1)
		WHERE tenant_id = $1 AND phase <> 'in_bed' AND start_date < '2026-01-11T12:00:00Z'`, e.tenant)
	e.exec(`UPDATE ohs_data.sleep_sessions SET total_sleep_seconds = NULL WHERE tenant_id = $1`, e.tenant)

	e.exec(repairSleepPhasesSQL, e.tenant, e.user)
	e.exec(repairSleepSessionsSQL, e.tenant, e.user)
	assert.Equal(t, 0, e.count("sleep_sessions"))
	assert.Equal(t, 0, e.count("sleep_phases"))
	e.exec(reprocessUserSQL, e.tenant, e.user)
	for _, j := range jobs {
		e.process(j)
	}

	got := e.nights()
	require.Len(t, got, 3)
	assertFigures(t, got[0], 27300, 2700, 6300, 18300, 600)
	assertFigures(t, got[1], 2400, 0, 0, 2400, 0)
	assertFigures(t, got[2], 27600, 5400, 0, 22200, 0)
	assert.Equal(t, 13, e.count("sleep_phases"))
}
