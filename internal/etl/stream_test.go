// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func planSample(hash string, start time.Time, replaceable bool, externalID string) model.QuantitySample {
	return model.QuantitySample{
		QuantityType: "step_count", ContentHash: hash, StartDate: start,
		Replaceable: replaceable, SupersedeMoved: externalID != "", ExternalID: externalID,
	}
}

// An object within one chunk is held whole; one more bulk record and the plan
// keeps the held records only.
func TestObjectPlanSpillsPastOneChunk(t *testing.T) {
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	p := newObjectPlan(scope{provider: "dev"}, "key", 3)
	for i := range 3 {
		spilled := p.add(&model.MapResult{
			QuantitySamples: []model.QuantitySample{planSample(fmt.Sprint("h", i), at, false, "")},
			Workouts:        []model.Workout{{ContentHash: fmt.Sprint("w", i)}},
		})
		assert.False(t, spilled)
	}
	assert.False(t, p.spilled)
	assert.Len(t, p.acc.QuantitySamples, 3)

	assert.True(t, p.add(&model.MapResult{
		CategorySamples: []model.CategorySample{{ContentHash: "c"}},
		Deletions:       []model.Deletion{{ExternalID: "gone"}},
	}), "the fourth bulk record is one too many")
	assert.True(t, p.spilled)
	assert.Empty(t, p.acc.QuantitySamples)
	assert.Empty(t, p.acc.CategorySamples)
	assert.Len(t, p.acc.Workouts, 3, "held records stay")
	assert.Len(t, p.acc.Deletions, 1)
	assert.Equal(t, bulkCounts{quantity: 3, category: 1}, p.n)

	assert.False(t, p.add(&model.MapResult{
		HeartbeatSamples: []model.HeartbeatSample{{RRMS: 800}},
		Observations:     []model.Observation{{CanonicalCode: "x"}},
		SleepSessions:    []model.SleepSession{{ContentHash: "s"}},
		AliasExternalIDs: true,
	}))
	assert.Empty(t, p.acc.HeartbeatSamples)
	assert.Empty(t, p.acc.Observations)
	assert.Len(t, p.acc.SleepSessions, 1)
	assert.True(t, p.acc.AliasExternalIDs, "the flags of a record with bulk records only are kept")
	assert.Equal(t, bulkCounts{quantity: 3, category: 1, heartbeat: 1, observation: 1}, p.n)
}

// Only the Replaceable samples that have another copy in the object are
// diverted: the same identity, or the same provider record under another
// start.
func TestObjectPlanDivertsCopies(t *testing.T) {
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	p := newObjectPlan(scope{provider: "dev"}, "key", 0)
	p.add(&model.MapResult{QuantitySamples: []model.QuantitySample{
		planSample("plain", at, false, ""),                    // not Replaceable: no position
		planSample("once", at, true, ""),                      // 0
		planSample("twice", at, true, ""),                     // 1
		planSample("moved-a", at, true, "rec"),                // 2
		planSample("twice", at, true, ""),                     // 3: same identity as 1
		planSample("twice", at.Add(time.Hour), true, ""),      // 4: another start, another identity
		planSample("moved-b", at.Add(time.Hour), true, "rec"), // 5: same record as 2
		planSample("alone", at, true, "other"),                // 6
	}})
	require.True(t, p.spilled)
	p.seal()
	assert.Equal(t, map[uint32]struct{}{1: {}, 3: {}, 2: {}, 5: {}}, p.divert)
	assert.Nil(t, p.same)
	assert.Nil(t, p.moved)
	assert.Equal(t, bulkCounts{quantity: 8, replaceable: 7}, p.n)
}

// The hours of a set come out by type, then hour, in slices of hourBatch.
func TestHourSetBatches(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	set := hourSet{}
	for i := range hourBatch + 10 {
		set.add("heart_rate", base.Add(time.Duration(hourBatch+9-i)*time.Hour+17*time.Minute))
	}
	set.add("heart_rate", base.Add(30*time.Minute)) // an hour already there
	set.add("body_mass", base.Add(-48*time.Hour))
	assert.Equal(t, hourBatch+11, set.len())
	assert.Len(t, set.keys(), hourBatch+11)

	var got []HourKey
	var sizes []int
	require.NoError(t, set.batches(func(hours []HourKey) error {
		sizes = append(sizes, len(hours))
		got = append(got, hours...)
		return nil
	}))
	assert.Equal(t, []int{hourBatch, 11}, sizes)
	assert.Equal(t, HourKey{QuantityType: "body_mass", Bucket: base.Add(-48 * time.Hour)}, got[0])
	for i := range hourBatch + 10 {
		assert.Equal(t, HourKey{QuantityType: "heart_rate", Bucket: base.Add(time.Duration(i) * time.Hour)}, got[i+1])
	}

	stop := errors.New("stop")
	assert.ErrorIs(t, set.batches(func([]HourKey) error { return stop }), stop)
	var none hourSet
	none.add("heart_rate", base)
	assert.Zero(t, none.len(), "a nil set collects nothing")
}

// The zone of a device upload is the one of its latest record that names a
// zone this host knows; a record dated after now is not trusted.
func TestZoneSeenKeepsTheLatest(t *testing.T) {
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	md := func(zone string) map[string]any { return map[string]any{MetadataTimezone: zone} }
	var z zoneSeen
	z.add(&model.MapResult{
		QuantitySamples: []model.QuantitySample{
			{StartDate: now.Add(-48 * time.Hour), Metadata: md("Europe/Paris")},
			{StartDate: now.Add(-2 * time.Hour), Metadata: md("Not/AZone")},
			{StartDate: now.Add(-time.Hour)},
			{StartDate: now.Add(time.Hour), Metadata: md("Asia/Tokyo")},
		},
		Workouts: []model.Workout{{StartDate: now.Add(-24 * time.Hour), Metadata: md("America/New_York")}},
	}, now)
	assert.Equal(t, "America/New_York", z.name)
	assert.Equal(t, now.Add(-24*time.Hour), z.at)

	z.add(&model.MapResult{CategorySamples: []model.CategorySample{{StartDate: now.Add(-72 * time.Hour), Metadata: md("Asia/Kolkata")}}}, now)
	assert.Equal(t, "America/New_York", z.name, "an older record teaches nothing")
}
