// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/ultrahuman/ultrahumanfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMapMetricsDay -fuzztime=60s ./connectors/ultrahuman/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzNow is the validation clock of the pipeline check: late enough that a
// recorded day is never in the future.
var fuzzNow = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

// FuzzMapMetricsDay feeds arbitrary stored days to Map, the way the ETL hands
// them over (decoded into a map). The date and the answer body are fuzzed
// apart, so the mutator works inside the body. Map must not panic and must
// be deterministic; what it returns names its user and hash, carries finite
// numbers, real instants and no negative duration; and a line validation
// would store keeps the canonical model's invariants.
func FuzzMapMetricsDay(f *testing.F) {
	for _, p := range [][2]string{
		{"2026-03-08", "metrics_2026-03-08.json"},
		{"2026-03-09", "metrics_2026-03-09.json"},
		{"2026-03-09", "metrics_empty.json"},
	} {
		f.Add(p[0], string(ultrahumanfake.File(p[1])))
	}
	for _, s := range []string{
		`{}`, `{"data":null}`, `{"data":{"metric_data":[]}}`, `{"data":{"metric_data":null,"metrics":{"2026-03-08":[]}}}`,
		`{"data":{"metric_data":[{"type":"hr","object":null},{"type":"HR","object":{"values":[{"value":60,"timestamp":1772931600}]}}]}}`,
		`{"data":{"metric_data":[{"type":"hr","object":{"values":[{"value":"NaN","timestamp":1772931600},{"value":"Inf","timestamp":1772931900},{"value":61,"timestamp":"NaN"}]}}]}}`,
		`{"data":{"metric_data":[{"type":"hr","object":{"values":[{"value":60,"timestamp":0},{"value":60,"timestamp":253402300799},{"value":60,"timestamp":1e308}]}}]}}`,
		`{"data":{"metric_data":[{"type":"hr","object":{"values":[{"value":60,"timestamp":1772931600},{"value":62,"timestamp":1772931600}]}}]}}`,
		`{"data":{"metric_data":[{"type":"temp","object":{"unit":"°F","values":[{"value":"nan","timestamp":1772931600}]}},{"type":"temperature_deviation","object":{"value":"NaN"}}]}}`,
		`{"data":{"metric_data":[{"type":"steps","object":{"total":"Inf","day_start_timestamp":"NaN"}},{"type":"vo2_max","object":{"value":"+Inf"}}]}}`,
		`{"data":{"metric_data":[{"type":"glucose","object":{"unit":"mmol/L","values":[{"value":1e308,"timestamp":1772931600}]}}]}}`,
		`{"data":{"metric_data":[{"type":"hr","object":{"day_start_timestamp":1772974800}}]}}`,
		`{"data":{"metric_data":[{"type":"Sleep","object":{"bedtime_start":1772920800,"bedtime_end":1772949600,"sleep_stages":[{"type":"deep_sleep","stage_time":"Inf"},{"type":"rem_sleep","stage_time":1e300}],"quick_metrics":[{"type":"total_sleep","value":1e300}],"sleep_score":{"score":"NaN"},"spo2":{"value":"Inf"}}}]}}`,
		`{"data":{"metric_data":[{"type":"Sleep","object":{"bedtime_start":1772949600,"bedtime_end":1772920800,"sleep_graph":{"data":[{"type":"deep_sleep","start":1772949600,"end":1772920800}]}}}]}}`,
		`{"data":{"metric_data":[{"type":"Sleep","object":{"bedtime_start":"NaN","bedtime_end":1772949600,"sleep_graph":{"data":[{"type":"awake","start":"NaN","end":1772949600}]}}}]}}`,
		`{"data":{"metric_data":[{"type":"hr","object":{"values":[[[[[[[[[[{}]]]]]]]]]]}}]}}`,
	} {
		f.Add("2026-03-08", s)
	}
	f.Add("0001-01-01", `{"data":{"metric_data":[{"type":"vo2_max","object":{"value":45}}]}}`)
	f.Add("9999-12-31", `{"data":{"metric_data":[{"type":"vo2_max","object":{"value":45}}]}}`)
	f.Add("2026-02-30", `{"data":{"metric_data":[{"type":"vo2_max","object":{"value":45}}]}}`)

	c := New(Options{})
	f.Fuzz(func(t *testing.T, date, body string) {
		if !json.Valid([]byte(body)) {
			return
		}
		line, err := json.Marshal(rawDay{Date: date, Body: json.RawMessage(body)})
		if err != nil {
			return
		}
		var fields map[string]any
		if json.Unmarshal(line, &fields) != nil || fields == nil {
			return
		}
		raw := connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: DataTypeMetrics, Fields: fields}
		a, errA := c.Map(raw)
		b, errB := New(Options{}).Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
		}
		if errA != nil {
			return
		}
		for _, s := range a.QuantitySamples {
			if s.UserID != testUserID || s.Provider != Slug || s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" {
				t.Fatalf("sample without identity: %+v", s)
			}
			if s.StartDate.IsZero() || s.EndDate.IsZero() {
				t.Fatalf("sample at the zero time: %+v", s)
			}
		}
		for _, s := range a.SleepSessions {
			// Stage figures are copied as sent; validation parks an absurd one.
			if s.ContentHash == "" || s.ExternalID == "" || !s.BedtimeEnd.After(s.BedtimeStart) {
				t.Fatalf("broken sleep session: %+v", s)
			}
		}
		for _, p := range a.SleepPhases {
			if p.DurationS <= 0 || !p.EndDate.After(p.StartDate) || p.ParentContentHash == "" {
				t.Fatalf("broken sleep phase: %+v", p)
			}
		}
		for _, msg := range synthtest.Invariants(&a, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("Map emitted a broken record: %s", msg)
		}
		for _, r := range synthtest.Run(c, []synth.Line{{DataType: DataTypeMetrics, JSON: line}}, fuzzNow) {
			if r.Outcome == synthtest.Panic || (r.Outcome == synthtest.Stored && len(r.Broken) > 0) {
				t.Fatalf("pipeline: %s %s %v", r.Outcome, r.Detail, r.Broken)
			}
		}
	})
}

// FuzzUnixTime checks the timestamp reader: a time it accepts lies in
// 2015..2100.
func FuzzUnixTime(f *testing.F) {
	for _, v := range []float64{0, 1772931600, 1772931600123, 4102444800, 4102444801, -1, 1e308, math.NaN(), math.Inf(1)} {
		f.Add(v)
	}
	lo, hi := time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 1, 0, time.UTC)
	f.Fuzz(func(t *testing.T, v float64) {
		at, ok := unixTime(v)
		if ok && (at.Before(lo) || !at.Before(hi)) {
			t.Fatalf("unixTime(%v) = %v", v, at)
		}
	})
}
