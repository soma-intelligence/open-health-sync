// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPriors_Valid(t *testing.T) {
	p := DefaultPriors()
	require.NotNil(t, p)
	assert.NotEmpty(t, p.Version)
	for _, e := range p.Entries {
		assert.Containsf(t, []string{TagVerified, TagSecondary, TagUnconfirmed}, e.Tag, "%s/%s", e.Metric, e.Key)
		assert.NotEmptyf(t, e.Citation, "%s/%s", e.Metric, e.Key)
		assert.Truef(t, e.Sigma > 0 || e.SigmaRel > 0, "%s/%s: no sigma", e.Metric, e.Key)
		assert.GreaterOrEqualf(t, e.Nu, 2.0, "%s/%s", e.Metric, e.Key)
	}
}

// Every metric resolves to something, so a figure never goes unscored.
func TestDefaultPriors_GenericForEveryMetric(t *testing.T) {
	p := DefaultPriors()
	for _, m := range Metrics() {
		e, lvl, ok := p.Lookup(m.Name, Device{Model: "unknown_thing"}, ContextUnknown)
		require.Truef(t, ok, "%s has no prior", m.Name)
		assert.Equalf(t, LevelGeneric, lvl, "%s", m.Name)
		assert.Equal(t, TagUnconfirmed, e.Tag)
	}
}

func TestPriors_FallbackChain(t *testing.T) {
	p := DefaultPriors()
	// Model level: Oura Gen 3 resting heart rate, Dial et al. 2025.
	e, lvl, ok := p.Lookup(MetricRestingHeartRate, Device{Model: "oura_gen3", Family: "oura", Sensor: SensorRingPPG}, ContextSleep)
	require.True(t, ok)
	assert.Equal(t, LevelModel, lvl)
	assert.InDelta(t, -0.88, e.Bias, 1e-9)
	assert.Equal(t, TagVerified, e.Tag)

	// Family level: a Fitbit model nobody studied falls back to the
	// family meta-analysis.
	e, lvl, _ = p.Lookup(MetricHeartRate, Device{Model: "fitbit_new", Family: "fitbit", Sensor: SensorWristPPG}, ContextRest)
	assert.Equal(t, LevelFamily, lvl)
	assert.InDelta(t, -3.39, e.Bias, 1e-9)

	// Sensor level, with the context: wrist PPG during exercise.
	e, lvl, _ = p.Lookup(MetricHeartRate, Device{Model: "x", Family: "galaxy_watch", Sensor: SensorWristPPG}, ContextExercise)
	assert.Equal(t, LevelSensor, lvl)
	assert.Equal(t, ContextExercise, e.Context)

	// A context the sensor has no entry for takes the sensor's any-context
	// entry before the generic one.
	e, lvl, _ = p.Lookup(MetricHeartRate, Device{Sensor: SensorWristPPG}, ContextUnknown)
	assert.Equal(t, LevelSensor, lvl)
	assert.Equal(t, Context(""), e.Context)
}

func TestPrior_ValueDependentScales(t *testing.T) {
	e := Prior{Sigma: 3, SigmaRel: 0.04, Bias: 1, BiasRel: 0.01, BiasSD: 0.3, BiasSDRel: 0}
	assert.InDelta(t, 5, e.SigmaAt(100), 1e-12)
	assert.InDelta(t, 2, e.BiasAt(100), 1e-12)
	assert.InDelta(t, 0.3, e.BiasSDAt(100), 1e-12)
}

func TestParsePriors_Rejects(t *testing.T) {
	bad := []string{
		`{"version":"x","entries":[{"metric":"nope","level":"generic","sigma":1,"nu":4,"tag":"U","citation":"c"}]}`,
		`{"version":"x","entries":[{"metric":"heart_rate","level":"generic","sigma":1,"nu":4,"tag":"Q","citation":"c"}]}`,
		`{"version":"x","entries":[{"metric":"heart_rate","level":"generic","sigma":-1,"nu":4,"tag":"U","citation":"c"}]}`,
		`{"version":"x","entries":[{"metric":"heart_rate","level":"family","key":"","sigma":1,"nu":4,"tag":"U","citation":"c"}]}`,
		`{"version":"x","entries":[{"metric":"heart_rate","level":"generic","sigma":1,"nu":4,"tag":"U","citation":""}]}`,
		`{"version":"x","entries":[{"metric":"heart_rate","level":"generic","sigma":1,"nu":4,"tag":"U","citation":"c"},{"metric":"heart_rate","level":"generic","sigma":2,"nu":4,"tag":"U","citation":"c"}]}`,
		`{"version":"","entries":[]}`,
		`not json`,
	}
	for _, b := range bad {
		_, err := ParsePriors([]byte(b))
		assert.Errorf(t, err, "accepted %s", b)
	}
	_, err := ParsePriors([]byte(`{"version":"x","entries":[{"metric":"heart_rate","level":"generic","sigma":1,"nu":4,"tag":"U","citation":"c"}]}`))
	assert.NoError(t, err)
}

// The research note writes Bland-Altman limits; the table stores the SD
// they imply. Spot check two conversions.
func TestDefaultPriors_ConvertedFromLimits(t *testing.T) {
	p := DefaultPriors()
	e, _, _ := p.Lookup(MetricSleepDuration, Device{Model: "apple_watch_s8", Family: "apple_watch"}, ContextSleep)
	assert.InDelta(t, (77.6+38.4)/3.92, e.Sigma, 0.01)
	e, _, _ = p.Lookup(MetricVO2Max, Device{Sensor: SensorWristPPG}, ContextUnknown)
	assert.InDelta(t, (9.7+9.9)/3.92, e.Sigma, 0.01)
	assert.False(t, math.IsNaN(e.Bias))
}
