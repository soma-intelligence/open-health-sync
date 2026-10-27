// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/polar/polarfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMap -fuzztime=60s ./connectors/polar/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzDataTypes are the data types FuzzMap picks from by index, with the
// recorded answer that seeds each.
var fuzzDataTypes = []struct{ dataType, file string }{
	{DataTypeExercise, "exercises.json"},
	{DataTypeSleep, "sleep.json"},
	{DataTypeNightlyRecharge, "nightly_recharge.json"},
	{DataTypeActivity, "activities.json"},
	{DataTypeContinuousHR, "continuous_hr.json"},
	{DataTypePhysicalInfo, "physical_info.json"},
	{"unknown", ""},
}

// fixtureRecords returns the records of a recorded answer: the items of a
// top level array, of the one list an object wraps, or the object itself.
func fixtureRecords(f *testing.F, file string) []json.RawMessage {
	b := polarfake.File(file)
	var list []json.RawMessage
	if json.Unmarshal(b, &list) == nil {
		return list
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		f.Fatal(err)
	}
	for _, v := range obj {
		if json.Unmarshal(v, &list) == nil && len(obj) == 1 {
			return list
		}
	}
	return []json.RawMessage{b}
}

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map), then down the worker's path
// (synthtest.Run). Map must not panic, must be deterministic, must not emit
// a number that is not finite, and what validation would store keeps the
// canonical invariants: a duration that fits between its start and the
// validation clock, an end not before its start, an offset within ±14 h.
func FuzzMap(f *testing.F) {
	for i, d := range fuzzDataTypes {
		if d.file == "" {
			continue
		}
		for _, r := range fixtureRecords(f, d.file) {
			var buf bytes.Buffer
			if err := json.Compact(&buf, r); err != nil {
				f.Fatal(err)
			}
			f.Add(uint8(i), buf.Bytes())
		}
	}
	for _, s := range []string{
		`{}`, `{"id":null,"start_time":null}`, `{"id":"E","start_time":"2026-03-01T00:00:00"}`,
		`{"id":"E","start_time":"1970-01-01T00:00:00Z","duration":"PT0S"}`,
		`{"id":"E","start_time":"9999-12-31T23:59:59","duration":"P99999999999999999999D"}`,
		`{"id":"E","start_time":"2026-03-01T00:00:00+14:00","duration":"PT1.5.5S"}`,
		`{"id":"E","start_time":"2026-03-01T00:00:00","start_time_utc_offset":-720,"duration":"PT1H"}`,
		`{"id":"E","start_time":"2026-03-01T00:00:00","calories":1e308,"distance":-1,"heart_rate":{"average":"NaN"}}`,
		`{"id":"E","start_time":"2026-03-01T00:00:00","sport":[],"detailed_sport_info":{"a":{"b":[[[[]]]]}}}`,
		`{"date":"2026-03-04","sleep_start_time":"2026-03-04T07:00:00Z","sleep_end_time":"2026-03-03T23:00:00Z"}`,
		`{"date":"2026-03-04","sleep_start_time":"2026-03-03T23:00:00","sleep_end_time":"2026-03-04T07:00:00","deep_sleep":9223372036854774784,"rem_sleep":9223372036854774784,"light_sleep":5648}`,
		`{"date":"2026-03-04","utc_offset_minutes":-720,"heart_rate_samples":[{"heart_rate":60,"sample_time":"23:59:59"},{"heart_rate":60,"sample_time":"23:59:59"}]}`,
		`{"date":"2026-03-04","heart_rate_variability_avg":1e308,"breathing_rate_avg":-1}`,
		`{"start_time":"2026-03-04T00:00:00","active_duration":"PT99999999999999H","steps":1e308}`,
		`{"weight":1e308,"height":0,"created":"0001-01-01T00:00:00"}`,
	} {
		for i := range fuzzDataTypes {
			f.Add(uint8(i), []byte(s))
		}
	}
	f.Fuzz(func(t *testing.T, kind uint8, line []byte) {
		fuzzMap(t, fuzzDataTypes[int(kind)%len(fuzzDataTypes)].dataType, line, fakeNow)
	})
}

// fuzzMap checks what Map owes on any line of dataType.
func fuzzMap(t *testing.T, dataType string, line []byte, now time.Time) {
	var fields map[string]any
	if json.Unmarshal(line, &fields) != nil || fields == nil {
		return
	}
	raw := connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields}
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
	offsets := []map[string]any{}
	for _, w := range a.Workouts {
		if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" || w.Provider != Slug {
			t.Fatalf("workout without identity: %+v", w)
		}
		offsets = append(offsets, w.Metadata)
	}
	for _, s := range a.QuantitySamples {
		if s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" || s.Provider != Slug {
			t.Fatalf("sample without identity: %+v", s)
		}
		offsets = append(offsets, s.Metadata)
	}
	for _, s := range a.SleepSessions {
		if s.ContentHash == "" || s.ExternalID == "" {
			t.Fatalf("sleep without identity: %+v", s)
		}
		offsets = append(offsets, s.Metadata)
	}
	for _, p := range a.SleepPhases {
		if p.ParentContentHash == "" || p.Phase == "" || !p.EndDate.After(p.StartDate) {
			t.Fatalf("phase without identity or span: %+v", p)
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
		for _, md := range offsets {
			if m, ok := md["utc_offset_minutes"].(int); ok && (m > 14*60 || m < -14*60) {
				t.Fatalf("stored a record with a UTC offset of %d minutes", m)
			}
		}
	}
}

// FuzzHandleInbound feeds arbitrary bodies, signed or not, to the webhook
// route. It must not panic; an unsigned event is refused; what it returns
// names a user and a window that is not empty and not in the future.
func FuzzHandleInbound(f *testing.F) {
	for _, s := range []string{
		`{"event":"EXERCISE","user_id":10101010,"entity_id":"EXC0001","timestamp":"2026-03-05T09:00:00Z","url":"https://x"}`,
		`{"event":"SLEEP","user_id":"10101010","date":"2026-03-04"}`,
		`{"event":"PING"}`, `{"event":"CONTINUOUS_HEART_RATE","user_id":1,"date":"9999-12-31","timestamp":"0001-01-01T00:00:00"}`,
		`{"event":"ACTIVITY_SUMMARY","user_id":1e400}`, `{"event":"","user_id":1}`, `[]`, `null`, `{} {}`,
	} {
		f.Add([]byte(s), true)
	}
	c := New(Options{ClientID: testClientID, ClientSecret: testClientSecret, WebhookSecret: testWebhookSecret, Now: func() time.Time { return fakeNow }})
	f.Fuzz(func(t *testing.T, body []byte, signed bool) {
		r := httptest.NewRequest(http.MethodPost, "/inbound/polar", bytes.NewReader(body))
		if signed {
			r.Header.Set(HeaderSignature, Signature(testWebhookSecret, body))
		} else {
			r.Header.Set(HeaderSignature, Signature("not-the-secret", body))
		}
		events, err := c.HandleInbound(context.Background(), r)
		if err != nil {
			return
		}
		if !signed && len(events) > 0 {
			t.Fatal("an unsigned event was acted on")
		}
		for _, e := range events {
			if e.ProviderUserID == "" || e.DataType == "" || !e.From.Before(e.To) || e.To.After(fakeNow) {
				t.Fatalf("event without a user, type or window: %+v", e)
			}
		}
	})
}

// FuzzDecodeList reads arbitrary answers as a list endpoint does. It must not
// panic, and what it returns are JSON values.
func FuzzDecodeList(f *testing.F) {
	for _, d := range fuzzDataTypes {
		if d.file != "" {
			f.Add(polarfake.File(d.file), "nights")
		}
	}
	for _, s := range []string{`[]`, `null`, `{}`, `{"nights":null}`, `{"nights":[null,1,"a"]}`, `{"nights":{}}`, `[[[[[]]]]]`} {
		f.Add([]byte(s), "nights")
	}
	f.Fuzz(func(t *testing.T, body []byte, key string) {
		items, err := decodeList(body, key)
		if err != nil {
			return
		}
		for _, it := range items {
			if !json.Valid(it) {
				t.Fatalf("item is not JSON: %q", it)
			}
		}
	})
}
