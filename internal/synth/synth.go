// SPDX-License-Identifier: AGPL-3.0-only

// Package synth generates synthetic health data for every provider, for tests.
//
// A generation has two steps. First a Timeline is built from a seed, a persona
// and a list of scenarios: an invented person living a number of days, with the
// time zones they spend each day in, the devices they wear and what each
// device measured (sleep, heart rate, steps, workouts, weigh-ins, blood
// pressure, glucose, ECG strips, meals). Then a renderer per provider writes
// that timeline in the provider's own wire format, as the provider documents it
// (the sources are listed in docs/specs/synthetic-data.md), and applies the
// document-level scenarios: duplicates, missing and null fields, values out of
// range, unit variants, malformed JSON.
//
// Nothing here comes from a real person. The persona figures are round values
// from public physiology, the identifiers are invented, and the same seed gives
// the same bytes on every machine.
package synth

import (
	"fmt"
	"sort"
	"time"
)

// Line is one raw payload a provider renderer produced: the input of a
// connector's Map (cloud providers) or one line of an upload part (device
// providers).
type Line struct {
	// DataType is the connector's data type the line is stored under.
	DataType string
	// Day is the UTC day the line is filed under.
	Day time.Time
	// JSON is the line. A line with Fault FaultMalformed may not be JSON.
	JSON []byte
	// Fault names the defect a scenario put in the line on purpose, empty
	// for a line that follows the provider's documentation.
	Fault Fault
	// Tag names what a scenario did to a line that stays valid: "duplicate",
	// "revision", "unit:lb", "optional_absent", "unknown_field", "dst".
	Tag string
}

// Fault is the kind of defect a line carries on purpose.
type Fault string

// Faults. A line without a fault follows the vendor's documentation and must
// map without error. A line with one may be refused, but must never panic the
// mapper or yield a record that breaks the invariants of the canonical model.
const (
	FaultNone         Fault = ""
	FaultMissingField Fault = "missing_field" // a field the documentation marks required is absent
	FaultNullField    Fault = "null_field"    // a field set to null
	FaultOutOfRange   Fault = "out_of_range"  // a value no body or device produces: negative steps, HR 400, SpO2 140 %
	FaultWrongType    Fault = "wrong_type"    // a number as a string, an object as an array
	FaultTimestamp    Fault = "timestamp"     // end before start, unreadable date, year 1970 or 2099
	FaultUnknownEnum  Fault = "unknown_enum"  // an enum value the documentation does not list
	FaultUnknownType  Fault = "unknown_type"  // a data type the connector does not know
	FaultUnit         Fault = "unknown_unit"  // a unit string no documentation lists
	FaultMalformed    Fault = "malformed"     // not JSON, truncated, BOM, invalid UTF-8, NaN, a number beyond float64
	FaultControlChar  Fault = "control_char"  // a NUL or other control character inside a string
)

// Valid reports whether the line follows the provider's documentation.
func (l Line) Valid() bool { return l.Fault == FaultNone }

// Options choose what a generation holds.
type Options struct {
	// Seed makes the generation reproducible. Zero is a valid seed.
	Seed uint64
	// Start is the first local day. Zero picks 2026-03-20, so the default
	// window crosses the European spring DST change.
	Start time.Time
	// Days is the number of days, 14 when zero.
	Days int
	// Persona picks the invented person, PersonaActive when empty.
	Persona Persona
	// Scenarios lists what to put in the data. Empty means Baseline only.
	Scenarios []Scenario
	// Volume scales the sampling rates: 1 is a realistic wearable, higher
	// values give denser series (Huge sets 60, one heart rate a second).
	Volume float64
	// Now is the time the data is generated or fetched at, two days after
	// the last day when zero. A timestamp after it is in the future.
	Now time.Time
}

func (o Options) withDefaults() Options {
	if o.Start.IsZero() {
		o.Start = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	}
	o.Start = time.Date(o.Start.Year(), o.Start.Month(), o.Start.Day(), 0, 0, 0, 0, time.UTC)
	if o.Days <= 0 {
		o.Days = 14
	}
	if o.Persona == "" {
		o.Persona = PersonaActive
	}
	if len(o.Scenarios) == 0 {
		o.Scenarios = []Scenario{Baseline}
	}
	if o.Volume <= 0 {
		o.Volume = 1
	}
	for _, s := range o.Scenarios {
		if s == Huge && o.Volume < 60 {
			o.Volume = 60
		}
	}
	return o
}

// Has reports whether the options ask for a scenario.
func (o Options) Has(s Scenario) bool {
	for _, x := range o.Scenarios {
		if x == s || x == All {
			return true
		}
	}
	return false
}

// Scenario is one thing a generation puts in the data on purpose.
type Scenario string

// Scenarios. The timeline ones change what the person lives; the document
// ones change how the provider writes it.
const (
	// Baseline is a clean dataset at home, one device per kind.
	Baseline Scenario = "baseline"

	// Timeline scenarios.
	DST         Scenario = "dst"          // both DST changes of the home zone, nights across them, a 02:30 on the skipped hour
	Travel      Scenario = "travel"       // flights through +05:30, +05:45, +12:45, -10:00 and +14:00 zones, across the date line
	MultiDevice Scenario = "multi_device" // watch, phone, ring and strap measuring the same minutes
	Gaps        Scenario = "gaps"         // days without data, a device off for hours, empty answers
	Extremes    Scenario = "extremes"     // valid but rare: HR 35 and 205, a 20 hour ultra, a 13 hour night, glucose 45 and 400
	Huge        Scenario = "huge"         // one sample a second where devices allow it; for nightly runs

	// Document scenarios.
	Duplicates   Scenario = "duplicates"    // exact copies, re-sent revisions, the same record under two ids
	Missing      Scenario = "missing"       // optional fields absent or null, required fields absent
	OutOfRange   Scenario = "out_of_range"  // negative counts, HR 0 and 400, SpO2 above 100 %, end before start
	UnitVariants Scenario = "unit_variants" // every unit the vendor documents (lb, mmol/L, degF, mi, kJ) and unknown ones
	Malformed    Scenario = "malformed"     // truncated JSON, wrong types, BOM, invalid UTF-8, NaN, huge numbers
	Unknown      Scenario = "unknown"       // unknown fields, enum values and data types, as a newer API would send
	Timestamps   Scenario = "timestamps"    // every documented time format, local times, epoch, far future

	// All turns every scenario on except Huge.
	All Scenario = "all"
)

// Scenarios lists every scenario, in the order the documentation gives them.
var Scenarios = []Scenario{Baseline, DST, Travel, MultiDevice, Gaps, Extremes, Huge,
	Duplicates, Missing, OutOfRange, UnitVariants, Malformed, Unknown, Timestamps, All}

// ParseScenario returns the scenario of a name.
func ParseScenario(name string) (Scenario, error) {
	for _, s := range Scenarios {
		if string(s) == name {
			return s, nil
		}
	}
	return "", fmt.Errorf("synth: unknown scenario %q", name)
}

// Sorted returns lines in day, data type and byte order, so outputs compare.
func Sorted(lines []Line) []Line {
	out := append([]Line(nil), lines...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Day.Equal(out[j].Day) {
			return out[i].Day.Before(out[j].Day)
		}
		return out[i].DataType < out[j].DataType
	})
	return out
}
