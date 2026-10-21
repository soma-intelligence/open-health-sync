// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"math"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Value-range rules for quantity samples.
//
// Why this is a table and not a switch: the switch it replaces covered 16 of the
// 37 quantity types the storage-unit contract declares, so two thirds of the
// canonical vocabulary reached the hypertables with no value check at all. A
// table plus the coverage guard in validator_rules_test.go makes an undeclared
// range a test failure rather than a silent hole, which is the same shape as the
// mapper registry: adding a metric forces you to say what a plausible value is.
//
// Every bound below is expressed in the STORAGE unit for its quantity type
// (model.StorageUnit). That is only sound because pipeline.Processor runs
// model.CanonicalizeUnits immediately before validation, so a sample reaching
// here is already in kg, celsius, mg/dL and so on. The unit guard test asserts
// the two tables agree; rule.Unit is what documents the assumption in place.

// Severity separates a value that cannot be real from one that is merely
// unlikely.
//
// The distinction matters because parking a record is destructive to the user's
// series: a rejected sample does not reach the hypertables, so the metric reads
// as missing and the grade drops. A resting heart rate of 300 bpm is a sensor
// artifact and dropping it is right. A body mass of 310 kg is rare but real for
// somebody, and dropping it would quietly erase that person's weight history.
// Suspect values are written and flagged so downstream can decide, rather than
// being lost in failed_sync_records.
type Severity string

const (
	// SeverityReject means the value is outside what the metric can physically
	// be. The record is parked in failed_sync_records and never written.
	SeverityReject Severity = "reject"
	// SeveritySuspect means the value is possible but implausible. The record is
	// written, and the offending sample is flagged in its metadata.
	SeveritySuspect Severity = "suspect"
)

// unbounded is the upper hard bound for metrics with no physical ceiling
// (cumulative counts, energy, distance, masses). It also selects the phrasing:
// an error against an unbounded rule reads "must be non-negative" / "must be
// positive" rather than "out of range", which is the wording the pre-table
// validator used and what the recovery tooling greps for.
var unbounded = math.Inf(1)

// rangeRule is the value contract for one canonical quantity type.
//
// Bands nest: HardMin <= SoftMin < SoftMax <= HardMax. Outside the hard band is
// a reject; inside the hard band but outside the soft band is a suspect. A value
// never produces both, so one bad sample stays one error.
type rangeRule struct {
	// Unit is the storage unit the bounds assume. Empty means the quantity type
	// has no declared storage unit (a legacy alias), so no unit check runs.
	Unit string

	HardMin, HardMax float64
	// HardMinExclusive rejects a value equal to HardMin. Used by the masses,
	// where zero is not a measurement but a missing reading.
	HardMinExclusive bool

	SoftMin, SoftMax float64
}

// rangeRules is keyed by canonical quantity type. Hard bands are carried over
// unchanged from the switch that preceded this table, so this change adds checks
// without narrowing any existing one.
var rangeRules = map[string]rangeRule{
	// --- Cardiovascular -----------------------------------------------------
	model.QTHeartRate: {
		Unit: "bpm",
		// Soft floor 30: sub-30 bpm happens in trained athletes at rest and in
		// real bradycardia, so it is flagged, never dropped.
		HardMin: 20, HardMax: 300, SoftMin: 30, SoftMax: 220,
	},
	model.QTRestingHeartRate: {
		Unit: "bpm",
		// Resting values above ~110 usually mean the provider mislabelled an
		// active sample rather than that the heart rate is real.
		HardMin: 20, HardMax: 300, SoftMin: 30, SoftMax: 110,
	},
	"walking_heart_rate_average": {
		Unit:    "bpm",
		HardMin: 20, HardMax: 300, SoftMin: 50, SoftMax: 160,
	},
	model.QTRespiratoryRate: {
		// Storage unit is "bpm" meaning breaths per minute here, see the note in
		// mapper/units.go: HealthKit reports respiratory rate as count/min and
		// apple_health has always canonicalized count/min to "bpm".
		Unit:    "bpm",
		HardMin: 4, HardMax: 60, SoftMin: 8, SoftMax: 30,
	},
	model.QTHRVSDNN: {
		Unit: "ms",
		// Soft floor 3 ms catches the classic dropped reading: SDNN of exactly 0
		// means no beat-to-beat variability at all, which no live heart produces.
		HardMin: 0, HardMax: unbounded, SoftMin: 3, SoftMax: 300,
	},
	model.QTHRVRMSSD: {
		Unit: "ms",
		// Same reasoning as SDNN: an RMSSD of 0 is a dropped reading. Nightly
		// RMSSD rarely passes 250 ms even in endurance athletes.
		HardMin: 0, HardMax: unbounded, SoftMin: 3, SoftMax: 300,
	},
	"vo2_max": {
		Unit: "mL/kg/min",
		// The human record is around 96; 90 as the soft ceiling leaves room for
		// elite athletes without accepting a decimal-shifted value.
		HardMin: 0, HardMax: 150, SoftMin: 10, SoftMax: 90,
	},
	model.QTOxygenSaturation: {
		Unit: "percent",
		// Soft floor 70: wrist pulse oximeters read badly under about 80, and a
		// genuine severe desaturation must still land rather than be dropped.
		HardMin: 0, HardMax: 100, SoftMin: 70, SoftMax: 100,
	},
	"blood_pressure_systolic": {
		Unit:    "mmHg",
		HardMin: 50, HardMax: 300, SoftMin: 70, SoftMax: 200,
	},
	"blood_pressure_diastolic": {
		Unit:    "mmHg",
		HardMin: 20, HardMax: 200, SoftMin: 40, SoftMax: 130,
	},
	"pulse_wave_velocity": {
		Unit:    "m/s",
		HardMin: 0, HardMax: 100, SoftMin: 3, SoftMax: 25,
	},
	"vascular_age": {
		Unit:    "years",
		HardMin: 0, HardMax: 150, SoftMin: 15, SoftMax: 110,
	},

	// --- Temperature --------------------------------------------------------
	model.QTBodyTemperature: {
		Unit:    "celsius",
		HardMin: 25, HardMax: 45, SoftMin: 33, SoftMax: 42,
	},
	model.QTSkinTemperature: {
		// Skin runs cooler than core and tracks ambient, so the band is wider
		// than body_temperature in both directions.
		Unit:    "celsius",
		HardMin: 0, HardMax: 50, SoftMin: 20, SoftMax: 42,
	},
	model.QTBodyTemperatureDeviation: {
		// A delta, not an absolute, so it is legitimately negative.
		Unit:    "celsius",
		HardMin: -15, HardMax: 15, SoftMin: -3, SoftMax: 3,
	},

	// --- Body composition ---------------------------------------------------
	"body_mass": {
		Unit:    "kg",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 20, SoftMax: 300,
	},
	"body_mass_index": {
		Unit:    "kg/m2",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 10, SoftMax: 70,
	},
	"lean_body_mass": {
		Unit:    "kg",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 10, SoftMax: 150,
	},
	"body_fat_mass": {
		Unit:    "kg",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 1, SoftMax: 150,
	},
	"muscle_mass": {
		Unit:    "kg",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 5, SoftMax: 120,
	},
	"bone_mass": {
		// The adult skeleton is roughly 2 to 4 kg dry; scales report 2 to 5.
		Unit:    "kg",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 1, SoftMax: 8,
	},
	"water_mass": {
		Unit:    "kg",
		HardMin: 0, HardMinExclusive: true, HardMax: unbounded, SoftMin: 10, SoftMax: 100,
	},
	"body_fat_percentage": {
		// Essential fat is about 3 percent in men and 10 in women, so a reading
		// of 0 or 100 is a scale that failed to measure, not a body.
		Unit:    "percent",
		HardMin: 0, HardMax: 100, SoftMin: 2, SoftMax: 70,
	},
	"visceral_fat_rating": {
		Unit:    "index",
		HardMin: 0, HardMax: 100, SoftMin: 1, SoftMax: 40,
	},
	"height": {
		Unit:    "cm",
		HardMin: 10, HardMax: 300, SoftMin: 45, SoftMax: 220,
	},
	"blood_glucose": {
		// mg/dL is the storage unit; mmol/L readings are converted upstream by
		// model.ToStorageUnit ("blood_glucose|mmol/l"). The hard ceiling covers
		// severe DKA and HHS, which reach roughly 1000 to 1200.
		Unit:    "mg/dL",
		HardMin: 0, HardMax: 1500, SoftMin: 30, SoftMax: 600,
	},

	// --- Activity -----------------------------------------------------------
	// Soft ceilings here are sized against the observed extremes in the live
	// dataset (68302 steps, 17536 kcal, 70962 m in a single sample) with room to
	// spare, because these arrive as totals over windows the validator cannot
	// see. Narrowing them needs a look at real data first, not intuition.
	model.QTStepCount: {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 200000,
	},
	model.QTStepCountIntraday: {
		// Intraday buckets are strictly smaller than their daily parent, but the
		// bucket width is not carried on the sample, so the parent ceiling is the
		// only bound that cannot produce false suspects.
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 200000,
	},
	model.QTFlightsClimbed: {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 2000,
	},
	model.QTActiveEnergyBurned: {
		Unit:    "kcal",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 30000,
	},
	model.QTActiveEnergyIntraday: {
		Unit:    "kcal",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 30000,
	},
	model.QTBasalEnergyBurned: {
		Unit:    "kcal",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 20000,
	},
	model.QTTotalEnergyBurned: {
		Unit:    "kcal",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 30000,
	},
	// Health Connect caps a basal metabolic rate at 10,000 kcal a day.
	model.QTBasalMetabolicRate: {
		Unit:    "kcal/d",
		HardMin: 0, HardMax: 10000, SoftMin: 500, SoftMax: 5000,
	},
	model.QTDistanceWalkingRunning: {
		Unit:    "m",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 300000,
	},
	model.QTDistanceWalkRunIntday: {
		Unit:    "m",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 300000,
	},

	// Apple ring times, in minutes since the storage unit was declared. Unlike
	// the totals above these have a real ceiling: a sample covers at most the
	// day it belongs to, so 1440 is physical, not defensive. The hard bound is
	// also what catches a regression to millisecond storage, since any exercise
	// minute expressed in ms lands at 60000.
	"apple_exercise_time": {
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 600,
	},
	"apple_move_time": {
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 600,
	},
	"apple_stand_time": {
		// Stand time accrues in short bursts across the waking day, so its
		// plausible ceiling sits higher than exercise.
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 960,
	},

	// --- Apple Health long tail ---------------------------------------------
	// Distances arrive as totals over windows the validator cannot see, like
	// distance_walking_running above, so only the soft ceiling bounds them.
	"distance_cycling": {
		Unit:    "m",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 500000,
	},
	"distance_swimming": {
		Unit:    "m",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 50000,
	},
	"distance_wheelchair": {
		Unit:    "m",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 300000,
	},
	"distance_downhill_snow_sports": {
		Unit:    "m",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 200000,
	},
	"push_count": {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 100000,
	},
	"nike_fuel": {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 50000,
	},
	"physical_effort": {
		// kcal/kg/h is numerically close to METs: 1 at rest, about 20 for an
		// elite effort.
		Unit:    "kcal/kg/h",
		HardMin: 0, HardMax: 50, SoftMin: 0.5, SoftMax: 25,
	},
	"walking_speed": {
		Unit:    "m/s",
		HardMin: 0, HardMax: 15, SoftMin: 0.1, SoftMax: 3,
	},
	"running_speed": {
		Unit:    "m/s",
		HardMin: 0, HardMax: 20, SoftMin: 0.5, SoftMax: 11,
	},
	"running_power": {
		Unit:    "W",
		HardMin: 0, HardMax: 3000, SoftMin: 0, SoftMax: 1000,
	},
	"cycling_power": {
		Unit:    "W",
		HardMin: 0, HardMax: 5000, SoftMin: 0, SoftMax: 2000,
	},
	"running_stride_length": {
		Unit:    "m",
		HardMin: 0, HardMax: 5, SoftMin: 0.3, SoftMax: 3,
	},
	"running_vertical_oscillation": {
		Unit:    "cm",
		HardMin: 0, HardMax: 50, SoftMin: 2, SoftMax: 20,
	},
	"running_ground_contact_time": {
		Unit:    "ms",
		HardMin: 0, HardMax: 2000, SoftMin: 100, SoftMax: 500,
	},
	"underwater_depth": {
		Unit:    "m",
		HardMin: 0, HardMax: 1000, SoftMin: 0, SoftMax: 100,
	},
	"waist_circumference": {
		Unit:    "cm",
		HardMin: 0, HardMinExclusive: true, HardMax: 300, SoftMin: 40, SoftMax: 200,
	},
	"apple_sleeping_wrist_temperature": {
		// Wrist skin during sleep, absolute: cooler than core.
		Unit:    "celsius",
		HardMin: 0, HardMax: 50, SoftMin: 28, SoftMax: 40,
	},
	"basal_body_temperature": {
		Unit:    "celsius",
		HardMin: 25, HardMax: 45, SoftMin: 34, SoftMax: 39,
	},
	"water_temperature": {
		Unit:    "celsius",
		HardMin: -5, HardMax: 100, SoftMin: 0, SoftMax: 40,
	},
	"blood_alcohol_content": {
		// Percent by volume: 0.08 is a common driving limit, above 0.4 is
		// rarely survived.
		Unit:    "percent",
		HardMin: 0, HardMax: 100, SoftMin: 0, SoftMax: 0.5,
	},
	"number_of_alcoholic_beverages": {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 30,
	},
	"electrodermal_activity": {
		Unit:    "uS",
		HardMin: 0, HardMax: 1000, SoftMin: 0, SoftMax: 100,
	},
	"environmental_audio_exposure": {
		Unit:    "dBASPL",
		HardMin: 0, HardMax: 200, SoftMin: 20, SoftMax: 130,
	},
	"headphone_audio_exposure": {
		Unit:    "dBASPL",
		HardMin: 0, HardMax: 200, SoftMin: 20, SoftMax: 120,
	},
	"environmental_sound_reduction": {
		Unit:    "dBASPL",
		HardMin: 0, HardMax: 200, SoftMin: 0, SoftMax: 60,
	},
	"forced_expiratory_volume_1": {
		Unit:    "L",
		HardMin: 0, HardMax: 15, SoftMin: 0.3, SoftMax: 7,
	},
	"forced_vital_capacity": {
		Unit:    "L",
		HardMin: 0, HardMax: 15, SoftMin: 0.5, SoftMax: 8,
	},
	"peak_expiratory_flow_rate": {
		Unit:    "L/min",
		HardMin: 0, HardMax: 1500, SoftMin: 50, SoftMax: 900,
	},
	"inhaler_usage": {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 30,
	},
	"insulin_delivery": {
		Unit:    "IU",
		HardMin: 0, HardMax: 1000, SoftMin: 0, SoftMax: 150,
	},
	"number_of_times_fallen": {
		Unit:    "count",
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 10,
	},
	"uv_exposure": {
		Unit:    "index",
		HardMin: 0, HardMax: 30, SoftMin: 0, SoftMax: 15,
	},

	// --- Dimensionless provider indices -------------------------------------
	model.QTReadinessHRVBalance: {
		Unit:    "index",
		HardMin: 0, HardMax: 100, SoftMin: 1, SoftMax: 100,
	},
	model.QTStrain: {
		// WHOOP strain is a 0 to 21 scale. Anything above it means the value was
		// read from the wrong field, but it is written and flagged rather than
		// dropped so the mismatch is visible instead of looking like missing data.
		Unit:    "index",
		HardMin: 0, HardMax: 100, SoftMin: 0, SoftMax: 21,
	},
	model.QTWorkoutStrain: {
		Unit:    "index",
		HardMin: 0, HardMax: 100, SoftMin: 0, SoftMax: 21,
	},
	model.QTReadinessScore: {
		Unit:    "score",
		HardMin: 0, HardMax: 100, SoftMin: 1, SoftMax: 100,
	},
	model.QTSleepScore: {
		Unit:    "score",
		HardMin: 0, HardMax: 100, SoftMin: 1, SoftMax: 100,
	},
	// Minutes of one day: more than 1440 cannot happen. Most of a day spent
	// in high stress (soft ceiling 16 h) is possible but worth a flag.
	model.QTStressHighTime: {
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 960,
	},
	model.QTRecoveryHighTime: {
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 960,
	},
	// Minutes of one day at an intensity. Ten hours of moderate or vigorous
	// activity is rare enough to flag.
	model.QTModerateActivityTime: {
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 600,
	},
	model.QTVigorousActivityTime: {
		Unit:    "min",
		HardMin: 0, HardMax: 1440, SoftMin: 0, SoftMax: 600,
	},

	// A person's own ratings. The scale is the contract (model.SelfReportScales):
	// the connector refuses anything else before it is stored, so these bounds
	// only catch a rating that reached the pipeline another way.
	model.QTReportedFeeling:        selfReportRule(model.QTReportedFeeling),
	model.QTReportedMood:           selfReportRule(model.QTReportedMood),
	model.QTReportedStress:         selfReportRule(model.QTReportedStress),
	model.QTReportedEnergy:         selfReportRule(model.QTReportedEnergy),
	model.QTReportedSleepQuality:   selfReportRule(model.QTReportedSleepQuality),
	model.QTReportedMuscleSoreness: selfReportRule(model.QTReportedMuscleSoreness),
	model.QTReportedExertion:       selfReportRule(model.QTReportedExertion),
	model.QTReportedPain:           selfReportRule(model.QTReportedPain),

	// --- Legacy aliases without a declared storage unit ---------------------
	// These are not in model.StorageUnits, so CanonicalizeUnits leaves their
	// unit untouched and the rule declares no Unit: enforcing one would reject
	// whatever spelling the source happens to send. They are kept because the
	// pre-table validator checked them and rows carrying these keys exist.
	"spo2": {
		HardMin: 0, HardMax: 100, SoftMin: 70, SoftMax: 100,
	},
	"daily_spo2": {
		HardMin: 0, HardMax: 100, SoftMin: 70, SoftMax: 100,
	},
	"distance": {
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 300000,
	},
	"exercise_time": {
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 86400,
	},
	"intraday_activity": {
		HardMin: 0, HardMax: unbounded, SoftMin: 0, SoftMax: 200000,
	},
}

// selfReportRule is the rule of a self report type: its scale, as hard and
// soft bounds alike, since every rating on the scale is as plausible as any
// other.
func selfReportRule(qt string) rangeRule {
	sc, _ := model.SelfReportScale(qt)
	return rangeRule{Unit: "score",
		HardMin: float64(sc.Min), HardMax: float64(sc.Max), SoftMin: float64(sc.Min), SoftMax: float64(sc.Max)}
}
