// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"slices"
)

// Metric names. They are the canonical quantity types of the storage model
// where one exists; the sleep ones name the nightly figures of a sleep
// session.
const (
	MetricHeartRate         = "heart_rate"
	MetricRestingHeartRate  = "resting_heart_rate"
	MetricHRVRMSSD          = "hrv_rmssd"
	MetricHRVSDNN           = "hrv_sdnn"
	MetricSleepDuration     = "sleep_duration"
	MetricSleepStageMinutes = "sleep_stage_minutes"
	MetricStepCount         = "step_count"
	MetricActiveEnergy      = "active_energy_burned"
	MetricOxygenSaturation  = "oxygen_saturation"
	MetricVO2Max            = "vo2_max"
	MetricBloodGlucose      = "blood_glucose"
	MetricSystolic          = "blood_pressure_systolic"
	MetricDiastolic         = "blood_pressure_diastolic"
	MetricBodyMass          = "body_mass"
	MetricBodyFat           = "body_fat_percentage"
)

// Scale is the space errors are modelled in.
type Scale int

const (
	// Linear errors add: a heart rate reads 3 bpm high.
	Linear Scale = iota
	// Log errors multiply: an HRV reads 8% low. Bias, sigma, the tolerance
	// and the intervals are in natural log units, the value is not.
	Log
)

// Grain is the alignment unit of a metric for agreement between sources.
type Grain string

// Grains.
const (
	GrainReading Grain = "reading" // 5 minute buckets
	GrainNight   Grain = "night"
	GrainDay     Grain = "day"
)

// Metric is what the score knows about one kind of figure.
type Metric struct {
	Name string
	// Unit is the storage unit of the value.
	Unit  string
	Scale Scale
	Grain Grain
	// Abs is the absolute tolerance, Rel the relative one. With both set,
	// Abs applies up to Above and Rel above it (heart rate, glucose). With
	// only Rel, the tolerance is Rel times the value; on a Log metric it is
	// log(1 + Rel).
	Abs, Rel, Above float64
	// Hard limits reject (grade X); outside the soft band a value is
	// suspect and its sigma is inflated. Defaults for adults from the
	// research note, section 4.1.
	HardMin, HardMax float64
	SoftMin, SoftMax float64
	// Resolution is the smallest step the value is reported in. Half of it
	// floors an absolute error before its logarithm is taken.
	Resolution float64
	// Basis says where the tolerance comes from.
	Basis string
}

// Tolerance returns the clinically meaningful error for a figure of value v,
// in the metric's error space (value units, or log units on a Log metric).
func (m Metric) Tolerance(v float64) float64 {
	if m.Scale == Log {
		return math.Log1p(m.Rel)
	}
	switch {
	case m.Abs > 0 && m.Rel > 0:
		if v > m.Above {
			return m.Rel * v
		}
		return m.Abs
	case m.Rel > 0:
		return m.Rel * math.Abs(v)
	}
	return m.Abs
}

// Forward maps a value to the error space.
func (m Metric) Forward(v float64) float64 {
	if m.Scale == Log {
		return math.Log(v)
	}
	return v
}

// Inverse maps a point of the error space back to a value.
func (m Metric) Inverse(z float64) float64 {
	if m.Scale == Log {
		return math.Exp(z)
	}
	return z
}

// Plausible is the outcome of the range check.
type Plausible int

// Plausibility outcomes.
const (
	PlausibleOK Plausible = iota
	PlausibleSuspect
	PlausibleReject
)

// Plausibility checks v against the metric's hard and soft limits.
func (m Metric) Plausibility(v float64) Plausible {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0) || v < m.HardMin || v > m.HardMax:
		return PlausibleReject
	case m.Scale == Log && v <= 0:
		return PlausibleReject
	case v < m.SoftMin || v > m.SoftMax:
		return PlausibleSuspect
	}
	return PlausibleOK
}

// metrics is the v1 table of the spec. Limits are the defaults of the
// research note (section 4.1, tagged U there): they mirror the validator's
// value rules for the same quantities and only decide the grade X and the
// suspect flag of this package.
var metrics = map[string]Metric{
	MetricHeartRate: {Name: MetricHeartRate, Unit: "bpm", Grain: GrainReading, Abs: 5, Rel: 0.10, Above: 100,
		HardMin: 25, HardMax: 230, SoftMin: 35, SoftMax: 205, Resolution: 1, Basis: "CTA-2065 MAPE 10%"},
	MetricRestingHeartRate: {Name: MetricRestingHeartRate, Unit: "bpm", Grain: GrainNight, Abs: 3,
		HardMin: 30, HardMax: 120, SoftMin: 35, SoftMax: 100, Resolution: 1, Basis: "literature, nightly resting heart rate"},
	MetricHRVRMSSD: {Name: MetricHRVRMSSD, Unit: "ms", Scale: Log, Grain: GrainNight, Rel: 0.10,
		HardMin: 3, HardMax: 300, SoftMin: 5, SoftMax: 200, Resolution: 1, Basis: "literature, 10% on the log scale"},
	MetricHRVSDNN: {Name: MetricHRVSDNN, Unit: "ms", Scale: Log, Grain: GrainReading, Rel: 0.10,
		HardMin: 3, HardMax: 300, SoftMin: 5, SoftMax: 250, Resolution: 1, Basis: "literature, 10% on the log scale"},
	MetricSleepDuration: {Name: MetricSleepDuration, Unit: "min", Grain: GrainNight, Abs: 30,
		HardMin: 0, HardMax: 960, SoftMin: 60, SoftMax: 720, Resolution: 1, Basis: "polysomnography validation practice"},
	MetricSleepStageMinutes: {Name: MetricSleepStageMinutes, Unit: "min", Grain: GrainNight, Abs: 20,
		HardMin: 0, HardMax: 960, SoftMin: 0, SoftMax: 480, Resolution: 1, Basis: "polysomnography validation practice"},
	MetricStepCount: {Name: MetricStepCount, Unit: "count", Grain: GrainDay, Rel: 0.10,
		HardMin: 0, HardMax: 50000, SoftMin: 0, SoftMax: 40000, Resolution: 1, Basis: "literature, daily total"},
	MetricActiveEnergy: {Name: MetricActiveEnergy, Unit: "kcal", Grain: GrainDay, Rel: 0.10,
		HardMin: 0, HardMax: 8000, SoftMin: 0, SoftMax: 4000, Resolution: 1, Basis: "literature, daily total"},
	MetricOxygenSaturation: {Name: MetricOxygenSaturation, Unit: "percent", Grain: GrainReading, Abs: 2,
		HardMin: 50, HardMax: 100, SoftMin: 88, SoftMax: 100, Resolution: 1, Basis: "ISO 80601-2-61 (ARMS 4%)"},
	MetricVO2Max: {Name: MetricVO2Max, Unit: "mL/kg/min", Grain: GrainDay, Abs: 3.5,
		HardMin: 10, HardMax: 90, SoftMin: 15, SoftMax: 80, Resolution: 0.1, Basis: "literature, 1 MET"},
	MetricBloodGlucose: {Name: MetricBloodGlucose, Unit: "mg/dL", Grain: GrainReading, Abs: 15, Rel: 0.15, Above: 100,
		HardMin: 20, HardMax: 600, SoftMin: 40, SoftMax: 400, Resolution: 1, Basis: "ISO 15197"},
	MetricSystolic: {Name: MetricSystolic, Unit: "mmHg", Grain: GrainReading, Abs: 5,
		HardMin: 60, HardMax: 260, SoftMin: 80, SoftMax: 180, Resolution: 1, Basis: "ISO 81060-2"},
	MetricDiastolic: {Name: MetricDiastolic, Unit: "mmHg", Grain: GrainReading, Abs: 5,
		HardMin: 30, HardMax: 160, SoftMin: 45, SoftMax: 110, Resolution: 1, Basis: "ISO 81060-2"},
	MetricBodyMass: {Name: MetricBodyMass, Unit: "kg", Grain: GrainDay, Abs: 0.5,
		HardMin: 20, HardMax: 350, SoftMin: 30, SoftMax: 250, Resolution: 0.1, Basis: "practice"},
	MetricBodyFat: {Name: MetricBodyFat, Unit: "percent", Grain: GrainDay, Abs: 2,
		HardMin: 3, HardMax: 70, SoftMin: 5, SoftMax: 60, Resolution: 0.1, Basis: "practice"},
}

// LookupMetric returns the registered metric of a name.
func LookupMetric(name string) (Metric, bool) {
	m, ok := metrics[name]
	return m, ok
}

// Metrics lists the registered metrics by name.
func Metrics() []Metric {
	out := make([]Metric, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b Metric) int {
		if a.Name < b.Name {
			return -1
		}
		return 1
	})
	return out
}
