// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
)

// Model is the residual model of stage 4: tree ensembles that predict the
// mean log absolute error of a figure around its prior bias, from its
// context. Several ensembles (one per cross-fitting fold) are averaged in
// log space. Nu is the t the prediction is turned into a scale with.
type Model struct {
	Version   string      `json:"version"`
	Nu        float64     `json:"nu"`
	Ensembles []*Ensemble `json:"ensembles,omitempty"`
	// Metrics lists the metrics the model was fitted on with enough figures;
	// it leaves the others to the priors. Empty means every metric.
	Metrics []string `json:"metrics,omitempty"`
	// Trained says what the model was fitted on, for the report.
	Trained string `json:"trained,omitempty"`
}

// Covers says whether the model applies to a metric.
func (m *Model) Covers(metric string) bool {
	return len(m.Metrics) == 0 || slices.Contains(m.Metrics, metric)
}

// Features is what is known around a figure for the residual model. A NaN
// is unknown; NewFeatures returns all unknown.
type Features struct {
	Hour      float64 // local hour of day, 0 to 24
	Baseline  float64 // the user's usual value of the metric
	Rate      float64 // change of the metric per minute
	Motion    float64 // ENMO, g
	Steps     float64 // per minute
	HR        float64 // heart rate around the figure
	RestingHR float64
}

// NewFeatures returns features with nothing known.
func NewFeatures() Features {
	n := math.NaN()
	return Features{n, n, n, n, n, n, n}
}

// Scale returns the predicted scale of the error of a figure.
func (m *Model) Scale(in Input, ctx Context, metric Metric) float64 {
	var sum float64
	for _, e := range m.Ensembles {
		sum += e.Predict(EncodeFeatures(e.Features, in, ctx, metric))
	}
	return math.Exp(sum/float64(len(m.Ensembles)) - MeanLogAbsT(m.Nu))
}

// EncodeFeatures builds the vector the ensemble reads, by feature name:
// value, baseline_delta, rate, hour, motion, steps, hr, hr_above_rest,
// flags, sources, and one-hot metric=, family=, sensor=, context=.
func EncodeFeatures(names []string, in Input, ctx Context, metric Metric) []float64 {
	f := in.Features
	if f == (Features{}) {
		f = NewFeatures()
	}
	z := metric.Forward(in.Value)
	nflags := 0
	for _, fl := range dedupFlags(in.Flags) {
		if fl.inflating() {
			nflags++
		}
	}
	x := make([]float64, len(names))
	for i, name := range names {
		v := math.NaN()
		switch name {
		case "value":
			v = z
		case "baseline_delta":
			if !math.IsNaN(f.Baseline) && (metric.Scale != Log || f.Baseline > 0) {
				v = z - metric.Forward(f.Baseline)
			}
		case "rate":
			v = math.Abs(f.Rate)
		case "hour":
			v = f.Hour
		case "motion":
			v = f.Motion
		case "steps":
			v = f.Steps
		case "hr":
			v = f.HR
		case "hr_above_rest":
			v = f.HR - f.RestingHR
		case "flags":
			v = float64(nflags)
		case "sources":
			v = float64(in.IndependentSources)
		default:
			if k, val, ok := strings.Cut(name, "="); ok {
				got := ""
				switch k {
				case "metric":
					got = in.Metric
				case "family":
					got = in.Device.Group()
				case "sensor":
					got = in.Device.Sensor
				case "context":
					got = string(ctx)
				}
				v = 0
				if got == val {
					v = 1
				}
			}
		}
		x[i] = v
	}
	return x
}

// ParseModel reads an exported model. A file without an ensemble is the
// placeholder of a repository where the bench has not run: no model.
func ParseModel(b []byte) (*Model, error) {
	var m Model
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if len(m.Ensembles) == 0 {
		return nil, nil
	}
	for i, e := range m.Ensembles {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if m.Ensembles[i], err = ParseEnsemble(raw); err != nil {
			return nil, err
		}
	}
	if m.Version == "" {
		return nil, errors.New("model: no version")
	}
	if m.Nu == 0 {
		m.Nu = math.Inf(1)
	}
	return &m, nil
}

// The bench writes the trained model and its calibration here; they are
// derived numbers only (tree splits, conformal factors).
var (
	//go:embed model.json
	embeddedModel []byte
	//go:embed calibration.json
	embeddedCalibration []byte

	defaultsOnce sync.Once
	defaultModel *Model
	defaultCal   *Calibration
)

func loadDefaults() {
	defaultsOnce.Do(func() {
		m, err := ParseModel(embeddedModel)
		if err != nil {
			panic("quality: embedded model.json: " + err.Error())
		}
		defaultModel = m
		var probe struct {
			Groups map[string]any `json:"groups"`
		}
		if json.Unmarshal(embeddedCalibration, &probe) == nil && len(probe.Groups) > 0 {
			c, err := ParseCalibration(embeddedCalibration)
			if err != nil {
				panic("quality: embedded calibration.json: " + err.Error())
			}
			defaultCal = c
		}
	})
}

// DefaultModel returns the embedded residual model, nil before the bench
// has trained one.
func DefaultModel() *Model { loadDefaults(); return defaultModel }

// DefaultCalibration returns the embedded calibration, nil before the bench
// has fitted one.
func DefaultCalibration() *Calibration { loadDefaults(); return defaultCal }
