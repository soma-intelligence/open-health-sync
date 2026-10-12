// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// allFixtureLines is every record line of the fixtures, by data type.
func allFixtureLines(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for dt, file := range map[string]string{
		DataTypeCycle: "cycles.json", DataTypeRecovery: "recoveries.json",
		DataTypeSleep: "sleeps.json", DataTypeWorkout: "workouts.json",
	} {
		var all struct {
			Records []json.RawMessage `json:"records"`
		}
		require.NoError(t, json.Unmarshal(whoopfake.File(file), &all))
		for _, r := range all.Records {
			out[dt] = append(out[dt], string(r))
		}
	}
	out[DataTypeBody] = []string{edit(t, string(whoopfake.File("body.json")), map[string]any{"snapshot_date": "2026-03-03"})}
	return out
}

// Map is pure: the same line maps to the same records, field for field, so
// a reprocessed raw object writes nothing new.
func TestMap_Idempotent(t *testing.T) {
	for dt, lines := range allFixtureLines(t) {
		for i, line := range lines {
			a, errA := mapLine(t, dt, line)
			b, errB := mapLine(t, dt, line)
			require.NoError(t, errA, "%s %d", dt, i)
			require.NoError(t, errB)
			assert.Equal(t, a, b, "%s %d", dt, i)
		}
	}
}

// Every record mapped from the fixtures passes the ETL's validation rules.
func TestMap_FixturesPassTheETLValidator(t *testing.T) {
	now := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	for dt, lines := range allFixtureLines(t) {
		for i, line := range lines {
			res := mustMap(t, dt, line)
			require.Empty(t, model.CanonicalizeUnits(&res), "%s %d", dt, i) // as the ETL does first
			assert.Empty(t, etl.ValidateMapResultAt(&res, now), "%s %d", dt, i)
		}
	}
}

// Null and missing score objects and figures give no figure and no error.
func TestMap_NullsAndMissingScores(t *testing.T) {
	const span = `"start":"2026-03-01T00:00:00.000Z","end":"2026-03-01T07:00:00.000Z","timezone_offset":"+01:00"`
	for dt, line := range map[string]string{
		DataTypeCycle:    `{"id":1,` + span + `,"score_state":"SCORED","score":null}`,
		DataTypeRecovery: `{"cycle_id":1,"sleep_id":"s","created_at":"2026-03-01T07:00:00.000Z","score_state":"SCORED","score":null}`,
	} {
		res := mustMap(t, dt, line)
		assert.True(t, res.IsEmpty(), dt)
	}
	res := mustMap(t, DataTypeCycle, `{"id":1,`+span+`,"score_state":"SCORED","score":{"strain":null,"kilojoule":null,"average_heart_rate":null,"max_heart_rate":null}}`)
	assert.True(t, res.IsEmpty())
	res = mustMap(t, DataTypeRecovery, `{"cycle_id":1,"sleep_id":"s","created_at":"2026-03-01T07:00:00.000Z","score_state":"SCORED","score":{"recovery_score":null,"hrv_rmssd_milli":42}}`)
	require.Len(t, res.QuantitySamples, 1)
	assert.Empty(t, res.DailySummaries)

	sl := mustMap(t, DataTypeSleep, `{"id":"s",`+span+`,"nap":false,"score_state":"SCORED","score":{"stage_summary":null,"sleep_needed":null,"respiratory_rate":null}}`)
	require.Len(t, sl.SleepSessions, 1)
	s := sl.SleepSessions[0]
	assert.Nil(t, s.TotalSleepSeconds)
	assert.Nil(t, s.DeepSleepSeconds)
	assert.Empty(t, sl.QuantitySamples)

	sl = mustMap(t, DataTypeSleep, `{"id":"s",`+span+`,"score_state":"SCORED","score":{"stage_summary":{"total_rem_sleep_time_milli":60000}}}`)
	assert.Equal(t, 60, *sl.SleepSessions[0].TotalSleepSeconds, "the stages given")

	wk := mustMap(t, DataTypeWorkout, `{"id":"w",`+span+`,"sport_name":"running","score_state":"SCORED","score":{"strain":null,"kilojoule":null,"distance_meter":null,"zone_durations":null}}`)
	require.Len(t, wk.Workouts, 1)
	assert.Nil(t, wk.Workouts[0].ActiveEnergyKcal)
	assert.Nil(t, wk.Workouts[0].DistanceM)
	assert.Empty(t, wk.QuantitySamples)

	body := mustMap(t, DataTypeBody, `{"height_meter":null,"weight_kilogram":null,"max_heart_rate":null,"snapshot_date":"2026-03-03"}`)
	assert.True(t, body.IsEmpty())
}

// SCORED figures only: a score object sent with another state is ignored.
func TestMap_ScoreStates(t *testing.T) {
	for _, state := range []string{"PENDING_SCORE", "UNSCORABLE", "", "SOMETHING_NEW"} {
		cy := mustMap(t, DataTypeCycle, `{"id":1,"start":"2026-03-01T00:00:00Z","score_state":"`+state+`","score":{"strain":5}}`)
		assert.True(t, cy.IsEmpty(), state)
		rc := mustMap(t, DataTypeRecovery, `{"cycle_id":1,"sleep_id":"s","created_at":"2026-03-01T07:00:00Z","score_state":"`+state+`","score":{"hrv_rmssd_milli":40}}`)
		assert.True(t, rc.IsEmpty(), state)
		sl := mustMap(t, DataTypeSleep, `{"id":"s","start":"2026-03-01T00:00:00Z","end":"2026-03-01T07:00:00Z","score_state":"`+state+`","score":{"respiratory_rate":15}}`)
		require.Len(t, sl.SleepSessions, 1, state)
		assert.Nil(t, sl.SleepSessions[0].RespiratoryRate, state)
		assert.Equal(t, state, sl.SleepSessions[0].Metadata["score_state"])
		wk := mustMap(t, DataTypeWorkout, `{"id":"w","start":"2026-03-01T00:00:00Z","end":"2026-03-01T01:00:00Z","sport_name":"running","score_state":"`+state+`","score":{"strain":5}}`)
		require.Len(t, wk.Workouts, 1, state)
		assert.Empty(t, wk.QuantitySamples, state)
	}
}

// PENDING_SCORE then SCORED: every record family keeps its identity, so the
// scored copy replaces (Replaceable) rather than adds.
func TestMap_PendingThenScoredKeepsIdentity(t *testing.T) {
	pendingCycle := mustMap(t, DataTypeCycle, `{"id":5,"start":"2026-03-01T00:00:00Z","score_state":"PENDING_SCORE","step_count":1200}`)
	scoredCycle := mustMap(t, DataTypeCycle, `{"id":5,"start":"2026-03-01T00:00:00Z","end":"2026-03-02T00:00:00Z","score_state":"SCORED","step_count":9000,"score":{"strain":9,"kilojoule":8000}}`)
	require.Len(t, pendingCycle.DailySummaries, 1, "the steps are there before the score")
	assert.Equal(t, pendingCycle.DailySummaries[0].ContentHash, scoredCycle.DailySummaries[0].ContentHash)
	assert.Equal(t, 1200, *pendingCycle.DailySummaries[0].StepCount)
	assert.Equal(t, 9000, *scoredCycle.DailySummaries[0].StepCount)
	assert.True(t, scoredCycle.DailySummaries[0].Replaceable)

	sleep := func(state, score string) model.MapResult {
		return mustMap(t, DataTypeSleep, `{"id":"s1","start":"2026-03-01T00:00:00Z","end":"2026-03-01T07:00:00Z","score_state":"`+state+`"`+score+`}`)
	}
	a, b := sleep("PENDING_SCORE", ""), sleep("SCORED", `,"score":{"respiratory_rate":15,"sleep_performance_percentage":80}`)
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
	c := sleep("SCORED", `,"score":{"respiratory_rate":16,"sleep_performance_percentage":85}`)
	assert.Equal(t, b.QuantitySamples[0].ContentHash, c.QuantitySamples[0].ContentHash, "a re-score replaces the respiratory rate")
	assert.True(t, c.QuantitySamples[0].Replaceable)
}

// The cycle's step_count is the day's step count in the WHOOP summary; null
// (no step data) and absurd values give nothing.
func TestMapCycle_StepCount(t *testing.T) {
	res := mustMap(t, DataTypeCycle, fixture(t, "cycles.json", 1))
	require.Len(t, res.DailySummaries, 1)
	assert.Equal(t, 10450, *res.DailySummaries[0].StepCount)
	for _, n := range []string{"null", "-5", "1e9"} {
		res := mustMap(t, DataTypeCycle, `{"id":1,"start":"2026-03-01T00:00:00Z","score_state":"PENDING_SCORE","step_count":`+n+`}`)
		assert.True(t, res.IsEmpty(), n)
	}
	res = mustMap(t, DataTypeCycle, fixture(t, "cycles.json", 2))
	assert.Nil(t, res.DailySummaries[0].StepCount, "step_count null in the fixture")
	for _, s := range res.QuantitySamples {
		assert.NotEqual(t, model.QTStepCount, s.QuantityType, "a cycle is not a calendar day")
	}
}

// An unknown sport (new names appear, sport_id is gone since 2025-09) is
// "other" with the name kept.
func TestMapWorkout_SportNamesAndIDs(t *testing.T) {
	for name, want := range map[string]string{
		"Hiking/Rucking": "hiking", "Track & Field": "running", "HIIT": "high_intensity_interval_training",
		"Stroller Jogging": "running", "Dog Walking": "walking", "Stadium Steps": "stair_climbing",
		"Ice Bath": "other", "Other": "other", "Activity": "other", "": "other",
	} {
		res := mustMap(t, DataTypeWorkout, `{"id":"w","start":"2026-03-01T00:00:00Z","end":"2026-03-01T01:00:00Z","sport_name":"`+name+`","score_state":"SCORED","score":{"strain":3}}`)
		assert.Equal(t, want, res.Workouts[0].ActivityType, name)
		assert.Equal(t, name, res.Workouts[0].Metadata["sport_name"])
		_, hasID := res.Workouts[0].Metadata["sport_id"]
		assert.False(t, hasID, "no sport_id sent, none stored")
	}
	res := mustMap(t, DataTypeWorkout, `{"id":"w","start":"2026-03-01T00:00:00Z","end":"2026-03-01T01:00:00Z","sport_name":"x","sport_id":-1,"score_state":"UNSCORABLE"}`)
	assert.EqualValues(t, -1, res.Workouts[0].Metadata["sport_id"])
	assert.Equal(t, "other", res.Workouts[0].ActivityType)
}

// A nap is a sleep session of its own, flagged, never merged into the night.
func TestMapSleep_NapIsItsOwnSession(t *testing.T) {
	night := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 0))
	nap := mustMap(t, DataTypeSleep, edit(t, fixture(t, "sleeps.json", 0), map[string]any{
		"id": "aaaaaaaa-0000-4000-8000-0000000000ff", "nap": true,
		"start": "2026-03-01T13:00:00.000Z", "end": "2026-03-01T13:40:00.000Z",
	}))
	assert.NotEqual(t, night.SleepSessions[0].ContentHash, nap.SleepSessions[0].ContentHash)
	assert.Equal(t, true, nap.SleepSessions[0].Metadata["nap"])
	assert.Equal(t, true, nap.QuantitySamples[0].Metadata["nap"])
}

// A sleep crossing midnight keeps its exact UTC times, and its zone names the
// local offset so the ETL dates it on the local day of its start.
func TestMapSleep_CrossesMidnight(t *testing.T) {
	res := mustMap(t, DataTypeSleep, `{"id":"s","start":"2026-03-01T21:30:00.000Z","end":"2026-03-02T05:00:00.000Z","timezone_offset":"-05:00","nap":false,"score_state":"PENDING_SCORE"}`)
	s := res.SleepSessions[0]
	assert.True(t, s.BedtimeStart.Equal(ts("2026-03-01T21:30:00Z")))
	assert.True(t, s.BedtimeEnd.Equal(ts("2026-03-02T05:00:00Z")))
	loc, err := time.LoadLocation(s.Metadata["timezone"].(string))
	require.NoError(t, err)
	assert.Equal(t, 1, s.BedtimeStart.In(loc).Day(), "16:30 local on the 1st")
	assert.Equal(t, 2, s.BedtimeEnd.In(loc).Day())
}

// Offsets that are not whole hours, and either side of a daylight saving
// change: the zone stored gives every record the local day WHOOP's offset
// gives it.
func TestMap_TimeZones(t *testing.T) {
	cases := []struct {
		offset, start string
		wantZone      string
		wantCycleDay  int // March
	}{
		{"+05:30", "2026-03-01T17:45:00.000Z", "Asia/Kolkata", 2},     // 23:15 local
		{"+05:45", "2026-03-01T18:00:00.000Z", "Asia/Kathmandu", 2},   // 23:45 local
		{"-03:30", "2026-03-02T02:00:00.000Z", "America/St_Johns", 2}, // 22:30 local on the 1st
		{"-05:00", "2026-03-08T04:00:00.000Z", "Etc/GMT+5", 8},        // US winter time: 23:00 on the 7th
		{"-04:00", "2026-03-09T03:30:00.000Z", "Etc/GMT+4", 9},        // US summer time: 23:30 on the 8th
		{"+12:45", "2026-03-01T10:00:00.000Z", "Pacific/Chatham", 2},  // 22:45 local... Chatham has +13:45 in March
	}
	for _, c := range cases {
		line := `{"id":1,"start":"` + c.start + `","timezone_offset":"` + c.offset + `","score_state":"SCORED","score":{"strain":4}}`
		res := mustMap(t, DataTypeCycle, line)
		st := res.QuantitySamples[0]
		zone, _ := st.Metadata["timezone"].(string)
		if c.offset == "+12:45" {
			assert.Empty(t, zone, "no listed zone has +12:45 in March: UTC, the offset kept")
			assert.Equal(t, "+12:45", st.Metadata["timezone_offset"])
			continue
		}
		assert.Equal(t, c.wantZone, zone, c.offset)
		loc, err := time.LoadLocation(zone)
		require.NoError(t, err)
		_, off := st.StartDate.In(loc).Zone()
		want, _ := offsetSeconds(c.offset)
		assert.Equal(t, want, off, "%s: the zone has WHOOP's offset at that instant", c.offset)
		assert.Equal(t, c.wantCycleDay, res.DailySummaries[0].SummaryDate.Day(), c.offset)
	}
}

// The current cycle has no end: null or absent, both read as open.
func TestMapCycle_NullOrMissingEnd(t *testing.T) {
	for _, end := range []string{`"end":null,`, ``, `"end":"",`} {
		res := mustMap(t, DataTypeCycle, `{"id":1,"start":"2026-03-01T00:00:00Z",`+end+`"score_state":"SCORED","score":{"strain":2}}`)
		s := res.QuantitySamples[0]
		assert.True(t, s.EndDate.Equal(s.StartDate), end)
		assert.Equal(t, true, s.Metadata["cycle_open"], end)
	}
}

// Recovery samples are stored under the sleep the recovery belongs to, which
// is how WHOOP names a recovery (and deletes it with its sleep).
func TestMapRecovery_KeyedBySleep(t *testing.T) {
	res := mustMap(t, DataTypeRecovery, fixture(t, "recoveries.json", 1))
	require.NotEmpty(t, res.QuantitySamples)
	const sleepID = "aaaaaaaa-0000-4000-8000-000000000001"
	for _, s := range res.QuantitySamples {
		assert.Equal(t, sleepID+"_recovery_"+s.QuantityType, s.ExternalID)
		assert.True(t, strings.HasPrefix(s.ExternalID, sleepID+"_"), "a deletion of the sleep covers it")
		assert.Equal(t, sleepID, s.Metadata["whoop_sleep_id"])
		assert.Equal(t, "93001", s.Metadata["whoop_cycle_id"])
	}
	// The sleep's own samples do not fall under the recovery's id.
	sl := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 0))
	assert.False(t, strings.HasPrefix(sl.QuantitySamples[0].ExternalID, sleepID+"_recovery"))

	// No sleep id (documented as always present): the cycle id stands in.
	res = mustMap(t, DataTypeRecovery, `{"cycle_id":7,"created_at":"2026-03-01T07:00:00Z","score_state":"SCORED","score":{"hrv_rmssd_milli":40}}`)
	assert.Equal(t, "cycle-7_recovery_hrv_rmssd", res.QuantitySamples[0].ExternalID)
}

// A deletion of a sleep id and of a recovery id: what each covers, as the
// ETL matches it (the id, or the id followed by "_").
func TestMapWebhook_DeletionCoverage(t *testing.T) {
	const sleepID = "aaaaaaaa-0000-4000-8000-000000000001"
	covers := func(deleted, externalID string) bool {
		return externalID == deleted || strings.HasPrefix(externalID, deleted+"_")
	}
	sleep := mustMap(t, DataTypeSleep, fixture(t, "sleeps.json", 0))
	rec := mustMap(t, DataTypeRecovery, fixture(t, "recoveries.json", 1))

	assert.True(t, covers(sleepID, sleep.SleepSessions[0].ExternalID))
	for _, s := range append(sleep.QuantitySamples, rec.QuantitySamples...) {
		assert.True(t, covers(sleepID, s.ExternalID), "sleep.deleted covers %s", s.ExternalID)
	}
	recoveryDeleted := deletedRecordID(EventRecoveryDeleted, sleepID)
	for _, s := range rec.QuantitySamples {
		assert.True(t, covers(recoveryDeleted, s.ExternalID), "recovery.deleted covers %s", s.ExternalID)
	}
	assert.False(t, covers(recoveryDeleted, sleep.SleepSessions[0].ExternalID), "recovery.deleted leaves the sleep")
	for _, s := range sleep.QuantitySamples {
		assert.False(t, covers(recoveryDeleted, s.ExternalID), "recovery.deleted leaves %s", s.ExternalID)
	}
}
