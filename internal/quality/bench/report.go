// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// Figures of one person are autocorrelated (a missed reading is usually
// next to another), so every interval below is a subject-cluster bootstrap:
// people are resampled with replacement, all their figures with them.
// Paired comparisons between variants use the same resamples.

// BootstrapRuns is the number of subject resamples behind each interval.
const BootstrapRuns = 400

// Gate groups need this many figures and subjects.
const (
	GateMinPoints   = 50
	GateMinSubjects = 5
)

// Stats summarizes the outcomes of a group of figures for one variant.
type Stats struct {
	N, Subjects int
	PICP        [4]float64
	MPIW90      float64
	IS90        float64
	CRPS        float64
	// ECE is over 20 equal-mass bins, the primary figure; ECE10 over 10
	// equal-width bins.
	ECE, ECE10, Brier float64
	// MeanErr and SDErr describe the device against the reference.
	MeanErr, SDErr float64
}

func summarize(res *Result, idx []int, variant string) Stats {
	var s Stats
	s.N = len(idx)
	if s.N == 0 {
		return s
	}
	subs := map[string]bool{}
	q := make([]float64, 0, len(idx))
	hit := make([]bool, 0, len(idx))
	var sumE, sumE2 float64
	for _, i := range idx {
		o := res.Outcomes[variant][i]
		r := &res.Records[i]
		subs[r.Subject] = true
		for k := range Levels {
			if o.Covered[k] {
				s.PICP[k]++
			}
		}
		s.MPIW90 += o.Width90
		s.IS90 += o.IS90
		s.CRPS += o.CRPS
		q = append(q, o.Q)
		hit = append(hit, o.Hit)
		e := errorOf(r)
		sumE += e
		sumE2 += e * e
	}
	n := float64(s.N)
	for k := range Levels {
		s.PICP[k] /= n
	}
	s.MPIW90 /= n
	s.IS90 /= n
	s.CRPS /= n
	s.ECE = quality.ECEEqualMass(q, hit, 20)
	s.ECE10 = quality.ECE(q, hit, 10)
	s.Brier = quality.Brier(q, hit)
	s.Subjects = len(subs)
	s.MeanErr = sumE / n
	s.SDErr = math.Sqrt(math.Max(0, (sumE2-n*s.MeanErr*s.MeanErr)/math.Max(n-1, 1)))
	return s
}

// Boot is a point estimate and its subject resamples, per variant.
type Boot struct {
	Point   map[string]Stats
	Samples map[string][]Stats
}

// bootstrap resamples the subjects of idx BootstrapRuns times.
func bootstrap(res *Result, idx []int, seed uint64) Boot {
	b := Boot{Point: map[string]Stats{}, Samples: map[string][]Stats{}}
	by := map[string][]int{}
	var subs []string
	for _, i := range idx {
		s := res.Records[i].Subject
		if _, ok := by[s]; !ok {
			subs = append(subs, s)
		}
		by[s] = append(by[s], i)
	}
	sort.Strings(subs)
	for _, v := range Variants {
		b.Point[v] = summarize(res, idx, v)
	}
	if len(subs) < 2 {
		return b
	}
	r := rand.New(rand.NewPCG(seed, 99))
	for range BootstrapRuns {
		var sample []int
		for range subs {
			sample = append(sample, by[subs[r.IntN(len(subs))]]...)
		}
		for _, v := range Variants {
			b.Samples[v] = append(b.Samples[v], summarize(res, sample, v))
		}
	}
	return b
}

// CI is the 2.5 and 97.5% percentiles of a statistic over the resamples.
func (b Boot) CI(variant string, f func(Stats) float64) (float64, float64) {
	xs := make([]float64, 0, len(b.Samples[variant]))
	for _, s := range b.Samples[variant] {
		xs = append(xs, f(s))
	}
	return pct(xs, 0.025), pct(xs, 0.975)
}

// Diff is a paired difference a - b of a statistic, with its interval.
func (b Boot) Diff(a, c string, f func(Stats) float64) (float64, float64, float64) {
	var xs []float64
	for i := range b.Samples[a] {
		xs = append(xs, f(b.Samples[a][i])-f(b.Samples[c][i]))
	}
	return f(b.Point[a]) - f(b.Point[c]), pct(xs, 0.025), pct(xs, 0.975)
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	v := append([]float64(nil), xs...)
	sort.Float64s(v)
	return v[int(math.Round(p*float64(len(v)-1)))]
}

// withCI prints a point estimate and its interval.
func withCI(b Boot, v string, f func(Stats) float64, digits int) string {
	lo, hi := b.CI(v, f)
	return fmt.Sprintf("%s [%s, %s]", fd(f(b.Point[v]), digits), fd(lo, digits), fd(hi, digits))
}

func fd(x float64, digits int) string {
	if math.IsNaN(x) {
		return "n/a"
	}
	return fmt.Sprintf("%.*f", digits, x)
}

func f3(x float64) string { return fd(x, 3) }
func f2(x float64) string { return fd(x, 2) }

var (
	statPICP90 = func(s Stats) float64 { return s.PICP[2] }
	statECE    = func(s Stats) float64 { return s.ECE }
	statCRPS   = func(s Stats) float64 { return s.CRPS }
)

// groupKey is a report row: dataset, metric, device model, context.
type groupKey struct{ dataset, metric, device, context string }

func (k groupKey) String() string {
	return k.dataset + " / " + k.metric + " / " + k.device + " / " + k.context
}

func groups(res *Result, keep func(*Record) bool) ([]groupKey, map[groupKey][]int) {
	m := map[groupKey][]int{}
	for i := range res.Records {
		r := &res.Records[i]
		if keep(r) {
			k := groupKey{r.Dataset, r.Metric, r.Device.Model, string(r.Context)}
			m[k] = append(m[k], i)
		}
	}
	keys := make([]groupKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys, m
}

// Real says a record comes from a reference dataset, not the synthetic
// source.
func Real(r *Record) bool { return r.Dataset != "synthetic" }

// Synthetic says a record comes from the synthetic source.
func Synthetic(r *Record) bool { return r.Dataset == "synthetic" }

func selectIdx(res *Result, keep func(*Record) bool) []int {
	var idx []int
	for i := range res.Records {
		if keep(&res.Records[i]) {
			idx = append(idx, i)
		}
	}
	return idx
}

// Gate is one acceptance gate.
type Gate struct {
	Name   string
	Value  string
	Passed bool
	// ReportOnly marks a check the spec sets no threshold for.
	ReportOnly bool
}

// Analysis is everything computed once per backtest for the report.
type Analysis struct {
	Res      *Result
	Keep     func(*Record) bool
	Overall  Boot               // heart rate on the kept records
	Metrics  map[string]Boot    // per metric
	Groups   []groupKey         // every group
	GroupIdx map[groupKey][]int // their figures
	GroupB   map[groupKey]Boot
	// Floor is, per group, the ECE a perfectly calibrated q would show on
	// the same figures: outcomes drawn as independent coin flips with
	// probability q. ECE is biased upward in small groups, and this is the
	// part of it that is noise; autocorrelation makes the real floor higher.
	Floor    map[groupKey]float64          // their bootstraps
	Subject  map[string]map[string]float64 // variant -> subject -> PICP90
	Subjects []string
}

// Analyze bootstraps the overall, per metric and per group figures and the
// per-subject coverage of the kept records.
func Analyze(res *Result, keep func(*Record) bool) *Analysis {
	a := &Analysis{Res: res, Keep: keep, Metrics: map[string]Boot{}, GroupB: map[groupKey]Boot{}, Floor: map[groupKey]float64{},
		Subject: map[string]map[string]float64{}}
	a.Overall = bootstrap(res, selectIdx(res, func(r *Record) bool { return keep(r) && r.Metric == quality.MetricHeartRate }), 1)
	byMetric := map[string]bool{}
	for _, i := range selectIdx(res, keep) {
		byMetric[res.Records[i].Metric] = true
	}
	seed := uint64(2)
	ms := sortedKeys(byMetric)
	for _, m := range ms {
		a.Metrics[m] = bootstrap(res, selectIdx(res, func(r *Record) bool { return keep(r) && r.Metric == m }), seed)
		seed++
	}
	a.Groups, a.GroupIdx = groups(res, keep)
	for _, k := range a.Groups {
		a.GroupB[k] = bootstrap(res, a.GroupIdx[k], seed)
		a.Floor[k] = eceFloor(res, a.GroupIdx[k], seed)
		seed++
	}
	// Per subject PICP90, heart rate.
	for _, v := range Variants {
		hit, n := map[string]float64{}, map[string]float64{}
		for i := range res.Records {
			r := &res.Records[i]
			if !keep(r) || r.Metric != quality.MetricHeartRate {
				continue
			}
			n[r.Subject]++
			if res.Outcomes[v][i].Covered[2] {
				hit[r.Subject]++
			}
		}
		a.Subject[v] = map[string]float64{}
		for s := range n {
			a.Subject[v][s] = hit[s] / n[s]
		}
	}
	for s := range a.Subject[VariantFull] {
		a.Subjects = append(a.Subjects, s)
	}
	sort.Strings(a.Subjects)
	return a
}

// eceFloor is the mean ECE (20 equal-mass bins) of the full variant's q
// against outcomes drawn as independent coin flips with probability q.
func eceFloor(res *Result, idx []int, seed uint64) float64 {
	r := rand.New(rand.NewPCG(seed, 17))
	q := make([]float64, len(idx))
	for j, i := range idx {
		q[j] = res.Outcomes[VariantFull][i].Q
	}
	hit := make([]bool, len(idx))
	const runs = 100
	var sum float64
	for range runs {
		for j := range q {
			hit[j] = r.Float64() < q[j]
		}
		sum += quality.ECEEqualMass(q, hit, 20)
	}
	return sum / runs
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *Analysis) eligible(k groupKey) bool {
	p := a.GroupB[k].Point[VariantFull]
	return p.N >= GateMinPoints && p.Subjects >= GateMinSubjects
}

// Gates checks the acceptance gates on the full variant: the spec's
// (PICP90 in [0.88, 0.92] overall and in [0.85, 0.95] per group, ECE of q
// below 0.03, CRPS no worse than the priors alone) with ECE per group and
// CRPS against the calibrated baseline added, and per-subject coverage
// reported.
func (a *Analysis) Gates() []Gate {
	o := a.Overall
	p90 := o.Point[VariantFull].PICP[2]
	ece := o.Point[VariantFull].ECE
	gates := []Gate{
		{Name: "PICP90 overall in [0.88, 0.92] (heart rate)", Value: withCI(o, VariantFull, statPICP90, 3), Passed: p90 >= 0.88 && p90 <= 0.92},
		{Name: "ECE of q overall below 0.03 (heart rate, 20 equal-mass bins)", Value: withCI(o, VariantFull, statECE, 3), Passed: ece < 0.03},
	}
	var badCov, badECE []string
	checked := 0
	for _, k := range a.Groups {
		if !a.eligible(k) {
			continue
		}
		checked++
		s := a.GroupB[k].Point[VariantFull]
		if s.PICP[2] < 0.85 || s.PICP[2] > 0.95 {
			badCov = append(badCov, k.String())
		}
		if s.ECE >= 0.03 {
			badECE = append(badECE, k.String())
		}
	}
	gates = append(gates,
		Gate{Name: fmt.Sprintf("PICP90 in [0.85, 0.95] per group (n >= %d, >= %d subjects)", GateMinPoints, GateMinSubjects),
			Value: fmt.Sprintf("%d of %d groups outside", len(badCov), checked), Passed: len(badCov) == 0},
		Gate{Name: "ECE of q below 0.03 per group (same groups)",
			Value: fmt.Sprintf("%d of %d groups above", len(badECE), checked), Passed: len(badECE) == 0})
	for _, m := range sortedBootKeys(a.Metrics) {
		b := a.Metrics[m]
		d, lo, hi := b.Diff(VariantFull, VariantPrior, statCRPS)
		gates = append(gates, Gate{Name: "CRPS " + m + ": full minus prior only", Value: fmt.Sprintf("%s [%s, %s]", f3(d), f3(lo), f3(hi)), Passed: d <= 0})
		d, lo, hi = b.Diff(VariantFull, VariantPriorConformal, statCRPS)
		gates = append(gates, Gate{Name: "CRPS " + m + ": full minus prior+conformal", Value: fmt.Sprintf("%s [%s, %s]", f3(d), f3(lo), f3(hi)), Passed: d <= 0})
	}
	out := 0
	for _, s := range a.Subjects {
		if v := a.Subject[VariantFull][s]; v < 0.85 || v > 0.95 {
			out++
		}
	}
	gates = append(gates, Gate{Name: "Subjects with PICP90 in [0.85, 0.95] (heart rate)",
		Value: fmt.Sprintf("%d of %d inside", len(a.Subjects)-out, len(a.Subjects)), Passed: out == 0, ReportOnly: true})
	return gates
}

func sortedBootKeys(m map[string]Boot) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GroupsFailing lists the eligible groups outside the coverage band or
// above the ECE gate.
func (a *Analysis) GroupsFailing() []string {
	var out []string
	for _, k := range a.Groups {
		if !a.eligible(k) {
			continue
		}
		b := a.GroupB[k]
		s := b.Point[VariantFull]
		if s.PICP[2] < 0.85 || s.PICP[2] > 0.95 || s.ECE >= 0.03 {
			out = append(out, fmt.Sprintf("%s: PICP90 %s, ECE %s, floor %s (n %d, %d subjects)", k, withCI(b, VariantFull, statPICP90, 3),
				withCI(b, VariantFull, statECE, 3), f3(a.Floor[k]), s.N, s.Subjects))
		}
	}
	return out
}

func passFail(g Gate) string {
	switch {
	case g.ReportOnly:
		return "reported"
	case g.Passed:
		return "pass"
	}
	return "fail"
}

// ReportInput is everything the report shows.
type ReportInput struct {
	Version  string
	Lag0     *Analysis // the headline: timestamps as the devices wrote them
	Fitted   *Analysis // each device's timestamps shifted by its fitted lag
	Synth    *Analysis // the synthetic source of the headline run
	Datasets []DatasetInfo
	SynthRes SynthResult
	Galaxy   GalaxyResult
	GalaxyTC []TCCheck
	DaliaLag float64
	// ContextConf counts dataset -> label -> inferred context.
	ContextConf map[string]map[string]map[quality.Context]int
}

// DatasetInfo is one row of the dataset table.
type DatasetInfo struct {
	Name, Licence, Attribution, Tells, CannotTell string
	Subjects, Figures                             int
	Loaded                                        bool
	Note                                          string
}

func headline(w func(string, ...any), a *Analysis, title string) {
	w("\n### %s\n\n", title)
	w("| Variant | n | Subj. | PICP50 | PICP80 | PICP90 | PICP95 | MPIW90 | IS90 | CRPS | ECE (20 eq.-mass) | ECE (10 eq.-width) | Brier |\n")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	o := a.Overall
	for _, v := range Variants {
		p := o.Point[v]
		w("| %s | %d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", v, p.N, p.Subjects,
			withCI(o, v, func(s Stats) float64 { return s.PICP[0] }, 3),
			withCI(o, v, func(s Stats) float64 { return s.PICP[1] }, 3),
			withCI(o, v, statPICP90, 3),
			withCI(o, v, func(s Stats) float64 { return s.PICP[3] }, 3),
			withCI(o, v, func(s Stats) float64 { return s.MPIW90 }, 1),
			withCI(o, v, func(s Stats) float64 { return s.IS90 }, 1),
			withCI(o, v, statCRPS, 2),
			withCI(o, v, statECE, 3),
			withCI(o, v, func(s Stats) float64 { return s.ECE10 }, 3),
			withCI(o, v, func(s Stats) float64 { return s.Brier }, 3))
	}
	w("\nPaired with the calibrated baseline (full minus prior+conformal, same subject resamples):\n\n")
	w("| Statistic | Difference [95%% CI] |\n|---|---|\n")
	for _, st := range []struct {
		name   string
		f      func(Stats) float64
		digits int
	}{
		{"CRPS (bpm)", statCRPS, 3},
		{"IS90 (bpm)", func(s Stats) float64 { return s.IS90 }, 2},
		{"MPIW90 (bpm)", func(s Stats) float64 { return s.MPIW90 }, 2},
		{"PICP90", statPICP90, 3},
		{"ECE of q", statECE, 3},
		{"Brier", func(s Stats) float64 { return s.Brier }, 4},
	} {
		d, lo, hi := o.Diff(VariantFull, VariantPriorConformal, st.f)
		w("| %s | %s [%s, %s] |\n", st.name, fd(d, st.digits), fd(lo, st.digits), fd(hi, st.digits))
	}
}

func gateTable(w func(string, ...any), a *Analysis) {
	w("| Gate | Value [95%% CI] | Result |\n|---|---|---|\n")
	for _, g := range a.Gates() {
		w("| %s | %s | %s |\n", g.Name, g.Value, passFail(g))
	}
	if f := a.GroupsFailing(); len(f) > 0 {
		w("\nGroups failing a per group gate:\n\n")
		for _, s := range f {
			w("- %s\n", s)
		}
	}
}

func subjectTable(w func(string, ...any), a *Analysis) {
	w("| Variant | Subjects | Min | 10th pct. | Median | 90th pct. | Max | Outside [0.85, 0.95] |\n|---|---|---|---|---|---|---|---|\n")
	for _, v := range Variants {
		var xs []float64
		out := 0
		for _, s := range a.Subjects {
			x := a.Subject[v][s]
			xs = append(xs, x)
			if x < 0.85 || x > 0.95 {
				out++
			}
		}
		sort.Float64s(xs)
		w("| %s | %d | %s | %s | %s | %s | %s | %d |\n", v, len(xs), f3(xs[0]), f3(pct(xs, 0.1)), f3(pct(xs, 0.5)), f3(pct(xs, 0.9)), f3(xs[len(xs)-1]), out)
	}
	w("\nFull variant, per subject (device figures pooled per person):\n\n| Subject | PICP90 |\n|---|---|\n")
	for _, s := range a.Subjects {
		w("| %s | %s |\n", s, f3(a.Subject[VariantFull][s]))
	}
}

func groupTable(w func(string, ...any), a *Analysis, withCIs bool) {
	w("| Dataset | Metric | Device | Context | n | Subj. | Error mean | Error SD | PICP50 | PICP90 | PICP95 | MPIW90 | CRPS | CRPS prior+conf. | CRPS prior | ECE (20 eq.-mass) | ECE floor | ECE (10 eq.-width) | ECE prior+conf. |\n")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, k := range a.Groups {
		b := a.GroupB[k]
		s, pc, p := b.Point[VariantFull], b.Point[VariantPriorConformal], b.Point[VariantPrior]
		p90, ece := f3(s.PICP[2]), f3(s.ECE)
		if withCIs {
			p90, ece = withCI(b, VariantFull, statPICP90, 3), withCI(b, VariantFull, statECE, 3)
		}
		w("| %s | %s | %s | %s | %d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", k.dataset, k.metric, k.device, k.context,
			s.N, s.Subjects, f2(s.MeanErr), f2(s.SDErr), f3(s.PICP[0]), p90, f3(s.PICP[3]), f2(s.MPIW90), f3(s.CRPS), f3(pc.CRPS), f3(p.CRPS),
			ece, f3(a.Floor[k]), f3(s.ECE10), f3(pc.ECE))
	}
}

// Markdown renders the published report.
func Markdown(in ReportInput) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Quality score backtest\n\n")
	w("Model `%s`. Generated by `OHS_QUALITY_WRITE=1 go test -tags bench ./internal/quality/bench -run TestBench -v`;\n", in.Version)
	w("see [docs/quality.md](../quality.md) for the method. Leave one subject out: every figure is scored by a residual\n")
	w("model and conformal factors fitted without its subject. Every interval is a 95%% subject-cluster bootstrap (%d\n", BootstrapRuns)
	w("resamples of people, all their figures with them): figures of one person are autocorrelated, so a pooled count\n")
	w("of figures overstates the evidence. Numbers only; only derived figures enter the repository.\n\n")
	w("Headline: device timestamps as written (lag 0), as production sees them. The fitted-lag variant, each device's\n")
	w("timestamps shifted by the lag that best matches the reference, is reported beside it for comparison only;\n")
	w("production applies no lag.\n\n")
	w("Group sizes are counted in subjects as well as figures: a gate group needs %d figures and %d subjects; a\n", GateMinPoints, GateMinSubjects)
	w("calibration group needs %d figures and %d subjects for its shift and gamma, %d and %d for its own degrees\n", 50, quality.DefaultMinSubjects, quality.MinNuPoints, quality.MinNuSubjects)
	w("of freedom, %d and %d for its q map, and otherwise falls back to its parent.\n\n", quality.MinQPoints, quality.MinQSubjects)

	w("## Datasets\n\n| Dataset | Licence | Subjects | Figures | What it tells | What it cannot tell |\n|---|---|---|---|---|---|\n")
	for _, d := range in.Datasets {
		if !d.Loaded {
			w("| %s | %s | not in this run | | %s | %s. %s |\n", d.Name, d.Licence, d.Tells, d.CannotTell, d.Note)
			continue
		}
		w("| %s | %s | %d | %d | %s | %s |\n", d.Name, d.Licence, d.Subjects, d.Figures, d.Tells, d.CannotTell)
	}
	w("\nAttribution:\n\n")
	for _, d := range in.Datasets {
		w("- %s\n", d.Attribution)
	}

	w("\n## Headline: heart rate on the reference datasets\n")
	headline(w, in.Lag0, "Lag 0 (timestamps as written)")
	if in.Fitted != nil {
		headline(w, in.Fitted, fmt.Sprintf("Fitted lag (E4 %+.0f s in PPG-DaLiA; Galaxy Watch %+.0f s, E4 %+.0f s in GalaxyPPG)", in.DaliaLag, in.Galaxy.WatchLag, in.Galaxy.E4Lag))
	}

	w("\n## Acceptance gates, reference datasets, lag 0 (full variant)\n\n")
	gateTable(w, in.Lag0)
	if in.Fitted != nil {
		w("\n## The same gates with the fitted lag\n\n")
		gateTable(w, in.Fitted)
	}

	w("\n## Per subject coverage, lag 0 (heart rate, PICP90)\n\n")
	subjectTable(w, in.Lag0)

	w("\n## Per dataset, metric, device and context, lag 0\n\nFull variant unless named. Error is device minus reference (mean, SD) in the metric's error space. ECE floor\n")
	w("is the ECE a perfectly calibrated q would show on the same figures (independent outcomes drawn with probability\n")
	w("q): ECE over 20 bins is biased upward in small groups, and the floor is that bias. Autocorrelated figures\n")
	w("raise the real floor further. The ECE intervals are percentile bootstraps of a biased statistic, so they can\n")
	w("sit above the point estimate.\n\n")
	groupTable(w, in.Lag0, true)
	if in.Fitted != nil {
		w("\n### Fitted lag\n\n")
		groupTable(w, in.Fitted, true)
	}

	if in.Synth != nil {
		w("\n## Synthetic source\n\nA machinery check, not a gate: the synthetic errors are drawn from the priors, so the priors only baseline is\n")
		w("right by construction and anything learned can only add sampling noise. For metrics whose q is the same for\n")
		w("every figure of a device (nightly and daily totals), leave one subject out makes ECE close to zero by\n")
		w("construction, so it says nothing there.\n\n")
		gateTable(w, in.Synth)
		w("\n")
		groupTable(w, in.Synth, false)
	}

	w("\n## Triple collocation without a reference\n\n")
	w("Error SD estimated by triple collocation (covariance form) against the SD of the actual errors. Target ratio 0.8 to 1.25.\n\n")
	w("| Set | Device | Checks | Median estimated SD | Median true SD | Median ratio | Within 0.8 to 1.25 | Clipped to 0 |\n|---|---|---|---|---|---|---|---|\n")
	tcRows := func(set string, cs []TCCheck) {
		by := map[string][]TCCheck{}
		var devs []string
		for _, c := range cs {
			if _, ok := by[c.Device]; !ok {
				devs = append(devs, c.Device)
			}
			by[c.Device] = append(by[c.Device], c)
		}
		for _, d := range devs {
			var ratios, est, tru []float64
			within, clipped := 0, 0
			for _, c := range by[d] {
				est = append(est, c.Estimated)
				if c.Clipped {
					clipped++
				}
				if c.True > 0 {
					tru = append(tru, c.True)
					r := c.Estimated / c.True
					ratios = append(ratios, r)
					if r >= 0.8 && r <= 1.25 {
						within++
					}
				}
			}
			w("| %s | %s | %d | %s | %s | %s | %d | %d |\n", set, d, len(by[d]), f2(pct(est, 0.5)), f2(pct(tru, 0.5)), f2(pct(ratios, 0.5)), within, clipped)
		}
	}
	tcRows("synthetic, three independent devices", in.SynthRes.TC)
	tcRows("synthetic, the Garmin's relay copy counted as a source", in.SynthRes.TCWithCopy)
	tcRows("GalaxyPPG, Galaxy Watch 5 + E4 + Polar H10, per subject (10 s buckets, lag 0)", in.GalaxyTC)

	w("\n## Stage 0 copies (synthetic)\n\n| Injected copies | Grouped with their original | Readings wrongly merged |\n|---|---|---|\n")
	w("| %d | %d | %d |\n", in.SynthRes.CopiesInjected, in.SynthRes.CopiesFound, in.SynthRes.FalseMerges)

	w("\n## Context inferred from device data, against the dataset's labels (lag 0)\n\n")
	dsets := make([]string, 0, len(in.ContextConf))
	for d := range in.ContextConf {
		dsets = append(dsets, d)
	}
	sort.Strings(dsets)
	ctxs := []quality.Context{quality.ContextRest, quality.ContextDay, quality.ContextExercise, quality.ContextSleep, quality.ContextUnknown}
	for _, d := range dsets {
		w("%s:\n\n| Label | rest | day | exercise | sleep | unknown |\n|---|---|---|---|---|---|\n", d)
		labels := make([]string, 0, len(in.ContextConf[d]))
		for l := range in.ContextConf[d] {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, l := range labels {
			w("| %s |", l)
			for _, c := range ctxs {
				w(" %d |", in.ContextConf[d][l][c])
			}
			w("\n")
		}
		w("\n")
	}
	w("Walch et al. 2019 is not part of this run: it is held out as an untouched evaluation of this frozen model, see\n[walch-eval.md](walch-eval.md).\n")
	return b.String()
}
