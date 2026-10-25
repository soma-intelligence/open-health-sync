// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// priorOnly is a scorer with the embedded priors and nothing learned.
func priorOnly() *Scorer { return &Scorer{Priors: DefaultPriors()} }

var oura3 = Device{Model: "oura_gen3", Family: "oura", Sensor: SensorRingPPG}

// One source, nothing else: the score is the prior's, by the spec's formula.
func TestScore_PriorOnlyGolden(t *testing.T) {
	q := priorOnly().Score(Input{Metric: MetricRestingHeartRate, Value: 52, Device: oura3, Context: ContextSleep, IndependentSources: 1})
	s := math.Hypot(0.28, 1.00) // bias_sd is 1.00/sqrt(13) to two decimals
	want := StudentTCDF((3+0.88)/s, 4) - StudentTCDF((-3+0.88)/s, 4)
	assert.InDelta(t, want, q.Q, 1e-9)
	assert.Equal(t, "A", q.Grade)
	assert.InDelta(t, -0.88, q.Bias, 1e-12)
	assert.InDelta(t, 52.88, q.Estimate, 1e-12)
	assert.InDelta(t, 3, q.Tolerance, 1e-12)
	assert.InDelta(t, 52.88-s*StudentTQuantile(0.975, 4), q.Interval95[0], 1e-9)
	assert.InDelta(t, 52.88+s*StudentTQuantile(0.84, 4), q.Interval68[1], 1e-9)
	assert.Equal(t, TagVerified, q.Evidence.PriorTag)
	assert.Contains(t, q.Evidence.PriorCitation, "Dial")
	assert.Equal(t, LevelModel, q.Evidence.PriorLevel)
	assert.Contains(t, q.Flags, FlagSingleSource)
	assert.Equal(t, ContextSleep, q.Context)
}

func TestProbWithin_Properties(t *testing.T) {
	// Monotone in the tolerance.
	prev := 0.0
	for tol := 0.0; tol <= 20; tol += 0.25 {
		p := ProbWithin(tol, 1.5, 3, 4)
		assert.GreaterOrEqual(t, p, prev)
		prev = p
	}
	assert.InDelta(t, 1, ProbWithin(1e6, 1.5, 3, 4), 1e-9)
	assert.Equal(t, 0.0, ProbWithin(0, 1.5, 3, 4))
	// Largest without bias, symmetric in its sign, falls with the scale.
	assert.Greater(t, ProbWithin(5, 0, 3, 4), ProbWithin(5, 2, 3, 4))
	assert.InDelta(t, ProbWithin(5, 2, 3, 4), ProbWithin(5, -2, 3, 4), 1e-12)
	assert.Greater(t, ProbWithin(5, 0, 2, 4), ProbWithin(5, 0, 4, 4))
}

// q rises when the tolerance is looser, through the whole score.
func TestScore_MonotoneInTolerance(t *testing.T) {
	saved := metrics[MetricRestingHeartRate]
	defer func() { metrics[MetricRestingHeartRate] = saved }()
	prev := -1.0
	for _, tol := range []float64{0.5, 1, 2, 3, 5, 8} {
		m := saved
		m.Abs = tol
		metrics[MetricRestingHeartRate] = m
		q := priorOnly().Score(Input{Metric: MetricRestingHeartRate, Value: 52, Device: oura3, Context: ContextSleep})
		assert.Greater(t, q.Q, prev)
		prev = q.Q
	}
}

func TestScore_Grades(t *testing.T) {
	for _, c := range []struct {
		q     float64
		grade string
	}{{0.95, "A"}, {0.9, "A"}, {0.8, "B"}, {0.75, "B"}, {0.6, "C"}, {0.5, "C"}, {0.2, "D"}} {
		assert.Equal(t, c.grade, gradeOf(c.q))
	}
}

func TestScore_RejectedAndSuspect(t *testing.T) {
	sc := priorOnly()
	q := sc.Score(Input{Metric: MetricHeartRate, Value: 300, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest})
	assert.Equal(t, "X", q.Grade)
	assert.Equal(t, 0.0, q.Q)
	assert.Contains(t, q.Flags, FlagRejected)

	base := sc.Score(Input{Metric: MetricHeartRate, Value: 40, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest})
	sus := sc.Score(Input{Metric: MetricHeartRate, Value: 32, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest})
	assert.Contains(t, sus.Flags, FlagSuspectRange)
	assert.InDelta(t, 2*base.Sigma, sus.Sigma, 1e-9)
	assert.InDelta(t, 2, sus.Components.Plausibility.Factor, 1e-12)

	// A caller flag of the validator inflates the same way.
	jump := sc.Score(Input{Metric: MetricHeartRate, Value: 40, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest, Flags: []Flag{FlagRateJump, FlagRateJump}})
	assert.InDelta(t, 2*base.Sigma, jump.Sigma, 1e-9, "a flag counts once")

	u := sc.Score(Input{Metric: "nope", Value: 1})
	assert.Contains(t, u.Flags, FlagUnknownMetric)
	assert.Equal(t, "", u.Grade)
}

// Agreement between sources shrinks the prior toward the data, more with
// more effective observations, less for a verified prior.
func TestScore_AgreementShrinks(t *testing.T) {
	sc := priorOnly()
	in := Input{Metric: MetricRestingHeartRate, Value: 52, Device: oura3, Context: ContextSleep, IndependentSources: 3}
	base := sc.Score(in)
	in.Agreement = &Agreement{Method: MethodTripleCollocation, Sigma: 3, NEff: 50}
	q := sc.Score(in)
	assert.InDelta(t, math.Sqrt(1.00*3), q.Sigma, 1e-9, "w0 = 50 for a V prior, n_eff 50: the geometric mean")
	assert.InDelta(t, 0.5, q.Components.Agreement.Weight, 1e-12)
	assert.Less(t, q.Q, base.Q)
	assert.NotContains(t, q.Flags, FlagSingleSource)
	assert.Equal(t, base.Bias, q.Bias, "no anchor, the bias stays the prior's")

	in.Agreement = &Agreement{Method: MethodBlandAltman, Sigma: 1, NEff: 400, Anchored: true, Bias: -2, BiasSD: 0.05}
	q = sc.Score(in)
	assert.InDelta(t, -2, q.Bias, 0.05)
	assert.Less(t, q.BiasSD, base.BiasSD)
}

// Calibration widens or narrows the intervals by gamma and sets nu.
func TestScore_Calibration(t *testing.T) {
	sc := priorOnly()
	in := Input{Metric: MetricRestingHeartRate, Value: 52, Device: oura3, Context: ContextSleep}
	base := sc.Score(in)
	sc.Calibration = &Calibration{Version: "c1", MinPoints: 50, Groups: map[string]GroupCal{
		"resting_heart_rate|oura": {N: 100, Gamma: 2, Nu: math.Inf(1)},
	}}
	q := sc.Score(in)
	s := math.Hypot(base.BiasSD, base.Sigma)
	assert.InDelta(t, q.Estimate+2*s*NormalQuantile(0.975), q.Interval95[1], 1e-9)
	assert.Less(t, q.Q, base.Q)
	assert.Equal(t, "c1", q.Evidence.CalibrationVersion)
	assert.Equal(t, "resting_heart_rate|oura", q.Evidence.CalibrationGroup)
	assert.InDelta(t, 2, q.Evidence.Gamma, 1e-12)
}

// The residual model scales sigma per figure.
func TestScore_ModelScalesSigma(t *testing.T) {
	sc := priorOnly()
	in := Input{Metric: MetricHeartRate, Value: 70, Device: Device{Family: "galaxy_watch", Sensor: SensorWristPPG}, Context: ContextRest}
	base := sc.Score(in)
	sTarget := 4 * math.Hypot(base.BiasSD, base.Sigma)
	sc.Model = &Model{Version: "m1", Nu: 4, Ensemble: &Ensemble{Features: []string{"value"}, Base: math.Log(sTarget) + MeanLogAbsT(4), LearningRate: 1}}
	q := sc.Score(in)
	assert.InDelta(t, sTarget, math.Hypot(q.BiasSD, q.Sigma), 1e-9)
	assert.Greater(t, q.Components.ML.Factor, 3.0)
	assert.Equal(t, "m1", q.Evidence.ModelVersion)
}

// HRV is scored on the log scale; intervals come back in milliseconds.
func TestScore_LogScale(t *testing.T) {
	q := priorOnly().Score(Input{Metric: MetricHRVRMSSD, Value: 45, Device: oura3, Context: ContextSleep})
	assert.InDelta(t, math.Log(1.1), q.Tolerance, 1e-12)
	assert.Less(t, q.Interval95[0], 45.0)
	assert.Greater(t, q.Interval95[1], 45.0)
	assert.InDelta(t, 45*45, q.Interval95[0]*q.Interval95[1], 1e-6, "symmetric on the log scale")
	assert.Equal(t, "log", q.Predictive.Space)
}

func TestQuality_JSONContract(t *testing.T) {
	q := priorOnly().Score(Input{Metric: MetricHeartRate, Value: 70, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest})
	b, err := json.Marshal(q)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	for _, k := range []string{"q", "grade", "tolerance", "bias", "bias_sd", "sigma", "estimate", "interval_68", "interval_95", "context", "components", "flags", "evidence"} {
		assert.Containsf(t, m, k, "the contract field %q", k)
	}
	comp := m["components"].(map[string]any)
	for _, k := range []string{"prior", "agreement", "ml", "plausibility"} {
		assert.Contains(t, comp, k)
	}
	ev := m["evidence"].(map[string]any)
	for _, k := range []string{"sources_independent", "prior_citation", "prior_tag", "model_version", "calibration_version"} {
		assert.Contains(t, ev, k)
	}
}

func TestQuality_IntervalAt(t *testing.T) {
	q := priorOnly().Score(Input{Metric: MetricHeartRate, Value: 70, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest})
	lo, hi := q.IntervalAt(0.95)
	assert.InDelta(t, q.Interval95[0], lo, 1e-12)
	assert.InDelta(t, q.Interval95[1], hi, 1e-12)
	lo50, hi50 := q.IntervalAt(0.5)
	assert.Greater(t, lo50, lo)
	assert.Less(t, hi50, hi)
}

func TestNewScorer_Defaults(t *testing.T) {
	sc := NewScorer()
	require.NotNil(t, sc.Priors)
	q := sc.Score(Input{Metric: MetricHeartRate, Value: 70, Device: Device{Sensor: SensorWristPPG}, Context: ContextRest})
	assert.NotEmpty(t, q.Grade)
}
