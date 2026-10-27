// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"hash/fnv"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// The backtest hides the reference, scores every figure, and compares. The
// split is leave one subject out: for each subject, the residual model is
// the average of three ensembles, each fitted on two of three folds of the
// other subjects, and the conformal factors are fitted on each fold's
// residuals under the ensemble that did not see it (crossFit). No figure is
// ever scored by a model or a calibration that saw its subject, and the
// shipped model is built the same way on everyone.

// Variants of the score the report compares.
const (
	VariantPrior          = "prior"           // priors only, nothing learned
	VariantPriorConformal = "prior+conformal" // priors, calibrated
	VariantFull           = "full"            // priors, residual model, calibrated
)

// Variants lists them in report order.
var Variants = []string{VariantPrior, VariantPriorConformal, VariantFull}

// Levels are the interval levels the report checks.
var Levels = []float64{0.5, 0.8, 0.9, 0.95}

// Outcome is what the report needs of one scored figure.
type Outcome struct {
	Q       float64
	Hit     bool // |value - reference| <= tolerance
	Covered [4]bool
	Width90 float64
	IS90    float64
	CRPS    float64
	Grade   string
}

// Params are the residual model's fitting parameters.
var Params = quality.GBRTParams{Trees: 150, LearningRate: 0.05, MaxDepth: 4, MinLeaf: 100, Lambda: 10, Bins: 64}

// ModelNu is the t the residual model's mean log error is turned into a
// scale with.
const ModelNu = 4

// FeatureNames returns the residual model's features for a set of records.
func FeatureNames(recs []Record) []string {
	names := []string{"value", "baseline_delta", "rate", "hr_above_rest", "motion", "steps", "hour", "flags", "sources"}
	seen := map[string]bool{}
	var cats []string
	for _, r := range recs {
		for _, c := range []string{"metric=" + r.Metric, "family=" + r.Device.Group()} {
			if !seen[c] {
				seen[c] = true
				cats = append(cats, c)
			}
		}
	}
	sort.Strings(cats)
	names = append(names, cats...)
	for _, c := range []quality.Context{quality.ContextRest, quality.ContextExercise, quality.ContextSleep, quality.ContextDay, quality.ContextUnknown} {
		names = append(names, "context="+string(c))
	}
	return names
}

// input is the score's input for a record: everything but the reference.
func input(r *Record) quality.Input {
	return quality.Input{Metric: r.Metric, Value: r.Value, Device: r.Device, Context: r.Context, Flags: r.Flags,
		Agreement: r.Agreement, IndependentSources: r.Sources, Features: r.Features}
}

// errorOf is value minus reference in the metric's error space.
func errorOf(r *Record) float64 {
	m, _ := quality.LookupMetric(r.Metric)
	return m.Forward(r.Value) - m.Forward(r.Ref)
}

// target is the residual model's target: the log absolute error around the
// prior bias, floored at half the value's resolution.
func target(r *Record, pri *quality.Priors) float64 {
	m, _ := quality.LookupMetric(r.Metric)
	q := (&quality.Scorer{Priors: pri}).Score(input(r))
	floor := m.Resolution / 2
	if m.Scale == quality.Log {
		floor = 0.005
	}
	return math.Log(math.Abs(errorOf(r)-q.Bias) + floor)
}

// fitEnsemble fits one tree ensemble on records and lists the metrics with
// enough figures for it to apply to.
func fitEnsemble(recs []Record, names []string, pri *quality.Priors) (*quality.Ensemble, []string, error) {
	X := make([][]float64, len(recs))
	y := make([]float64, len(recs))
	for i := range recs {
		r := &recs[i]
		m, _ := quality.LookupMetric(r.Metric)
		ctx := r.Context
		if ctx == "" {
			ctx = quality.ContextUnknown
		}
		X[i] = quality.EncodeFeatures(names, input(r), ctx, m)
		y[i] = target(r, pri)
	}
	e, err := quality.FitGBRT(X, y, names, Params)
	if err != nil {
		return nil, nil, err
	}
	count := map[string]int{}
	for _, r := range recs {
		count[r.Metric]++
	}
	var covered []string
	for m, n := range count {
		if n >= MinModelFigures {
			covered = append(covered, m)
		}
	}
	sort.Strings(covered)
	return e, covered, nil
}

// crossFit is the procedure the backtest validates and the repository
// ships: the training subjects in three folds; for each fold an ensemble
// fitted on the two others scores it, and those out-of-fold residuals fit
// the calibration. The model is the three ensembles averaged, applying to
// the metrics all three cover. It also returns the calibration of the
// priors alone on the same subjects, the calibrated baseline.
func crossFit(train [3][]Record, names []string, pri *quality.Priors, version string) (*quality.Model, *quality.Calibration, *quality.Calibration, error) {
	prior := &quality.Scorer{Priors: pri}
	model := &quality.Model{Version: version, Nu: ModelNu}
	var priorPts, fullPts []quality.CalPoint
	covered := map[string]int{}
	for k := range 3 {
		var fit []Record
		for j := range 3 {
			if j != k {
				fit = append(fit, train[j]...)
			}
		}
		e, cov, err := fitEnsemble(fit, names, pri)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, m := range cov {
			covered[m]++
		}
		model.Ensembles = append(model.Ensembles, e)
		one := &quality.Model{Version: version, Nu: ModelNu, Ensembles: []*quality.Ensemble{e}, Metrics: cov}
		priorPts = append(priorPts, calPoints(train[k], prior)...)
		fullPts = append(fullPts, calPoints(train[k], &quality.Scorer{Priors: pri, Model: one})...)
	}
	for m, n := range covered {
		if n == 3 {
			model.Metrics = append(model.Metrics, m)
		}
	}
	sort.Strings(model.Metrics)
	return model, quality.FitCalibration(fullPts, quality.CalOptions{Version: version}),
		quality.FitCalibration(priorPts, quality.CalOptions{Version: version}), nil
}

// MinModelFigures is the fewest training figures of a metric for the
// residual model to apply to it; below, the metric keeps the priors' sigma.
const MinModelFigures = 3000

// residual is (error - location) / scale of an uncalibrated score.
func residual(r *Record, q quality.Quality) float64 {
	return (errorOf(r) - q.Predictive.Location) / q.Predictive.Scale
}

func calPath(r *Record) []string {
	ctx := r.Context
	if ctx == "" {
		ctx = quality.ContextUnknown
	}
	return []string{r.Metric, r.Device.Group(), string(ctx)}
}

// calPoints scores records with sc (which must have no calibration) and
// returns their normalized residuals.
func calPoints(recs []Record, sc *quality.Scorer) []quality.CalPoint {
	pts := make([]quality.CalPoint, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		q := sc.Score(input(r))
		if q.Grade == "X" || q.Predictive.Scale <= 0 {
			continue
		}
		raw := q.Predictive.Scale
		pts = append(pts, quality.CalPoint{Path: calPath(r), Residual: residual(r, q), Tol: q.Tolerance / raw, Bias: q.Predictive.Location / raw, Subject: r.Subject})
	}
	return pts
}

// outcome scores one record with a scorer and measures the result.
func outcome(r *Record, sc *quality.Scorer) Outcome {
	q := sc.Score(input(r))
	m, _ := quality.LookupMetric(r.Metric)
	o := Outcome{Q: q.Q, Grade: q.Grade, Hit: math.Abs(errorOf(r)) <= q.Tolerance}
	for k, l := range Levels {
		lo, hi := q.IntervalAt(l)
		o.Covered[k] = r.Ref >= lo && r.Ref <= hi
	}
	lo, hi := q.IntervalAt(0.9)
	o.Width90 = hi - lo
	o.IS90 = quality.IntervalScore(lo, hi, r.Ref, 0.1)
	nu := q.Predictive.Nu
	if nu == 0 {
		nu = math.Inf(1)
	}
	if q.Predictive.Scale > 0 {
		o.CRPS = quality.CRPSStudentT(m.Forward(r.Ref), q.Predictive.Center, q.Predictive.Scale, nu)
	}
	return o
}

// fold3 puts a subject in one of three folds.
func fold3(subject string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(subject))
	return int(h.Sum32() % 3)
}

// Result holds the backtest's outcomes, per variant, aligned with the
// records.
type Result struct {
	Records  []Record
	Outcomes map[string][]Outcome
	Names    []string
}

// Backtest runs leave one subject out over all records.
func Backtest(recs []Record, pri *quality.Priors, maxTrain int) *Result {
	names := FeatureNames(recs)
	bySubject := map[string][]int{}
	var subjects []string
	for i, r := range recs {
		if _, ok := bySubject[r.Subject]; !ok {
			subjects = append(subjects, r.Subject)
		}
		bySubject[r.Subject] = append(bySubject[r.Subject], i)
	}
	sort.Strings(subjects)
	res := &Result{Records: recs, Outcomes: map[string][]Outcome{}, Names: names}
	for _, v := range Variants {
		res.Outcomes[v] = make([]Outcome, len(recs))
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, held := range subjects {
		wg.Add(1)
		sem <- struct{}{}
		go func(held string) {
			defer wg.Done()
			defer func() { <-sem }()
			// The training subjects, thinned, in three folds by hash. Each
			// fold's residuals come from a model fitted on the two others, so
			// the calibration sees every training subject out of sample.
			var train [3][]Record
			for _, s := range subjects {
				if s == held {
					continue
				}
				for _, i := range thinStrata(recs, bySubject[s], maxTrain) {
					train[fold3(s)] = append(train[fold3(s)], recs[i])
				}
			}
			model, fullCalib, priorCalib, err := crossFit(train, names, pri, "fold")
			if err != nil {
				panic(err)
			}
			prior := &quality.Scorer{Priors: pri}
			priorCal := &quality.Scorer{Priors: pri, Calibration: priorCalib}
			fullCal := &quality.Scorer{Priors: pri, Model: model, Calibration: fullCalib}
			for _, i := range bySubject[held] {
				r := &recs[i]
				res.Outcomes[VariantPrior][i] = outcome(r, prior)
				res.Outcomes[VariantPriorConformal][i] = outcome(r, priorCal)
				res.Outcomes[VariantFull][i] = outcome(r, fullCal)
			}
		}(held)
	}
	wg.Wait()
	return res
}

// thinStrata keeps at most n figures of each metric and device of a
// subject, evenly spaced in time order, so the dense heart rate series do not
// crowd out the nightly and daily figures.
func thinStrata(recs []Record, idx []int, n int) []int {
	by := map[string][]int{}
	var keys []string
	for _, i := range idx {
		k := recs[i].Metric + "|" + recs[i].Device.Model
		if _, ok := by[k]; !ok {
			keys = append(keys, k)
		}
		by[k] = append(by[k], i)
	}
	var out []int
	for _, k := range keys {
		out = append(out, thin(by[k], n)...)
	}
	sort.Ints(out)
	return out
}

// thin keeps at most n of the indexes, evenly spaced (all when n <= 0).
func thin(idx []int, n int) []int {
	if n <= 0 || len(idx) <= n {
		return idx
	}
	out := make([]int, 0, n)
	step := float64(len(idx)) / float64(n)
	for k := range n {
		out = append(out, idx[int(float64(k)*step)])
	}
	return out
}

// FinalModel runs the validated procedure on every subject: the shipped
// model is the three cross-fitted ensembles, its calibration their
// out-of-fold residuals, exactly what each backtest fold did without its
// held out subject.
func FinalModel(recs []Record, names []string, pri *quality.Priors, maxTrain int, version string) (*quality.Model, *quality.Calibration, error) {
	bySubject := map[string][]int{}
	var subs []string
	for i, r := range recs {
		if _, ok := bySubject[r.Subject]; !ok {
			subs = append(subs, r.Subject)
		}
		bySubject[r.Subject] = append(bySubject[r.Subject], i)
	}
	sort.Strings(subs)
	var train [3][]Record
	for _, s := range subs {
		for _, i := range thinStrata(recs, bySubject[s], maxTrain) {
			train[fold3(s)] = append(train[fold3(s)], recs[i])
		}
	}
	model, cal, _, err := crossFit(train, names, pri, version)
	return model, cal, err
}
