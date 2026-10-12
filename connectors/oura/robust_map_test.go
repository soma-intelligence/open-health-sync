// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// allFixtureLines returns every document of every recorded answer, by data
// type.
func allFixtureLines(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, dt := range AllDataTypes {
		var p struct {
			Data []json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(ourafake.File(dt+".json"), &p))
		for _, d := range p.Data {
			out[dt] = append(out[dt], string(d))
		}
	}
	return out
}

// schemaOf returns the document schema of a data type's endpoint.
func schemaOf(t *testing.T, d *schemaDoc, dataType string) map[string]any {
	t.Helper()
	page := d.Components.Schemas[strings.TrimPrefix(d.Endpoints[dataType].Schema, "#/components/schemas/")].(map[string]any)
	items := page["properties"].(map[string]any)["data"].(map[string]any)["items"].(map[string]any)
	return d.Components.Schemas[strings.TrimPrefix(items["$ref"].(string), "#/components/schemas/")].(map[string]any)
}

// Every field the spec marks optional may be null or missing, in every data
// type: Map never fails on one (it stores less).
func TestMap_NullAndMissingOptionalFields(t *testing.T) {
	d := loadSchemaDoc(t)
	for dt, lines := range allFixtureLines(t) {
		s := schemaOf(t, d, dt)
		required := map[string]bool{}
		for _, r := range s["required"].([]any) {
			required[r.(string)] = true
		}
		for _, line := range lines {
			var doc map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &doc))
			for field := range s["properties"].(map[string]any) {
				if required[field] {
					continue
				}
				for _, variant := range []string{"null", "missing"} {
					m := map[string]any{}
					for k, v := range doc {
						m[k] = v
					}
					if variant == "null" {
						m[field] = nil
					} else {
						delete(m, field)
					}
					b, _ := json.Marshal(m)
					_, err := mapLine(t, dt, string(b))
					assert.NoError(t, err, "%s: %s %s", dt, field, variant)
				}
			}
			// All optional fields null at once.
			m := map[string]any{}
			for k, v := range doc {
				if required[k] {
					m[k] = v
				} else {
					m[k] = nil
				}
			}
			b, _ := json.Marshal(m)
			_, err := mapLine(t, dt, string(b))
			assert.NoError(t, err, "%s: every optional field null", dt)
		}
	}
}

// A null contributor is no value: no readiness_hrv_balance sample, and no
// zero in metadata.
func TestMapDailyReadiness_NullContributors(t *testing.T) {
	line := edit(t, fixture(t, DataTypeDailyReadiness, 0), map[string]any{
		"contributors": map[string]any{"hrv_balance": nil, "activity_balance": nil, "sleep_balance": 70},
	})
	res := mustMap(t, DataTypeDailyReadiness, line)
	got := byType(res.QuantitySamples)
	assert.NotContains(t, got, model.QTReadinessHRVBalance)
	assert.Contains(t, got, model.QTReadinessScore)
	c := got[model.QTReadinessScore].Metadata["contributors"].(map[string]any)
	assert.Equal(t, map[string]any{"sleep_balance": 70.0}, c)

	line = edit(t, fixture(t, DataTypeDailySleep, 0), map[string]any{"contributors": map[string]any{"timing": nil}})
	s := mustMap(t, DataTypeDailySleep, line).QuantitySamples[0]
	assert.NotContains(t, s.Metadata, "contributors")
}

// Enum values Oura may add later are stored, not refused.
func TestMap_UnknownEnumValues(t *testing.T) {
	sleep := mustMap(t, DataTypeSleep, edit(t, fixture(t, DataTypeSleep, 1), map[string]any{"type": "siesta", "sleep_algorithm_version": "v9"}))
	require.Len(t, sleep.SleepSessions, 1)
	assert.Equal(t, "siesta", sleep.SleepSessions[0].Metadata["oura_type"])
	assert.Empty(t, sleep.QuantitySamples, "only a long_sleep gives the night's samples")
	assert.Empty(t, sleep.Deletions)

	w := mustMap(t, DataTypeWorkout, edit(t, fixture(t, DataTypeWorkout, 0), map[string]any{
		"activity": "underwaterHockey", "intensity": "extreme", "source": "telepathy",
	}))
	require.Len(t, w.Workouts, 1)
	assert.Equal(t, "other", w.Workouts[0].ActivityType)
	assert.Equal(t, "underwaterHockey", w.Workouts[0].Metadata["oura_activity"])
	assert.Equal(t, "extreme", w.Workouts[0].Metadata["intensity"])

	hr := mustMap(t, DataTypeHeartRate, `{"bpm":61,"source":"teleport","timestamp":"2026-03-01T10:00:00+00:00","timestamp_unix":1772359200000}`)
	require.Len(t, hr.QuantitySamples, 1)
	assert.Equal(t, "teleport", hr.QuantitySamples[0].Metadata["oura_source"])

	for _, typ := range []string{"nap", "rest", "body_status", "unknown_moment", ""} {
		assert.True(t, isEmpty(mustMap(t, DataTypeSession, edit(t, fixture(t, DataTypeSession, 0), map[string]any{"type": typ}))), typ)
	}
	ses := mustMap(t, DataTypeSession, edit(t, fixture(t, DataTypeSession, 0), map[string]any{"mood": "ecstatic"}))
	assert.Equal(t, "ecstatic", ses.CategorySamples[0].Metadata["mood"])

	st := mustMap(t, DataTypeDailyStress, edit(t, fixture(t, DataTypeDailyStress, 0), map[string]any{"day_summary": "zen"}))
	assert.Equal(t, "zen", st.QuantitySamples[0].Metadata["day_summary"])
}

// A timestamp or day outside the years 2000 to 2199 is an error, not a record
// that could not even be encoded (found by FuzzMapDailySleep and others).
func TestMap_OutOfRangeTimes(t *testing.T) {
	for _, tc := range []struct{ dataType, line string }{
		{DataTypeDailySleep, edit(t, fixture(t, DataTypeDailySleep, 0), map[string]any{"day": "0000-01-01", "timestamp": "0000-01-01T00:00:00+01:00"})},
		{DataTypeDailyReadiness, edit(t, fixture(t, DataTypeDailyReadiness, 0), map[string]any{"day": "1999-12-31"})},
		{DataTypeVO2Max, edit(t, fixture(t, DataTypeVO2Max, 0), map[string]any{"day": "2200-01-01", "timestamp": "2200-01-01T00:00:00+00:00"})},
		{DataTypeWorkout, edit(t, fixture(t, DataTypeWorkout, 0), map[string]any{"start_datetime": "0000-01-01T00:00:00+01:00"})},
		{DataTypeSession, edit(t, fixture(t, DataTypeSession, 0), map[string]any{"end_datetime": "9999-12-31T23:59:59+00:00"})},
		{DataTypeSleep, edit(t, fixture(t, DataTypeSleep, 1), map[string]any{"bedtime_end": "9999-12-31T23:59:59Z"})},
		{DataTypeHeartRate, `{"bpm":60,"source":"awake","timestamp_unix":270000000000000}`},
		{DataTypeHeartRate, `{"bpm":60,"source":"awake","timestamp":"1970-01-01T00:00:00+00:00"}`},
	} {
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(tc.line), &fields))
		_, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: tc.dataType, Fields: fields})
		assert.ErrorContains(t, err, "out of range", tc.dataType)
	}
	edge := mustMap(t, DataTypeHeartRate, `{"bpm":60,"source":"awake","timestamp":"2000-01-01T00:00:00+00:00"}`)
	assert.Len(t, edge.QuantitySamples, 1, "2000 itself is in range")
}

// A sleep period of no length is nothing to store, and no error.
func TestMapSleep_ZeroLength(t *testing.T) {
	line := edit(t, fixture(t, DataTypeSleep, 1), map[string]any{
		"bedtime_start": "2026-03-02T07:00:00+01:00", "bedtime_end": "2026-03-02T07:00:00+01:00",
	})
	res := mustMap(t, DataTypeSleep, line)
	assert.True(t, res.IsEmpty())

	line = edit(t, fixture(t, DataTypeSleep, 1), map[string]any{"bedtime_end": "2026-03-01T22:00:00+01:00"})
	_, err := mapLine(t, DataTypeSleep, line)
	assert.Error(t, err, "an end before the start is malformed")
}

// checkPhases asserts the invariants of a session's phases: inside the
// period, in order, without overlap, each of a known stage and positive
// length.
func checkPhases(t *testing.T, s model.SleepSession, phases []model.SleepPhase) {
	t.Helper()
	known := map[string]bool{model.SleepStageDeep: true, model.SleepStageLight: true, model.SleepStageREM: true, model.SleepStageAwake: true}
	prev := s.BedtimeStart
	total := 0
	for _, p := range phases {
		assert.True(t, known[p.Phase], p.Phase)
		assert.False(t, p.StartDate.Before(prev), "in order, no overlap")
		assert.True(t, p.EndDate.After(p.StartDate), "positive length")
		assert.False(t, p.EndDate.After(s.BedtimeEnd), "inside the period")
		assert.Equal(t, int(p.EndDate.Sub(p.StartDate).Seconds()), p.DurationS)
		assert.Equal(t, s.ContentHash, p.ParentContentHash)
		total += p.DurationS
		prev = p.EndDate
	}
	assert.LessOrEqual(t, total, int(s.BedtimeEnd.Sub(s.BedtimeStart).Seconds()))
}

// Hypnograms of odd lengths and odd characters give sound phases.
func TestMapSleep_OddHypnograms(t *testing.T) {
	period := func(steps string) (model.SleepSession, []model.SleepPhase) {
		line := edit(t, fixture(t, DataTypeSleep, 2), map[string]any{"sleep_phase_5_min": steps}) // a 30 minute nap
		res := mustMap(t, DataTypeSleep, line)
		require.Len(t, res.SleepSessions, 1)
		return res.SleepSessions[0], res.SleepPhases
	}
	cases := map[string]int{ // steps -> phases
		"":                        0,
		"2":                       1,
		"222222":                  1,
		"2222222222":              1, // longer than the period: clipped
		"1234":                    4,
		"12x34":                   4, // an unknown step is dropped and breaks the run
		"22x22":                   2,
		"xxxx":                    0,
		"0":                       0,
		"2ü2":                     2, // a multibyte character: dropped bytes
		"   ":                     0,
		strings.Repeat("3", 1000): 1,
	}
	for steps, want := range cases {
		s, phases := period(steps)
		assert.Len(t, phases, want, "%q", steps)
		checkPhases(t, s, phases)
	}
	_, phases := period("2222222222")
	assert.Equal(t, 30*60, phases[0].DurationS, "clipped at the period's end")
}

// A night across midnight, and the long_sleep, sleep and late_nap types.
func TestMapSleep_TypesAndMidnight(t *testing.T) {
	night := mustMap(t, DataTypeSleep, fixture(t, DataTypeSleep, 1))
	s := night.SleepSessions[0]
	assert.Equal(t, "2026-03-01", s.BedtimeStart.In(paris(t)).Format(time.DateOnly))
	assert.Equal(t, "2026-03-02", s.BedtimeEnd.In(paris(t)).Format(time.DateOnly))
	assert.Equal(t, "2026-03-02", s.Metadata["day"], "Oura's day is the wake-up day")
	checkPhases(t, s, night.SleepPhases)
	assert.Len(t, night.QuantitySamples, 2, "long_sleep: resting heart rate and respiratory rate")

	for _, typ := range []string{"sleep", "late_nap"} {
		res := mustMap(t, DataTypeSleep, edit(t, fixture(t, DataTypeSleep, 1), map[string]any{"type": typ}))
		require.Len(t, res.SleepSessions, 1, typ)
		assert.Empty(t, res.QuantitySamples, "%s: a nap does not give the day's resting heart rate", typ)
		assert.Equal(t, s.ContentHash, res.SleepSessions[0].ContentHash, "the type is not part of the identity")
	}
}

// deletion and rest periods: a deletion of the Oura id, nothing else, even
// with every figure present.
func TestMapSleep_RemovedPeriodsAreOnlyDeletions(t *testing.T) {
	for _, typ := range []string{"deleted", "rest"} {
		res := mustMap(t, DataTypeSleep, edit(t, fixture(t, DataTypeSleep, 1), map[string]any{"type": typ}))
		assert.Equal(t, []model.Deletion{{Provider: Slug, ExternalID: "sleep-0001"}}, res.Deletions, typ)
		assert.Empty(t, res.SleepSessions)
		assert.Empty(t, res.SleepPhases)
		assert.Empty(t, res.QuantitySamples)
		// A removed period needs no valid times.
		res = mustMap(t, DataTypeSleep, `{"id":"x","type":"`+typ+`","bedtime_start":"","bedtime_end":"garbage"}`)
		assert.Len(t, res.Deletions, 1)
	}
}

// A re-scored night (new figures, new hypnogram, same Oura id and start)
// keeps its identity: the stored session, phases and samples are replaced.
func TestMapSleep_RescoredKeepsIdentity(t *testing.T) {
	a := mustMap(t, DataTypeSleep, fixture(t, DataTypeSleep, 1))
	b := mustMap(t, DataTypeSleep, edit(t, fixture(t, DataTypeSleep, 1), map[string]any{
		"total_sleep_duration": 26000, "lowest_heart_rate": 48, "average_breath": 14.5,
		"sleep_phase_5_min": "4422222222111111", "bedtime_end": "2026-03-02T07:30:00+01:00",
	}))
	assert.Equal(t, a.SleepSessions[0].ContentHash, b.SleepSessions[0].ContentHash)
	assert.True(t, b.SleepSessions[0].Replaceable)
	ah, bh := byType(a.QuantitySamples), byType(b.QuantitySamples)
	for _, qt := range []string{model.QTRestingHeartRate, model.QTRespiratoryRate} {
		assert.Equal(t, ah[qt].ContentHash, bh[qt].ContentHash, qt)
		assert.True(t, bh[qt].Replaceable)
		assert.NotEqual(t, ah[qt].Value, bh[qt].Value)
	}
	assert.Equal(t, 48.0, bh[model.QTRestingHeartRate].Value)
}

// Offsets that are not whole hours, and daylight saving changes: each record
// lands on the user's local day.
func TestMap_OffsetsAndLocalDays(t *testing.T) {
	type check struct {
		dataType, line string
		zone           string
		localDay       string // the local day of the record's start
	}
	sleep := func(start, end, day string) string {
		return edit(t, fixture(t, DataTypeSleep, 1), map[string]any{"bedtime_start": start, "bedtime_end": end, "day": day, "sleep_phase_5_min": "2222"})
	}
	activity := func(day, ts string) string {
		return edit(t, fixture(t, DataTypeDailyActivity, 0), map[string]any{"day": day, "timestamp": ts})
	}
	workout := func(start, end string) string {
		return edit(t, fixture(t, DataTypeWorkout, 0), map[string]any{"start_datetime": start, "end_datetime": end})
	}
	session := func(start, end string) string {
		return edit(t, fixture(t, DataTypeSession, 0), map[string]any{"start_datetime": start, "end_datetime": end})
	}
	cases := []check{
		{DataTypeSleep, sleep("2026-03-01T23:30:00+05:30", "2026-03-02T06:30:00+05:30", "2026-03-02"), "Asia/Kolkata", "2026-03-01"},
		{DataTypeSleep, sleep("2026-03-01T23:45:00+05:45", "2026-03-02T06:00:00+05:45", "2026-03-02"), "Asia/Kathmandu", "2026-03-01"},
		{DataTypeSleep, sleep("2026-03-01T22:00:00-03:30", "2026-03-02T06:00:00-03:30", "2026-03-02"), "America/St_Johns", "2026-03-01"},
		// Europe goes to summer time at 01:00 UTC on 29 March 2026.
		{DataTypeSleep, sleep("2026-03-28T23:30:00+01:00", "2026-03-29T07:30:00+02:00", "2026-03-29"), "Etc/GMT-1", "2026-03-28"},
		{DataTypeDailyActivity, activity("2026-03-01", "2026-03-01T04:00:00+05:30"), "Asia/Kolkata", "2026-03-01"},
		{DataTypeDailyActivity, activity("2026-03-01", "2026-03-01T04:00:00-03:30"), "America/St_Johns", "2026-03-01"},
		{DataTypeDailyActivity, activity("2026-03-01", "2026-03-01T04:00:00+05:45"), "Asia/Kathmandu", "2026-03-01"},
		{DataTypeDailyActivity, activity("2026-03-29", "2026-03-29T04:00:00+02:00"), "Etc/GMT-2", "2026-03-29"},
		{DataTypeDailyActivity, activity("2026-03-01", "2026-03-01T04:00:00+14:00"), "Etc/GMT-14", "2026-03-01"},
		{DataTypeDailyActivity, activity("2026-03-01", "2026-03-01T04:00:00-12:00"), "Etc/GMT+12", "2026-03-01"},
		{DataTypeWorkout, workout("2026-03-01T23:50:00-03:30", "2026-03-02T00:20:00-03:30"), "America/St_Johns", "2026-03-01"},
		{DataTypeSession, session("2026-03-01T00:05:00+05:45", "2026-03-01T00:15:00+05:45"), "Asia/Kathmandu", "2026-03-01"},
	}
	for i, c := range cases {
		res := mustMap(t, c.dataType, c.line)
		var start time.Time
		var md map[string]any
		switch {
		case len(res.SleepSessions) > 0:
			start, md = res.SleepSessions[0].BedtimeStart, res.SleepSessions[0].Metadata
		case len(res.Workouts) > 0:
			start, md = res.Workouts[0].StartDate, res.Workouts[0].Metadata
		case len(res.CategorySamples) > 0:
			start, md = res.CategorySamples[0].StartDate, res.CategorySamples[0].Metadata
		default:
			require.NotEmpty(t, res.QuantitySamples, i)
			start, md = res.QuantitySamples[0].StartDate, res.QuantitySamples[0].Metadata
			// A daily figure spans its local day exactly.
			end := res.QuantitySamples[0].EndDate
			assert.Equal(t, 24*time.Hour-time.Second, end.Sub(start), "case %d", i)
		}
		require.Equal(t, c.zone, md["timezone"], "case %d", i)
		loc, err := time.LoadLocation(c.zone)
		require.NoError(t, err)
		assert.Equal(t, c.localDay, start.In(loc).Format(time.DateOnly), "case %d", i)
		assert.Equal(t, time.UTC, start.Location(), "stored in UTC, case %d", i)
	}

	// The night across the change of time lasts 7 hours, not 8.
	res := mustMap(t, DataTypeSleep, cases[3].line)
	s := res.SleepSessions[0]
	assert.Equal(t, 7*time.Hour, s.BedtimeEnd.Sub(s.BedtimeStart))
}

// Mapping is pure: the same line mapped twice, by two connectors, or with its
// keys in another order, gives identical records and content hashes.
func TestMap_Idempotent(t *testing.T) {
	webhookLine := `{"event_type":"delete","data_type":"workout","object_id":"workout-0001","event_time":"2026-03-03T11:59:00+00:00","user_id":"u"}`
	lines := allFixtureLines(t)
	lines[DataTypeWebhook] = []string{webhookLine}
	for dt, ls := range lines {
		for _, line := range ls {
			a := mustMap(t, dt, line)
			b := mustMap(t, dt, line)
			assert.True(t, reflect.DeepEqual(a, b), "%s: %s", dt, line)

			// Keys reversed.
			var m map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(line), &m))
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Sort(sort.Reverse(sort.StringSlice(keys)))
			var sb strings.Builder
			sb.WriteString("{")
			for i, k := range keys {
				if i > 0 {
					sb.WriteString(",")
				}
				fmt.Fprintf(&sb, "%q:%s", k, m[k])
			}
			sb.WriteString("}")
			c := mustMap(t, dt, sb.String())
			assert.Equal(t, hashes(a), hashes(c), "%s: key order", dt)
		}
	}
}

// hashes lists the content hashes of a result, in order.
func hashes(r model.MapResult) []string {
	var out []string
	for _, s := range r.QuantitySamples {
		out = append(out, s.ContentHash)
	}
	for _, s := range r.CategorySamples {
		out = append(out, s.ContentHash)
	}
	for _, s := range r.SleepSessions {
		out = append(out, s.ContentHash)
	}
	for _, p := range r.SleepPhases {
		out = append(out, p.ParentContentHash+p.StartDate.String()+p.Phase)
	}
	for _, w := range r.Workouts {
		out = append(out, w.ContentHash)
	}
	for _, d := range r.Deletions {
		out = append(out, "del:"+d.ExternalID)
	}
	return out
}

// Map reads the line the ETL hands it: the JSON decoded into a map, numbers
// as float64. Large integers and fractional seconds survive that.
func TestMap_ETLDecodedFields(t *testing.T) {
	line := `{"bpm":61,"source":"awake","timestamp":"2026-03-01T10:00:00.123+00:00","timestamp_unix":1772359200123}`
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeHeartRate, Fields: fields})
	require.NoError(t, err)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, time.Date(2026, 3, 1, 10, 0, 0, 123e6, time.UTC), res.QuantitySamples[0].StartDate)

	// Only timestamp_unix.
	res = mustMap(t, DataTypeHeartRate, `{"bpm":61,"source":"awake","timestamp_unix":1772359200000}`)
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC), res.QuantitySamples[0].StartDate)
	_, err = mapLine(t, DataTypeHeartRate, `{"bpm":61,"source":"awake"}`)
	assert.Error(t, err, "no time at all")
}

// Every record of every fixture carries the fields the ETL keys on.
func TestMap_RecordsAreComplete(t *testing.T) {
	for dt, ls := range allFixtureLines(t) {
		for _, line := range ls {
			res := mustMap(t, dt, line)
			for _, s := range res.QuantitySamples {
				_, _, err := model.ToStorageUnit(s.QuantityType, s.Value, s.Unit)
				assert.NoError(t, err, "%s %s: a canonical name in a convertible unit", dt, s.QuantityType)
			}
			for _, s := range res.QuantitySamples {
				assert.NotEmpty(t, s.ExternalID, dt)
				assert.False(t, s.EndDate.Before(s.StartDate), dt)
			}
			for _, s := range res.SleepSessions {
				assert.NotEmpty(t, s.ContentHash)
				assert.NotEmpty(t, s.ExternalID)
				assert.Equal(t, testUserID, s.UserID)
				checkPhases(t, s, res.SleepPhases)
			}
			for _, w := range res.Workouts {
				assert.NotEmpty(t, w.ContentHash)
				assert.NotEmpty(t, w.ExternalID)
				assert.Equal(t, testUserID, w.UserID)
			}
			for _, c := range res.CategorySamples {
				assert.NotEmpty(t, c.ContentHash)
				assert.NotEmpty(t, c.ExternalID)
			}
		}
	}
}
