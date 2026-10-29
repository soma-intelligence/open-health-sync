// SPDX-License-Identifier: AGPL-3.0-only

package xiaomi

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
//	go test -run='^$' -fuzz=^FuzzMap$ -fuzztime=60s ./connectors/xiaomi/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzNow is the validation clock of the fuzz targets.
var fuzzNow = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

// fuzzUpload is the recorded upload.
func fuzzUpload(f *testing.F) []byte {
	b, err := os.ReadFile("testdata/upload.ndjson")
	if err != nil {
		f.Fatal(err)
	}
	return b
}

// plausible bounds the times a mapped record may carry: unix() reads 2000 to
// 2100.
func plausible(t time.Time) bool {
	return t.IsZero() || (t.Year() >= 2000 && t.Year() <= 2100)
}

// FuzzMap feeds arbitrary lines to Map, the way the ETL hands them over
// (decoded into a map). Map must not panic, must be deterministic, must not
// emit a number that is not finite, a time outside what it reads, or a
// negative duration, and what it returns must carry its identity. What
// validation would store keeps the canonical model's invariants.
func FuzzMap(f *testing.F) {
	sc := bufio.NewScanner(bytes.NewReader(fuzzUpload(f)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			f.Add(append([]byte(nil), l...))
		}
	}
	for _, s := range []string{
		`{}`, `{"table":null,"key":null}`, `{"table":"fitness_data","key":"heart_rate","value":null}`,
		`{"table":"fitness_data","key":"heart_rate","value":{"time":"NaN","bpm":60}}`,
		`{"table":"fitness_data","key":"steps","time":1772960400,"value":{"steps":"Inf","distance":"+Inf","timezone":"NaN"}}`,
		`{"table":"fitness_data","key":"calories","time":1772960400,"value":{"calories":1e308,"timezone":1e308}}`,
		`{"table":"fitness_data","key":"heart_rate","time":0,"value":{"time":0,"bpm":60}}`,
		`{"table":"fitness_data","key":"heart_rate","value":{"time":253402300799,"bpm":60}}`,
		`{"table":"fitness_data","key":"weight","time":1772960400,"value":{"weight":"80","body_fat_rate":"NaN","timezone":-48}}`,
		`{"table":"fitness_data","key":"sleep","time":1772960400,"value":{"bedtime":1772900000,"wake_up_time":1772930000,"sleep_deep_duration":1e300,"sleep_light_duration":1e300,"sleep_rem_duration":1e300}}`,
		`{"table":"fitness_data","key":"sleep","value":{"segment_details":[{"bedtime":1772900000,"wake_up_time":1772930000,"items":[{"start_time":1772900000,"end_time":1772910000,"state":2},{"start_time":1772900000,"end_time":1772910000,"state":2}]},{"bedtime":"NaN","wake_up_time":"NaN"}]}}`,
		`{"table":"sport_record","key":"outdoor_running","value":{"start_time":1772900000,"duration":1e300,"calories":"Inf"}}`,
		`{"table":"sport_record","key":"outdoor_running","value":{"start_time":1772900000,"end_time":1772903600,"duration":-5,"distance":"NaN"}}`,
		`{"table":"fitness_data","key":"heart_rate","value":{"time":{"time":{"time":{"time":1}}}}}`,
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
		for _, msg := range synthtest.Invariants(&a, time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC)) {
			if !strings.Contains(msg, "is in the future") {
				t.Fatal(msg)
			}
		}
		for _, s := range a.QuantitySamples {
			if s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" || s.Provider != Slug || !plausible(s.StartDate) || s.StartDate.IsZero() {
				t.Fatalf("bad sample: %+v", s)
			}
		}
		for _, s := range a.SleepSessions {
			if s.ContentHash == "" || s.ExternalID == "" || !plausible(s.BedtimeStart) || !plausible(s.BedtimeEnd) {
				t.Fatalf("bad sleep: %+v", s)
			}
			for _, p := range []*int{s.DeepSleepSeconds, s.REMSleepSeconds, s.LightSleepSeconds, s.AwakeSeconds, s.TotalSleepSeconds} {
				if p != nil && *p < 0 {
					t.Fatalf("sleep with a negative duration: %+v", s)
				}
			}
		}
		for _, p := range a.SleepPhases {
			if p.DurationS < 0 || !plausible(p.StartDate) {
				t.Fatalf("bad phase: %+v", p)
			}
		}
		for _, w := range a.Workouts {
			if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" || !plausible(w.StartDate) || (w.DurationS != nil && *w.DurationS < 0) {
				t.Fatalf("bad workout: %+v", w)
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
// panic, and a body it accepts has only JSON object lines with a table and a
// key.
func FuzzParseUpload(f *testing.F) {
	f.Add(fuzzUpload(f))
	f.Add([]byte("\n\n"))
	f.Add([]byte(`{"table":"fitness_data","key":"steps"}` + "\r\n" + `{"table":"","key":"x"}`))
	f.Add([]byte(`[{"table":"t","key":"k"}]`))
	f.Add([]byte(strings.Repeat("{", 4096)))
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
				Table string `json:"table"`
				Key   string `json:"key"`
			}
			if json.Unmarshal(l, &head) != nil || head.Table == "" || head.Key == "" {
				t.Fatalf("accepted line %q", l)
			}
		}
	})
}
