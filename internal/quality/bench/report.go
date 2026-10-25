// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// Stats summarizes the outcomes of a group of figures for one variant.
type Stats struct {
	N, Subjects int
	PICP        [4]float64
	MPIW90      float64
	IS90        float64
	CRPS        float64
	ECE, Brier  float64
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
	errs := make([]float64, 0, len(idx))
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
		errs = append(errs, errorOf(r))
	}
	n := float64(s.N)
	for k := range Levels {
		s.PICP[k] /= n
	}
	s.MPIW90 /= n
	s.IS90 /= n
	s.CRPS /= n
	s.ECE = quality.ECE(q, hit, 10)
	s.Brier = quality.Brier(q, hit)
	s.Subjects = len(subs)
	var m float64
	for _, e := range errs {
		m += e
	}
	m /= n
	var v float64
	for _, e := range errs {
		v += (e - m) * (e - m)
	}
	s.MeanErr, s.SDErr = m, math.Sqrt(v/math.Max(n-1, 1))
	return s
}

// groupKey is a report row: dataset, metric, device model, context.
type groupKey struct{ dataset, metric, device, context string }

func groups(res *Result) ([]groupKey, map[groupKey][]int) {
	m := map[groupKey][]int{}
	for i, r := range res.Records {
		k := groupKey{r.Dataset, r.Metric, r.Device.Model, string(r.Context)}
		m[k] = append(m[k], i)
	}
	keys := make([]groupKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.dataset != b.dataset {
			return a.dataset < b.dataset
		}
		if a.metric != b.metric {
			return a.metric < b.metric
		}
		if a.device != b.device {
			return a.device < b.device
		}
		return a.context < b.context
	})
	return keys, m
}

// Gate is one acceptance gate of the spec.
type Gate struct {
	Name   string
	Value  string
	Passed bool
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

// Gates checks the spec's acceptance gates on the full variant, over the
// records keep selects: PICP90 in [0.88, 0.92] overall and in [0.85, 0.95]
// in every group of at least 50 figures, ECE of q below 0.03 overall, and
// CRPS no worse than the prior only baseline in every metric (CRPS is in
// each metric's unit, so it is compared metric by metric).
func Gates(res *Result, keep func(*Record) bool) []Gate {
	all := selectIdx(res, keep)
	if len(all) == 0 {
		return nil
	}
	full := summarize(res, all, VariantFull)
	prior := summarize(res, all, VariantPrior)
	gates := []Gate{
		{"PICP90 overall in [0.88, 0.92]", f3(full.PICP[2]), full.PICP[2] >= 0.88 && full.PICP[2] <= 0.92},
		{"ECE of q overall below 0.03", f3(full.ECE), full.ECE < 0.03},
	}
	_ = prior
	keys, m := groups(res)
	var bad []string
	checked := 0
	for _, k := range keys {
		if len(m[k]) < 50 || !keep(&res.Records[m[k][0]]) {
			continue
		}
		checked++
		s := summarize(res, m[k], VariantFull)
		if s.PICP[2] < 0.85 || s.PICP[2] > 0.95 {
			bad = append(bad, fmt.Sprintf("%s %s %s %s (%s)", k.dataset, k.metric, k.device, k.context, f3(s.PICP[2])))
		}
	}
	gates = append(gates, Gate{"PICP90 in [0.85, 0.95] per group with n >= 50",
		fmt.Sprintf("%d of %d groups outside", len(bad), checked), len(bad) == 0})
	byMetric := map[string][]int{}
	for _, i := range all {
		byMetric[res.Records[i].Metric] = append(byMetric[res.Records[i].Metric], i)
	}
	ms := make([]string, 0, len(byMetric))
	for k := range byMetric {
		ms = append(ms, k)
	}
	sort.Strings(ms)
	worse := 0
	for _, k := range ms {
		if summarize(res, byMetric[k], VariantFull).CRPS > summarize(res, byMetric[k], VariantPrior).CRPS {
			worse++
		}
	}
	gates = append(gates, Gate{"CRPS no worse than prior only, in every metric", fmt.Sprintf("%d of %d metrics worse", worse, len(ms)), worse == 0})
	for _, k := range ms {
		f := summarize(res, byMetric[k], VariantFull)
		p := summarize(res, byMetric[k], VariantPrior)
		gates = append(gates, Gate{"CRPS " + k + " no worse than prior only", fmt.Sprintf("%s vs %s", f3(f.CRPS), f3(p.CRPS)), f.CRPS <= p.CRPS})
	}
	for _, k := range ms {
		f := summarize(res, byMetric[k], VariantFull)
		gates = append(gates, Gate{"ECE of q, " + k, f3(f.ECE), f.ECE < 0.03})
	}
	return gates
}

// GroupsOutside lists the groups whose PICP90 is outside [0.85, 0.95].
func GroupsOutside(res *Result, keep func(*Record) bool) []string {
	keys, m := groups(res)
	var out []string
	for _, k := range keys {
		if len(m[k]) < 50 || !keep(&res.Records[m[k][0]]) {
			continue
		}
		s := summarize(res, m[k], VariantFull)
		if s.PICP[2] < 0.85 || s.PICP[2] > 0.95 {
			out = append(out, fmt.Sprintf("%s / %s / %s / %s: %s (n %d)", k.dataset, k.metric, k.device, k.context, f3(s.PICP[2]), s.N))
		}
	}
	return out
}

func f3(x float64) string { return fmt.Sprintf("%.3f", x) }
func f2(x float64) string { return fmt.Sprintf("%.2f", x) }

func passFail(b bool) string {
	if b {
		return "pass"
	}
	return "fail"
}

// ReportInput is everything the report shows.
type ReportInput struct {
	Version     string
	Res         *Result
	Datasets    []DatasetInfo
	Synth       SynthResult
	Galaxy      GalaxyResult
	GalaxyTC    []TCCheck
	DaliaLag    float64
	Walch       *WalchResult
	ContextConf map[string]map[string]map[quality.Context]int // dataset -> label -> context -> n
}

// DatasetInfo is one row of the dataset table.
type DatasetInfo struct {
	Name, Licence, Attribution, Tells, CannotTell string
	Subjects, Figures                             int
	Loaded                                        bool
	Note                                          string
}

// Markdown renders the published report.
func Markdown(in ReportInput) string {
	var b strings.Builder
	res := in.Res
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Quality score backtest\n\n")
	w("Model `%s`. Generated by `OHS_QUALITY_WRITE=1 go test -tags bench ./internal/quality/bench -run TestBench -v`;\n", in.Version)
	w("see [docs/quality.md](../quality.md) for the method. Leave one subject out: every figure is scored by a residual\n")
	w("model and conformal factors fitted without its subject. Numbers only; only derived figures enter the repository.\n\n")

	w("## Datasets\n\n| Dataset | Licence | Subjects | Figures | What it tells | What it cannot tell |\n|---|---|---|---|---|---|\n")
	for _, d := range in.Datasets {
		if !d.Loaded {
			w("| %s | %s | not run | | %s | %s. %s |\n", d.Name, d.Licence, d.Tells, d.CannotTell, d.Note)
			continue
		}
		w("| %s | %s | %d | %d | %s | %s |\n", d.Name, d.Licence, d.Subjects, d.Figures, d.Tells, d.CannotTell)
	}
	w("\nAttribution:\n\n")
	for _, d := range in.Datasets {
		w("- %s\n", d.Attribution)
	}

	for _, part := range []struct {
		title, note string
		keep        func(*Record) bool
	}{
		{"Acceptance gates on the reference datasets (full variant)", "", Real},
		{"The same gates on the synthetic source", "A machinery check, not a gate: the synthetic errors are drawn from the priors, so the priors only\nbaseline is right by construction and anything learned can only add sampling noise. For metrics whose q\nis the same for every figure of a device (nightly and daily totals), leave one subject out makes ECE close\nto zero by construction, so it says nothing there.\n\n", Synthetic},
	} {
		gates := Gates(res, part.keep)
		if gates == nil {
			continue
		}
		w("\n## %s\n\n%s| Gate | Value | Result |\n|---|---|---|\n", part.title, part.note)
		for _, g := range gates {
			w("| %s | %s | %s |\n", g.Name, g.Value, passFail(g.Passed))
		}
		if out := GroupsOutside(res, part.keep); len(out) > 0 {
			w("\nGroups with PICP90 outside [0.85, 0.95]:\n\n")
			for _, o := range out {
				w("- %s\n", o)
			}
		}
	}

	w("\n## Heart rate, by variant\n\n`prior`: priors only. `prior+conformal`: priors with conformal factors. `full`: priors, residual model and\nconformal factors. In bpm.\n\n")
	w("| Data | Variant | n | PICP50 | PICP80 | PICP90 | PICP95 | MPIW90 | IS90 | CRPS | ECE(q) | Brier |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, part := range []struct {
		name string
		keep func(*Record) bool
	}{{"reference datasets", Real}, {"synthetic", Synthetic}} {
		idx := selectIdx(res, func(r *Record) bool { return part.keep(r) && r.Metric == quality.MetricHeartRate })
		if len(idx) == 0 {
			continue
		}
		for _, v := range Variants {
			s := summarize(res, idx, v)
			w("| %s | %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", part.name, v, s.N, f3(s.PICP[0]), f3(s.PICP[1]), f3(s.PICP[2]), f3(s.PICP[3]), f2(s.MPIW90), f2(s.IS90), f3(s.CRPS), f3(s.ECE), f3(s.Brier))
		}
	}

	w("\n## Per dataset, metric, device and context\n\nFull variant; `prior` columns are the prior only baseline. Error is device minus reference (mean, SD), in the\nmetric's error space (natural log for HRV).\n\n")
	w("| Dataset | Metric | Device | Context | n | Subj. | Error mean | Error SD | PICP50 | PICP80 | PICP90 | PICP95 | MPIW90 | IS90 | CRPS | CRPS prior | ECE(q) | ECE prior | Brier |\n")
	w("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	keys, m := groups(res)
	for _, k := range keys {
		s := summarize(res, m[k], VariantFull)
		p := summarize(res, m[k], VariantPrior)
		w("| %s | %s | %s | %s | %d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", k.dataset, k.metric, k.device, k.context, s.N, s.Subjects,
			f2(s.MeanErr), f2(s.SDErr), f3(s.PICP[0]), f3(s.PICP[1]), f3(s.PICP[2]), f3(s.PICP[3]), f2(s.MPIW90), f2(s.IS90), f3(s.CRPS), f3(p.CRPS), f3(s.ECE), f3(p.ECE), f3(s.Brier))
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
			na := func(x float64) string {
				if math.IsNaN(x) {
					return "n/a"
				}
				return f2(x)
			}
			w("| %s | %s | %d | %s | %s | %s | %d | %d |\n", set, d, len(by[d]), na(percentile(est, 0.5)), na(percentile(tru, 0.5)),
				na(percentile(ratios, 0.5)), within, clipped)
		}
	}
	tcRows("synthetic, three independent devices", in.Synth.TC)
	tcRows("synthetic, the Garmin's relay copy counted as a source", in.Synth.TCWithCopy)
	tcRows("GalaxyPPG, Galaxy Watch 5 + E4 + Polar H10, per subject (10 s buckets)", in.GalaxyTC)

	w("\n## Stage 0 copies (synthetic)\n\n| Injected copies | Grouped with their original | Readings wrongly merged |\n|---|---|---|\n")
	w("| %d | %d | %d |\n", in.Synth.CopiesInjected, in.Synth.CopiesFound, in.Synth.FalseMerges)

	w("\n## Context inferred from device data, against the dataset's labels\n\n")
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
	w("Alignment: E4 heart rate shifted by %.0f s in PPG-DaLiA; Galaxy Watch by %.0f s and E4 by %.0f s in GalaxyPPG\n(the shift that best matches the reference, one per device and dataset).\n", in.DaliaLag, in.Galaxy.WatchLag, in.Galaxy.E4Lag)
	if in.Walch != nil {
		w("\n%s\n", in.Walch.Summary)
	}
	return b.String()
}
