// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

// This table pre-canonicalizes HealthKit's unit spellings during Apple Health
// mapping. It is not the authority: internal/model/units.go holds the storage
// unit vocabulary for every provider and the ETL applies it at one choke point
// after mapping, so anything missed here is still corrected before the write.
// That vocabulary was chosen to match what this table produces, which is why
// this pass is a no-op on top of it for every unit but "%": that one is renamed
// to "fraction" here precisely so the choke point can tell a HealthKit reading
// apart from a real percentage and rescale it. Add new units to
// internal/model/units.go, not here.
//
// wireUnitToCanonical maps the SDK's unit strings (HKUnit.unitString) to the
// canonical unit names.
var wireUnitToCanonical = map[string]string{
	"count":     "count",
	"count/min": "bpm",
	"m":         "m",
	"km":        "m",
	"kg":        "kg",
	"lb":        "kg",
	// HealthKit's "%" is a 0..1 FRACTION, not a percentage: HKUnit.percent()
	// is defined that way and only spells itself "%", so the SDK exports
	// an SpO2 of 97% as 0.97 (unitMap entry ("%", .percent())). Renaming it to
	// "percent" here is what made the column mixed-scale, because oura, whoop
	// and withings send the same "%" spelling for a real percentage. Naming it
	// what it is instead lets the pipeline choke point scale it:
	// internal/model/units.go declares the "<type>|fraction" conversions
	// (oxygen saturation, body fat, blood alcohol). A ratio metric with no
	// declared storage unit keeps the honest "fraction" label rather than a
	// "percent" one that lies about the number.
	"%":    "fraction",
	"kcal": "kcal",
	// HealthKit spells the large calorie, a kilocalorie, "Cal": an export
	// written with Calories as the energy unit carries it on every energy
	// record. The choke point in internal/model/units.go lowercases units
	// before matching, so left alone "Cal" became "cal", the small calorie,
	// and every value landed 1,000x too low.
	"Cal":    "kcal",
	"cal":    "kcal",
	"degC":   "celsius",
	"mg/dL":  "mg/dL",
	"mmHg":   "mmHg",
	"ms":     "ms",
	"min":    "min",
	"s":      "s",
	"L":      "L",
	"mL":     "mL",
	"L/min":  "L/min",
	"dBASPL": "dBASPL",
	"cm":     "cm",
	"g":      "g",
	"mg":     "mg",
	"mcg":    "mcg",
	"IU":     "IU",
	"W":      "W",
	"m/s":    "m/s",
	"Hz":     "Hz",
}

// wireUnitConversion defines value multipliers for unit conversion.
// If a unit is not in this map, the value passes through unchanged.
var wireUnitConversion = map[string]float64{
	"km":  1000.0,
	"lb":  0.45359237,
	"cal": 0.001,
}

// dimensionlessCountUnit renames HealthKit's "count" spelling for the quantity
// types where it does not mean a tally. The "%" entry above never fires for them,
// because they never arrive spelled "%": HKQuantity.is(compatibleWith:) is true
// for .count() on EVERY dimensionless quantity, and the SDK's unitMap tests
// ("count", .count()) before ("%", .percent()), so a percent-typed sample matches
// the first entry and exports as "count". An SpO2 of 97% ships as value 0.97 unit
// "count", body fat as 0.2767, and BMI (genuinely HKUnit.count() in HealthKit) as
// 37.29. Left alone all three reach the choke point with no count -> percent or
// count -> kg/m2 rule and fail as reason_code='unit' records: that single gap is
// what filled the failure log with tens of thousands of rows.
//
// Keyed by canonical quantity type rather than applied globally, because "count"
// still means a tally for step_count and flights_climbed. The two ratio metrics
// target "fraction" and not "percent" for the same reason the "%" entry does:
// internal/model/units.go carries the x100 rescale on the "|fraction" key, and oura,
// whoop and withings send a real percentage under their own spellings.
var dimensionlessCountUnit = map[string]string{
	"oxygen_saturation":   "fraction",
	"body_fat_percentage": "fraction",
	"body_mass_index":     "kg/m2",
	// Blood alcohol is a 0..1 ratio in HealthKit too.
	"blood_alcohol_content": "fraction",
}

// NormalizeUnit returns the canonical unit name for a wire unit string.
// Unknown units are returned unchanged.
func NormalizeUnit(wireUnit string) string {
	if canonical, ok := wireUnitToCanonical[wireUnit]; ok {
		return canonical
	}
	return wireUnit
}

// ConvertValue applies unit conversion if needed and returns the converted
// value along with its canonical unit name.
func ConvertValue(value float64, wireUnit string) (float64, string) {
	canonicalUnit := NormalizeUnit(wireUnit)
	if factor, ok := wireUnitConversion[wireUnit]; ok {
		return value * factor, canonicalUnit
	}
	return value, canonicalUnit
}

// ConvertValueForType is ConvertValue with the canonical quantity type in hand, so
// HealthKit's ambiguous dimensionless "count" resolves per metric (see
// dimensionlessCountUnit). Every call site that knows the canonical type must use
// this one; ConvertValue stays for the ones that do not (nutrition), where "count"
// is never a ratio. The value is untouched here: the rename alone is what lets the
// choke point in internal/model/units.go apply the right transform.
func ConvertValueForType(canonicalType string, value float64, wireUnit string) (float64, string) {
	if unit, ok := dimensionlessCountUnit[canonicalType]; ok && NormalizeUnit(wireUnit) == "count" {
		return value, unit
	}
	return ConvertValue(value, wireUnit)
}
