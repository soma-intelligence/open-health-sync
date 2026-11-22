// SPDX-License-Identifier: AGPL-3.0-only

package labs

// A result is stored as sent: its value and its UCUM unit. For a few common
// analytes a normalized value is stored next to it, in one canonical unit, so
// results from labs that report in different units compare. A conversion is
// listed only when it is exact and documented: a molar mass for a molar to
// mass conversion, the IFCC to NGSP master equation for HbA1c. The analyte is
// known by its LOINC code alone, never by a display name, and the unit must
// be the exact UCUM code (case-sensitive: "mmol/L", not "mmol/l"). Anything
// else gets no normalized value: nothing is guessed. Values are not rounded.
//
// docs/labs.md carries this table; TestNormalizeTable pins every row.

// Analyte is one analyte with its canonical unit and the units it converts
// from.
type Analyte struct {
	Name string
	// LOINC lists the codes of the analyte, in either unit.
	LOINC []string
	// Unit is the canonical unit.
	Unit string
	// From maps a UCUM unit to the conversion into Unit. The canonical unit
	// itself converts as the identity.
	From map[string]Conversion
}

// Conversion converts a value into the canonical unit.
type Conversion struct {
	// Factor multiplies the value; Offset is added after (HbA1c only).
	Factor float64
	Divide bool
	Offset float64
	// Basis documents the conversion.
	Basis string
}

func (c Conversion) apply(v float64) float64 {
	if c.Divide {
		return v/c.Factor + c.Offset
	}
	return v*c.Factor + c.Offset
}

var identity = Conversion{Factor: 1, Basis: "same unit"}

// Analytes is the normalization table.
var Analytes = []Analyte{
	{
		Name: "glucose",
		// 2345-7 serum or plasma mass, 2339-0 blood mass, 1558-6 fasting
		// serum or plasma mass, 14749-6 serum or plasma molar, 15074-8 blood
		// molar.
		LOINC: []string{"2345-7", "2339-0", "1558-6", "14749-6", "15074-8"},
		Unit:  "mg/dL",
		From: map[string]Conversion{
			"mg/dL":  identity,
			"mmol/L": {Factor: 18.0156, Basis: "molar mass of glucose 180.156 g/mol"},
		},
	},
	{
		Name: "cholesterol (total, HDL, LDL)",
		// 2093-3 total mass, 14647-2 total molar, 35200-5 total (HL7 lipid
		// example), 2085-9 HDL mass, 13457-7 LDL calculated mass, 18262-6
		// LDL direct mass, 2089-1 LDL mass.
		LOINC: []string{"2093-3", "14647-2", "35200-5", "2085-9", "13457-7", "18262-6", "2089-1"},
		Unit:  "mg/dL",
		From: map[string]Conversion{
			"mg/dL":  identity,
			"mmol/L": {Factor: 38.665, Basis: "molar mass of cholesterol 386.65 g/mol"},
		},
	},
	{
		Name: "HbA1c",
		// 4548-4 and 17856-6 in % (NGSP), 59261-8 in mmol/mol (IFCC).
		LOINC: []string{"4548-4", "17856-6", "59261-8"},
		Unit:  "%",
		From: map[string]Conversion{
			"%":        identity,
			"mmol/mol": {Factor: 10.929, Divide: true, Offset: 2.15, Basis: "IFCC to NGSP master equation: NGSP = IFCC/10.929 + 2.15"},
		},
	},
	{
		Name: "creatinine",
		// 2160-0 serum or plasma mass, 38483-4 blood mass, 14682-9 serum or
		// plasma molar.
		LOINC: []string{"2160-0", "38483-4", "14682-9"},
		Unit:  "mg/dL",
		From: map[string]Conversion{
			"mg/dL":  identity,
			"umol/L": {Factor: 88.40, Divide: true, Basis: "molar mass of creatinine 113.12 g/mol"},
		},
	},
	{
		Name: "vitamin D (25-hydroxy)",
		// 1989-3 and 62292-8 mass, 14635-7 molar.
		LOINC: []string{"1989-3", "62292-8", "14635-7"},
		Unit:  "ng/mL",
		From: map[string]Conversion{
			"ng/mL":  identity,
			"nmol/L": {Factor: 2.496, Divide: true, Basis: "molar mass of 25-hydroxyvitamin D3 400.64 g/mol"},
		},
	},
}

var byLOINC = func() map[string]*Analyte {
	m := map[string]*Analyte{}
	for i := range Analytes {
		for _, c := range Analytes[i].LOINC {
			m[c] = &Analytes[i]
		}
	}
	return m
}()

// Normalize returns a value in the canonical unit of its analyte, and that
// unit, when the analyte (by LOINC code) and the unit are in the table.
func Normalize(loinc string, value float64, unit string) (float64, string, bool) {
	a := byLOINC[loinc]
	if a == nil {
		return 0, "", false
	}
	c, ok := a.From[unit]
	if !ok {
		return 0, "", false
	}
	return c.apply(value), a.Unit, true
}
