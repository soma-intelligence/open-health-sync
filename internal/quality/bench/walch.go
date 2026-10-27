// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// Walch et al. 2019 (Sleep 42(12); PhysioNet sleep-accel 1.0.0, ODC-By
// 1.0): 31 people, one night of polysomnography each, wearing an Apple
// Watch that logged raw acceleration and heart rate. The release has the
// sleep stages scored from the PSG every 30 s, but no ECG heart rate and no
// sleep figure computed by Apple. So it tells the bench two things only: how
// far a wrist figure of total sleep made from the watch's motion is from
// polysomnography, and how often the context classifier calls a scored
// sleep epoch sleep. It cannot say anything about heart rate error.

// WalchActigraphy is the device of the Walch total sleep figure: a fixed
// count threshold rule on the Apple Watch acceleration, run by the bench.
var WalchActigraphy = quality.Device{Model: "walch_actigraphy_rule", Family: "actigraphy_rule", Sensor: quality.SensorWristPPG}

// Rule constants, chosen before looking at the labels (the usual wrist
// actigraphy shape: still for most of a 5 minute window means asleep).
const (
	walchEpoch      = 30.0  // s
	walchStillENMO  = 0.015 // g, mean over an epoch
	walchHalfWindow = 4     // epochs each side
	walchStillShare = 0.8   // share of still epochs in the window
)

// WalchResult summarizes what Walch can tell.
type WalchResult struct {
	Nights  int
	Summary string
	// Epochs counts scored epochs by PSG label (wake, sleep) and by the
	// rule's call.
	Epochs map[string]map[string]int
}

func walchRoot(dir string) (string, bool) {
	for _, p := range glob(filepath.Join(dir, "walch", "extracted", "*", "labels")) {
		return filepath.Dir(p), true
	}
	return "", false
}

// readPairs reads a whitespace or comma separated file of numbers.
func readNumbers(path string) ([][]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		fs := strings.FieldsFunc(sc.Text(), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
		row := make([]float64, len(fs))
		for i, s := range fs {
			row[i] = atof(s)
		}
		if len(row) > 0 {
			out = append(out, row)
		}
	}
	return out, sc.Err()
}

// LoadWalch reads the extracted release and returns one total sleep record
// per night.
func LoadWalch(dir string) ([]Record, *WalchResult, error) {
	root, ok := walchRoot(dir)
	if !ok || !exists(filepath.Join(root, "motion")) {
		return nil, nil, ErrMissing
	}
	res := &WalchResult{Epochs: map[string]map[string]int{"wake": {}, "sleep": {}}}
	var recs []Record
	var errs []float64
	for _, lf := range glob(filepath.Join(root, "labels", "*_labeled_sleep.txt")) {
		id := strings.TrimSuffix(filepath.Base(lf), "_labeled_sleep.txt")
		labels, err := readNumbers(lf)
		if err != nil || len(labels) == 0 {
			continue
		}
		acc, err := readNumbers(filepath.Join(root, "motion", id+"_acceleration.txt"))
		if err != nil || len(acc) == 0 {
			continue
		}
		// Mean ENMO per 30 s epoch, epochs keyed by their start on the PSG
		// clock (labels start at 0).
		sum, n := map[int]float64{}, map[int]float64{}
		for _, a := range acc {
			if len(a) < 4 || a[0] < 0 {
				continue
			}
			k := int(a[0] / walchEpoch)
			mag := math.Sqrt(a[1]*a[1] + a[2]*a[2] + a[3]*a[3])
			sum[k] += math.Max(0, mag-1)
			n[k]++
		}
		still := func(k int) (bool, bool) {
			if n[k] == 0 {
				return false, false
			}
			return sum[k]/n[k] < walchStillENMO, true
		}
		var refMin, devMin float64
		scored := 0
		for _, l := range labels {
			if len(l) < 2 || l[1] < 0 {
				continue
			}
			k := int(l[0] / walchEpoch)
			var s, c int
			for j := k - walchHalfWindow; j <= k+walchHalfWindow; j++ {
				if st, ok := still(j); ok {
					c++
					if st {
						s++
					}
				}
			}
			if c == 0 {
				continue
			}
			scored++
			asleep := float64(s)/float64(c) >= walchStillShare
			truth := "wake"
			if l[1] > 0 {
				truth = "sleep"
				refMin += walchEpoch / 60
			}
			call := "wake"
			if asleep {
				call = "sleep"
				devMin += walchEpoch / 60
			}
			res.Epochs[truth][call]++
		}
		if scored < 120 {
			continue
		}
		res.Nights++
		recs = append(recs, Record{Dataset: "walch", Subject: "walch/" + id, Metric: quality.MetricSleepDuration, Device: WalchActigraphy,
			Value: math.Round(devMin), Ref: refMin, Context: quality.ContextSleep, Label: "night", Features: quality.NewFeatures(), Sources: 1})
		errs = append(errs, math.Round(devMin)-refMin)
	}
	if len(recs) == 0 {
		return nil, nil, fmt.Errorf("walch: no night with motion and labels")
	}
	sort.Float64s(errs)
	m, sd := meanSD(errs)
	ss, sw := res.Epochs["sleep"], res.Epochs["wake"]
	sens := float64(ss["sleep"]) / math.Max(1, float64(ss["sleep"]+ss["wake"]))
	spec := float64(sw["wake"]) / math.Max(1, float64(sw["sleep"]+sw["wake"]))
	res.Summary = fmt.Sprintf("Walch et al. 2019: %d nights. The bench's actigraphy rule on the watch's motion calls %.1f%% of PSG sleep epochs sleep (sensitivity) and %.1f%% of PSG wake epochs wake (specificity); its total sleep is %.1f min off on average (SD %.1f) against polysomnography. The release has no ECG, so no heart rate error can be measured from it.",
		res.Nights, 100*sens, 100*spec, m, sd)
	return recs, res, nil
}

func meanSD(x []float64) (float64, float64) {
	var m float64
	for _, v := range x {
		m += v
	}
	m /= float64(len(x))
	var s float64
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	return m, math.Sqrt(s / math.Max(1, float64(len(x)-1)))
}
