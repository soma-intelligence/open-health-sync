// SPDX-License-Identifier: AGPL-3.0-only

package healthconnect

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=^FuzzMap$ -fuzztime=60s ./connectors/healthconnect/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzNow is the validation clock of the fuzz targets.
var fuzzNow = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

// fuzzLines are the lines of the recorded upload.
func fuzzLines(f *testing.F) [][]byte {
	b, err := os.ReadFile("testdata/upload.ndjson")
	if err != nil {
		f.Fatal(err)
	}
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			out = append(out, append([]byte(nil), l...))
		}
	}
	return out
}

// FuzzMap feeds arbitrary lines to Map, the way the ETL hands them over
// (decoded into a map). Map must not panic, must be deterministic, must not
// emit a number that is not finite, and what it returns must carry its
// identity. What validation would store keeps the canonical model's
// invariants.
func FuzzMap(f *testing.F) {
	for _, l := range fuzzLines(f) {
		f.Add(l)
	}
	for _, s := range []string{
		`{}`, `{"recordType":null}`, `{"recordType":"Steps","metadata":null}`, `{"recordType":"Steps","metadata":{"id":""}}`,
		`{"recordType":"Steps","metadata":{"id":"x"},"startTime":"1970-01-01T00:00:00Z","endTime":"9999-12-31T23:59:59Z","count":1e308}`,
		`{"recordType":"Weight","metadata":{"id":"x"},"time":"2026-03-02T08:00:00+14:00","zoneOffset":"+23:59","weightKilograms":-0}`,
		`{"recordType":"Hydration","metadata":{"id":"x"},"startTime":"2026-03-02T08:00:00Z","endTime":"2026-03-02T08:00:00-12:00","volumeLiters":1e308}`,
		`{"recordType":"Nutrition","metadata":{"id":"x"},"startTime":"2026-03-02T08:00:00Z","endTime":"2026-03-02T09:00:00Z","sodiumGrams":1e308,"energyKilocalories":1e308}`,
		`{"recordType":"HeartRate","metadata":{"id":"x"},"samples":[{"time":"2026-03-02T08:00:00Z","beatsPerMinute":60},{"time":"2026-03-02T08:00:00Z","beatsPerMinute":60},{"time":null,"beatsPerMinute":null}]}`,
		`{"recordType":"HeartRate","metadata":{"id":"x"},"samples":[]}`,
		`{"recordType":"SleepSession","metadata":{"id":"x"},"startTime":"2026-03-02T00:00:00Z","endTime":"2026-03-02T08:00:00Z","stages":[{"startTime":"0001-01-01T00:00:00Z","endTime":"9999-12-31T23:59:59Z","stage":"deep"},{"startTime":"0001-01-01T00:00:00Z","endTime":"9999-12-31T23:59:59Z","stage":"rem"}]}`,
		`{"recordType":"ExerciseSession","metadata":{"id":"x"},"startTime":"0001-01-01T00:00:00Z","endTime":"9999-12-31T23:59:59Z"}`,
		`{"recordType":"Deletion","recordId":""}`, `{"recordType":"Deletion","recordId":"r"}`,
		`{"recordType":"BloodPressure","metadata":{"id":"x"},"time":"2026-03-02T08:00:00Z","systolicMillimetersOfMercury":"NaN","diastolicMillimetersOfMercury":80}`,
		`{"recordType":"Steps","metadata":{"id":"x","device":{"device":{"device":{"device":{}}}}}}`,
	} {
		f.Add([]byte(s))
	}

	c := New()
	f.Fuzz(func(t *testing.T, line []byte) {
		var fields map[string]any
		if json.Unmarshal(line, &fields) != nil || fields == nil {
			return
		}
		raw := connector.RawRecord{UserID: testUser, Provider: Slug, Fields: fields}
		a, errA := c.Map(raw)
		b, errB := New().Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
		}
		if errA != nil {
			return
		}
		for _, msg := range synthtest.Invariants(&a, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)) {
			if !strings.Contains(msg, "ends before it starts") && !strings.Contains(msg, "is in the future") {
				t.Fatal(msg)
			}
		}
		for _, n := range a.NutritionLogs {
			if _, err := json.Marshal(n.NutrientsJSON); err != nil {
				t.Fatalf("nutrients do not encode: %v", err)
			}
		}
		for _, s := range a.QuantitySamples {
			if s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" || s.Provider != Slug || s.StartDate.IsZero() {
				t.Fatalf("sample without identity: %+v", s)
			}
		}
		for _, s := range a.SleepSessions {
			if s.ContentHash == "" || s.ExternalID == "" {
				t.Fatalf("sleep without identity: %+v", s)
			}
		}
		for _, p := range a.SleepPhases {
			if p.DurationS < 0 {
				t.Fatalf("phase of negative duration: %+v", p)
			}
		}
		for _, w := range a.Workouts {
			if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" || (w.DurationS != nil && *w.DurationS < 0) {
				t.Fatalf("bad workout: %+v", w)
			}
		}
		for _, d := range a.Deletions {
			if d.ExternalID == "" {
				t.Fatal("deletion without id")
			}
		}
		if len(etl.Rejections(etl.ValidateMapResultAt(&a, fuzzNow))) == 0 {
			if broken := synthtest.Invariants(&a, fuzzNow); len(broken) > 0 {
				t.Fatalf("validation would store %v", broken)
			}
		}
	})
}

// FuzzParseUpload feeds arbitrary bodies to the upload check. It must not
// panic, and a body it accepts has only lines Map can be handed: JSON
// objects with a recordType.
func FuzzParseUpload(f *testing.F) {
	b, err := os.ReadFile("testdata/upload.ndjson")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Add([]byte("\n\n"))
	f.Add([]byte(`{"recordType":"Steps"}` + "\r\n" + `{"recordType":""}`))
	f.Add([]byte(`[{"recordType":"Steps"}]`))
	f.Add([]byte(strings.Repeat("[", 4096)))
	c := New()
	f.Fuzz(func(t *testing.T, body []byte) {
		err := c.ParseUpload(connector.UploadMeta{}, bytes.NewReader(body), nil)
		if err != nil {
			if !errors.Is(err, ErrInvalidLine) && !errors.Is(err, ErrNoLines) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			return
		}
		for _, l := range bytes.Split(body, []byte("\n")) {
			l = bytes.TrimSpace(l)
			if len(l) == 0 {
				continue
			}
			var head struct {
				RecordType string `json:"recordType"`
			}
			if json.Unmarshal(l, &head) != nil || head.RecordType == "" {
				t.Fatalf("accepted line %q", l)
			}
		}
	})
}
