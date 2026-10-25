// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// GalaxyPPG (Park et al., Sci Data 12:892, 2025; Zenodo 14635823, CC BY
// 4.0): 24 people through a stress and activity protocol (baseline, a
// speech and a singing stress task, meditation, reading, typing, standing,
// walking, jogging, running) wearing a Galaxy Watch 5, an Empatica E4 and a
// Polar H10 chest strap. The device figures are the heart rates each watch
// computed itself at 1 Hz; the reference is the Polar H10's. With two wrist
// devices and a strap on every subject it is also the reference-free test of
// triple collocation.

// GalaxyWatch5 and GalaxyE4 are the devices of GalaxyPPG.
var (
	GalaxyWatch5 = quality.Device{Model: "galaxy_watch_5", Family: "galaxy_watch", Sensor: quality.SensorWristPPG}
	GalaxyE4     = DaliaE4
)

// galaxyLabels maps the protocol's sessions to activity labels.
func galaxyLabel(session string) string {
	switch {
	case strings.HasPrefix(session, "rest"), strings.HasPrefix(session, "meditation"), session == "baseline", session == "adaptation", session == "screen-reading":
		return "seated"
	case strings.HasPrefix(session, "tsst"), strings.HasPrefix(session, "ssst"):
		return "stress_task"
	case strings.HasSuffix(session, "typing"):
		return "typing"
	}
	return session // standing, walking, jogging, running
}

type galaxySubject struct {
	name          string
	polar         series
	watch, e4     series
	watchStatus   map[float64]int
	watchENMO     map[int64]float64 // 10 s bucket -> mean ENMO
	e4ENMO        map[int64]float64
	events        []labelled
	hasEvents     bool
	start, finish float64
}

type labelled struct {
	start, end float64
	label      string
}

func (s galaxySubject) labelAt(t float64) string {
	for _, e := range s.events {
		if t >= e.start && t < e.end {
			return e.label
		}
	}
	return "transient"
}

// enmoBuckets averages max(0, |a| - 1 g) per 10 s bucket.
func enmoBuckets(t []float64, mag []float64) map[int64]float64 {
	sum, n := map[int64]float64{}, map[int64]float64{}
	for i := range t {
		k := int64(math.Floor(t[i] / 10))
		sum[k] += math.Max(0, mag[i]-1)
		n[k]++
	}
	for k := range sum {
		sum[k] /= n[k]
	}
	return sum
}

func loadGalaxySubject(dir, name string) (galaxySubject, error) {
	s := galaxySubject{name: name, watchStatus: map[float64]int{}}
	p := func(parts ...string) string { return filepath.Join(append([]string{dir, name}, parts...)...) }
	// Polar H10 stamps in UTC+9.
	if _, rows, err := readCSV(p("PolarH10", "HR.csv")); err == nil {
		for _, r := range rows {
			hr := atof(r[1])
			if hr > 0 {
				s.polar.t = append(s.polar.t, atof(r[0])/1000-9*3600)
				s.polar.v = append(s.polar.v, hr)
			}
		}
	} else {
		return s, err
	}
	if _, rows, err := readCSV(p("GalaxyWatch", "HR.csv")); err == nil {
		for _, r := range rows {
			hr, st := atof(r[2]), int(atof(r[3]))
			if hr > 0 {
				t := atof(r[1]) / 1000
				s.watch.t, s.watch.v = append(s.watch.t, t), append(s.watch.v, hr)
				s.watchStatus[t] = st
			}
		}
	}
	if _, rows, err := readCSV(p("E4", "HR.csv")); err == nil {
		for _, r := range rows {
			hr := atof(r[0])
			if hr > 0 {
				s.e4.t, s.e4.v = append(s.e4.t, atof(r[1])/1e6), append(s.e4.v, hr)
			}
		}
	}
	if _, rows, err := readCSV(p("GalaxyWatch", "ACC.csv")); err == nil {
		var t, mag []float64
		for _, r := range rows {
			x, y, z := atof(r[2]), atof(r[3]), atof(r[4])
			t = append(t, atof(r[1])/1000)
			mag = append(mag, math.Sqrt(x*x+y*y+z*z)/9.80665)
		}
		s.watchENMO = enmoBuckets(t, mag)
	}
	if _, rows, err := readCSV(p("E4", "ACC.csv")); err == nil {
		var t, mag []float64
		for _, r := range rows {
			x, y, z := atof(r[0]), atof(r[1]), atof(r[2])
			t = append(t, atof(r[3])/1e6)
			mag = append(mag, math.Sqrt(x*x+y*y+z*z)/64)
		}
		s.e4ENMO = enmoBuckets(t, mag)
	}
	if _, rows, err := readCSV(p("Event.csv")); err == nil {
		open := map[string]float64{}
		for _, r := range rows {
			t := atof(r[0]) / 1000
			if r[2] == "ENTER" {
				open[r[1]] = t
			} else if st, ok := open[r[1]]; ok {
				s.events = append(s.events, labelled{st, t, galaxyLabel(r[1])})
			}
		}
		sort.Slice(s.events, func(i, j int) bool { return s.events[i].start < s.events[j].start })
		if len(s.events) > 0 {
			s.hasEvents = true
			s.start, s.finish = s.events[0].start, s.events[len(s.events)-1].end
		}
	}
	for _, x := range []*series{&s.polar, &s.watch, &s.e4} {
		sort.Sort(byTime(*x))
	}
	return s, nil
}

// bestLag finds the shift (s) of a device's timestamps that best matches
// the reference, over all subjects: watches stamp an estimate at the end of
// the window it was computed on.
func bestLag(pairs [][2]series) float64 {
	best, bestErr := 0.0, math.Inf(1)
	for lag := -60.0; lag <= 60; lag++ {
		var errs []float64
		for _, p := range pairs {
			dev, ref := p[0], p[1]
			for i := 0; i < len(dev.t); i += 5 {
				if r, ok := ref.at(dev.t[i]+lag, 1); ok {
					errs = append(errs, math.Abs(dev.v[i]-r))
				}
			}
		}
		if m := percentile(errs, 0.5); m < bestErr {
			best, bestErr = lag, m
		}
	}
	return best
}

// GalaxyResult is what LoadGalaxy returns besides the records.
type GalaxyResult struct {
	WatchLag, E4Lag float64
	// Triples holds, per subject, the aligned watch, E4 and Polar series for
	// the triple collocation test.
	Triples [][3][]quality.Point
}

// LoadGalaxy reads the extracted GalaxyPPG files. Device figures are kept
// every 5 s.
func LoadGalaxy(dir string) ([]Record, GalaxyResult, error) {
	root := filepath.Join(dir, "galaxyppg", "extracted", "Dataset")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, GalaxyResult{}, ErrMissing
	}
	var subs []galaxySubject
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "P") {
			continue
		}
		s, err := loadGalaxySubject(root, e.Name())
		if err != nil || len(s.polar.t) == 0 {
			continue
		}
		subs = append(subs, s)
	}
	if len(subs) == 0 {
		return nil, GalaxyResult{}, ErrMissing
	}
	var wp, ep [][2]series
	for _, s := range subs {
		if len(s.watch.t) > 0 {
			wp = append(wp, [2]series{s.watch, s.polar})
		}
		if len(s.e4.t) > 0 {
			ep = append(ep, [2]series{s.e4, s.polar})
		}
	}
	res := GalaxyResult{WatchLag: bestLag(wp), E4Lag: bestLag(ep)}
	var out []Record
	for _, s := range subs {
		inWindow := func(t float64) bool { return !s.hasEvents || (t >= s.start && t < s.finish) }
		both := len(s.watch.t) > 0 && len(s.e4.t) > 0
		sources := 1
		if both {
			sources = 2
		}
		mk := func(dev series, d quality.Device, lag float64, enmo map[int64]float64, status map[float64]int) []Record {
			var recs []Record
			last := math.Inf(-1)
			for i, t := range dev.t {
				if t-last < 5 || !inWindow(t) {
					continue
				}
				ref, ok := s.polar.at(t+lag, 1)
				if !ok {
					continue
				}
				last = t
				r := Record{Dataset: "galaxyppg", Subject: "galaxyppg/" + s.name, Metric: quality.MetricHeartRate, Device: d,
					T: t + lag, Value: dev.v[i], Ref: ref, Label: s.labelAt(t + lag), Sources: sources}
				if status != nil && status[t] != 1 {
					r.Flags = []quality.Flag{quality.FlagLowSignal}
				}
				recs = append(recs, r)
			}
			// Motion over the 10 s bucket before the figure, on the device's clock.
			annotate(recs, dev, func(t float64) float64 {
				if v, ok := enmo[int64(math.Floor((t-lag)/10))]; ok {
					return v
				}
				return math.NaN()
			}, nil)
			return recs
		}
		var w, e []Record
		if len(s.watch.t) > 0 {
			w = mk(s.watch, GalaxyWatch5, res.WatchLag, s.watchENMO, s.watchStatus)
		}
		if len(s.e4.t) > 0 {
			e = mk(s.e4, GalaxyE4, res.E4Lag, s.e4ENMO, nil)
		}
		if both {
			shift := func(x series, lag float64) []quality.Point {
				pts := make([]quality.Point, 0, len(x.t))
				for i, t := range x.t {
					if inWindow(t + lag) {
						pts = append(pts, quality.Point{T: unixTime(t + lag), V: x.v[i]})
					}
				}
				return pts
			}
			res.Triples = append(res.Triples, [3][]quality.Point{shift(s.watch, res.WatchLag), shift(s.e4, res.E4Lag), shift(s.polar, 0)})
		}
		if both {
			pairAgreements(w, e, 10)
		}
		out = append(out, w...)
		out = append(out, e...)
	}
	return out, res, nil
}

// pairAgreements sets the stage 3 estimate of two independent devices of
// one subject, per context, from their own readings only: Bland-Altman on
// readings aligned to the grain (seconds), the variance of the difference
// split by the priors. Contexts with fewer than 30 pairs get none.
func pairAgreements(a, b []Record, grain float64) {
	pri := quality.DefaultPriors()
	pts := func(rs []Record, ctx quality.Context) []quality.Point {
		var out []quality.Point
		for _, r := range rs {
			if r.Context == ctx {
				out = append(out, quality.Point{T: unixTime(r.T), V: r.Value})
			}
		}
		return out
	}
	g := time.Duration(grain * float64(time.Second))
	for _, ctx := range []quality.Context{quality.ContextRest, quality.ContextDay, quality.ContextExercise, quality.ContextSleep} {
		pa, pb := pts(a, ctx), pts(b, ctx)
		x, _ := quality.Align(g, pa, pb)
		if len(x) < 30 || len(a) == 0 || len(b) == 0 {
			continue
		}
		prA, _, _ := pri.Lookup(quality.MetricHeartRate, a[0].Device, ctx)
		prB, _, _ := pri.Lookup(quality.MetricHeartRate, b[0].Device, ctx)
		agA, errA := quality.PairAgreement(g, pa, pb, prA.Sigma, prB.Sigma, false)
		agB, errB := quality.PairAgreement(g, pb, pa, prB.Sigma, prA.Sigma, false)
		for i := range a {
			if a[i].Context == ctx && errA == nil {
				ag := agA
				a[i].Agreement = &ag
			}
		}
		for i := range b {
			if b[i].Context == ctx && errB == nil {
				ag := agB
				b[i].Agreement = &ag
			}
		}
	}
}
