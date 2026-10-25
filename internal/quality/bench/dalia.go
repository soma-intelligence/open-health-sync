// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// PPG-DaLiA (Reiss et al. 2019, Sensors 19:3079; UCI 495, CC BY 4.0): 15
// people through a daily life protocol (sitting, stairs, table soccer,
// cycling, driving, lunch, walking, working) with an Empatica E4 on the
// wrist and a chest ECG. The reference is the mean ECG heart rate of 8 s
// windows every 2 s; the device figure is the heart rate the E4 computed
// itself (its HR.csv). scripts/quality/dalia_to_csv.py turns the subject
// pickles into the CSV files read here.

var daliaActivities = map[string]string{"0": "transient", "1": "sitting", "2": "stairs", "3": "table_soccer", "4": "cycling",
	"5": "driving", "6": "lunch", "7": "walking", "8": "working"}

// DaliaE4 is the device of PPG-DaLiA.
var DaliaE4 = quality.Device{Model: "empatica_e4", Family: "empatica_e4", Sensor: quality.SensorWristPPG}

// daliaLag is the shift (s) between an E4 HR.csv timestamp and the centre
// of the reference window it matches best; LoadDalia estimates it once over
// all subjects (it is about how the E4 stamps its estimate, not an error).
func daliaLag(subjects map[string][2]series) float64 {
	best, bestErr := 0.0, math.Inf(1)
	for lag := -60.0; lag <= 60; lag++ {
		var errs []float64
		for _, s := range subjects {
			ref, dev := s[0], s[1]
			for i := 0; i < len(ref.t); i += 10 {
				if v, ok := dev.at(ref.t[i]+lag, 0.6); ok {
					errs = append(errs, math.Abs(v-ref.v[i]))
				}
			}
		}
		if m := percentile(errs, 0.5); m < bestErr {
			best, bestErr = lag, m
		}
	}
	return best
}

// LoadDalia reads the converted CSV files. Every 4th reference window is
// kept, so the windows do not overlap. With fitLag, each window is matched
// with the E4 reading taken the fitted lag later; without, with the reading
// stamped at the window's centre, as production would.
func LoadDalia(dir string, fitLag bool) ([]Record, float64, error) {
	files := glob(filepath.Join(dir, "ppg-dalia", "csv", "S*_windows.csv"))
	if len(files) == 0 {
		return nil, 0, ErrMissing
	}
	type subj struct {
		name           string
		ref, dev, enmo series
		act            []string
	}
	var subs []subj
	pairs := map[string][2]series{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), "_windows.csv")
		_, rows, err := readCSV(f)
		if err != nil {
			return nil, 0, err
		}
		s := subj{name: name}
		for _, r := range rows {
			t := atof(r[0])
			s.ref.t, s.ref.v = append(s.ref.t, t), append(s.ref.v, atof(r[1]))
			s.act = append(s.act, daliaActivities[r[2]])
			s.enmo.t, s.enmo.v = append(s.enmo.t, t), append(s.enmo.v, atof(r[3]))
		}
		_, hr, err := readCSV(filepath.Join(dir, "ppg-dalia", "csv", name+"_e4hr.csv"))
		if err != nil {
			return nil, 0, err
		}
		for _, r := range hr {
			s.dev.t, s.dev.v = append(s.dev.t, atof(r[0])), append(s.dev.v, atof(r[1]))
		}
		sort.Sort(byTime(s.dev))
		subs = append(subs, s)
		pairs[name] = [2]series{s.ref, s.dev}
	}
	lag := 0.0
	if fitLag {
		lag = daliaLag(pairs)
	}
	var out []Record
	for _, s := range subs {
		var recs []Record
		for i := 0; i < len(s.ref.t); i += 4 {
			v, ok := s.dev.at(s.ref.t[i]+lag, 0.6)
			if !ok || math.IsNaN(v) || math.IsNaN(s.ref.v[i]) {
				continue
			}
			recs = append(recs, Record{Dataset: "ppg_dalia", Subject: "dalia/" + s.name, Metric: quality.MetricHeartRate,
				Device: DaliaE4, T: s.ref.t[i] + lag, RefT: s.ref.t[i], Value: v, Ref: s.ref.v[i], Label: s.act[i], Sources: 1})
		}
		// The E4's motion and heart rate share the E4's clock.
		enmo := s.enmo
		annotate(recs, s.dev, func(t float64) float64 {
			v, ok := enmo.at(t, 2)
			if !ok {
				return math.NaN()
			}
			return v
		}, nil)
		out = append(out, recs...)
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("ppg_dalia: no aligned windows")
	}
	return out, lag, nil
}

type byTime series

func (s byTime) Len() int           { return len(s.t) }
func (s byTime) Less(i, j int) bool { return s.t[i] < s.t[j] }
func (s byTime) Swap(i, j int)      { s.t[i], s.t[j] = s.t[j], s.t[i]; s.v[i], s.v[j] = s.v[j], s.v[i] }
