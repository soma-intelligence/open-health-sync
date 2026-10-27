// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMapValue -fuzztime=60s ./connectors/bluetooth/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzChars are the characteristics FuzzMapValue picks from by index.
var fuzzChars = []string{CharHeartRate, CharBloodPressure, CharWeight, CharBodyComposition, CharGlucose,
	CharTemperature, CharPLXSpotCheck, CharPLXContinuous, "2a19"}

// fuzzFar is a validation clock past any time a line can name, so the
// invariants checked on the raw Map output leave the future to validation.
var fuzzFar = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// FuzzMapValue decodes arbitrary characteristic values arriving at arbitrary
// times. Map must not panic, must be deterministic, must emit only finite
// numbers, samples with an identity and a unit, heartbeats in time order
// with a non-negative interval, a named zone that loads, and a line
// validation would store keeps the canonical invariants.
func FuzzMapValue(f *testing.F) {
	nan, nres, inf, ninf, reserved := []byte{0xFF, 0x07}, []byte{0x00, 0x08}, []byte{0xFE, 0x07}, []byte{0x02, 0x08}, []byte{0x01, 0x08}
	fnan, finf := []byte{0xFF, 0xFF, 0x7F, 0x00}, []byte{0xFE, 0xFF, 0x7F, 0x00}
	for _, s := range []struct {
		char  int
		value []byte
	}{
		{0, []byte{0x00, 70}},
		{0, []byte{0x16, 72, 0x00, 0x04, 0x10, 0x04}},
		{0, []byte{0x1F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{0, []byte{0x10, 60, 0x00}},
		{0, []byte{0x04, 0}},
		{1, cat([]byte{0x1E}, sf(120, 0), sf(80, 0), sf(93, 0), dt(2026, 10, 16, 21, 0, 0), sf(70, 0), []byte{1}, le16(0x3F))},
		{1, cat([]byte{0x01}, sf(16, 0), sf(10, 0), nan)},
		{1, cat([]byte{0x00}, nres, inf, ninf)},
		{1, cat([]byte{0xFF}, sf(2047, 7), sf(-2048, -8), reserved)},
		{2, cat([]byte{0x0E}, le16(14600), dt(2026, 10, 16, 7, 30, 0), []byte{1}, le16(229), le16(1780))},
		{2, cat([]byte{0x0F}, le16(0xFFFF), dt(0, 0, 0, 0, 0, 0), []byte{0xFF}, le16(0xFFFF), le16(0xFFFF))},
		{3, cat(le16(0x1FFF), le16(200), dt(2026, 2, 30, 25, 61, 61), []byte{2}, bytes.Repeat(le16(0xFFFE), 9))},
		{4, cat([]byte{0x0B}, le16(7), dt(2026, 10, 16, 21, 0, 0), le16(0x7FFF), sf(95, -5), []byte{0x11}, le16(0xFFFF))},
		{4, cat([]byte{0x07}, le16(1), dt(65535, 12, 31, 23, 59, 59), le16(0x8000), sf(2047, 7), []byte{0xA4})},
		{5, cat([]byte{0x06}, fl(3675, -2), dt(2026, 10, 16, 21, 0, 0), []byte{9})},
		{5, cat([]byte{0x01}, []byte{0xFF, 0xFF, 0x7F, 0x7F})},
		{5, cat([]byte{0x00}, fnan)},
		{5, cat([]byte{0x00}, finf)},
		{6, cat([]byte{0x1F}, sf(98, 0), sf(64, 0), dt(1970, 1, 1, 0, 0, 0), le16(0x4040), le16(0), []byte{0}, sf(1, 0))},
		{7, cat([]byte{0x1F}, sf(97, 0), sf(65, 0), sf(1, 0), sf(1, 0), sf(1, 0), sf(1, 0), le16(1<<15))},
		{8, []byte{100}},
		{0, nil},
		{1, []byte{0x02}},
	} {
		for _, at := range []string{at, "1970-01-01T00:00:00Z", "9999-12-31T23:59:59+14:00", "2026-10-16T21:04:05-12:00"} {
			f.Add(uint8(s.char), s.value, at, int16(-1))
		}
	}
	f.Add(uint8(1), cat([]byte{0x08}, sf(120, 0), sf(80, 0), sf(93, 0), []byte{3}), at, int16(2))
	f.Add(uint8(2), cat([]byte{0x04}, le16(14600), []byte{0xFF}), at, int16(254))

	f.Fuzz(func(t *testing.T, ci uint8, value []byte, arrived string, deviceUser int16) {
		if len(value) == 0 || len(value) > MaxValueBytes {
			return
		}
		fs := map[string]any{
			"at": arrived, "char": fuzzChars[int(ci)%len(fuzzChars)], "value": hex.EncodeToString(value),
			"session": "9f2c4e1a7b3d5c60",
			"device":  map[string]any{"name": "Polar H10 0A1B2C3D", "manufacturer": "Polar Electro Oy", "model": "H10"},
		}
		if deviceUser >= 0 {
			fs["device_user"] = int(deviceUser)
		}
		line, err := json.Marshal(fs)
		if err != nil {
			return
		}
		var fields map[string]any
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		raw := connector.RawRecord{UserID: testUser, Provider: Slug, DataType: DataType, Fields: fields}
		a, errA := New().Map(raw)
		b, errB := New().Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
		}
		if errA != nil {
			return
		}
		for _, msg := range synthtest.Invariants(&a, fuzzFar) {
			if strings.Contains(msg, "not finite") {
				t.Fatalf("Map emitted %s", msg)
			}
		}
		for _, s := range a.QuantitySamples {
			if s.ContentHash == "" || s.QuantityType == "" || s.Unit == "" || s.UserID != testUser || s.StartDate.IsZero() {
				t.Fatalf("sample without identity: %+v", s)
			}
			if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
				t.Fatalf("sample value %v", s.Value)
			}
			if z, ok := s.Metadata["timezone"].(string); ok {
				if _, err := time.LoadLocation(z); err != nil {
					t.Fatalf("named zone %q does not load", z)
				}
			}
		}
		for i, h := range a.HeartbeatSamples {
			if h.RRMS < 0 || math.IsNaN(h.RRMS) || h.Timestamp.IsZero() {
				t.Fatalf("heartbeat %+v", h)
			}
			if i > 0 && h.Timestamp.Before(a.HeartbeatSamples[i-1].Timestamp) {
				t.Fatalf("heartbeats out of order")
			}
		}
		for _, r := range synthtest.Run(New(), []synth.Line{{DataType: DataType, JSON: line}}, atTime(t)) {
			if r.Outcome == synthtest.Stored && len(r.Broken) > 0 {
				t.Fatalf("stored a record breaking %v", r.Broken)
			}
		}
	})
}

// FuzzParseUpload feeds arbitrary bodies to ParseUpload. It must not panic,
// and a body it accepts holds lines Map takes without a contract error.
func FuzzParseUpload(f *testing.F) {
	good := `{"at":"` + at + `","char":"2a37","value":"0046","session":"9f2c4e1a7b3d5c60"}`
	f.Add([]byte(good + "\n"))
	f.Add([]byte(good + "\r\n\n" + good))
	f.Add([]byte("\n\n"))
	f.Add([]byte(`{"at":"2026-10-16T21:04:05Z","char":"00002a37-0000-1000-8000-00805f9b34fb","value":"00","session":"aaaaaaaa","device_user":254}`))
	f.Add([]byte(`{"at":"x","char":"2a37","value":"zz","session":"!"}`))
	f.Add([]byte(`[` + good + `]`))
	f.Add([]byte(`{"at":"2026-10-16T21:04:05Z","char":"2a37","value":"` + strings.Repeat("ab", MaxValueBytes+1) + `","session":"aaaaaaaa"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if err := New().ParseUpload(connector.UploadMeta{Type: DataType}, bytes.NewReader(body), nil); err != nil {
			return
		}
		for _, l := range bytes.Split(body, []byte("\n")) {
			l = bytes.TrimSpace(l)
			if len(l) == 0 {
				continue
			}
			var fields map[string]any
			if err := json.Unmarshal(l, &fields); err != nil {
				t.Fatalf("accepted a line the ETL cannot read: %q", l)
			}
			if _, err := New().Map(connector.RawRecord{UserID: testUser, Provider: Slug, DataType: DataType, Fields: fields}); err != nil &&
				strings.Contains(err.Error(), ErrInvalidLine.Error()) {
				t.Fatalf("accepted a line Map refuses: %v: %q", err, l)
			}
		}
	})
}
