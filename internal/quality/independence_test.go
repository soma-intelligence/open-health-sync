// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

// A Garmin heart rate relayed through Apple Health is the same observation
// as the one read from Garmin directly: grouped by provenance even when the
// relay rounded the value.
func TestGroupCopies_ByProvenance(t *testing.T) {
	obs := []Obs{
		{ID: "g1", Provider: "garmin", Source: "garmin", Start: t0, Value: 71.4},
		{ID: "a1", Provider: "apple_health", Source: "garmin", Start: t0.Add(20 * time.Second), Value: 71},
		{ID: "w1", Provider: "apple_health", Source: "apple_watch", Start: t0.Add(10 * time.Second), Value: 71},
	}
	gs := GroupCopies(obs, time.Minute, 1e-9)
	require.Len(t, gs, 2)
	assert.Equal(t, []int{0, 1}, gs[0].Members)
	assert.Equal(t, 0, gs[0].Representative, "the direct read is kept")
	assert.Equal(t, ReasonProvenance, gs[0].Reason)
	assert.Equal(t, []int{2}, gs[1].Members)
}

// The same original id is one observation whatever the times say.
func TestGroupCopies_ByOriginalID(t *testing.T) {
	obs := []Obs{
		{ID: "x", Provider: "health_connect", Source: "oura", OriginalID: "abc", Start: t0, Value: 50},
		{ID: "y", Provider: "oura", Source: "oura", OriginalID: "abc", Start: t0.Add(3 * time.Hour), Value: 50},
	}
	gs := GroupCopies(obs, time.Minute, 1e-9)
	require.Len(t, gs, 1)
	assert.Equal(t, 1, gs[0].Representative)
}

// Without provenance, an equal value at the same start is a copy; a
// different value or a later start is not.
func TestGroupCopies_ByValueAndTime(t *testing.T) {
	obs := []Obs{
		{ID: "1", Provider: "withings", Source: "withings", Start: t0, Value: 72.35},
		{ID: "2", Provider: "apple_health", Source: "", Start: t0.Add(30 * time.Second), Value: 72.35},
		{ID: "3", Provider: "health_connect", Source: "", Start: t0.Add(30 * time.Second), Value: 72.4},
		{ID: "4", Provider: "health_connect", Source: "", Start: t0.Add(2 * time.Hour), Value: 72.35},
	}
	gs := GroupCopies(obs, time.Minute, 1e-9)
	require.Len(t, gs, 3)
	assert.Equal(t, []int{0, 1}, gs[0].Members)
	assert.Equal(t, ReasonValueTime, gs[0].Reason)
}

// A relay without a producer is one copy, of one reading: when two direct
// reads match its value, it goes with the nearest in time and does not glue
// them together.
func TestGroupCopies_RelayJoinsOnlyTheNearest(t *testing.T) {
	obs := []Obs{
		{ID: "garmin", Provider: "garmin", Source: "garmin", Start: t0, Value: 64},
		{ID: "watch", Provider: "apple_watch", Source: "apple_watch", Start: t0.Add(9 * time.Second), Value: 64},
		{ID: "relay", Provider: "apple_health", Start: t0.Add(time.Second), Value: 64},
	}
	gs := GroupCopies(obs, time.Minute, 1e-9)
	require.Len(t, gs, 2)
	assert.Equal(t, []int{0, 2}, gs[0].Members)
	assert.Equal(t, []int{1}, gs[1].Members)
}

// Two readings of one device through the same pipe are two observations.
func TestGroupCopies_SameSourceSamePipeIsNotACopy(t *testing.T) {
	obs := []Obs{
		{ID: "1", Provider: "oura", Source: "oura", Start: t0, Value: 60},
		{ID: "2", Provider: "oura", Source: "oura", Start: t0.Add(30 * time.Second), Value: 60},
	}
	assert.Len(t, GroupCopies(obs, time.Minute, 1e-9), 2)
}

func TestErrorFamily(t *testing.T) {
	assert.Equal(t, ErrorFamily("apple_watch", MetricHeartRate), ErrorFamily("apple_watch", MetricHRVSDNN))
	assert.Equal(t, ErrorFamily("garmin", MetricHeartRate), ErrorFamily("garmin", MetricActiveEnergy))
	assert.NotEqual(t, ErrorFamily("garmin", MetricHeartRate), ErrorFamily("oura", MetricHeartRate))
	assert.Equal(t, 2, IndependentSources([]string{"apple_watch", "apple_watch", "oura"}, MetricHeartRate))
}

func TestComparable(t *testing.T) {
	assert.False(t, Comparable(Definition{Metric: MetricHRVSDNN}, Definition{Metric: MetricHRVRMSSD}))
	assert.False(t, Comparable(Definition{Metric: MetricSleepDuration, Kind: "in_bed"}, Definition{Metric: MetricSleepDuration, Kind: "asleep"}))
	assert.True(t, Comparable(Definition{Metric: MetricSleepDuration, Kind: "asleep"}, Definition{Metric: MetricSleepDuration}))
	assert.True(t, Comparable(Definition{Metric: MetricHeartRate}, Definition{Metric: MetricHeartRate}))
}
