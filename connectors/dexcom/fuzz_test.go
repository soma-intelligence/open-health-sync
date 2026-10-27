// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	sdexcom "github.com/soma-intelligence/open-health-sync/internal/synth/dexcom"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMapEGV -fuzztime=60s ./connectors/dexcom/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzNow is the validation clock of the pipeline check: late enough that a
// documented reading is never in the future.
var fuzzNow = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

// FuzzMapEGV feeds arbitrary EGV lines to Map, the way the ETL hands them
// over (decoded into a map). Map must not panic and must be deterministic;
// what it returns names its user, record and hash, carries finite numbers
// and a real instant; and a line validation would store keeps the canonical
// model's invariants.
func FuzzMapEGV(f *testing.F) {
	tl := synth.Build(synth.Options{Seed: 3, Days: 1, Now: fuzzNow})
	n := 0
	for _, l := range sdexcom.Lines(tl) {
		if l.DataType == DataTypeEGV && n < 8 {
			f.Add(string(l.JSON))
			n++
		}
	}
	for _, rec := range []map[string]any{
		egvRecord("r1", "2026-03-01T08:00:00", "2026-03-01T09:00:00", 112),
		egvRecord("r2", "2026-03-01T08:00:00Z", "2026-03-01T09:00:00+01:00", 39),
		egvRecord("r3", "2026-03-01T08:00:00", "2026-03-01T22:00:00", 401),
	} {
		b, err := json.Marshal(rec)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(string(b))
	}
	for _, s := range []string{
		`{}`, `{"recordId":null,"value":null}`, `{"recordId":"x","value":100}`,
		`{"recordId":"x","systemTime":"1970-01-01T00:00:00","value":100}`,
		`{"recordId":"x","systemTime":"0001-01-01T00:00:00","value":100}`,
		`{"recordId":"x","systemTime":"9999-12-31T23:59:59","value":100}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00","displayTime":"2026-03-01T08:00:00+23:59","value":100}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00","displayTime":"2026-03-01T08:00:00-23:59","value":100}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00","displayTime":"2026-03-02T22:00:00","value":100}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00+14:00","displayTime":"2026-03-01T08:00:00-12:00","value":1e308,"unit":"mmol/L"}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00","value":-1,"trendRate":-1e308,"status":"low"}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00","value":"NaN"}`,
		`{"recordId":"x","systemTime":"2026-03-01T08:00:00","value":100,"unit":"furlongs"}`,
		`{"recordId":["x"],"systemTime":{"a":{"b":{"c":[]}}},"value":[100]}`,
	} {
		f.Add(s)
	}

	c := New(Options{})
	f.Fuzz(func(t *testing.T, line string) {
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
			return
		}
		raw := connector.RawRecord{UserID: testUser, Provider: Slug, DataType: DataTypeEGV, Fields: fields}
		a, errA := c.Map(raw)
		b, errB := New(Options{}).Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
		}
		if errA != nil {
			return
		}
		for _, s := range a.QuantitySamples {
			if s.UserID != testUser || s.Provider != Slug || s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" {
				t.Fatalf("sample without identity: %+v", s)
			}
			if s.StartDate.IsZero() || s.StartDate.Location() != time.UTC {
				t.Fatalf("sample without a UTC instant: %v", s.StartDate)
			}
			if off, ok := s.Metadata["utc_offset_minutes"].(int); ok && (off < -12*60 || off > 14*60) {
				t.Fatalf("utc_offset_minutes %d is outside -12 h..+14 h", off)
			}
		}
		for _, msg := range synthtest.Invariants(&a, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("Map emitted a broken record: %s", msg)
		}
		for _, r := range synthtest.Run(c, []synth.Line{{DataType: DataTypeEGV, JSON: []byte(line)}}, fuzzNow) {
			if r.Outcome == synthtest.Panic || (r.Outcome == synthtest.Stored && len(r.Broken) > 0) {
				t.Fatalf("pipeline: %s %s %v", r.Outcome, r.Detail, r.Broken)
			}
		}
	})
}

// FuzzDisplayOffset checks the offset reader on arbitrary display and system
// times: a reading is within -12 h..+14 h.
func FuzzDisplayOffset(f *testing.F) {
	for _, p := range [][2]string{
		{"2026-03-01T08:00:00", "2026-03-01T09:00:00"},
		{"2026-03-01T08:00:00", "2026-03-01T09:00:00+01:00"},
		{"2026-03-01T08:00:00", "2026-03-01T09:00:00+23:59"},
		{"2026-03-01T08:00:00", "2026-03-01T09:00:00-23:00"},
		{"2026-03-01T08:00:00Z", "2026-03-01T13:45:00"},
		{"0001-01-01T00:00:00", "9999-12-31T23:59:59"},
	} {
		f.Add(p[0], p[1])
	}
	f.Fuzz(func(t *testing.T, system, display string) {
		off, ok := displayOffset(system, display)
		if !ok {
			return
		}
		if off < -12*60 || off > 14*60 {
			t.Fatalf("displayOffset(%q, %q) = %d minutes", system, display, off)
		}
		if z := offsetZone(off); z != "" {
			if _, err := time.LoadLocation(z); err != nil {
				t.Fatalf("offset %d named zone %q: %v", off, z, err)
			}
		}
	})
}
