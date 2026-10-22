// SPDX-License-Identifier: AGPL-3.0-only

// Package bluetooth renders a synthetic timeline as the characteristic
// values standard Bluetooth health devices send, in the upload lines of
// POST /v1/ingest/bluetooth.
//
// Source: the Bluetooth SIG GATT Specification Supplement and its YAML
// structures (bitbucket.org/bluetooth-SIG/public, gss/), for Heart Rate
// Measurement (2a37), Blood Pressure Measurement (2a35), Weight Measurement
// (2a9d), Temperature Measurement (2a1c) and Glucose Measurement (2a18):
// flags, field order, little endian integers, the IEEE 11073-20601 SFLOAT
// and FLOAT (medfloat16, medfloat32) and their special values (Table 2.1),
// the Date Time structure, RR intervals in 1/1024 s, weight at 0.005 kg or
// 0.01 lb, glucose in kg/L or mol/L. The line envelope (at, char, value,
// session, device, device_user) is the upload contract of
// docs/connectors/bluetooth.md.
package bluetooth

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Slug is the connector the lines are for.
const Slug = "bluetooth"

// DataType is the X-OHS-Type of every part.
const DataType = "gatt"

var lineShape = synth.Shape{
	Required: []string{"at", "char", "value", "session"},
	Optional: []string{"device", "device.name", "device.manufacturer", "device.model", "device.firmware", "device_user"},
	Numbers:  map[string][2]float64{"device_user": {0, 254}},
	Times:    []string{"at"},
	Enums:    []string{"char"},
	// No Strings: the contract caps each device string at 128 characters,
	// so the long string variant would not be a documented line.
	// The uploader's offset is required: RFC 3339 with milliseconds.
	TimeLayout: "2006-01-02T15:04:05.000Z07:00",
}

// sfloat encodes v as a medfloat16 with the given exponent.
func sfloat(v float64, exp int) uint16 {
	m := int(math.Round(v / math.Pow10(exp)))
	return uint16(exp&0xF)<<12 | uint16(m&0x0FFF)
}

// float32m encodes v as a medfloat32 with the given exponent.
func float32m(v float64, exp int) uint32 {
	m := int(math.Round(v / math.Pow10(exp)))
	return uint32(exp&0xFF)<<24 | uint32(m&0xFFFFFF)
}

// Special SFLOAT values (GSS Table 2.1).
const (
	sfNaN  = 0x07FF
	sfNRes = 0x0800
	sfPInf = 0x07FE
)

type buf []byte

func (b buf) u8(v int) buf     { return append(b, byte(v)) }
func (b buf) u16(v uint16) buf { return binary.LittleEndian.AppendUint16(b, v) }
func (b buf) u32(v uint32) buf { return binary.LittleEndian.AppendUint32(b, v) }

// dateTime appends the Date Time structure: year, month, day, hours,
// minutes, seconds, the device's wall clock.
func (b buf) dateTime(t time.Time) buf {
	return b.u16(uint16(t.Year())).u8(int(t.Month())).u8(t.Day()).u8(t.Hour()).u8(t.Minute()).u8(t.Second())
}

type renderer struct {
	tl *synth.Timeline
	e  *synth.Emitter
	r  *synth.Rand
}

func (g *renderer) add(at time.Time, char string, value []byte, session string, dev synth.Doc, f synth.Fault, tag string) {
	d := synth.Doc{"at": g.tl.Local(at).Format("2006-01-02T15:04:05.000Z07:00"), "char": char,
		"value": hex.EncodeToString(value), "session": session}
	if dev != nil {
		d["device"] = dev
	}
	if f == synth.FaultNone && tag == "" {
		g.e.Add(DataType, at, d, lineShape)
		return
	}
	g.e.Raw(synth.Line{DataType: DataType, Day: at.UTC().Truncate(24 * time.Hour), JSON: synth.Marshal(d), Fault: f, Tag: tag})
}

// Lines renders tl as GATT upload lines.
func Lines(tl *synth.Timeline) []synth.Line {
	g := &renderer{tl: tl, e: synth.NewEmitter(tl, Slug), r: synth.NewRand(tl.Opt.Seed, "bluetooth")}
	g.e.UnknownType = "gatt_v2"
	o := tl.Opt
	strap := synth.Doc{"name": "Polar H10", "manufacturer": "Polar Electro Oy", "model": "H10", "firmware": "5.0.0"}
	// A chest strap during every workout: one notification a second with
	// the RR intervals of the beats since the last one.
	first := true
	for _, w := range tl.Recorded() {
		if w.End.After(o.Now) {
			continue
		}
		session := g.r.Hex(16)
		for t := w.Start; t.Before(w.End); t = t.Add(time.Duration(float64(time.Second) / math.Min(o.Volume, 1))) {
			hr := tl.HeartRate(t)
			v := buf{}
			flags := 0x16 // contact supported and detected, RR present
			if hr > 255 || first {
				flags |= 0x01
			}
			v = v.u8(flags)
			if flags&0x01 != 0 {
				v = v.u16(uint16(math.Round(hr)))
			} else {
				v = v.u8(int(math.Round(hr)))
			}
			rr := 60 / hr * 1024
			for beat := 0.0; beat < hr/60; beat++ {
				v = v.u16(uint16(math.Round(rr)))
			}
			g.add(t, "2a37", v, session, strap, synth.FaultNone, "")
			if first {
				first = false
				g.faults(t, session, strap)
			}
		}
	}
	cuff := synth.Doc{"name": "BLESmart_0000015", "manufacturer": "OMRON HEALTHCARE", "model": "M7 Intelli IT"}
	for i, b := range tl.BPs {
		if b.At.After(o.Now) {
			continue
		}
		v := buf{}
		flags := 0x1E // time stamp, pulse, user id, status; mmHg
		kpa := o.Has(synth.UnitVariants) && i%2 == 1
		if kpa {
			flags |= 0x01
		}
		v = v.u8(flags)
		mean := b.Diastolic + (b.Systolic-b.Diastolic)/3
		if kpa {
			k := 0.133322
			v = v.u16(sfloat(b.Systolic*k, -1)).u16(sfloat(b.Diastolic*k, -1)).u16(sfloat(mean*k, -1))
		} else {
			v = v.u16(sfloat(b.Systolic, 0)).u16(sfloat(b.Diastolic, 0)).u16(sfloat(mean, 0))
		}
		v = v.dateTime(tl.Local(b.At)).u16(sfloat(b.Pulse, 0)).u8(1)
		status := 0
		if b.Irregular {
			status |= 0x04
		}
		v = v.u16(uint16(status))
		g.add(b.At.Add(30*time.Second), "2a35", v, g.r.Hex(16), cuff, synth.FaultNone, "")
	}
	scale := synth.Doc{"name": "Yunmai Mini 2", "model": "M1690"}
	for i, w := range tl.Weighs {
		if w.At.After(o.Now) {
			continue
		}
		v := buf{}
		lb := o.Has(synth.UnitVariants) && i%2 == 1
		flags := 0x0E // time stamp, user id, BMI and height
		if lb {
			flags |= 0x01
		}
		v = v.u8(flags)
		if lb {
			v = v.u16(uint16(math.Round(w.WeightKG / 0.45359237 / 0.01)))
		} else {
			v = v.u16(uint16(math.Round(w.WeightKG / 0.005)))
		}
		v = v.dateTime(tl.Local(w.At)).u8(0xFF).u16(uint16(math.Round(w.BMI * 10)))
		if lb {
			v = v.u16(uint16(math.Round(tl.P.HeightCM / 2.54 * 10)))
		} else {
			v = v.u16(uint16(math.Round(tl.P.HeightCM * 10)))
		}
		g.add(w.At, "2a9d", v, g.r.Hex(16), scale, synth.FaultNone, "")
	}
	thermo := synth.Doc{"name": "Braun IRT6520", "manufacturer": "Kaz USA"}
	for i, s := range tl.Sleeps {
		if s.Nap || s.End.After(o.Now) {
			continue
		}
		c := 36.5 + s.TempDelta
		v := buf{}
		f := o.Has(synth.UnitVariants) && i%2 == 1
		if f {
			v = v.u8(0x07).u32(float32m(c*9/5+32, -1))
		} else {
			v = v.u8(0x06).u32(float32m(c, -2))
		}
		v = v.dateTime(tl.Local(s.End)).u8(3) // 3: ear
		g.add(s.End.Add(5*time.Minute), "2a1c", v, g.r.Hex(16), thermo, synth.FaultNone, "")
	}
	meter := synth.Doc{"name": "Contour7830H", "manufacturer": "Ascensia Diabetes Care", "model": "Contour Next One"}
	seq := 100
	for i, d := range tl.Days {
		if d.Missing || d.End.After(o.Now) {
			continue
		}
		for _, h := range []float64{7, 13.5, 19.5} {
			t := synth.AtLocal(d, h)
			mg := tl.Glucose(t)
			seq++
			v := buf{}
			mol := o.Has(synth.UnitVariants) && i%2 == 1
			flags := 0x03 // time offset, concentration and type-location
			if mol {
				flags |= 0x04
			}
			v = v.u8(flags).u16(uint16(seq)).dateTime(tl.Local(t)).u16(0)
			if mol {
				v = v.u16(sfloat(mg/18.0182/1000, -5)) // mol/L
			} else {
				v = v.u16(sfloat(mg/1e5, -5)) // kg/L
			}
			v = v.u8(0x11) // capillary whole blood, finger
			g.add(t.Add(time.Hour), "2a18", v, g.r.Hex(16), meter, synth.FaultNone, "")
		}
	}
	return g.e.Lines()
}

// faults writes the value level scenarios once.
func (g *renderer) faults(t time.Time, session string, dev synth.Doc) {
	o := g.tl.Opt
	if o.Has(synth.Unknown) {
		g.add(t, "2a19", []byte{87}, session, dev, synth.FaultNone, "battery_level")
		g.add(t, "0000fe59-0000-1000-8000-00805f9b34fb", []byte{1, 2, 3}, session, dev, synth.FaultNone, "vendor_service")
		g.add(t, "00002A37-0000-1000-8000-00805F9B34FB", []byte{0x06, 72}, session, dev, synth.FaultNone, "full_uuid")
	}
	if o.Has(synth.Extremes) {
		// Contact supported but not detected: no heart rate.
		g.add(t, "2a37", []byte{0x04, 70}, session, dev, synth.FaultNone, "no_contact")
		g.add(t, "2a37", []byte{0x06, 0}, session, dev, synth.FaultNone, "zero_bpm")
	}
	if o.Has(synth.OutOfRange) {
		bp := buf{}.u8(0x00).u16(sfNaN).u16(sfloat(80, 0)).u16(sfloat(93, 0))
		g.add(t, "2a35", bp, session, dev, synth.FaultOutOfRange, "sfloat_nan")
		bp = buf{}.u8(0x00).u16(sfNRes).u16(sfPInf).u16(sfloat(93, 0))
		g.add(t, "2a35", bp, session, dev, synth.FaultOutOfRange, "sfloat_nres_inf")
		g.add(t, "2a9d", buf{}.u8(0x00).u16(0xFFFF), session, dev, synth.FaultOutOfRange, "weight_unsuccessful")
		g.add(t, "2a1c", buf{}.u8(0x00).u32(0x007FFFFF), session, dev, synth.FaultOutOfRange, "float_nan")
	}
	if o.Has(synth.Timestamps) {
		// A cuff whose clock was never set.
		bp := buf{}.u8(0x02).u16(sfloat(120, 0)).u16(sfloat(80, 0)).u16(sfloat(93, 0)).dateTime(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		g.add(t, "2a35", bp, session, dev, synth.FaultNone, "clock_unset")
		bp = buf{}.u8(0x02).u16(sfloat(120, 0)).u16(sfloat(80, 0)).u16(sfloat(93, 0)).u16(0).u8(0).u8(0).u8(0).u8(0).u8(0)
		g.add(t, "2a35", bp, session, dev, synth.FaultNone, "clock_zero")
	}
	if o.Has(synth.Malformed) {
		g.add(t, "2a37", []byte{0x01, 72}, session, dev, synth.FaultMalformed, "short_uint16")
		g.add(t, "2a35", []byte{0x04, 0x78, 0x00}, session, dev, synth.FaultMalformed, "short_compound")
		g.add(t, "2a18", []byte{0x03, 1}, session, dev, synth.FaultMalformed, "short_glucose")
		g.add(t, "2a37", []byte{}, session, dev, synth.FaultMalformed, "empty_value")
		// A slice, not a map: the order of the lines is part of the output.
		for _, m := range [][2]string{
			{"odd_hex", `{"at":"2026-03-20T10:00:00.000+01:00","char":"2a37","value":"064","session":"` + session + `"}`},
			{"not_hex", `{"at":"2026-03-20T10:00:00.000+01:00","char":"2a37","value":"zz48","session":"` + session + `"}`},
			{"no_offset", `{"at":"2026-03-20T10:00:00","char":"2a37","value":"0648","session":"` + session + `"}`},
			{"bad_session", `{"at":"2026-03-20T10:00:00.000+01:00","char":"2a37","value":"0648","session":"a b"}`},
		} {
			tag, js := m[0], m[1]
			g.e.Raw(synth.Line{DataType: DataType, Day: t.UTC().Truncate(24 * time.Hour), JSON: []byte(js), Fault: synth.FaultMalformed, Tag: tag})
		}
	}
}
