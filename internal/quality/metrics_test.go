// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The table in the spec, value by value.
func TestMetricTolerances_MatchSpec(t *testing.T) {
	cases := []struct {
		metric string
		value  float64
		tol    float64
	}{
		{MetricHeartRate, 60, 5},
		{MetricHeartRate, 100, 5},
		{MetricHeartRate, 150, 15},
		{MetricRestingHeartRate, 55, 3},
		{MetricHRVRMSSD, 40, math.Log(1.1)},
		{MetricHRVSDNN, 80, math.Log(1.1)},
		{MetricSleepDuration, 420, 30},
		{MetricSleepStageMinutes, 90, 20},
		{MetricStepCount, 8000, 800},
		{MetricActiveEnergy, 500, 50},
		{MetricOxygenSaturation, 97, 2},
		{MetricVO2Max, 45, 3.5},
		{MetricBloodGlucose, 80, 15},
		{MetricBloodGlucose, 200, 30},
		{MetricSystolic, 120, 5},
		{MetricDiastolic, 80, 5},
		{MetricBodyMass, 70, 0.5},
		{MetricBodyFat, 22, 2},
	}
	for _, c := range cases {
		m, ok := LookupMetric(c.metric)
		require.Truef(t, ok, "metric %q is not registered", c.metric)
		assert.InDeltaf(t, c.tol, m.Tolerance(c.value), 1e-12, "%s at %v", c.metric, c.value)
	}
}

// Every metric declares a unit, a positive resolution and hard limits that
// contain its soft ones.
func TestMetrics_Consistent(t *testing.T) {
	require.NotEmpty(t, Metrics())
	for _, m := range Metrics() {
		assert.NotEmptyf(t, m.Unit, "%s: unit", m.Name)
		assert.Positivef(t, m.Resolution, "%s: resolution", m.Name)
		assert.LessOrEqualf(t, m.HardMin, m.SoftMin, "%s", m.Name)
		assert.Lessf(t, m.SoftMin, m.SoftMax, "%s", m.Name)
		assert.LessOrEqualf(t, m.SoftMax, m.HardMax, "%s", m.Name)
		assert.NotEmptyf(t, m.Basis, "%s: basis", m.Name)
		assert.Positivef(t, m.Tolerance(m.SoftMin+(m.SoftMax-m.SoftMin)/2), "%s: tolerance", m.Name)
	}
}

func TestMetric_LogScaleRoundTrip(t *testing.T) {
	m, _ := LookupMetric(MetricHRVRMSSD)
	assert.InDelta(t, math.Log(42), m.Forward(42), 1e-12)
	assert.InDelta(t, 42, m.Inverse(m.Forward(42)), 1e-12)
	h, _ := LookupMetric(MetricHeartRate)
	assert.Equal(t, 61.0, h.Forward(61))
}

func TestMetric_Plausibility(t *testing.T) {
	m, _ := LookupMetric(MetricHeartRate)
	assert.Equal(t, PlausibleOK, m.Plausibility(70))
	assert.Equal(t, PlausibleSuspect, m.Plausibility(31))
	assert.Equal(t, PlausibleReject, m.Plausibility(300))
	assert.Equal(t, PlausibleReject, m.Plausibility(math.NaN()))
}
