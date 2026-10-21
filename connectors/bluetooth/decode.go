// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import (
	"math"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Characteristics this connector decodes, as 16-bit UUIDs of the Bluetooth
// SIG assigned numbers.
const (
	CharHeartRate        = "2a37" // Heart Rate Measurement, Heart Rate service 180d
	CharBloodPressure    = "2a35" // Blood Pressure Measurement, service 1810
	CharWeight           = "2a9d" // Weight Measurement, Weight Scale service 181d
	CharBodyComposition  = "2a9c" // Body Composition Measurement, service 181b
	CharGlucose          = "2a18" // Glucose Measurement, Glucose service 1808
	CharTemperature      = "2a1c" // Temperature Measurement, Health Thermometer service 1809
	CharPLXSpotCheck     = "2a5e" // PLX Spot-check Measurement, Pulse Oximeter service 1822
	CharPLXContinuous    = "2a5f" // PLX Continuous Measurement, Pulse Oximeter service 1822
	unknownUser          = 0xFF   // the User ID of "unknown user"
	kPaToMmHg            = 7.50062
	measurementUnsuccess = 0xFFFF // a weight, mass or body fat the device could not measure
	glucoseControl       = 10     // glucose type: control solution
	glucoseLocationCtrl  = 4      // glucose sample location: control solution
)

// reading is one figure of a value.
type reading struct {
	qt, unit string
	value    float64
	md       map[string]any // metadata of this figure only
}

// measurement is what one characteristic value decodes to.
type measurement struct {
	readings []reading
	// rr are the RR intervals in milliseconds, oldest first, rounded to the
	// microsecond (Heart Rate Measurement only).
	rr []float64
	// clock is the device's time stamp, when the value has one; offset
	// minutes are added to it (the glucose time offset field).
	clock  *wallClock
	offset int
	// clockNotSet: the device says its clock is not to be trusted.
	clockNotSet bool
	// userIndex is the User ID the value carries, -1 when none.
	userIndex int
	// md is the metadata of every figure of the value.
	md map[string]any
	// stream marks a value sent every second or so (HighFrequency).
	stream bool
}

func newMeasurement() *measurement {
	return &measurement{userIndex: -1, md: map[string]any{}}
}

func (m *measurement) add(qt, unit string, v float64, md map[string]any) {
	m.readings = append(m.readings, reading{qt: qt, unit: unit, value: v, md: md})
}

// decoders maps a characteristic to its decoder.
var decoders = map[string]func(*reader) (*measurement, error){
	CharHeartRate:       decodeHeartRate,
	CharBloodPressure:   decodeBloodPressure,
	CharWeight:          decodeWeight,
	CharBodyComposition: decodeBodyComposition,
	CharGlucose:         decodeGlucose,
	CharTemperature:     decodeTemperature,
	CharPLXSpotCheck:    decodePLXSpotCheck,
	CharPLXContinuous:   decodePLXContinuous,
}

// decodeHeartRate: flags (bit 0 value is uint16, bits 1-2 sensor contact,
// bit 3 energy expended, bit 4 RR intervals), heart rate, energy (uint16 kJ),
// then RR intervals (uint16, 1/1024 s) to the end of the value.
func decodeHeartRate(r *reader) (*measurement, error) {
	m := newMeasurement()
	m.stream = true
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	var hr int
	if flags&0x01 != 0 {
		hr, err = r.u16()
	} else {
		hr, err = r.u8()
	}
	if err != nil {
		return nil, err
	}
	if flags&0x08 != 0 {
		if err := r.skip(2); err != nil {
			return nil, err
		}
	}
	if flags&0x10 != 0 {
		if r.left()%2 != 0 {
			return nil, ErrTruncated
		}
		for r.left() > 0 {
			v, _ := r.u16()
			m.rr = append(m.rr, math.Round(float64(v)*1e6/1024)/1e3)
		}
	}
	if (flags>>1)&0x03 == 2 { // contact supported, not detected: not on the skin
		m.rr = nil
		return m, nil
	}
	if hr > 0 {
		m.add(model.QTHeartRate, "bpm", float64(hr), nil)
	}
	return m, nil
}

// decodeBloodPressure: flags (bit 0 kPa, bit 1 time stamp, bit 2 pulse rate,
// bit 3 user ID, bit 4 measurement status), systolic, diastolic and mean
// arterial pressure (SFLOAT), then the optional fields in that order.
func decodeBloodPressure(r *reader) (*measurement, error) {
	m := newMeasurement()
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	sys, okSys, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	dia, okDia, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	mean, okMean, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	if flags&0x02 != 0 {
		c, err := r.dateTime()
		if err != nil {
			return nil, err
		}
		m.clock = &c
	}
	var pulse decimal
	okPulse := false
	if flags&0x04 != 0 {
		if pulse, okPulse, err = r.sfloat(); err != nil {
			return nil, err
		}
	}
	if flags&0x08 != 0 {
		if m.userIndex, err = r.u8(); err != nil {
			return nil, err
		}
	}
	if flags&0x10 != 0 {
		st, err := r.u16()
		if err != nil {
			return nil, err
		}
		if names := bpStatus(st); len(names) > 0 {
			m.md["measurement_status"] = names
		}
	}
	unit := "mmHg"
	if flags&0x01 != 0 {
		unit = "kPa"
	}
	// One pressure without the other is not a reading.
	if okSys && okDia {
		var pair map[string]any
		if okMean {
			v := mean.float(0)
			if unit == "kPa" {
				v *= kPaToMmHg
			}
			pair = map[string]any{"mean_arterial_pressure": math.Round(v*1000) / 1000}
		}
		m.add("blood_pressure_systolic", unit, sys.float(0), pair)
		m.add("blood_pressure_diastolic", unit, dia.float(0), pair)
	}
	if okPulse {
		m.add(model.QTHeartRate, "bpm", pulse.float(0), map[string]any{"context": "blood_pressure"})
	}
	return m, nil
}

func bpStatus(st int) []string {
	var out []string
	for _, b := range []struct {
		mask int
		name string
	}{
		{0x0001, "body_movement"},
		{0x0002, "cuff_too_loose"},
		{0x0004, "irregular_pulse"},
		{0x0018, "pulse_rate_out_of_range"},
		{0x0020, "improper_position"},
	} {
		if st&b.mask != 0 {
			out = append(out, b.name)
		}
	}
	return out
}

// mass is a weight or mass field: 0.005 kg, or 0.01 lb in imperial units.
func mass(v int, imperial bool) (float64, string) {
	if imperial {
		return float64(v) / 100, "lb"
	}
	return float64(v*5) / 1000, "kg"
}

// height is a height field: 0.001 m, or 0.1 in in imperial units.
func height(v int, imperial bool) (float64, string) {
	if imperial {
		return float64(v) / 10, "in"
	}
	return float64(v) / 1000, "m"
}

// decodeWeight: flags (bit 0 imperial, bit 1 time stamp, bit 2 user ID,
// bit 3 BMI and height), weight, then the optional fields in that order.
func decodeWeight(r *reader) (*measurement, error) {
	m := newMeasurement()
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	imperial := flags&0x01 != 0
	w, err := r.u16()
	if err != nil {
		return nil, err
	}
	if flags&0x02 != 0 {
		c, err := r.dateTime()
		if err != nil {
			return nil, err
		}
		m.clock = &c
	}
	if flags&0x04 != 0 {
		if m.userIndex, err = r.u8(); err != nil {
			return nil, err
		}
	}
	if w == measurementUnsuccess {
		return m, nil
	}
	v, unit := mass(w, imperial)
	m.add("body_mass", unit, v, nil)
	if flags&0x08 != 0 {
		bmi, err := r.u16()
		if err != nil {
			return nil, err
		}
		h, err := r.u16()
		if err != nil {
			return nil, err
		}
		if bmi > 0 {
			m.add("body_mass_index", "kg/m2", float64(bmi)/10, nil)
		}
		if h > 0 {
			v, unit := height(h, imperial)
			m.add("height", unit, v, nil)
		}
	}
	return m, nil
}

// decodeBodyComposition: flags (uint16: bit 0 imperial, bit 1 time stamp,
// bit 2 user ID, bit 3 basal metabolism, bit 4 muscle percentage, bit 5
// muscle mass, bit 6 fat free mass, bit 7 soft lean mass, bit 8 body water
// mass, bit 9 impedance, bit 10 weight, bit 11 height, bit 12 multiple
// packets), body fat (0.1 %), then the optional fields in flag order.
func decodeBodyComposition(r *reader) (*measurement, error) {
	m := newMeasurement()
	flags, err := r.u16()
	if err != nil {
		return nil, err
	}
	imperial := flags&0x0001 != 0
	fat, err := r.u16()
	if err != nil {
		return nil, err
	}
	if flags&0x0002 != 0 {
		c, err := r.dateTime()
		if err != nil {
			return nil, err
		}
		m.clock = &c
	}
	if flags&0x0004 != 0 {
		if m.userIndex, err = r.u8(); err != nil {
			return nil, err
		}
	}
	if fat != measurementUnsuccess {
		m.add("body_fat_percentage", "percent", float64(fat)/10, nil)
	}
	// The uint16 fields after the user ID, in order; qt "" is read and
	// not stored.
	for _, f := range []struct {
		bit  int
		qt   string
		kind byte // m: mass, h: height
	}{
		{0x0008, "", 0},              // basal metabolism, kJ
		{0x0010, "", 0},              // muscle percentage
		{0x0020, "muscle_mass", 'm'}, // muscle mass
		{0x0040, "lean_body_mass", 'm'},
		{0x0080, "", 0}, // soft lean mass
		{0x0100, "water_mass", 'm'},
		{0x0200, "", 0}, // impedance
		{0x0400, "body_mass", 'm'},
		{0x0800, "height", 'h'},
	} {
		if flags&f.bit == 0 {
			continue
		}
		v, err := r.u16()
		if err != nil {
			return nil, err
		}
		if f.qt == "" || v == measurementUnsuccess || v == 0 {
			continue
		}
		var val float64
		var unit string
		if f.kind == 'h' {
			val, unit = height(v, imperial)
		} else {
			val, unit = mass(v, imperial)
		}
		m.add(f.qt, unit, val, nil)
	}
	return m, nil
}

var glucoseSpecimens = map[int]string{
	1: "capillary_whole_blood", 2: "capillary_plasma", 3: "venous_whole_blood", 4: "venous_plasma",
	5: "arterial_whole_blood", 6: "arterial_plasma", 7: "undetermined_whole_blood",
	8: "undetermined_plasma", 9: "interstitial_fluid",
}

var glucoseLocations = map[int]string{
	1: "finger", 2: "alternate_site", 3: "earlobe", 5: "subcutaneous_tissue",
}

var glucoseStatus = []string{
	"device_battery_low", "sensor_malfunction", "sample_size_insufficient", "strip_insertion_error",
	"strip_type_incorrect", "result_lower_than_device_range", "result_higher_than_device_range",
	"temperature_too_high", "temperature_too_low", "read_interrupted", "general_device_fault", "time_fault",
}

// decodeGlucose: flags (bit 0 time offset, bit 1 concentration with type
// and sample location, bit 2 mol/L, bit 3 sensor status annunciation),
// sequence number, base time, then the optional fields in that order.
func decodeGlucose(r *reader) (*measurement, error) {
	m := newMeasurement()
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	seq, err := r.u16()
	if err != nil {
		return nil, err
	}
	c, err := r.dateTime()
	if err != nil {
		return nil, err
	}
	m.clock = &c
	m.md["sequence_number"] = seq
	if flags&0x01 != 0 {
		if m.offset, err = r.s16(); err != nil {
			return nil, err
		}
	}
	var conc decimal
	okConc, typ, loc := false, 0, 0
	if flags&0x02 != 0 {
		if conc, okConc, err = r.sfloat(); err != nil {
			return nil, err
		}
		tl, err := r.u8()
		if err != nil {
			return nil, err
		}
		typ, loc = tl&0x0F, tl>>4
	}
	if flags&0x08 != 0 {
		st, err := r.u16()
		if err != nil {
			return nil, err
		}
		var names []string
		for i, n := range glucoseStatus {
			if st&(1<<i) != 0 {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			m.md["sensor_status"] = names
		}
		m.clockNotSet = st&(1<<11) != 0 // a time fault: the base time is wrong
	}
	if !okConc || typ == glucoseControl || loc == glucoseLocationCtrl {
		return m, nil // a meter check with control solution is not a reading
	}
	if s, ok := glucoseSpecimens[typ]; ok {
		m.md["specimen"] = s
	}
	if l, ok := glucoseLocations[loc]; ok {
		m.md["sample_location"] = l
	}
	if flags&0x04 != 0 {
		m.add("blood_glucose", "mmol/L", conc.float(3), nil) // mol/L to mmol/L
	} else {
		m.add("blood_glucose", "mg/dL", conc.float(5), nil) // kg/L to mg/dL
	}
	return m, nil
}

var temperatureLocations = map[int]string{
	1: "armpit", 2: "body", 3: "ear", 4: "finger", 5: "gastrointestinal_tract",
	6: "mouth", 7: "rectum", 8: "toe", 9: "tympanum",
}

// decodeTemperature: flags (bit 0 Fahrenheit, bit 1 time stamp, bit 2
// temperature type), temperature (FLOAT), then the optional fields.
func decodeTemperature(r *reader) (*measurement, error) {
	m := newMeasurement()
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	temp, ok, err := r.float32()
	if err != nil {
		return nil, err
	}
	if flags&0x02 != 0 {
		c, err := r.dateTime()
		if err != nil {
			return nil, err
		}
		m.clock = &c
	}
	if flags&0x04 != 0 {
		t, err := r.u8()
		if err != nil {
			return nil, err
		}
		if l, ok := temperatureLocations[t]; ok {
			m.md["measurement_location"] = l
		}
	}
	if !ok {
		return m, nil
	}
	unit := "celsius"
	if flags&0x01 != 0 {
		unit = "degF"
	}
	m.add(model.QTBodyTemperature, unit, temp.float(0), nil)
	return m, nil
}

// plxStatus applies a PLX Measurement Status field: false when the value is
// not a reading of this person (demonstration, testing, unavailable,
// invalid); the names of the warnings kept go to metadata.
func plxStatus(m *measurement, st int) bool {
	if st&(1<<10|1<<11|1<<13|1<<15) != 0 {
		return false
	}
	var names []string
	if st&(1<<6) != 0 {
		names = append(names, "early_estimated")
	}
	if st&(1<<14) != 0 {
		names = append(names, "questionable_measurement")
	}
	if len(names) > 0 {
		m.md["measurement_status"] = names
	}
	return true
}

func (m *measurement) addSpO2PR(spo2 decimal, okSpO2 bool, pr decimal, okPR bool) {
	if okSpO2 {
		m.add(model.QTOxygenSaturation, "percent", spo2.float(0), nil)
	}
	if okPR {
		m.add(model.QTHeartRate, "bpm", pr.float(0), map[string]any{"context": "pulse_oximeter"})
	}
}

// decodePLXSpotCheck: flags (bit 0 time stamp, bit 1 measurement status,
// bit 2 device and sensor status, bit 3 pulse amplitude index, bit 4 device
// clock not set), SpO2 and pulse rate (SFLOAT), then the optional fields.
func decodePLXSpotCheck(r *reader) (*measurement, error) {
	m := newMeasurement()
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	spo2, okSpO2, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	pr, okPR, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	if flags&0x01 != 0 {
		c, err := r.dateTime()
		if err != nil {
			return nil, err
		}
		m.clock = &c
	}
	m.clockNotSet = flags&0x10 != 0
	if flags&0x02 != 0 {
		st, err := r.u16()
		if err != nil {
			return nil, err
		}
		if !plxStatus(m, st) {
			return m, nil
		}
	}
	m.addSpO2PR(spo2, okSpO2, pr, okPR)
	return m, nil
}

// decodePLXContinuous: flags (bit 0 SpO2PR-Fast, bit 1 SpO2PR-Slow, bit 2
// measurement status, bit 3 device and sensor status, bit 4 pulse amplitude
// index), SpO2PR-Normal, then the optional fields. Only Normal is stored.
func decodePLXContinuous(r *reader) (*measurement, error) {
	m := newMeasurement()
	m.stream = true
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	spo2, okSpO2, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	pr, okPR, err := r.sfloat()
	if err != nil {
		return nil, err
	}
	for _, bit := range []int{0x01, 0x02} { // Fast, Slow: two SFLOAT each
		if flags&bit != 0 {
			if err := r.skip(4); err != nil {
				return nil, err
			}
		}
	}
	if flags&0x04 != 0 {
		st, err := r.u16()
		if err != nil {
			return nil, err
		}
		if !plxStatus(m, st) {
			return m, nil
		}
	}
	m.addSpO2PR(spo2, okSpO2, pr, okPR)
	return m, nil
}
