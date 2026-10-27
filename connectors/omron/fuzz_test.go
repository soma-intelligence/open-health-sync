// SPDX-License-Identifier: AGPL-3.0-only

package omron

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	somron "github.com/soma-intelligence/open-health-sync/internal/synth/omron"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMap -fuzztime=60s ./connectors/omron/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzNow is the validation clock of the pipeline check: late enough that a
// documented reading is never in the future.
var fuzzNow = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

// fuzzDataTypes are the data types FuzzMap picks from by index.
var fuzzDataTypes = []string{DataTypeBloodPressure, DataTypeWeight, "activity"}

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map). Map must not panic and must be
// deterministic; what it returns names its user, record and hash, carries
// finite numbers and a real instant; and a line validation would store keeps
// the canonical model's invariants.
func FuzzMap(f *testing.F) {
	tl := synth.Build(synth.Options{Seed: 3, Days: 2, Now: fuzzNow})
	seen := map[string]int{}
	for _, l := range somron.Lines(tl) {
		for i, dt := range fuzzDataTypes {
			if l.DataType == dt && seen[dt] < 6 {
				f.Add(uint8(i), string(l.JSON))
				seen[dt]++
			}
		}
	}
	bp, err := json.Marshal(bpRecord(5, "2026-03-02T08:00:00", "128", "82", 64))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(uint8(0), string(bp))
	for _, s := range []string{
		`{}`, `{"id":null}`, `{"id":"","dateTime":"2026-03-02T08:00:00"}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","systolic":0,"diastolic":0,"pulse":0,"possibleAfib":true}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","systolic":"Inf","diastolic":"+Inf","pulse":"NaN"}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","systolic":1e308,"diastolic":1e308,"pulse":-1e308}`,
		`{"id":1,"dateTime":"1970-01-01T00:00:00","systolic":120,"diastolic":80}`,
		`{"id":1,"dateTime":"0001-01-01T00:00:00","systolic":120,"diastolic":80}`,
		`{"id":1,"dateTime":"9999-12-31T23:59:59","systolic":120,"diastolic":80}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","dateTimeUtcOffset":"+-1:30","systolic":120,"diastolic":80}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","dateTimeUtcOffset":"-+5:00","systolic":120,"diastolic":80}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","dateTimeUtcOffset":"+23:59","systolic":120,"diastolic":80}`,
		`{"id":1,"dateTime":"2026-03-02T08:00:00","dateTimeUtcOffset":50400,"systolic":120,"diastolic":80}`,
		`{"id":{"a":[{"b":{}}]},"dateTime":"2026-03-02T08:00:00+14:00","systolic":[120],"diastolic":{"v":80}}`,
	} {
		f.Add(uint8(0), s)
	}
	for _, s := range []string{
		`{"id":2,"dateTime":"2026-03-02T08:00:00","weight":"Infinity","weightPref":"kg","bmiValue":"inf"}`,
		`{"id":2,"dateTime":"2026-03-02T08:00:00","weight":"NaN","weightPref":"lbs","bodyFatPercentage":"nan","visceralFatLevel":"1e400"}`,
		`{"id":2,"dateTime":"2026-03-02T08:00:00","weight":71.6,"weightPref":null,"bmiValue":null}`,
		`{"id":2,"dateTime":"2026-03-02 08:00:00","weight":"  71.6 ","weightPref":" KG "}`,
	} {
		f.Add(uint8(1), s)
	}

	c := New(Options{})
	f.Fuzz(func(t *testing.T, kind uint8, line string) {
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
			return
		}
		dt := fuzzDataTypes[int(kind)%len(fuzzDataTypes)]
		raw := connector.RawRecord{UserID: testUser, Provider: Slug, DataType: dt, Fields: fields}
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
			if s.StartDate.IsZero() {
				t.Fatal("sample at the zero time")
			}
		}
		for _, s := range a.CategorySamples {
			if s.ContentHash == "" || s.ExternalID == "" || s.CategoryType == "" || s.StartDate.IsZero() {
				t.Fatalf("category sample without identity: %+v", s)
			}
		}
		for _, msg := range synthtest.Invariants(&a, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("Map emitted a broken record: %s", msg)
		}
		for _, r := range synthtest.Run(c, []synth.Line{{DataType: dt, JSON: []byte(line)}}, fuzzNow) {
			if r.Outcome == synthtest.Panic || (r.Outcome == synthtest.Stored && len(r.Broken) > 0) {
				t.Fatalf("pipeline: %s %s %v", r.Outcome, r.Detail, r.Broken)
			}
		}
	})
}

// FuzzUTCOffset checks the offset reader on arbitrary JSON values: a reading
// is within -12 h..+14 h, and an offset written with a sign has that sign.
func FuzzUTCOffset(f *testing.F) {
	for _, s := range []string{`"+00:00"`, `"-05:30"`, `"+14:00"`, `-21600`, `"3600"`, `"+-1:30"`, `"-+5:00"`, `"+23:59"`, `"+5:-0"`, `null`, `""`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		off, ok := utcOffset(json.RawMessage(s))
		if !ok {
			return
		}
		if off < -12*60 || off > 14*60 {
			t.Fatalf("utcOffset(%s) = %d minutes", s, off)
		}
		if u := strings.Trim(strings.TrimSpace(s), `"`); strings.Contains(u, ":") && off != 0 {
			if neg := strings.HasPrefix(strings.TrimSpace(u), "-"); neg != (off < 0) {
				t.Fatalf("utcOffset(%s) = %d minutes, against its sign", s, off)
			}
		}
	})
}
