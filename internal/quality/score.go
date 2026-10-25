// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"slices"
)

// Input is one figure to score and what is known around it.
type Input struct {
	Metric string
	// Value is the reported value, in the metric's storage unit.
	Value   float64
	Device  Device
	Context Context
	// Flags are what stage 0 and the validator found (suspect_range,
	// rate_jump, relay_copy, rejected...).
	Flags []Flag
	// Agreement is the per-user stage 3 estimate for this source, metric
	// and context, nil when no other independent source overlaps.
	Agreement *Agreement
	// IndependentSources counts the independent sources of the metric for
	// the user, this one included.
	IndependentSources int
	Features           Features
}

// Quality is the contract of the spec. For a metric on the log scale
// (HRV), tolerance, bias, bias_sd and sigma are in natural log units, about
// relative errors; value, estimate and the intervals are in milliseconds.
type Quality struct {
	Q          float64    `json:"q"`
	Grade      string     `json:"grade"`
	Tolerance  float64    `json:"tolerance"`
	Bias       float64    `json:"bias"`
	BiasSD     float64    `json:"bias_sd"`
	Sigma      float64    `json:"sigma"`
	Estimate   float64    `json:"estimate"`
	Interval68 [2]float64 `json:"interval_68"`
	Interval95 [2]float64 `json:"interval_95"`
	Context    Context    `json:"context"`
	Components Components `json:"components"`
	Flags      []Flag     `json:"flags"`
	Evidence   Evidence   `json:"evidence"`
	// Predictive is the calibrated error distribution the score and the
	// intervals come from: value - true ~ Location + Scale * t(Nu).
	Predictive Predictive `json:"predictive"`
}

// Predictive is a location scale Student t in the metric's error space.
type Predictive struct {
	Space    string  `json:"space"` // linear or log
	Location float64 `json:"location"`
	Scale    float64 `json:"scale"`
	Nu       float64 `json:"nu"` // 0 is the normal
	// Center is the estimate of the true value in the error space.
	Center float64 `json:"center"`
}

// Components says what each stage contributed.
type Components struct {
	Prior struct {
		Bias  float64 `json:"bias"`
		Sigma float64 `json:"sigma"`
	} `json:"prior"`
	Agreement struct {
		Method string  `json:"method,omitempty"`
		Weight float64 `json:"weight"` // n_eff / (w0 + n_eff)
		Sigma  float64 `json:"sigma,omitempty"`
	} `json:"agreement"`
	ML struct {
		Factor float64 `json:"factor"` // multiplies sigma
	} `json:"ml"`
	Plausibility struct {
		Factor float64 `json:"factor"` // multiplies sigma
	} `json:"plausibility"`
}

// Evidence says what the score rests on.
type Evidence struct {
	SourcesIndependent int     `json:"sources_independent"`
	PriorCitation      string  `json:"prior_citation"`
	PriorTag           string  `json:"prior_tag"`
	PriorLevel         string  `json:"prior_level"`
	PriorsVersion      string  `json:"priors_version"`
	ModelVersion       string  `json:"model_version"`
	CalibrationVersion string  `json:"calibration_version"`
	CalibrationGroup   string  `json:"calibration_group,omitempty"`
	Gamma              float64 `json:"gamma"`
	// Shift is the calibration group's bias correction, in units of the
	// uncalibrated scale.
	Shift float64 `json:"shift"`
}

// maxMLFactor bounds the residual model's scaling of the prior sigma.
const maxMLFactor = 4

// Scorer scores figures. Priors is required; Model and Calibration are
// optional, and without them the score comes from the priors alone.
type Scorer struct {
	Priors      *Priors
	Model       *Model
	Calibration *Calibration
	// FlagFactor multiplies sigma once per distinct inflating flag, 2 when
	// zero.
	FlagFactor float64
}

// NewScorer returns a scorer with the embedded priors, residual model and
// calibration.
func NewScorer() *Scorer {
	return &Scorer{Priors: DefaultPriors(), Model: DefaultModel(), Calibration: DefaultCalibration()}
}

// ProbWithin is P(|bias + s T_nu| <= tol), the score's q.
func ProbWithin(tol, bias, s, nu float64) float64 {
	if tol <= 0 {
		return 0
	}
	if s <= 0 {
		if math.Abs(bias) <= tol {
			return 1
		}
		return 0
	}
	return math.Max(0, StudentTCDF((tol-bias)/s, nu)-StudentTCDF((-tol-bias)/s, nu))
}

func gradeOf(q float64) string {
	switch {
	case q >= 0.9:
		return "A"
	case q >= 0.75:
		return "B"
	case q >= 0.5:
		return "C"
	}
	return "D"
}

func dedupFlags(fs []Flag) []Flag {
	out := slices.Clone(fs)
	slices.Sort(out)
	return slices.Compact(out)
}

// Score runs stages 1 to 5 on one figure.
func (s *Scorer) Score(in Input) Quality {
	ctx := in.Context
	if ctx == "" {
		ctx = ContextUnknown
	}
	out := Quality{Context: ctx, Estimate: in.Value}
	m, ok := LookupMetric(in.Metric)
	if !ok {
		out.Flags = []Flag{FlagUnknownMetric}
		return out
	}
	flags := slices.Clone(in.Flags)
	// Stage 1: the hard limits reject, the soft ones flag.
	switch m.Plausibility(in.Value) {
	case PlausibleReject:
		flags = append(flags, FlagRejected)
	case PlausibleSuspect:
		flags = append(flags, FlagSuspectRange)
	}
	if in.IndependentSources <= 1 {
		flags = append(flags, FlagSingleSource)
	}
	flags = dedupFlags(flags)
	out.Flags = flags
	out.Tolerance = m.Tolerance(in.Value)
	if slices.Contains(flags, FlagRejected) {
		out.Grade = "X"
		out.Interval68 = [2]float64{in.Value, in.Value}
		out.Interval95 = out.Interval68
		return out
	}

	// Stage 2: the prior.
	z := m.Forward(in.Value)
	p, level, _ := s.Priors.Lookup(in.Metric, in.Device, ctx)
	bias, biasSD, sigmaPrior := priorBias(p, m, z), priorBiasSD(p, m, z), priorSigma(p, m, z)
	out.Components.Prior.Bias, out.Components.Prior.Sigma = bias, sigmaPrior
	out.Evidence = Evidence{SourcesIndependent: in.IndependentSources, PriorCitation: p.Citation, PriorTag: p.Tag,
		PriorLevel: level, PriorsVersion: s.Priors.Version, ModelVersion: "none", CalibrationVersion: "none"}

	// Stage 3: agreement with the user's other sources.
	agreeRatio := 1.0
	if ag := in.Agreement; ag != nil && ag.NEff > 0 && ag.Sigma > 0 {
		w0 := W0(p.Tag)
		post := ShrinkSigma(sigmaPrior, ag.Sigma, ag.NEff, w0)
		agreeRatio = post / sigmaPrior
		out.Components.Agreement.Method = ag.Method
		out.Components.Agreement.Weight = ag.NEff / (w0 + ag.NEff)
		out.Components.Agreement.Sigma = ag.Sigma
		if ag.Anchored && ag.BiasSD > 0 {
			bias, biasSD = BiasPosterior(bias, biasSD, ag.Bias, ag.BiasSD*math.Sqrt(ag.NEff), ag.NEff)
		}
	}

	// Stage 4: the residual model scales sigma for this figure's context.
	mlFactor := 1.0
	if s.Model != nil && s.Model.Ensemble != nil && s.Model.Covers(in.Metric) {
		sML := s.Model.Scale(in, ctx, m)
		// The model predicts the whole scale around the prior bias; the part
		// the prior's bias uncertainty explains is taken out.
		bsd := priorBiasSD(p, m, z)
		sigmaML := math.Sqrt(math.Max(sML*sML-bsd*bsd, 0))
		// The model scales the prior within a factor of four either way: a
		// figure unlike any it was trained on must not get an absurd sigma.
		mlFactor = math.Min(maxMLFactor, math.Max(1/maxMLFactor, sigmaML/sigmaPrior))
		out.Evidence.ModelVersion = s.Model.Version
	}
	out.Components.ML.Factor = mlFactor

	// Stage 1 again: soft flags inflate.
	plaus := 1.0
	ff := s.FlagFactor
	if ff == 0 {
		ff = 2
	}
	for _, f := range flags {
		if f.inflating() {
			plaus *= ff
		}
	}
	out.Components.Plausibility.Factor = plaus
	sigma := sigmaPrior * agreeRatio * mlFactor * plaus

	// Stage 5: calibration. The group's shift moves the bias by what the
	// prior missed against the references, in units of the uncalibrated
	// scale; gamma rescales.
	gamma, nu, shift := 1.0, p.Nu, 0.0
	if g, key, ok := s.Calibration.Lookup([]string{in.Metric, in.Device.Group(), string(ctx)}); ok {
		gamma, nu, shift = g.Gamma, g.Nu, g.Shift
		out.Evidence.CalibrationVersion = s.Calibration.Version
		out.Evidence.CalibrationGroup = key
	}
	out.Evidence.Gamma, out.Evidence.Shift = gamma, shift
	raw := math.Hypot(biasSD, sigma)
	bias += shift * raw
	scale := gamma * raw

	out.Bias, out.BiasSD, out.Sigma = bias, biasSD, sigma
	out.Q = ProbWithin(out.Tolerance, bias, scale, nu)
	if qm := s.Calibration.QMapFor([]string{in.Metric, in.Device.Group(), string(ctx)}); qm != nil {
		out.Q = qm.Apply(out.Q)
	}
	out.Grade = gradeOf(out.Q)
	space := "linear"
	if m.Scale == Log {
		space = "log"
	}
	nuOut := nu
	if math.IsInf(nu, 1) {
		nuOut = 0
	}
	out.Predictive = Predictive{Space: space, Location: bias, Scale: scale, Nu: nuOut, Center: z - bias}
	out.Estimate = m.Inverse(z - bias)
	out.Interval68[0], out.Interval68[1] = out.IntervalAt(0.68)
	out.Interval95[0], out.Interval95[1] = out.IntervalAt(0.95)
	return out
}

// The relative parts of a prior apply to the value; on the log scale the
// prior is already relative and they are not used.
func priorBias(p Prior, m Metric, z float64) float64 {
	if m.Scale == Log {
		return p.Bias
	}
	return p.BiasAt(z)
}

func priorBiasSD(p Prior, m Metric, z float64) float64 {
	if m.Scale == Log {
		return p.BiasSD
	}
	return p.BiasSDAt(z)
}

func priorSigma(p Prior, m Metric, z float64) float64 {
	if m.Scale == Log {
		return p.Sigma
	}
	return p.SigmaAt(z)
}

// IntervalAt returns the calibrated central interval of the given level
// for the true value, in the value's unit.
func (q Quality) IntervalAt(level float64) (float64, float64) {
	p := q.Predictive
	if p.Scale == 0 {
		return q.Estimate, q.Estimate
	}
	nu := p.Nu
	if nu == 0 {
		nu = math.Inf(1)
	}
	h := p.Scale * StudentTQuantile((1+level)/2, nu)
	lo, hi := p.Center-h, p.Center+h
	if p.Space == "log" {
		return math.Exp(lo), math.Exp(hi)
	}
	return lo, hi
}
