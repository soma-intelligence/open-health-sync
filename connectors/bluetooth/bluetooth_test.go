// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

const testUser = "00000000-0000-0000-0000-000000000001"

// The values below are built from the layouts of the GATT Specification
// Supplement with invented readings. None comes from a person.

// at is the arrival time of every test line: 21:04:05.120 at UTC+2.
const at = "2026-10-16T21:04:05.120+02:00"

func atTime(t *testing.T) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, at)
	require.NoError(t, err)
	return v
}

func le16(v int) []byte { return []byte{byte(v), byte(v >> 8)} }

// sf encodes an SFLOAT: a 12-bit mantissa and a 4-bit exponent.
func sf(mant, exp int) []byte {
	return le16(int(uint16(exp&0xF)<<12 | uint16(mant&0xFFF)))
}

// fl encodes a FLOAT: a 24-bit mantissa and an 8-bit exponent.
func fl(mant, exp int) []byte {
	m := uint32(mant) & 0xFFFFFF
	return []byte{byte(m), byte(m >> 8), byte(m >> 16), byte(exp)}
}

// dt encodes a Date Time.
func dt(y, mo, d, h, mi, s int) []byte {
	return append(le16(y), byte(mo), byte(d), byte(h), byte(mi), byte(s))
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

type lineOpt func(map[string]any)

func withDeviceUser(n int) lineOpt { return func(m map[string]any) { m["device_user"] = n } }
func withAt(s string) lineOpt      { return func(m map[string]any) { m["at"] = s } }

func fields(char string, value []byte, opts ...lineOpt) map[string]any {
	m := map[string]any{
		"at":      at,
		"char":    char,
		"value":   hex.EncodeToString(value),
		"session": "9f2c4e1a7b3d5c60",
		"device": map[string]any{
			"name": "Polar H10 0A1B2C3D", "manufacturer": "Polar Electro Oy",
			"model": "H10", "firmware": "5.0.0",
		},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func mapLine(t *testing.T, char string, value []byte, opts ...lineOpt) model.MapResult {
	t.Helper()
	res, err := New().Map(connector.RawRecord{UserID: testUser, Provider: Slug, DataType: DataType, Fields: fields(char, value, opts...)})
	require.NoError(t, err)
	return res
}

func byType(res model.MapResult) map[string]model.QuantitySample {
	out := map[string]model.QuantitySample{}
	for _, s := range res.QuantitySamples {
		out[s.QuantityType] = s
	}
	return out
}

func encodeLine(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b) + "\n"
}

func TestParseUpload(t *testing.T) {
	c := New()
	good := encodeLine(t, fields("2a37", []byte{0x00, 72})) + "\n" + encodeLine(t, fields("2a19", []byte{80}))
	require.NoError(t, c.ParseUpload(connector.UploadMeta{Type: DataType}, strings.NewReader(good), nil))

	bad := func(mut func(map[string]any)) string {
		m := fields("2a37", []byte{0x00, 72})
		mut(m)
		return encodeLine(t, m)
	}
	for name, body := range map[string]string{
		"not json":           "nope\n",
		"array":              "[1,2]\n",
		"no at":              bad(func(m map[string]any) { delete(m, "at") }),
		"at without offset":  bad(func(m map[string]any) { m["at"] = "2026-10-16T21:04:05" }),
		"no char":            bad(func(m map[string]any) { delete(m, "char") }),
		"bad char":           bad(func(m map[string]any) { m["char"] = "2a3z" }),
		"three digit char":   bad(func(m map[string]any) { m["char"] = "a37" }),
		"other 128-bit char": bad(func(m map[string]any) { m["char"] = "6a4e2401-667b-11e3-949a-0800200c9a66" }),
		"no value":           bad(func(m map[string]any) { delete(m, "value") }),
		"empty value":        bad(func(m map[string]any) { m["value"] = "" }),
		"odd hex":            bad(func(m map[string]any) { m["value"] = "abc" }),
		"not hex":            bad(func(m map[string]any) { m["value"] = "zz" }),
		"value too long":     bad(func(m map[string]any) { m["value"] = strings.Repeat("00", MaxValueBytes+1) }),
		"no session":         bad(func(m map[string]any) { delete(m, "session") }),
		"short session":      bad(func(m map[string]any) { m["session"] = "abc" }),
		"session with dot":   bad(func(m map[string]any) { m["session"] = "abcd.efgh" }),
		"long device name":   bad(func(m map[string]any) { m["device"] = map[string]any{"name": strings.Repeat("x", 129)} }),
		"device not object":  bad(func(m map[string]any) { m["device"] = "H10" }),
		"device user 255":    bad(func(m map[string]any) { m["device_user"] = 255 }),
		"device user -1":     bad(func(m map[string]any) { m["device_user"] = -1 }),
		"device user 1.5":    bad(func(m map[string]any) { m["device_user"] = 1.5 }),
		"no line":            "\n  \n",
	} {
		t.Run(name, func(t *testing.T) {
			err := c.ParseUpload(connector.UploadMeta{Type: DataType}, strings.NewReader(body), nil)
			require.Error(t, err)
		})
	}
	t.Run("other type header", func(t *testing.T) {
		err := c.ParseUpload(connector.UploadMeta{Type: "fit"}, strings.NewReader(good), nil)
		require.ErrorIs(t, err, ErrInvalidType)
	})
	t.Run("line over the cap", func(t *testing.T) {
		body := `{"x":"` + strings.Repeat("a", maxLineBytes) + `"}` + "\n"
		require.ErrorIs(t, c.ParseUpload(connector.UploadMeta{Type: DataType}, strings.NewReader(body), nil), ErrInvalidLine)
	})
}

func TestHeartRate(t *testing.T) {
	arrived := atTime(t)

	t.Run("uint8, no RR", func(t *testing.T) {
		res := mapLine(t, "2a37", []byte{0x00, 72})
		require.Len(t, res.QuantitySamples, 1)
		s := res.QuantitySamples[0]
		assert.Equal(t, model.QTHeartRate, s.QuantityType)
		assert.Equal(t, 72.0, s.Value)
		assert.Equal(t, "bpm", s.Unit)
		assert.True(t, s.StartDate.Equal(arrived))
		assert.True(t, s.EndDate.Equal(arrived))
		assert.Equal(t, Slug, s.Provider)
		assert.Equal(t, "polar", s.SourceSlug)
		assert.Equal(t, "Polar H10", s.DeviceID)
		assert.Equal(t, "H10", s.DeviceModel)
		assert.Equal(t, "Polar H10", s.SourceName)
		require.NotNil(t, s.WasUserEntered)
		assert.False(t, *s.WasUserEntered)
		assert.Equal(t, "2a37", s.Metadata["gatt_characteristic"])
		assert.Equal(t, "received", s.Metadata["clock"])
		assert.Equal(t, "Etc/GMT-2", s.Metadata["timezone"])
		assert.Equal(t, 120, s.Metadata["utc_offset_minutes"])
		assert.Equal(t, "5.0.0", s.Metadata["firmware"])
		assert.Equal(t, "9f2c4e1a7b3d5c60", s.Metadata["session"])
		assert.Empty(t, res.HeartbeatSamples)
		assert.True(t, res.HighFrequency)
	})

	t.Run("uint16, contact, energy and RR", func(t *testing.T) {
		// flags: uint16 value, contact supported and detected, energy, RR.
		v := cat([]byte{0x01 | 0x06 | 0x08 | 0x10}, le16(80), le16(120), le16(1024), le16(819))
		res := mapLine(t, "2a37", v)
		require.Len(t, res.QuantitySamples, 1)
		assert.Equal(t, 80.0, res.QuantitySamples[0].Value)
		require.Len(t, res.HeartbeatSamples, 2)
		first, last := res.HeartbeatSamples[0], res.HeartbeatSamples[1]
		assert.Equal(t, 1000.0, first.RRMS)
		assert.Equal(t, 799.805, last.RRMS) // 819/1024 s, to the microsecond
		assert.True(t, last.Timestamp.Equal(arrived.UTC()))
		assert.True(t, first.Timestamp.Equal(arrived.Add(-799805*time.Microsecond).UTC()))
		assert.Equal(t, "9f2c4e1a7b3d5c60", first.SeriesID)
		assert.Equal(t, testUser, first.UserID)
	})

	t.Run("no skin contact", func(t *testing.T) {
		res := mapLine(t, "2a37", cat([]byte{0x04 | 0x10}, []byte{90}, le16(700)))
		assert.Empty(t, res.QuantitySamples)
		assert.Empty(t, res.HeartbeatSamples)
	})

	t.Run("zero heart rate keeps RR", func(t *testing.T) {
		res := mapLine(t, "2a37", cat([]byte{0x10}, []byte{0}, le16(1024)))
		assert.Empty(t, res.QuantitySamples)
		assert.Len(t, res.HeartbeatSamples, 1)
	})

	t.Run("truncated", func(t *testing.T) {
		_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: fields("2a37", []byte{0x01, 80})})
		require.ErrorIs(t, err, ErrTruncated)
		require.ErrorContains(t, err, "2a37")
	})

	t.Run("odd RR byte", func(t *testing.T) {
		_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: fields("2a37", []byte{0x10, 70, 0x00})})
		require.ErrorIs(t, err, ErrTruncated)
	})

	t.Run("full 128-bit UUID", func(t *testing.T) {
		res := mapLine(t, "00002A37-0000-1000-8000-00805F9B34FB", []byte{0x00, 61})
		require.Len(t, res.QuantitySamples, 1)
		assert.Equal(t, "2a37", res.QuantitySamples[0].Metadata["gatt_characteristic"])
	})
}

func TestBloodPressure(t *testing.T) {
	// mmHg, time stamp, pulse, user, status: systolic 121, diastolic 79,
	// MAP 93, 2026-10-16 08:30:00 device time, pulse 64, user 1, irregular
	// pulse and body movement.
	v := cat([]byte{0x02 | 0x04 | 0x08 | 0x10}, sf(121, 0), sf(79, 0), sf(93, 0),
		dt(2026, 10, 16, 8, 30, 0), sf(64, 0), []byte{1}, le16(0x0001|0x0004))

	res := mapLine(t, "2a35", v)
	got := byType(res)
	require.Len(t, res.QuantitySamples, 3)
	sys, dia, pulse := got["blood_pressure_systolic"], got["blood_pressure_diastolic"], got[model.QTHeartRate]
	assert.Equal(t, 121.0, sys.Value)
	assert.Equal(t, "mmHg", sys.Unit)
	assert.Equal(t, 79.0, dia.Value)
	assert.Equal(t, 64.0, pulse.Value)
	want := time.Date(2026, 10, 16, 6, 30, 0, 0, time.UTC) // 08:30 at UTC+2
	assert.True(t, sys.StartDate.Equal(want), sys.StartDate)
	assert.Equal(t, "device", sys.Metadata["clock"])
	assert.Equal(t, 93.0, sys.Metadata["mean_arterial_pressure"])
	assert.Equal(t, []string{"body_movement", "irregular_pulse"}, sys.Metadata["measurement_status"])
	assert.Equal(t, 1, sys.Metadata["device_user"])
	assert.Equal(t, "blood_pressure", pulse.Metadata["context"])
	assert.False(t, res.HighFrequency)

	t.Run("this person's index", func(t *testing.T) {
		assert.Len(t, mapLine(t, "2a35", v, withDeviceUser(1)).QuantitySamples, 3)
	})
	t.Run("another person's index", func(t *testing.T) {
		assert.Empty(t, mapLine(t, "2a35", v, withDeviceUser(2)).QuantitySamples)
	})
	t.Run("unknown user is kept", func(t *testing.T) {
		u := cat([]byte{0x08}, sf(118, 0), sf(76, 0), sf(90, 0), []byte{0xFF})
		assert.Len(t, mapLine(t, "2a35", u, withDeviceUser(2)).QuantitySamples, 2)
	})
	t.Run("kPa", func(t *testing.T) {
		k := cat([]byte{0x01}, sf(160, -1), sf(105, -1), sf(124, -1))
		got := byType(mapLine(t, "2a35", k))
		assert.Equal(t, 16.0, got["blood_pressure_systolic"].Value)
		assert.Equal(t, "kPa", got["blood_pressure_systolic"].Unit)
		assert.Equal(t, 10.5, got["blood_pressure_diastolic"].Value)
		assert.InDelta(t, 93.0, got["blood_pressure_systolic"].Metadata["mean_arterial_pressure"], 0.01)
	})
	t.Run("a pressure that is not a number drops the pair", func(t *testing.T) {
		n := cat([]byte{0x04}, le16(0x07FF), sf(79, 0), sf(93, 0), sf(64, 0))
		res := mapLine(t, "2a35", n)
		require.Len(t, res.QuantitySamples, 1)
		assert.Equal(t, model.QTHeartRate, res.QuantitySamples[0].QuantityType)
	})
	t.Run("truncated", func(t *testing.T) {
		_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: fields("2a35", cat([]byte{0x02}, sf(121, 0), sf(79, 0), sf(93, 0), []byte{0xEA, 0x07}))})
		require.ErrorIs(t, err, ErrTruncated)
	})
}

func TestDeviceClock(t *testing.T) {
	arrived := atTime(t)
	bp := func(ts []byte) []byte { return cat([]byte{0x02}, sf(120, 0), sf(80, 0), sf(93, 0), ts) }
	for name, tc := range map[string]struct {
		ts     []byte
		device bool
		want   time.Time
	}{
		"set":                  {dt(2026, 10, 15, 22, 0, 0), true, time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)},
		"nine minutes ahead":   {dt(2026, 10, 16, 21, 13, 5), true, time.Date(2026, 10, 16, 19, 13, 5, 0, time.UTC)},
		"eleven minutes ahead": {dt(2026, 10, 16, 21, 15, 6), false, arrived},
		"year unknown":         {dt(0, 10, 16, 8, 0, 0), false, arrived},
		"month unknown":        {dt(2026, 0, 16, 8, 0, 0), false, arrived},
		"before 2000":          {dt(1999, 12, 31, 8, 0, 0), false, arrived},
		"not a date":           {dt(2026, 2, 30, 8, 0, 0), false, arrived},
		"hour 24":              {dt(2026, 10, 16, 24, 0, 0), false, arrived},
	} {
		t.Run(name, func(t *testing.T) {
			s := byType(mapLine(t, "2a35", bp(tc.ts)))["blood_pressure_systolic"]
			assert.True(t, s.StartDate.Equal(tc.want), "%s", s.StartDate)
			if tc.device {
				assert.Equal(t, "device", s.Metadata["clock"])
			} else {
				assert.Equal(t, "received", s.Metadata["clock"])
			}
		})
	}
}

func TestWeight(t *testing.T) {
	t.Run("SI with BMI and height", func(t *testing.T) {
		v := cat([]byte{0x02 | 0x08}, le16(15000), dt(2026, 10, 16, 7, 5, 0), le16(245), le16(1750))
		got := byType(mapLine(t, "2a9d", v))
		assert.Equal(t, 75.0, got["body_mass"].Value)
		assert.Equal(t, "kg", got["body_mass"].Unit)
		assert.Equal(t, 24.5, got["body_mass_index"].Value)
		assert.Equal(t, "kg/m2", got["body_mass_index"].Unit)
		assert.Equal(t, 1.75, got["height"].Value)
		assert.Equal(t, "m", got["height"].Unit)
		assert.Equal(t, "device", got["body_mass"].Metadata["clock"])
	})
	t.Run("imperial", func(t *testing.T) {
		v := cat([]byte{0x01 | 0x08}, le16(16500), le16(244), le16(690))
		got := byType(mapLine(t, "2a9d", v))
		assert.Equal(t, 165.0, got["body_mass"].Value)
		assert.Equal(t, "lb", got["body_mass"].Unit)
		assert.Equal(t, 69.0, got["height"].Value)
		assert.Equal(t, "in", got["height"].Unit)
	})
	t.Run("unsuccessful", func(t *testing.T) {
		assert.Empty(t, mapLine(t, "2a9d", cat([]byte{0x00}, le16(0xFFFF))).QuantitySamples)
	})
	t.Run("shared scale", func(t *testing.T) {
		v := cat([]byte{0x04}, le16(12000), []byte{3})
		assert.Empty(t, mapLine(t, "2a9d", v, withDeviceUser(1)).QuantitySamples)
		assert.Len(t, mapLine(t, "2a9d", v, withDeviceUser(3)).QuantitySamples, 1)
	})
}

func TestBodyComposition(t *testing.T) {
	// time stamp, user, basal metabolism, muscle %, muscle mass, fat free
	// mass, soft lean mass, body water mass, impedance, weight, height.
	flags := 0x0002 | 0x0004 | 0x0008 | 0x0010 | 0x0020 | 0x0040 | 0x0080 | 0x0100 | 0x0200 | 0x0400 | 0x0800
	v := cat(le16(flags), le16(215), dt(2026, 10, 16, 7, 6, 0), []byte{1},
		le16(7000), le16(412), le16(6200), le16(11800), le16(11000), le16(8400), le16(5200),
		le16(15000), le16(1750))
	res := mapLine(t, "2a9c", v)
	got := byType(res)
	assert.Equal(t, 21.5, got["body_fat_percentage"].Value)
	assert.Equal(t, "percent", got["body_fat_percentage"].Unit)
	assert.Equal(t, 31.0, got["muscle_mass"].Value)
	assert.Equal(t, 59.0, got["lean_body_mass"].Value)
	assert.Equal(t, 42.0, got["water_mass"].Value)
	assert.Equal(t, 75.0, got["body_mass"].Value)
	assert.Equal(t, 1.75, got["height"].Value)
	assert.Len(t, res.QuantitySamples, 6)
	assert.Equal(t, "device", got["body_mass"].Metadata["clock"])

	t.Run("fat only, imperial weight", func(t *testing.T) {
		v := cat(le16(0x0001|0x0400), le16(180), le16(16500))
		got := byType(mapLine(t, "2a9c", v))
		assert.Equal(t, 18.0, got["body_fat_percentage"].Value)
		assert.Equal(t, 165.0, got["body_mass"].Value)
		assert.Equal(t, "lb", got["body_mass"].Unit)
	})
	t.Run("unsuccessful fat", func(t *testing.T) {
		got := byType(mapLine(t, "2a9c", cat(le16(0x0400), le16(0xFFFF), le16(15000))))
		_, fat := got["body_fat_percentage"]
		assert.False(t, fat)
		assert.Equal(t, 75.0, got["body_mass"].Value)
	})
	t.Run("truncated", func(t *testing.T) {
		_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: fields("2a9c", cat(le16(0x0400), le16(215)))})
		require.ErrorIs(t, err, ErrTruncated)
	})
}

func TestGlucose(t *testing.T) {
	t.Run("kg/L, offset, capillary blood from a finger", func(t *testing.T) {
		v := cat([]byte{0x01 | 0x02}, le16(42), dt(2026, 10, 16, 7, 0, 0), le16(15), sf(98, -5), []byte{0x11})
		res := mapLine(t, "2a18", v)
		require.Len(t, res.QuantitySamples, 1)
		s := res.QuantitySamples[0]
		assert.Equal(t, "blood_glucose", s.QuantityType)
		assert.Equal(t, 98.0, s.Value)
		assert.Equal(t, "mg/dL", s.Unit)
		assert.True(t, s.StartDate.Equal(time.Date(2026, 10, 16, 5, 15, 0, 0, time.UTC)), s.StartDate)
		assert.Equal(t, 42, s.Metadata["sequence_number"])
		assert.Equal(t, "capillary_whole_blood", s.Metadata["specimen"])
		assert.Equal(t, "finger", s.Metadata["sample_location"])
	})
	t.Run("mol/L, interstitial fluid, sensor status", func(t *testing.T) {
		v := cat([]byte{0x02 | 0x04 | 0x08}, le16(7), dt(2026, 10, 16, 7, 0, 0), sf(54, -4), []byte{0xF9}, le16(0x0001|0x0040))
		s := mapLine(t, "2a18", v).QuantitySamples[0]
		assert.Equal(t, 5.4, s.Value)
		assert.Equal(t, "mmol/L", s.Unit)
		assert.Equal(t, "interstitial_fluid", s.Metadata["specimen"])
		_, loc := s.Metadata["sample_location"]
		assert.False(t, loc, "location 15 is not available")
		assert.Equal(t, []string{"device_battery_low", "result_higher_than_device_range"}, s.Metadata["sensor_status"])
	})
	t.Run("control solution", func(t *testing.T) {
		v := cat([]byte{0x02}, le16(8), dt(2026, 10, 16, 7, 0, 0), sf(110, -5), []byte{0x1A})
		assert.Empty(t, mapLine(t, "2a18", v).QuantitySamples)
		v = cat([]byte{0x02}, le16(9), dt(2026, 10, 16, 7, 0, 0), sf(110, -5), []byte{0x41})
		assert.Empty(t, mapLine(t, "2a18", v).QuantitySamples)
	})
	t.Run("no concentration", func(t *testing.T) {
		assert.Empty(t, mapLine(t, "2a18", cat([]byte{0x00}, le16(10), dt(2026, 10, 16, 7, 0, 0))).QuantitySamples)
	})
}

func TestTemperature(t *testing.T) {
	t.Run("celsius, mouth, time stamp", func(t *testing.T) {
		v := cat([]byte{0x02 | 0x04}, fl(368, -1), dt(2026, 10, 16, 6, 45, 0), []byte{6})
		s := mapLine(t, "2a1c", v).QuantitySamples[0]
		assert.Equal(t, model.QTBodyTemperature, s.QuantityType)
		assert.Equal(t, 36.8, s.Value)
		assert.Equal(t, "celsius", s.Unit)
		assert.Equal(t, "mouth", s.Metadata["measurement_location"])
		assert.Equal(t, "device", s.Metadata["clock"])
	})
	t.Run("fahrenheit", func(t *testing.T) {
		s := mapLine(t, "2a1c", cat([]byte{0x01}, fl(986, -1))).QuantitySamples[0]
		assert.Equal(t, 98.6, s.Value)
		assert.Equal(t, "degF", s.Unit)
	})
	t.Run("not a number", func(t *testing.T) {
		assert.Empty(t, mapLine(t, "2a1c", cat([]byte{0x00}, []byte{0xFF, 0xFF, 0x7F, 0x00})).QuantitySamples)
	})
}

func TestPulseOximeter(t *testing.T) {
	arrived := atTime(t)
	t.Run("spot check with time stamp", func(t *testing.T) {
		v := cat([]byte{0x01}, sf(97, 0), sf(62, 0), dt(2026, 10, 16, 20, 0, 0))
		got := byType(mapLine(t, "2a5e", v))
		assert.Equal(t, 97.0, got[model.QTOxygenSaturation].Value)
		assert.Equal(t, "percent", got[model.QTOxygenSaturation].Unit)
		assert.Equal(t, 62.0, got[model.QTHeartRate].Value)
		assert.Equal(t, "pulse_oximeter", got[model.QTHeartRate].Metadata["context"])
		assert.True(t, got[model.QTOxygenSaturation].StartDate.Equal(time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)))
	})
	t.Run("device clock not set", func(t *testing.T) {
		v := cat([]byte{0x01 | 0x10}, sf(97, 0), sf(62, 0), dt(2026, 10, 16, 20, 0, 0))
		s := byType(mapLine(t, "2a5e", v))[model.QTOxygenSaturation]
		assert.True(t, s.StartDate.Equal(arrived))
		assert.Equal(t, "received", s.Metadata["clock"])
	})
	t.Run("demonstration data", func(t *testing.T) {
		v := cat([]byte{0x02}, sf(97, 0), sf(62, 0), le16(1<<10))
		assert.Empty(t, mapLine(t, "2a5e", v).QuantitySamples)
	})
	t.Run("continuous with fast, slow, questionable", func(t *testing.T) {
		v := cat([]byte{0x01 | 0x02 | 0x04 | 0x08}, sf(98, 0), sf(60, 0), sf(97, 0), sf(61, 0), sf(98, 0), sf(59, 0),
			le16(1<<14), []byte{0, 0, 0})
		res := mapLine(t, "2a5f", v)
		got := byType(res)
		assert.Equal(t, 98.0, got[model.QTOxygenSaturation].Value)
		assert.Equal(t, 60.0, got[model.QTHeartRate].Value)
		assert.Equal(t, []string{"questionable_measurement"}, got[model.QTOxygenSaturation].Metadata["measurement_status"])
		assert.True(t, got[model.QTOxygenSaturation].StartDate.Equal(arrived))
		assert.True(t, res.HighFrequency)
	})
	t.Run("invalid measurement", func(t *testing.T) {
		v := cat([]byte{0x04}, sf(98, 0), sf(60, 0), le16(1<<15))
		assert.Empty(t, mapLine(t, "2a5f", v).QuantitySamples)
	})
}

func TestUnknownCharacteristic(t *testing.T) {
	res := mapLine(t, "2a19", []byte{80}) // battery level
	assert.True(t, res.IsEmpty())
}

func TestSource(t *testing.T) {
	for _, tc := range []struct {
		manufacturer, name, model string
		slug, id, display         string
	}{
		{"Polar Electro Oy", "Polar H10 0A1B2C3D", "H10", "polar", "Polar H10", "Polar H10"},
		{"OMRON HEALTHCARE Co., Ltd.", "BLEsmart_00000397", "HEM-7600T", "omron", "OMRON HEM-7600T", "BLEsmart"},
		{"", "TICKR 7F21", "", "wahoo", "", "TICKR"},
		{"A&D Medical", "A&D_UA-651", "UA-651BLE", "and", "A&D UA-651BLE", "A&D_UA-651"},
		{"Nordic Semiconductor", "Thermo", "", Slug, "", "Thermo"},
		{"Garmin", "Edge 1040", "", "garmin", "", "Edge 1040"},
		{"", "Thermo 123456", "", Slug, "", "Thermo"},
		{"", "", "", Slug, "", ""},
	} {
		s := sourceOf(device{Manufacturer: tc.manufacturer, Name: tc.name, Model: tc.model})
		assert.Equal(t, tc.slug, s.slug, tc.manufacturer+tc.name)
		assert.Equal(t, tc.id, s.deviceID, tc.manufacturer+tc.name)
		assert.Equal(t, tc.display, s.name, tc.manufacturer+tc.name)
	}
}

func TestTimeZone(t *testing.T) {
	s := mapLine(t, "2a37", []byte{0x00, 70}, withAt("2026-10-16T21:04:05+05:30")).QuantitySamples[0]
	assert.Equal(t, 330, s.Metadata["utc_offset_minutes"])
	_, zone := s.Metadata["timezone"]
	assert.False(t, zone)
	s = mapLine(t, "2a37", []byte{0x00, 70}, withAt("2026-10-16T19:04:05Z")).QuantitySamples[0]
	assert.Equal(t, "Etc/UTC", s.Metadata["timezone"])
}

func TestContentHash(t *testing.T) {
	a := mapLine(t, "2a35", cat([]byte{0x02}, sf(121, 0), sf(79, 0), sf(93, 0), dt(2026, 10, 16, 8, 30, 0)))
	b := mapLine(t, "2a35", cat([]byte{0x02}, sf(121, 0), sf(79, 0), sf(93, 0), dt(2026, 10, 16, 8, 30, 0)),
		withAt("2026-10-16T23:00:00+02:00"))
	c := mapLine(t, "2a35", cat([]byte{0x02}, sf(122, 0), sf(79, 0), sf(93, 0), dt(2026, 10, 16, 8, 30, 0)))
	hashes := func(r model.MapResult) map[string]string {
		out := map[string]string{}
		for _, s := range r.QuantitySamples {
			require.Len(t, s.ContentHash, 64)
			out[s.QuantityType] = s.ContentHash
		}
		return out
	}
	// The same stored reading, re-sent later from the cuff's memory, hashes the same.
	assert.Equal(t, hashes(a), hashes(b))
	assert.NotEqual(t, hashes(a)["blood_pressure_systolic"], hashes(c)["blood_pressure_systolic"])
	assert.Equal(t, hashes(a)["blood_pressure_diastolic"], hashes(c)["blood_pressure_diastolic"])
}

func TestSamplesDoNotShareMetadata(t *testing.T) {
	// The ETL validator writes a quality flag into one sample's metadata.
	res := mapLine(t, "2a35", cat([]byte{0x04}, sf(121, 0), sf(79, 0), sf(93, 0), sf(64, 0)))
	require.Len(t, res.QuantitySamples, 3)
	res.QuantitySamples[0].Metadata["quality_flag"] = "suspect"
	_, shared := res.QuantitySamples[1].Metadata["quality_flag"]
	assert.False(t, shared)
}

func TestSFLOATAndFLOAT(t *testing.T) {
	for _, tc := range []struct {
		raw  []byte
		want float64
		ok   bool
	}{
		{sf(121, 0), 121, true},
		{sf(-5, 0), -5, true},
		{sf(368, -1), 36.8, true},
		{sf(98, -5), 0.00098, true},
		{sf(12, 2), 1200, true},
		{le16(0x07FF), 0, false}, // NaN
		{le16(0x0800), 0, false}, // NRes
		{le16(0x07FE), 0, false}, // +INF
		{le16(0x0802), 0, false}, // -INF
		{le16(0x0801), 0, false}, // reserved
	} {
		d, ok := sfloatOf(uint16(tc.raw[0]) | uint16(tc.raw[1])<<8)
		assert.Equal(t, tc.ok, ok, "%x", tc.raw)
		if ok {
			assert.InDelta(t, tc.want, d.float(0), 1e-12, "%x", tc.raw)
		}
	}
	d, ok := sfloatOf(uint16(sf(98, -5)[0]) | uint16(sf(98, -5)[1])<<8)
	require.True(t, ok)
	assert.Equal(t, 98.0, d.float(5), "scaling moves the exponent, not the float")

	r := reader{b: fl(-3685, -2)}
	f, ok, err := r.float32()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, -36.85, f.float(0))
	for _, special := range []uint32{0x007FFFFF, 0x00800000, 0x007FFFFE, 0x00800002, 0x00800001} {
		r := reader{b: []byte{byte(special), byte(special >> 8), byte(special >> 16), 0}}
		_, ok, err := r.float32()
		require.NoError(t, err)
		assert.False(t, ok, "%x", special)
	}
}

// TestUnitsAreKnown: every unit the mapper emits converts to its storage
// unit, so no reading is parked as a unit error.
func TestUnitsAreKnown(t *testing.T) {
	for _, v := range []struct {
		char  string
		value []byte
	}{
		{CharHeartRate, []byte{0x00, 72}},
		{CharBloodPressure, cat([]byte{0x04}, sf(121, 0), sf(79, 0), sf(93, 0), sf(64, 0))},
		{CharBloodPressure, cat([]byte{0x01}, sf(160, -1), sf(105, -1), sf(124, -1))},
		{CharWeight, cat([]byte{0x08}, le16(15000), le16(245), le16(1750))},
		{CharWeight, cat([]byte{0x01 | 0x08}, le16(16500), le16(244), le16(690))},
		{CharBodyComposition, cat(le16(0x0020|0x0040|0x0100|0x0400|0x0800), le16(215), le16(6200), le16(11800), le16(8400), le16(15000), le16(1750))},
		{CharBodyComposition, cat(le16(0x0001|0x0400|0x0800), le16(215), le16(16500), le16(690))},
		{CharGlucose, cat([]byte{0x02}, le16(1), dt(2026, 10, 16, 7, 0, 0), sf(98, -5), []byte{0x11})},
		{CharGlucose, cat([]byte{0x02 | 0x04}, le16(2), dt(2026, 10, 16, 7, 0, 0), sf(54, -4), []byte{0x11})},
		{CharTemperature, cat([]byte{0x00}, fl(368, -1))},
		{CharTemperature, cat([]byte{0x01}, fl(986, -1))},
		{CharPLXSpotCheck, cat([]byte{0x00}, sf(97, 0), sf(62, 0))},
		{CharPLXContinuous, cat([]byte{0x00}, sf(97, 0), sf(62, 0))},
	} {
		res := mapLine(t, v.char, v.value)
		require.NotEmpty(t, res.QuantitySamples, v.char)
		assert.Empty(t, model.CanonicalizeUnits(&res), "%s %x", v.char, v.value)
	}
}
