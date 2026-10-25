// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"encoding/csv"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/quality"
)

// Record is one figure a device reported, with the reference value the
// pipeline never sees.
type Record struct {
	Dataset string
	// Subject is unique across datasets: the split unit of the backtest.
	Subject string
	Metric  string
	Device  quality.Device
	// T is seconds since the start of the subject's recording.
	T     float64
	Value float64
	Ref   float64
	// Context is what the pipeline infers from device-side data; Label is
	// the dataset's own activity or stage label, for the report only.
	Context  quality.Context
	Label    string
	Features quality.Features
	Sources  int
	Flags    []quality.Flag
	// Agreement is the stage 3 estimate for the figure's source and
	// context, from device data only.
	Agreement *quality.Agreement
}

// DataDir is OHS_QUALITY_DATA, else an ohs-quality-data directory next to
// the repository (or next to its parent, for a worktree inside it).
func DataDir() string {
	if d := os.Getenv("OHS_QUALITY_DATA"); d != "" {
		return d
	}
	repo := filepath.Join("..", "..", "..")
	for _, up := range []string{"..", filepath.Join("..", "..")} {
		d := filepath.Join(repo, up, "ohs-quality-data")
		if exists(d) {
			return d
		}
	}
	return filepath.Join(repo, "..", "ohs-quality-data")
}

// ErrMissing says a dataset is not on disk; the bench skips it.
var ErrMissing = errors.New("dataset not on disk")

// readCSV reads a whole CSV file with a header into rows of strings.
func readCSV(path string) ([]string, [][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = false
	header, err := r.Read()
	if err != nil {
		return nil, nil, err
	}
	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, rec)
	}
	return header, rows, nil
}

func atof(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

// percentile returns the p-quantile (0..1) of the non-NaN values.
func percentile(x []float64, p float64) float64 {
	v := make([]float64, 0, len(x))
	for _, a := range x {
		if !math.IsNaN(a) {
			v = append(v, a)
		}
	}
	if len(v) == 0 {
		return math.NaN()
	}
	sort.Float64s(v)
	i := int(p * float64(len(v)-1))
	return v[i]
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func glob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Strings(m)
	return m
}

// series is a time series sampled at irregular times, sorted.
type series struct {
	t, v []float64
}

// at returns the value of the sample nearest to t within tol seconds.
func (s series) at(t, tol float64) (float64, bool) {
	i := sort.SearchFloat64s(s.t, t)
	best, bd := math.NaN(), math.Inf(1)
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(s.t) {
			if d := math.Abs(s.t[j] - t); d < bd {
				best, bd = s.v[j], d
			}
		}
	}
	return best, bd <= tol
}

// annotate fills the device-side features and the inferred context of a
// subject's heart rate records: the resting heart rate is the 5th
// percentile of the device's own readings, the baseline their median, the
// rate the change over the previous 30 s.
func annotate(recs []Record, dev series, motion func(t float64) float64, asleep func(t float64) bool) {
	rest := percentile(dev.v, 0.05)
	base := percentile(dev.v, 0.5)
	for i := range recs {
		r := &recs[i]
		f := quality.NewFeatures()
		f.HR, f.RestingHR, f.Baseline = r.Value, rest, base
		if prev, ok := dev.at(r.T-30, 5); ok {
			f.Rate = (r.Value - prev) * 2
		}
		if motion != nil {
			f.Motion = motion(r.T)
		}
		r.Features = f
		sl := asleep != nil && asleep(r.T)
		r.Context = quality.ClassifyContext(quality.ContextInput{HR: f.HR, RestingHR: rest, Steps: math.NaN(), Motion: f.Motion, Asleep: sl})
	}
}

// unixTime turns seconds since the epoch into a time.
func unixTime(s float64) time.Time {
	sec := math.Floor(s)
	return time.Unix(int64(sec), int64((s-sec)*1e9)).UTC()
}
