// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pruneRejected must leave the MapResult untouched when it cannot scope the
// rejection, so the caller's whole-record drop is the only outcome.
func TestPruneRejected_LeavesRecordIntactOnUnknownRecordType(t *testing.T) {
	t.Parallel()

	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{{QuantityType: "heart_rate", Value: 72}},
	}
	structural := pruneRejected(mr, []ValidationError{
		{RecordType: "something_new", Field: "value", Index: 0, Severity: SeverityReject},
	})

	require.Len(t, structural, 1, "an unknown record type must fail safe as structural")
	assert.Len(t, mr.QuantitySamples, 1, "nothing may be pruned when the reject cannot be scoped")
}

// Several rejects in one record, across two families, all get scoped.
func TestPruneRejected_ScopesAcrossFamilies(t *testing.T) {
	t.Parallel()

	mr := &model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{QuantityType: "heart_rate", Value: 60},
			{QuantityType: "heart_rate", Value: 999},
			{QuantityType: "heart_rate", Value: 70},
		},
		HeartbeatSamples: []model.HeartbeatSample{
			{RRMS: 800}, {RRMS: -1},
		},
	}
	structural := pruneRejected(mr, []ValidationError{
		{RecordType: "quantity_sample", Index: 1, Severity: SeverityReject},
		{RecordType: "heartbeat_sample", Index: 1, Severity: SeverityReject},
	})

	assert.Empty(t, structural)
	require.Len(t, mr.QuantitySamples, 2)
	assert.Equal(t, 60.0, mr.QuantitySamples[0].Value)
	assert.Equal(t, 70.0, mr.QuantitySamples[1].Value)
	require.Len(t, mr.HeartbeatSamples, 1)
	assert.Equal(t, 800.0, mr.HeartbeatSamples[0].RRMS)
}

// A bad figure of a sleep session or a daily summary drops that figure only:
// the record and its other figures are still written.
func TestPruneRejected_ClearsOneFigure(t *testing.T) {
	t.Parallel()

	spo2, hr, steps := 0.95, 51.0, 9000
	bad := -3
	mr := &model.MapResult{
		SleepSessions:  []model.SleepSession{{SpO2Avg: &spo2, RestingHR: &hr}},
		DailySummaries: []model.DailySummary{{StepCount: &bad}, {StepCount: &steps}},
	}
	structural := pruneRejected(mr, []ValidationError{
		{RecordType: "sleep_session", Field: "spo2_avg", Index: 0, Severity: SeverityReject, Message: "spo2_avg out of range"},
		{RecordType: "daily_summary", Field: "step_count", Index: 0, Severity: SeverityReject, Message: "step_count out of range"},
	})

	assert.Empty(t, structural)
	require.Len(t, mr.SleepSessions, 1)
	assert.Nil(t, mr.SleepSessions[0].SpO2Avg)
	assert.Equal(t, &hr, mr.SleepSessions[0].RestingHR)
	assert.Equal(t, "spo2_avg", mr.SleepSessions[0].Metadata["quality_dropped"].([]string)[0])
	require.Len(t, mr.DailySummaries, 2)
	assert.Nil(t, mr.DailySummaries[0].StepCount)
	assert.Equal(t, &steps, mr.DailySummaries[1].StepCount)
}

// A bad bedtime is still structural: nothing is cleared, the record parks.
func TestPruneRejected_TimestampOfAParentIsStructural(t *testing.T) {
	t.Parallel()

	spo2 := 0.95
	mr := &model.MapResult{SleepSessions: []model.SleepSession{{SpO2Avg: &spo2}}}
	structural := pruneRejected(mr, []ValidationError{
		{RecordType: "sleep_session", Field: "spo2_avg", Index: 0, Severity: SeverityReject},
		{RecordType: "sleep_session", Field: "bedtime_start", Index: 0, Severity: SeverityReject},
	})
	require.Len(t, structural, 1)
	assert.NotNil(t, mr.SleepSessions[0].SpO2Avg, "nothing is pruned when the record parks")
}

// A unit that cannot be converted drops its own sample, not the record.
func TestPruneUnitErrors(t *testing.T) {
	t.Parallel()

	mr := &model.MapResult{QuantitySamples: []model.QuantitySample{
		{QuantityType: "heart_rate", Value: 60, Unit: "bpm"},
		{QuantityType: "body_mass", Value: 70, Unit: "furlongs"},
		{QuantityType: "heart_rate", Value: 62, Unit: "count/min"},
	}}
	errs := model.CanonicalizeUnits(mr)
	require.Len(t, errs, 1)
	pruneUnitErrors(mr, errs)
	require.Len(t, mr.QuantitySamples, 2)
	assert.Equal(t, 60.0, mr.QuantitySamples[0].Value)
	assert.Equal(t, "bpm", mr.QuantitySamples[1].Unit)
}
