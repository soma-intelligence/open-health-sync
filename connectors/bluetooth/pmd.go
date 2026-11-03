// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import "github.com/soma-intelligence/open-health-sync/internal/model"

// CharPolarPMDData is the data characteristic of Polar Measurement Data
// (service fb005c80-02e7-f387-1cad-8acd2d8df0c8), on which Polar straps and
// optical sensors stream ECG, PPG, accelerometer, PP intervals, gyroscope
// and magnetometer frames once a client starts them. The layouts are the ones
// Polar publishes next to its BLE SDK ("Polar Measurement Data", 2024, and
// "PPI Data"); none of the SDK's code is used.
const CharPolarPMDData = "fb005c82-02e7-f387-1cad-8acd2d8df0c8"

// pmdPPI is the measurement type of PP intervals, in the low six bits of a
// frame's first byte.
const pmdPPI = 3

// decodePolarPMD decodes the PP intervals of an optical sensor (Verity
// Sense, OH1, 360): six bytes per beat, heart rate (bpm), PP interval (ms),
// error estimate (ms), and flags: bit 0 the interval is not valid, bit 1 skin
// contact, bit 2 skin contact supported. That is how "PPI Data" and the open
// decoders read bit 2; one table of the 2024 document words it the other way
// round, and a recording from a real sensor settles it. Other frames (ECG,
// PPG, motion) are stored and not mapped: a continuous waveform needs its own
// storage decision.
//
// A frame starts with the measurement type, an 8-byte timestamp (nanoseconds
// since 2000 on the device's own clock, which nothing here sets, so it spaces
// samples and does not tell time) and the frame type, whose top bit marks a
// compressed frame.
func decodePolarPMD(r *reader) (*measurement, error) {
	m := newMeasurement()
	m.stream = true
	kind, err := r.u8()
	if err != nil {
		return nil, err
	}
	if err := r.skip(8); err != nil {
		return nil, err
	}
	frameType, err := r.u8()
	if err != nil {
		return nil, err
	}
	if kind&0x3f != pmdPPI || frameType != 0 {
		return m, nil
	}
	if r.left()%6 != 0 {
		return nil, ErrTruncated
	}
	hr := 0
	for r.left() > 0 {
		bpm, _ := r.u8()
		pp, _ := r.u16()
		_, _ = r.u16() // error estimate
		flags, _ := r.u8()
		onSkin := flags&0x04 == 0 || flags&0x02 != 0
		m.rr = append(m.rr, float64(pp))
		m.rrValid = append(m.rrValid, onSkin && flags&0x01 == 0)
		hr = 0
		if onSkin {
			hr = bpm
		}
	}
	m.md["polar_measurement"] = "pp_interval"
	if hr > 0 {
		m.add(model.QTHeartRate, "bpm", float64(hr), nil)
	}
	return m, nil
}
