// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Every statement of the deletion path stays inside the job's tenant and
// user, and the ones that pick a provider record name its provider and id.
func TestDeletionStatementsScopedToTenantAndUser(t *testing.T) {
	all := map[string]string{
		"deletionTombstone": deletionTombstone,
		"deletionSleep":     deletionSleep,
		"deletionPhases":    deletionPhases,
		"deletionWorkout":   deletionWorkout,
		"deletionRoutes":    deletionRoutes,
		"deletionTracks":    deletionTracks,
		"deletionSamples":   deletionSamples,
		"deletionCategory":  deletionCategory,
		"deletionNutrition": deletionNutrition,
		"tombstonesAny":     tombstonesAny,
		"tombstonesOf":      tombstonesOf,
	}
	for name, q := range all {
		if name == "deletionTombstone" {
			assert.Contains(t, q, "(tenant_id, user_id, provider, external_id)", name)
			assert.Contains(t, q, "VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING", name)
			continue
		}
		assert.Contains(t, q, "tenant_id = $1 AND user_id = $2", name)
	}
	for name, q := range map[string]string{
		"deletionSleep": deletionSleep, "deletionWorkout": deletionWorkout, "deletionNutrition": deletionNutrition,
	} {
		assert.Contains(t, q, "provider = $3 AND external_id = $4", name)
	}
	for name, q := range map[string]string{"deletionSamples": deletionSamples, "deletionCategory": deletionCategory} {
		assert.Contains(t, q, "provider = $3", name)
		assert.NotContains(t, q, "start_date", "%s: no time window, the external id index bounds it", name)
		assert.Contains(t, q, `external_id COLLATE "C" >= $4 AND external_id COLLATE "C" < $6`, "%s: a range on the index", name)
		assert.Contains(t, q, "external_id = $4 OR starts_with(external_id, $5)", name)
		assert.NotContains(t, q, "LIKE", "%s: an id holding %% or _ must not act as a pattern", name)
	}
	for _, q := range []string{deletionSleep, deletionWorkout} {
		assert.True(t, strings.HasPrefix(strings.TrimSpace(q), "DELETE FROM ohs_data."))
		assert.Contains(t, q, "RETURNING id")
	}
}

// The range of deletionExternalMatch holds every "<id>_..." in byte order,
// which is what the "C" collation compares; starts_with then drops the other
// ids of the range.
func TestDeletionPrefixRangeIsByteOrder(t *testing.T) {
	id := "r-9"
	lo, hi := id, id+"`"
	match := func(s string) bool {
		return s >= lo && s < hi && (s == id || strings.HasPrefix(s, id+"_"))
	}
	for _, s := range []string{"r-9", "r-9_", "r-9_strain", "r-9_recovery_resting_heart_rate", "r-9_\xff"} {
		assert.True(t, s >= lo && s < hi, "in the range: %q", s)
		assert.True(t, match(s), s)
	}
	for _, s := range []string{"r-8_x", "r-9a", "r-9-2_x", "r-90_x", "r-9`", "r-:", "s"} {
		assert.False(t, match(s), s)
	}
}

func TestSampleRecordIDs(t *testing.T) {
	assert.Nil(t, sampleRecordIDs(""))
	assert.Equal(t, []string{"abc"}, sampleRecordIDs("abc"))
	assert.Equal(t, []string{"abc_resting_heart_rate", "abc", "abc_resting", "abc_resting_heart"},
		sampleRecordIDs("abc_resting_heart_rate"))
	// A leading underscore never yields an empty record id.
	assert.Equal(t, []string{"_x"}, sampleRecordIDs("_x"))
}

// A tombstoned parent takes its stamped children with it; with no parent of
// the family left, unstamped children go too. Other records stay.
func TestDropTombstonedRecords(t *testing.T) {
	dead := map[string]bool{"sleep-1": true, "w-1": true, "cat-1": true}
	isDead := func(_, id string) bool { return dead[id] }
	acc := &model.MapResult{
		SleepSessions: []model.SleepSession{{ExternalID: "sleep-1", ContentHash: "s1"}, {ExternalID: "sleep-2", ContentHash: "s2"}},
		SleepPhases: []model.SleepPhase{
			{ParentContentHash: "s1"}, {ParentContentHash: "s2"}, {ParentContentHash: ""},
		},
		Workouts:        []model.Workout{{ExternalID: "w-1", ContentHash: "w1"}},
		RoutePoints:     []model.RoutePoint{{ParentContentHash: "w1"}, {ParentContentHash: ""}},
		CategorySamples: []model.CategorySample{{ExternalID: "cat-1"}, {ExternalID: "cat-2"}},
		QuantitySamples: []model.QuantitySample{
			{ExternalID: "sleep-1_resting_heart_rate"}, {ExternalID: "sleep-2_resting_heart_rate"}, {ExternalID: ""},
		},
	}
	c := Counts{}
	dropTombstonedRecords(acc, isDead, c)

	assert.Len(t, acc.SleepSessions, 1)
	assert.Equal(t, "sleep-2", acc.SleepSessions[0].ExternalID)
	// The unstamped phase stays: sleep-2 is still there to receive it.
	assert.Equal(t, []model.SleepPhase{{ParentContentHash: "s2"}, {ParentContentHash: ""}}, acc.SleepPhases)
	assert.Empty(t, acc.Workouts)
	assert.Empty(t, acc.RoutePoints, "with the only workout gone, unstamped points must not attach elsewhere")
	assert.Equal(t, []model.CategorySample{{ExternalID: "cat-2"}}, acc.CategorySamples)
	assert.Len(t, acc.QuantitySamples, 2)
	assert.Equal(t, "sleep-2_resting_heart_rate", acc.QuantitySamples[0].ExternalID)

	assert.Equal(t, TableCount{Skipped: 1}, c[tSleep])
	assert.Equal(t, TableCount{Skipped: 1}, c[tPhases])
	assert.Equal(t, TableCount{Skipped: 1}, c[tWorkouts])
	assert.Equal(t, TableCount{Skipped: 2}, c[tRoutes])
	assert.Equal(t, TableCount{Skipped: 1}, c[tCategory])
	assert.Equal(t, TableCount{Skipped: 1}, c[tQuantity])
}

// Figure names come from connectors: only the summary figures pass, once
// each and in a fixed order, so no other text reaches the statement.
func TestSummaryClearFigures(t *testing.T) {
	got, unknown := summaryClearFigures([]string{
		"resting_hr", "recovery_score; DROP TABLE ohs_data.daily_summaries", "recovery_score", "metadata",
		"tenant_id", "resting_hr", "RECOVERY_SCORE", "",
	})
	assert.Equal(t, []string{"recovery_score", "resting_hr"}, got)
	assert.Equal(t, 5, unknown)
	got, _ = summaryClearFigures(nil)
	assert.Empty(t, got)

	q := summaryClearStatement([]string{"recovery_score", "resting_hr"})
	assert.Contains(t, q, "SET recovery_score = NULL, resting_hr = NULL")
	assert.Contains(t, q, "(recovery_score IS NOT NULL OR resting_hr IS NOT NULL)")
	for name, q := range map[string]string{"clear": q, "dropEmpty": summaryDropEmpty} {
		assert.Contains(t, q, "tenant_id = $1 AND user_id = $2 AND provider = $3 AND summary_date = ANY($4::date[])", name)
		assert.Contains(t, q, "metadata->>'derived' IS DISTINCT FROM 'true'", "%s: the derived row is the ETL's", name)
	}
	for _, f := range dailyFigures {
		assert.Contains(t, summaryDropEmpty, f+" IS NULL", "only a summary without any figure goes")
	}
	assert.Contains(t, deletionSamples, "RETURNING metadata->>'"+model.MetadataSummaryDate+"'")
}

func TestParseSummaryDays(t *testing.T) {
	s := func(v string) *string { return &v }
	got := parseSummaryDays([]*string{s("2026-09-02"), nil, s("2026-09-02"), s("not a day"), s("2026-09-03")})
	assert.Equal(t, []time.Time{
		time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	}, got)
	assert.Empty(t, parseSummaryDays(nil))
}

// A summary that carries one record's figures goes with that record's
// tombstone, or the tombstone of the record it derives from; a summary of the
// whole day stays.
func TestDropTombstonedSummaries(t *testing.T) {
	dead := map[string]bool{"s-1_recovery": true, "s-2": true}
	acc := &model.MapResult{DailySummaries: []model.DailySummary{
		{RecordID: "s-1_recovery"}, {RecordID: "s-2_recovery"}, {RecordID: "s-3_recovery"}, {},
	}}
	c := Counts{}
	dropTombstonedRecords(acc, func(_, id string) bool { return dead[id] }, c)
	assert.Equal(t, []model.DailySummary{{RecordID: "s-3_recovery"}, {}}, acc.DailySummaries)
	assert.Equal(t, TableCount{Skipped: 2}, c[tDaily])
}
