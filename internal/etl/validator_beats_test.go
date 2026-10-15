// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ECG voltage and heartbeat (RR) sample validation tests ---

func TestValidateMapResult_ECGVoltageSamples(t *testing.T) {
	mr := &model.MapResult{
		ECGSamples: []model.ECGVoltageSample{
			{ECGID: "ecg-1", TimeSinceSampleStart: 0, VoltageUV: 120},
			{ECGID: "ecg-1", TimeSinceSampleStart: 0.002, VoltageUV: -340},
			{ECGID: "ecg-1", TimeSinceSampleStart: -0.002, VoltageUV: 15},
		},
	}
	errs := ValidateMapResult(mr)
	require.Len(t, errs, 1, "negative voltages are valid, a negative offset is not")
	assert.Equal(t, "ecg_voltage_sample", errs[0].RecordType)
	assert.Equal(t, "time_since_sample_start", errs[0].Field)
	assert.Equal(t, -0.002, errs[0].Value)
	assert.Equal(t, 2, errs[0].Index)
	assert.Len(t, Rejections(errs), 1, "it parks the record")
	assert.Empty(t, Suspicions(errs))
}

func TestValidateMapResult_HeartbeatSamples(t *testing.T) {
	now := time.Now()
	mr := &model.MapResult{
		HeartbeatSamples: []model.HeartbeatSample{
			{SeriesID: "s", Timestamp: now.Add(-time.Hour), RRMS: 812},
			{SeriesID: "s", Timestamp: now.Add(-time.Hour), RRMS: 0},
			{SeriesID: "s", Timestamp: now.Add(-time.Hour), RRMS: -1},
			{SeriesID: "s", Timestamp: now.Add(72 * time.Hour), RRMS: 800},
			{SeriesID: "s", Timestamp: now.Add(72 * time.Hour), RRMS: -5},
		},
	}
	errs := ValidateMapResult(mr)
	type key struct {
		index int
		field string
	}
	got := map[key]bool{}
	for _, e := range errs {
		assert.Equal(t, "heartbeat_sample", e.RecordType)
		got[key{e.Index, e.Field}] = true
	}
	assert.Equal(t, map[key]bool{
		{2, "rr_ms"}:     true,
		{3, "timestamp"}: true,
		{4, "timestamp"}: true,
		{4, "rr_ms"}:     true,
	}, got, "each broken rule is reported once, against its own record")
	assert.Len(t, Rejections(errs), len(errs), "every one of them parks its record")
}
