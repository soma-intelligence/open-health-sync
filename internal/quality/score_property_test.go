// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// extremes are the inputs a caller can hand the score by mistake: NaN, the
// infinities, the largest and smallest floats, zero.
var extremes = []float64{math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, -math.MaxFloat64,
	1e308, -1e308, 1e-300, -1e-300, math.SmallestNonzeroFloat64, 0, -1, 1}

// checkQuality fails t when a score breaks the contract: q in [0, 1], and
// no NaN in q, the estimate, the intervals or sigma.
func checkQuality(t *testing.T, label string, in Input, q Quality) {
	t.Helper()
	if math.IsNaN(q.Q) || q.Q < 0 || q.Q > 1 {
		t.Fatalf("%s: q = %v, want in [0, 1] (input %+v, agreement %+v)", label, q.Q, in, in.Agreement)
	}
	for name, v := range map[string]float64{"estimate": q.Estimate, "sigma": q.Sigma, "bias": q.Bias,
		"lo68": q.Interval68[0], "hi68": q.Interval68[1], "lo95": q.Interval95[0], "hi95": q.Interval95[1]} {
		if math.IsNaN(v) {
			t.Fatalf("%s: %s is NaN (input %+v, agreement %+v)", label, name, in, in.Agreement)
		}
	}
	if q.Grade != "X" && q.Grade != "" && !(q.Interval95[0] <= q.Interval68[0] && q.Interval68[0] <= q.Interval68[1] && q.Interval68[1] <= q.Interval95[1]) {
		t.Fatalf("%s: intervals not nested: 68 %v, 95 %v", label, q.Interval68, q.Interval95)
	}
}

// TestScore_ExtremeAgreement: an agreement estimate with a NaN, an infinity
// or a value at the edge of float64 in any field never turns the score into
// NaN. Before the fix a BiasSD of 1e-300 overflowed a precision in
// BiasPosterior and an infinite NEff divided infinity by infinity.
func TestScore_ExtremeAgreement(t *testing.T) {
	s := NewScorer()
	d := Device{Family: "apple_watch", Sensor: SensorWristPPG}
	for _, m := range Metrics() {
		v := (m.SoftMin + m.SoftMax) / 2
		for _, x := range extremes {
			for field := range 4 {
				for _, anchored := range []bool{false, true} {
					ag := Agreement{Method: MethodBlandAltman, Sigma: 1, NEff: 20, Bias: 0.5, BiasSD: 0.3, Anchored: anchored}
					*[]*float64{&ag.Sigma, &ag.NEff, &ag.Bias, &ag.BiasSD}[field] = x
					in := Input{Metric: m.Name, Value: v, Device: d, Context: ContextRest, IndependentSources: 2, Agreement: &ag}
					checkQuality(t, m.Name, in, s.Score(in))
				}
			}
		}
	}
}

// TestScore_ExtremeFeatures: features out of their range or not finite
// leave q in [0, 1] and the intervals finite.
func TestScore_ExtremeFeatures(t *testing.T) {
	s := NewScorer()
	for _, m := range Metrics() {
		v := (m.SoftMin + m.SoftMax) / 2
		for _, x := range extremes {
			for field := range 7 {
				f := Features{Hour: 3, Baseline: v, Rate: 0, Motion: 0, Steps: 0, HR: 60, RestingHR: 55}
				*[]*float64{&f.Hour, &f.Baseline, &f.Rate, &f.Motion, &f.Steps, &f.HR, &f.RestingHR}[field] = x
				in := Input{Metric: m.Name, Value: v, Device: Device{Sensor: SensorRingPPG}, Context: ContextSleep, IndependentSources: 1, Features: f}
				q := s.Score(in)
				checkQuality(t, m.Name, in, q)
				for _, b := range []float64{q.Interval95[0], q.Interval95[1], q.Estimate} {
					if math.IsInf(b, 0) {
						t.Fatalf("%s: infinite interval or estimate with features %+v", m.Name, f)
					}
				}
			}
		}
	}
}

// TestScore_Properties draws random figures over each metric's whole hard
// range, with agreements from the estimators themselves: q stays in [0, 1],
// is monotone in the tolerance, and nothing is NaN.
func TestScore_Properties(t *testing.T) {
	s := NewScorer()
	r := rand.New(rand.NewPCG(7, 11))
	devs := []Device{{}, {Model: "oura_gen3", Family: "oura", Sensor: SensorRingPPG}, {Family: "apple_watch", Sensor: SensorWristPPG}, {Sensor: SensorChestECG}, {Model: "unknown_model"}}
	ctxs := []Context{"", ContextRest, ContextExercise, ContextSleep, ContextDay, "not_a_context"}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	series := func(n int, base, noise float64) []Point {
		p := make([]Point, n)
		for i := range p {
			p[i] = Point{T: t0.Add(time.Duration(i) * 5 * time.Minute), V: base + noise*r.NormFloat64()}
		}
		return p
	}
	for _, m := range Metrics() {
		for range 1500 {
			v := m.HardMin + r.Float64()*(m.HardMax-m.HardMin)
			in := Input{Metric: m.Name, Value: v, Device: devs[r.IntN(len(devs))], Context: ctxs[r.IntN(len(ctxs))], IndependentSources: r.IntN(4)}
			switch r.IntN(3) {
			case 0:
				n := 2 + r.IntN(50)
				noise := []float64{0, 1e-12, 1, 50}[r.IntN(4)]
				if ag, err := PairAgreement(5*time.Minute, series(n, v, noise), series(n, v, noise), 3, 2, r.IntN(2) == 0); err == nil {
					in.Agreement = &ag
				}
			case 1:
				n := 3 + r.IntN(50)
				noise := []float64{0, 1e-12, 1, 50}[r.IntN(4)]
				if ag, err := TripleAgreement(5*time.Minute, series(n, v, noise), series(n, v, noise), series(n, v, noise)); err == nil {
					in.Agreement = &ag
				}
			}
			q := s.Score(in)
			checkQuality(t, m.Name, in, q)
			if q.Grade == "X" {
				continue
			}
			p := q.Predictive
			nu := p.Nu
			if nu == 0 {
				nu = math.Inf(1)
			}
			if a, b := ProbWithin(q.Tolerance, p.Location, p.Scale, nu), ProbWithin(2*q.Tolerance, p.Location, p.Scale, nu); b+1e-12 < a {
				t.Fatalf("%s: q not monotone in the tolerance: %v at tol, %v at 2 tol", m.Name, a, b)
			}
		}
	}
}

// TestScore_HugeN: an agreement over a million points, and NEff at the
// float limit, keep q finite.
func TestScore_HugeN(t *testing.T) {
	s := NewScorer()
	in := Input{Metric: MetricHeartRate, Value: 70, Device: Device{Family: "apple_watch"}, Context: ContextRest, IndependentSources: 3,
		Agreement: &Agreement{Method: MethodTripleCollocation, Sigma: 2, NEff: 1e6}}
	checkQuality(t, "n=1e6", in, s.Score(in))
	in.Agreement.NEff = math.MaxFloat64
	checkQuality(t, "n=max", in, s.Score(in))
}

// TestAgreement_NotFinite: the estimators refuse a series with a NaN or an
// infinity instead of returning a NaN sigma with no error.
func TestAgreement_NotFinite(t *testing.T) {
	ok := []float64{60, 61, 62, 63}
	for _, x := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		bad := []float64{60, x, 62, 63}
		if _, err := BlandAltman(bad, ok); err != ErrNotFinite {
			t.Errorf("BlandAltman with %v: err = %v, want ErrNotFinite", x, err)
		}
		if _, err := TripleCollocation(ok, bad, ok); err != ErrNotFinite {
			t.Errorf("TripleCollocation with %v: err = %v, want ErrNotFinite", x, err)
		}
	}
	// Differences that overflow: both values are finite, a - b is not.
	if _, err := BlandAltman([]float64{math.MaxFloat64, 0}, []float64{-math.MaxFloat64, 0}); err != ErrNotFinite {
		t.Errorf("overflowing difference: err = %v, want ErrNotFinite", err)
	}
}

// TestBiasPosterior_Extremes: a tiny sd or nEff, which used to overflow a
// precision and return NaN, gives a finite posterior between the prior and
// the data.
func TestBiasPosterior_Extremes(t *testing.T) {
	for _, c := range []struct{ sd, n float64 }{{1e-300, 20}, {0.3 * math.Sqrt(5e-324), 5e-324}, {1e-200, 1e300}, {1, math.MaxFloat64}} {
		b, sd := BiasPosterior(2, 1, 5, c.sd, c.n)
		if math.IsNaN(b) || math.IsNaN(sd) || b < 2 || b > 5 || sd < 0 || sd > 1 {
			t.Errorf("BiasPosterior(sd=%g, n=%g) = %v, %v", c.sd, c.n, b, sd)
		}
	}
	for _, x := range []float64{math.NaN(), math.Inf(1)} {
		if b, sd := BiasPosterior(2, 1, x, 1, 10); b != 2 || sd != 1 {
			t.Errorf("BiasPosterior(dbar=%v) = %v, %v, want the prior", x, b, sd)
		}
	}
}

// FuzzScore: any input, q in [0, 1] and nothing NaN.
func FuzzScore(f *testing.F) {
	f.Add("heart_rate", 70.0, 2.0, 20.0, 0.5, 0.3, true, 3.0, 60.0, "apple_watch", "rest", 2)
	f.Add("hrv_rmssd", 45.0, 0.1, 5e-324, 1e308, 1e-300, true, math.NaN(), 0.0, "", "sleep", 1)
	f.Add("step_count", 0.0, math.Inf(1), math.Inf(1), math.NaN(), 0.0, false, 1e308, -1.0, "oura", "", 0)
	s := NewScorer()
	f.Fuzz(func(t *testing.T, metric string, v, sigma, neff, bias, biassd float64, anchored bool, hour, hr float64, family, ctx string, sources int) {
		in := Input{Metric: metric, Value: v, Device: Device{Family: family}, Context: Context(ctx), IndependentSources: sources,
			Features: Features{Hour: hour, Baseline: math.NaN(), Rate: math.NaN(), Motion: math.NaN(), Steps: math.NaN(), HR: hr, RestingHR: math.NaN()},
			Agreement: &Agreement{Method: MethodBlandAltman, Sigma: sigma, NEff: neff, Bias: bias, BiasSD: biassd, Anchored: anchored}}
		checkQuality(t, metric, in, s.Score(in))
	})
}
