// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/intervalsicu/icufake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMap -fuzztime=60s ./connectors/intervalsicu/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzDataTypes are the data types FuzzMap picks from by index.
var fuzzDataTypes = []string{DataTypeActivity, DataTypeWellness, "unknown"}

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map), then down the worker's path
// (synthtest.Run). Map must not panic, must be deterministic, must not emit
// a number that is not finite, and what validation would store keeps the
// canonical invariants: a duration that fits between its start and the
// validation clock, an end not before its start.
func FuzzMap(f *testing.F) {
	for i, file := range []string{"activities.json", "wellness.json"} {
		for _, r := range icufake.List(file) {
			f.Add(uint8(i), []byte(r))
		}
	}
	for _, s := range []string{
		`{}`, `{"id":null,"start_date":null}`, `{"id":"i1","start_date":"2026-03-01T00:00:00Z"}`,
		`{"id":"i1","start_date":"1970-01-01T00:00:00Z","elapsed_time":0}`,
		`{"id":"i1","start_date":"9999-12-31T23:59:59Z","elapsed_time":1}`,
		`{"id":"i1","start_date_local":"2026-03-01T00:00:00","timezone":"Pacific/Kiritimati","elapsed_time":-5,"moving_time":-1}`,
		`{"id":"i1","start_date_local":"2026-03-01T00:00:00","timezone":"Etc/GMT+12"}`,
		`{"id":1e30,"start_date":"2026-03-01T00:00:00-12:00","elapsed_time":9223372036,"moving_time":3600}`,
		`{"id":"i1","start_date":"2026-03-01T00:00:00Z","elapsed_time":36028797018967568}`,
		`{"id":"i1","start_date":"2026-03-01T00:00:00Z","distance":1e308,"average_speed":-1e308,"joules":1e308,"calories":0}`,
		`{"id":"i1","start_date":"2026-03-01T00:00:00Z","type":[],"source":{"a":{"b":[[[[]]]]}}}`,
		`{"id":"i1","source":"STRAVA"}`,
		`{"id":"2026-03-01","weight":1e308,"spO2":0.5,"bodyFat":1,"steps":-1,"restingHR":"NaN"}`,
		`{"id":"0000-01-01","hrv":10}`, `{"id":"9999-12-31","hrv":10}`, `{"id":"2026-02-30","hrv":10}`,
	} {
		f.Add(uint8(0), []byte(s))
		f.Add(uint8(1), []byte(s))
	}
	f.Fuzz(func(t *testing.T, kind uint8, line []byte) {
		fuzzMap(t, fuzzDataTypes[int(kind)%len(fuzzDataTypes)], line, fakeNow)
	})
}

// fuzzMap checks what Map owes on any line of dataType.
func fuzzMap(t *testing.T, dataType string, line []byte, now time.Time) {
	var fields map[string]any
	if json.Unmarshal(line, &fields) != nil || fields == nil {
		return
	}
	raw := connector.RawRecord{UserID: "u", Provider: Slug, DataType: dataType, Fields: fields}
	a, errA := New(Options{}).Map(raw)
	b, errB := New(Options{}).Map(raw)
	if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
		t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
	}
	if errA != nil {
		return
	}
	for _, msg := range synthtest.Invariants(&a, time.Unix(1<<40, 0)) {
		if strings.Contains(msg, "not finite") {
			t.Fatalf("Map emitted %s", msg)
		}
	}
	for _, w := range a.Workouts {
		if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" || w.Provider != Slug || w.StartDate.IsZero() {
			t.Fatalf("workout without identity: %+v", w)
		}
	}
	for _, s := range a.QuantitySamples {
		if s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" || s.Provider != Slug || s.StartDate.IsZero() {
			t.Fatalf("sample without identity: %+v", s)
		}
	}
	for _, r := range synthtest.Run(New(Options{}), []synth.Line{{DataType: dataType, JSON: line}}, now) {
		if r.Outcome == synthtest.Panic {
			t.Fatalf("mapper panicked: %s", r.Detail)
		}
		if r.Outcome != synthtest.Stored {
			continue
		}
		if len(r.Broken) > 0 {
			t.Fatalf("stored a record breaking %v", r.Broken)
		}
		for _, w := range a.Workouts {
			if w.DurationS != nil && float64(*w.DurationS) > now.Add(48*time.Hour).Sub(w.StartDate).Seconds() {
				t.Fatalf("stored a workout of %d s starting %s", *w.DurationS, w.StartDate)
			}
		}
	}
}

// FuzzHandleInbound feeds arbitrary webhook bodies to the push route. It must
// not panic; a body without the secret is refused; what it returns names a
// user, a data type and a window that is not empty.
func FuzzHandleInbound(f *testing.F) {
	for _, s := range []string{
		`{"secret":"test-webhook-secret","events":[{"athlete_id":"i900001","type":"ACTIVITY_UPLOADED","activity":{"id":"i1001","start_date":"2026-03-01T07:00:00Z"}}]}`,
		`{"secret":"test-webhook-secret","events":[{"athlete_id":900001,"type":"WELLNESS_UPDATED","wellness":{"id":"2026-03-01"}},{"athlete_id":900001,"type":"WELLNESS_UPDATED","wellness":{"id":"2026-03-05"}}]}`,
		`{"secret":"test-webhook-secret","events":[{"athlete_id":"i1","type":"ACTIVITY_DELETED","activity":{"start_date_local":"0001-01-01T00:00:00"}}]}`,
		`{"secret":"test-webhook-secret","events":[{"athlete_id":"i1","type":"ACTIVITY_X","activity":{"start_date":"9999-12-31T23:59:59Z"}}]}`,
		`{"secret":"test-webhook-secret","events":[{"athlete_id":"","type":"WELLNESS_UPDATED"}]}`,
		`{"secret":"wrong","events":[]}`, `{"events":null}`, `[]`, `null`, `{} {}`,
	} {
		f.Add([]byte(s))
	}
	c := New(Options{WebhookSecret: testWebhookSecret, Now: func() time.Time { return fakeNow }})
	f.Fuzz(func(t *testing.T, body []byte) {
		r := httptest.NewRequest(http.MethodPost, "/inbound/intervals_icu", strings.NewReader(string(body)))
		events, err := c.HandleInbound(context.Background(), r)
		if err != nil {
			return
		}
		if !strings.Contains(string(body), testWebhookSecret) {
			t.Fatal("a body without the secret was accepted")
		}
		for _, e := range events {
			if e.ProviderUserID == "" || e.DataType == "" || !e.From.Before(e.To) {
				t.Fatalf("event without a user, type or window: %+v", e)
			}
		}
	})
}

// FuzzFetchList serves an arbitrary body as every list answer. Fetch must
// not panic or loop, and every line it emits is one line of compact JSON.
func FuzzFetchList(f *testing.F) {
	f.Add(icufake.File("activities.json"))
	f.Add(icufake.File("wellness.json"))
	for _, s := range []string{`[]`, `null`, `{}`, `[null]`, `[1,"a",[]]`, `[{"id":"2026-03-01"},{"id":"2026-03-01"}]`, `[{"start_date":"bad","start_date_local":"bad"}]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), Now: func() time.Time { return fakeNow },
			Sleep: func(context.Context, time.Duration) error { return nil }})
		sink := &recordingSink{}
		if err := c.Fetch(context.Background(), testConn(), fakeNow.AddDate(0, 0, -3), fakeNow, sink); err != nil {
			return
		}
		for _, call := range sink.calls {
			for _, l := range call.Lines {
				if strings.ContainsAny(string(l), "\n\r") || !json.Valid(l) {
					t.Fatalf("emitted line is not one line of JSON: %q", l)
				}
			}
		}
	})
}
