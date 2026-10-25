// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"fmt"
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// The synthetic source: invented people from internal/synth, whose true
// heart rate, sleep and steps are known, measured by devices whose bias and
// noise are drawn from the priors (a per-person bias, a per-figure Student t
// error scaled by the context's sigma). A Garmin relays every heart rate
// into Apple Health and some ring readings are delivered twice, so stage 0
// is tested too. By construction the priors are right here: the synthetic
// source checks the machinery (coverage, shrinkage, triple collocation, copy
// detection), not the priors.

// synthDevice is one simulated device.
type synthDevice struct {
	name    string // source slug
	dev     quality.Device
	metrics []string
	anchor  bool // a chest strap, worn only during workouts
	// offset is when, after the minute, the device stamps its reading:
	// devices never share a clock to the millisecond.
	offset time.Duration
}

var synthDevices = []synthDevice{
	{"apple_watch", quality.Device{Model: "apple_watch_s8", Family: "apple_watch", Sensor: quality.SensorWristPPG},
		[]string{quality.MetricHeartRate, quality.MetricSleepDuration, quality.MetricStepCount}, false, 0},
	{"garmin", quality.Device{Model: "garmin_vivosmart_4", Family: "garmin", Sensor: quality.SensorWristPPG},
		[]string{quality.MetricHeartRate, quality.MetricSleepDuration, quality.MetricStepCount}, false, 7 * time.Second},
	{"oura", quality.Device{Model: "oura_gen3", Family: "oura", Sensor: quality.SensorRingPPG},
		[]string{quality.MetricHeartRate, quality.MetricRestingHeartRate, quality.MetricHRVRMSSD, quality.MetricSleepDuration}, false, 13 * time.Second},
	{"polar", quality.Device{Model: "polar_h10", Family: "polar_strap", Sensor: quality.SensorChestECG},
		[]string{quality.MetricHeartRate}, true, 3 * time.Second},
}

func (d synthDevice) measures(metric string) bool {
	for _, m := range d.metrics {
		if m == metric {
			return true
		}
	}
	return false
}

// SynthResult is what the synthetic source reports besides its records.
type SynthResult struct {
	// TC compares, per person and device, the triple collocation estimate
	// of the heart rate error SD with the SD of the errors actually drawn.
	TC []TCCheck
	// TCWithCopy is the same estimate when the Garmin's Apple Health copy is
	// counted as a source in place of the Apple Watch.
	TCWithCopy []TCCheck
	// CopiesInjected counts the relayed and redelivered heart rates,
	// CopiesFound those stage 0 grouped with their original, FalseMerges
	// the distinct readings it wrongly grouped together.
	CopiesInjected, CopiesFound, FalseMerges int
}

// TCCheck is one recovery check.
type TCCheck struct {
	Subject, Device string
	Estimated, True float64
	Clipped         bool
}

// synthSample is one device figure before it becomes a record.
type synthSample struct {
	dev    synthDevice
	metric string
	t      time.Time
	value  float64
	truth  float64
	err    float64         // in the metric's error space
	ctx    quality.Context // the true context, which drew the error
	// pctx is the context the pipeline infers from device data.
	pctx     quality.Context
	features quality.Features
}

// tDraw draws a standard Student t with an integer nu.
func tDraw(r *synth.Rand, nu float64) float64 {
	var chi float64
	for range int(nu) {
		g := r.Norm(0, 1)
		chi += g * g
	}
	return r.Norm(0, 1) / math.Sqrt(chi/nu)
}

// LoadSynthetic generates n people over the given days.
func LoadSynthetic(n, days int) ([]Record, SynthResult) {
	var out []Record
	var res SynthResult
	pri := quality.DefaultPriors()
	for u := range n {
		seed := uint64(1000 + u)
		tl := synth.Build(synth.Options{Seed: seed, Days: days, Persona: synth.Personas[u%len(synth.Personas)],
			Scenarios: []synth.Scenario{synth.MultiDevice}})
		subject := fmt.Sprintf("synthetic/u%02d", u)
		r := synth.NewRand(seed, "quality")
		t0 := tl.Days[0].Start
		userBias := map[string]float64{}
		draw := func(d synthDevice, metric string, ctx quality.Context, t time.Time, truth float64) synthSample {
			m, _ := quality.LookupMetric(metric)
			pr, _, _ := pri.Lookup(metric, d.dev, ctx)
			z := m.Forward(truth)
			bias, biasSD, sigma := pr.Bias, pr.BiasSD, pr.Sigma
			if m.Scale == quality.Linear {
				bias, biasSD, sigma = pr.BiasAt(z), pr.BiasSDAt(z), pr.SigmaAt(z)
			}
			// The person's own bias: one draw per device, metric and context.
			key := d.name + "|" + metric + "|" + string(ctx)
			b, ok := userBias[key]
			if !ok {
				b = r.Norm(bias, biasSD)
				userBias[key] = b
			}
			v := m.Inverse(z + b + sigma*tDraw(r, pr.Nu))
			if metric == quality.MetricHeartRate || metric == quality.MetricRestingHeartRate || metric == quality.MetricStepCount {
				v = math.Max(1, math.Round(v))
			}
			return synthSample{dev: d, metric: metric, t: t.Add(d.offset), value: v, truth: truth, err: m.Forward(v) - z, ctx: ctx}
		}
		trueCtx := func(t time.Time) quality.Context {
			if _, ok := tl.SleepAt(t); ok {
				return quality.ContextSleep
			}
			if _, ok := tl.WorkoutAt(t); ok {
				return quality.ContextExercise
			}
			if tl.Steps(t.Add(-time.Minute), t) > 10 {
				return quality.ContextDay
			}
			return quality.ContextRest
		}
		var samples []synthSample
		// Heart rate every 15 minutes from the three wrist and ring devices,
		// every 3 minutes of a workout from all four.
		w := tl.Window()
		for t := w.Start.Add(12 * time.Hour); t.Before(w.End); t = t.Add(15 * time.Minute) {
			if _, in := tl.WorkoutAt(t); in || !tl.Worn(t) {
				continue
			}
			truth, ctx := tl.HeartRate(t), trueCtx(t)
			for _, d := range synthDevices {
				if !d.anchor {
					samples = append(samples, draw(d, quality.MetricHeartRate, ctx, t, truth))
				}
			}
		}
		for _, wo := range tl.Workouts {
			for t := wo.Start.Add(2 * time.Minute); t.Before(wo.End); t = t.Add(3 * time.Minute) {
				truth := tl.HeartRate(t)
				for _, d := range synthDevices {
					samples = append(samples, draw(d, quality.MetricHeartRate, quality.ContextExercise, t, truth))
				}
			}
		}
		for _, s := range tl.Sleeps {
			if s.Nap {
				continue
			}
			truths := map[string]float64{quality.MetricSleepDuration: s.Asleep().Minutes(),
				quality.MetricRestingHeartRate: s.LowestHR, quality.MetricHRVRMSSD: s.HRV}
			for _, d := range synthDevices {
				for _, m := range []string{quality.MetricSleepDuration, quality.MetricRestingHeartRate, quality.MetricHRVRMSSD} {
					if d.measures(m) {
						samples = append(samples, draw(d, m, quality.ContextSleep, s.End, truths[m]))
					}
				}
			}
		}
		for _, day := range tl.Days {
			truth := tl.Totals(day).Steps
			if truth <= 0 {
				continue
			}
			for _, d := range synthDevices {
				if d.measures(quality.MetricStepCount) {
					samples = append(samples, draw(d, quality.MetricStepCount, quality.ContextDay, day.End.Add(-time.Minute), truth))
				}
			}
		}

		synthStage0(samples, r, &res)
		synthTripleCollocation(subject, samples, &res)

		// The context the pipeline infers, then the stage 3 agreement of each
		// device and context with the others, then the records.
		for i := range samples {
			s := &samples[i]
			s.features, s.pctx = quality.NewFeatures(), s.ctx
			if s.metric == quality.MetricHeartRate {
				_, asleep := tl.SleepAt(s.t)
				_, inWorkout := tl.WorkoutAt(s.t)
				steps := tl.Steps(s.t.Add(-time.Minute), s.t)
				f := &s.features
				f.HR, f.RestingHR, f.Steps, f.Baseline = s.value, tl.P.RestingHR, steps, tl.P.RestingHR+15
				f.Hour = float64(tl.Local(s.t).Hour())
				s.pctx = quality.ClassifyContext(quality.ContextInput{HR: s.value, RestingHR: tl.P.RestingHR, Steps: steps,
					Motion: math.NaN(), Asleep: asleep, InWorkout: inWorkout})
			}
		}
		agree := synthAgreement(samples, pri)
		for _, s := range samples {
			nsrc := 0
			for _, d := range synthDevices {
				if d.measures(s.metric) && (!d.anchor || s.pctx == quality.ContextExercise) {
					nsrc++
				}
			}
			rec := Record{Dataset: "synthetic", Subject: subject, Metric: s.metric, Device: s.dev.dev, T: s.t.Sub(t0).Seconds(),
				Value: s.value, Ref: s.truth, Context: s.pctx, Label: string(s.ctx), Features: s.features, Sources: nsrc}
			if ag, ok := agree[agreeKey{s.dev.name, s.metric, s.pctx}]; ok {
				a := ag
				rec.Agreement = &a
			}
			out = append(out, rec)
		}
	}
	return out, res
}

// synthStage0 relays every Garmin heart rate into Apple Health with its
// start time (half of the relays lose the producer's name) and redelivers
// 5% of the ring's through Health Connect with their original id, then
// checks that GroupCopies puts each copy back with its original and merges
// nothing else.
func synthStage0(samples []synthSample, r *synth.Rand, res *SynthResult) {
	var obs []quality.Obs
	var origin []int // per obs: the index of the obs it copies, or its own
	for _, s := range samples {
		if s.metric != quality.MetricHeartRate {
			continue
		}
		i := len(obs)
		obs = append(obs, quality.Obs{ID: fmt.Sprint(i), Provider: s.dev.name, Source: s.dev.name, Start: s.t, Value: s.value})
		origin = append(origin, i)
	}
	n := len(obs)
	for i := range n {
		o := obs[i]
		switch {
		case o.Provider == "garmin":
			c := quality.Obs{ID: o.ID + "-ah", Provider: "apple_health", Source: "garmin", Start: o.Start, Value: o.Value}
			if r.Chance(0.5) {
				c.Source = ""
			}
			obs = append(obs, c)
			origin = append(origin, i)
			res.CopiesInjected++
		case o.Provider == "oura" && r.Chance(0.05):
			obs[i].OriginalID = o.ID
			obs = append(obs, quality.Obs{ID: o.ID + "-hc", Provider: "health_connect", Source: "oura", OriginalID: o.ID,
				Start: o.Start, Value: o.Value})
			origin = append(origin, i)
			res.CopiesInjected++
		}
	}
	for _, g := range quality.GroupCopies(obs, time.Minute, 1e-9) {
		originals := map[int]bool{}
		for _, m := range g.Members {
			originals[origin[m]] = true
		}
		// Copies whose original is in the same group were found.
		for _, m := range g.Members {
			if origin[m] != m && originals[origin[m]] && containsInt(g.Members, origin[m]) {
				res.CopiesFound++
			}
		}
		if len(originals) > 1 {
			res.FalseMerges += len(originals) - 1
		}
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// synthTripleCollocation runs triple collocation on the person's heart
// rates outside workouts, once with three independent devices and once
// with the Garmin's relayed copy in place of the Apple Watch: the copy has
// the Garmin's very errors, which is what stage 0 exists to prevent.
func synthTripleCollocation(subject string, samples []synthSample, res *SynthResult) {
	pts := map[string][]quality.Point{}
	errs := map[string][]float64{}
	for _, s := range samples {
		if s.metric != quality.MetricHeartRate || s.dev.anchor || s.ctx == quality.ContextExercise {
			continue
		}
		pts[s.dev.name] = append(pts[s.dev.name], quality.Point{T: s.t, V: s.value})
		errs[s.dev.name] = append(errs[s.dev.name], s.err)
	}
	run := func(a, b, c []quality.Point, names [3]string, sd [3]string) []TCCheck {
		x, y, z := quality.Align3(time.Minute, a, b, c)
		tc, err := quality.TripleCollocation(x, y, z)
		if err != nil {
			return nil
		}
		var out []TCCheck
		for k := range 3 {
			out = append(out, TCCheck{subject, names[k], math.Sqrt(tc.Var[k]), sdOf(errs[sd[k]]), tc.Clipped[k]})
		}
		return out
	}
	res.TC = append(res.TC, run(pts["apple_watch"], pts["garmin"], pts["oura"],
		[3]string{"apple_watch", "garmin", "oura"}, [3]string{"apple_watch", "garmin", "oura"})...)
	// The copy: the Garmin's readings again, a few seconds later.
	cp := make([]quality.Point, len(pts["garmin"]))
	for i, p := range pts["garmin"] {
		cp[i] = quality.Point{T: p.T.Add(10 * time.Second), V: p.V}
	}
	res.TCWithCopy = append(res.TCWithCopy, run(cp, pts["garmin"], pts["oura"],
		[3]string{"garmin_copy", "garmin", "oura"}, [3]string{"garmin", "garmin", "oura"})...)
}

func sdOf(x []float64) float64 {
	var m float64
	for _, v := range x {
		m += v
	}
	m /= float64(len(x))
	var s float64
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	return math.Sqrt(s / float64(len(x)-1))
}

type agreeKey struct {
	source, metric string
	ctx            quality.Context
}

// synthAgreement computes stage 3 per device, metric and inferred context: triple
// collocation with the two other wrist and ring devices, and the anchor's
// bias during workouts. Values are in the error space.
func synthAgreement(samples []synthSample, pri *quality.Priors) map[agreeKey]quality.Agreement {
	type k2 struct {
		source, metric string
		ctx            quality.Context
	}
	pts := map[k2][]quality.Point{}
	for _, s := range samples {
		m, _ := quality.LookupMetric(s.metric)
		k := k2{s.dev.name, s.metric, s.pctx}
		pts[k] = append(pts[k], quality.Point{T: s.t, V: m.Forward(s.value)})
	}
	out := map[agreeKey]quality.Agreement{}
	names := []string{"apple_watch", "garmin", "oura"}
	for _, ctx := range []quality.Context{quality.ContextRest, quality.ContextDay, quality.ContextSleep, quality.ContextExercise} {
		for i, a := range names {
			b, c := names[(i+1)%3], names[(i+2)%3]
			pa, pb, pc := pts[k2{a, quality.MetricHeartRate, ctx}], pts[k2{b, quality.MetricHeartRate, ctx}], pts[k2{c, quality.MetricHeartRate, ctx}]
			x, _, _ := quality.Align3(time.Minute, pa, pb, pc)
			if len(x) < 30 {
				continue
			}
			ag, err := quality.TripleAgreement(time.Minute, pa, pb, pc)
			if err != nil {
				continue
			}
			if ctx == quality.ContextExercise {
				dev := synthDevices[i].dev
				pr, _, _ := pri.Lookup(quality.MetricHeartRate, dev, ctx)
				if anc, err := quality.PairAgreement(time.Minute, pa, pts[k2{"polar", quality.MetricHeartRate, ctx}], pr.Sigma, 1, true); err == nil {
					ag.Anchored, ag.Bias, ag.BiasSD = true, anc.Bias, anc.BiasSD
				}
			}
			out[agreeKey{a, quality.MetricHeartRate, ctx}] = ag
		}
	}
	return out
}
