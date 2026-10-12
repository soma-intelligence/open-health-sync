// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMapResultCounts(t *testing.T) {
	var empty MapResult
	assert.True(t, empty.IsEmpty())
	assert.Equal(t, 0, empty.RecordCount())

	mr := MapResult{
		QuantitySamples: []QuantitySample{{TenantID: "t1"}, {TenantID: "t1"}},
		Observations:    []Observation{{TenantID: "t1"}},
		SourceKey:       "raw/t1/u1/oura/2025-03-01/b_0.ndjson.gz",
	}
	assert.False(t, mr.IsEmpty())
	assert.Equal(t, 3, mr.RecordCount())

	// A result that only deletes is not empty: the ETL must apply it.
	del := MapResult{Deletions: []Deletion{{ExternalID: "sleep-1"}}}
	assert.False(t, del.IsEmpty())
	assert.Equal(t, 1, del.RecordCount())
}

func TestEveryRecordCarriesATenant(t *testing.T) {
	// Compile-time guard: each record type exposes TenantID.
	_ = QuantitySample{TenantID: "t"}
	_ = CategorySample{TenantID: "t"}
	_ = SleepSession{TenantID: "t"}
	_ = SleepPhase{TenantID: "t"}
	_ = Workout{TenantID: "t"}
	_ = RoutePoint{TenantID: "t"}
	_ = NutritionLog{TenantID: "t"}
	_ = ECGRecording{TenantID: "t"}
	_ = ECGVoltageSample{TenantID: "t"}
	_ = HeartbeatSample{TenantID: "t"}
	_ = DailySummary{TenantID: "t"}
	_ = Observation{TenantID: "t"}
}
