// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"encoding/json"
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
//	go test -run='^$' -fuzz=^FuzzMap$ -fuzztime=60s ./connectors/fitbit/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzNow is the validation clock of the fuzz targets.
var fuzzNow = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map). Map must not panic, must be
// deterministic, must not emit a number that is not finite, and what it
// returns must carry its identity. What validation would store keeps the
// canonical model's invariants.
func FuzzMap(f *testing.F) {
	types := AllDataTypes
	for i, dt := range types {
		b, err := os.ReadFile("fitbitfake/testdata/" + dt + ".json")
		if err != nil {
			continue
		}
		var pts []json.RawMessage
		if err := json.Unmarshal(b, &pts); err != nil {
			f.Fatal(err)
		}
		for _, p := range pts {
			f.Add(uint8(i), []byte(p))
		}
	}
	for _, s := range []string{
		`{}`, `{"steps":null}`, `{"steps":{}}`, `{"steps":[]}`,
		`{"steps":{"interval":{"startTime":"2026-03-09T07:00:00Z","endTime":"2026-03-09T07:01:00Z","startUtcOffset":"NaNs"},"count":"NaN"}}`,
		`{"steps":{"interval":{"startTime":"2026-03-09T07:00:00Z","endTime":"2026-03-09T07:01:00Z","startUtcOffset":"Infs"},"count":"Inf"}}`,
		`{"steps":{"interval":{"startTime":"1970-01-01T00:00:00Z","endTime":"9999-12-31T23:59:59Z"},"count":1e308}}`,
		`{"steps":{"interval":{"startTime":"2026-03-09T07:00:00+14:00","endTime":"2026-03-09T07:01:00-12:00"},"count":"12"}}`,
		`{"steps":{"interval":{"startTime":"2026-03-09T07:01:00Z","endTime":"2026-03-09T07:00:00Z"},"count":"12"}}`,
		`{"steps":{"interval":{"startTime":"2026-03-09T07:00:00+23:59","endTime":"2026-03-09T07:01:00Z","startUtcOffset":"50399s"},"count":"-0"}}`,
		`{"steps":{"steps":{"steps":{"steps":{"steps":{}}}}}}`,
	} {
		f.Add(uint8(0), []byte(s))
	}
	f.Add(uint8(20), []byte(`{"sleep":{"interval":{"startTime":"2026-03-09T00:00:00Z","endTime":"2026-03-09T08:00:00Z"},"stages":[{"startTime":"2026-03-09T00:00:00Z","endTime":"2026-03-09T00:00:00Z","type":"DEEP"},{"startTime":"2026-03-09T00:00:00Z","endTime":"2026-03-09T00:00:00Z","type":"DEEP"}]}}`))
	f.Add(uint8(4), []byte(`{"exercise":{"interval":{"startTime":"2026-03-09T00:00:00Z","endTime":"2026-03-09T01:00:00Z"},"exerciseType":"RUNNING","metricsSummary":{"caloriesKcal":"NaN","distanceMillimeters":"Inf","averageHeartRateBeatsPerMinute":"1e400"},"activeDuration":"NaNs"}}`))

	c := New(Options{})
	f.Fuzz(func(t *testing.T, kind uint8, line []byte) {
		var fields map[string]any
		if json.Unmarshal(line, &fields) != nil || fields == nil {
			return
		}
		dt := types[int(kind)%len(types)]
		raw := connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dt, Fields: fields}
		a, errA := c.Map(raw)
		b, errB := New(Options{}).Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
		}
		if errA != nil {
			return
		}
		for _, msg := range synthtest.Invariants(&a, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)) {
			if !isOrderOrFuture(msg) {
				t.Fatalf("%s: %s", dt, msg)
			}
		}
		for _, s := range a.QuantitySamples {
			if s.ContentHash == "" || s.QuantityType == "" || s.Provider != Slug || s.StartDate.IsZero() {
				t.Fatalf("sample without identity: %+v", s)
			}
		}
		for _, s := range a.SleepSessions {
			if s.ContentHash == "" || s.ExternalID == "" || s.BedtimeStart.IsZero() {
				t.Fatalf("sleep without identity: %+v", s)
			}
		}
		for _, w := range a.Workouts {
			if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" || w.StartDate.IsZero() {
				t.Fatalf("workout without identity: %+v", w)
			}
		}
		for _, d := range a.DailySummaries {
			if d.ContentHash == "" || d.SummaryDate.IsZero() {
				t.Fatalf("summary without identity: %+v", d)
			}
		}
		if len(etl.Rejections(etl.ValidateMapResultAt(&a, fuzzNow))) == 0 {
			if broken := synthtest.Invariants(&a, fuzzNow); len(broken) > 0 {
				t.Fatalf("%s: validation would store %v", dt, broken)
			}
		}
	})
}

// isOrderOrFuture reports an invariant the input itself can break (an end
// before its start, a far future time), which validation refuses.
func isOrderOrFuture(msg string) bool {
	return strings.Contains(msg, "ends before it starts") || strings.Contains(msg, "is in the future")
}

// FuzzInbound feeds arbitrary delivery bodies to the notification reader.
// It must not panic, and every pull it asks for names a user and a pulled
// data type and spans a window that does not end before it starts.
func FuzzInbound(f *testing.F) {
	f.Add([]byte(upsertSteps))
	f.Add([]byte(`[` + upsertSteps + `,` + upsertSteps + `]`))
	f.Add([]byte(`{"healthUserId":"1","dataType":"steps","intervals":[{"physicalTimeInterval":{"startTime":"9999-12-31T23:59:59Z","endTime":"0001-01-01T00:00:00Z"}}]}`))
	f.Add([]byte(`{"healthUserId":"1","dataType":"sleep","intervals":[{"civilDateTimeInterval":{"startDateTime":{"date":{"year":2026,"month":2,"day":31}}}}]}`))
	f.Add([]byte(`[[[[[[[[]]]]]]]]`))
	f.Add([]byte(`{"data":null}`))
	c := New(Options{})
	c.now = func() time.Time { return fuzzNow }
	f.Fuzz(func(t *testing.T, body []byte) {
		msgs, err := parseNotifications(body)
		if err != nil {
			return
		}
		for _, e := range c.events(msgs) {
			if e.ProviderUserID == "" || e.DataType == "" {
				t.Fatalf("event without a user or type: %+v", e)
			}
			if _, ok := lookupDataType(e.DataType); !ok {
				t.Fatalf("event for a type not pulled: %q", e.DataType)
			}
			if e.From.IsZero() || e.To.IsZero() || e.To.Before(e.From) {
				t.Fatalf("bad window %v .. %v", e.From, e.To)
			}
		}
	})
}
