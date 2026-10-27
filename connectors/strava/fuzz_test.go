// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/strava/stravafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMap -fuzztime=60s ./connectors/strava/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzDataTypes are the data types FuzzMap picks from by index.
var fuzzDataTypes = []string{DataTypeActivity, DataTypeEvent, "unknown"}

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map), then down the worker's path
// (synthtest.Run). Map must not panic, must be deterministic, must not emit
// a number that is not finite, and what validation would store keeps the
// canonical invariants: a duration that fits between its start and the
// validation clock, an end not before its start.
func FuzzMap(f *testing.F) {
	for _, a := range stravafake.Activities() {
		f.Add(uint8(0), []byte(a))
	}
	f.Add(uint8(1), []byte(deleteEvent))
	f.Add(uint8(1), []byte(createEvent))
	for _, s := range []string{
		`{}`, `{"id":null,"start_date":null}`, `{"id":"1","start_date":"2026-03-01T00:00:00Z"}`,
		`{"id":1,"start_date":"1970-01-01T00:00:00Z","elapsed_time":0}`,
		`{"id":1,"start_date":"9999-12-31T23:59:59Z","elapsed_time":1}`,
		`{"id":1,"start_date":"2026-03-01T00:00:00+14:00","elapsed_time":-5,"moving_time":-1}`,
		`{"id":1,"start_date":"2026-03-01T00:00:00-12:00","elapsed_time":9223372036,"moving_time":3600}`,
		`{"id":1,"start_date":"2026-03-01T00:00:00Z","distance":1e308,"average_speed":-1e308,"kilojoules":1e308,"calories":0}`,
		`{"id":1e30,"start_date":"2026-03-01T00:00:00Z"}`, `{"id":-1,"start_date":"2026-03-01T00:00:00Z","timezone":"(GMT+23:59) Nowhere/Else"}`,
		`{"id":1,"start_date":"2026-03-01T00:00:00Z","sport_type":[],"type":{"a":{"b":{"c":[[[[]]]]}}}}`,
		`{"object_type":"activity","aspect_type":"delete","object_id":0}`,
		`{"object_type":"activity","aspect_type":"delete","object_id":"NaN"}`,
	} {
		f.Add(uint8(0), []byte(s))
	}
	now := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, kind uint8, line []byte) {
		dt := fuzzDataTypes[int(kind)%len(fuzzDataTypes)]
		fuzzMap(t, dt, line, now)
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
		if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" || w.Provider != Slug {
			t.Fatalf("workout without identity: %+v", w)
		}
	}
	for _, d := range a.Deletions {
		if d.ExternalID == "" || d.Provider != Slug {
			t.Fatalf("deletion without id: %+v", d)
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

// FuzzHandleInbound feeds arbitrary event bodies to the push route. It must
// not panic; an event it acts on names a user, and a stored delete line maps
// to exactly one deletion.
func FuzzHandleInbound(f *testing.F) {
	for _, s := range []string{createEvent, updateEvent, deleteEvent,
		`{"aspect_type":"update","object_type":"athlete","owner_id":1,"subscription_id":4242,"updates":{"authorized":"false"}}`,
		`{"aspect_type":"delete","object_type":"activity","owner_id":"1","object_id":"x","subscription_id":4242}`,
		`{"aspect_type":"delete","object_type":"activity","owner_id":1e400,"object_id":1,"subscription_id":4242}`,
		`{} {}`, `[]`, `null`, ``,
	} {
		f.Add([]byte(s), true)
	}
	f.Fuzz(func(t *testing.T, body []byte, withSubscription bool) {
		sub := ""
		if withSubscription {
			sub = "4242"
		}
		c := inboundConnector(sub)
		r := httptest.NewRequest(http.MethodPost, "/inbound/strava", strings.NewReader(string(body)))
		events, err := c.HandleInbound(context.Background(), r)
		if err != nil {
			return
		}
		for _, e := range events {
			if e.ProviderUserID == "" || e.DataType == "" {
				t.Fatalf("event without a user or type: %+v", e)
			}
			if e.Lines == nil && !e.From.Before(e.To) {
				t.Fatalf("pull with an empty window: %+v", e)
			}
			for _, l := range e.Lines {
				var fields map[string]any
				if err := json.Unmarshal(l, &fields); err != nil {
					t.Fatalf("stored line is not a JSON object: %q", l)
				}
				res, err := c.Map(connector.RawRecord{UserID: "u", Provider: Slug, DataType: e.DataType, Fields: fields})
				if err != nil || len(res.Deletions) != 1 {
					t.Fatalf("a stored delete does not map to one deletion: %v %+v", err, res)
				}
			}
		}
	})
}

// FuzzFetchPage serves an arbitrary body as the activities page. Fetch must
// not panic or loop, and every line it emits is one line of compact JSON.
func FuzzFetchPage(f *testing.F) {
	f.Add(stravafake.File("activities.json"))
	for _, s := range []string{`[]`, `null`, `{}`, `[null]`, `[1,"a",[]]`, `[{"start_date":"bad"}]`, `[{"start_date":"2026-03-01T00:00:00Z"},{"start_date":"2026-03-01T00:00:00Z"}]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), PageDelay: -1, PerPage: 200,
			Now:   func() time.Time { return fakeNow },
			Sleep: func(context.Context, time.Duration) error { return nil }})
		var sink recordingSink
		from := fakeNow.AddDate(0, 0, -7)
		if err := c.Fetch(context.Background(), conn(), from, fakeNow, &sink); err != nil {
			return
		}
		for _, l := range sink.lines() {
			if strings.ContainsAny(string(l), "\n\r") || !json.Valid(l) {
				t.Fatalf("emitted line is not one line of JSON: %q", l)
			}
		}
	})
}

