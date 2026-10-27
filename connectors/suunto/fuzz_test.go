// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/suunto/suuntofake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMapWorkout -fuzztime=60s ./connectors/suunto/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzFar is a validation clock past any time a line can name, so the
// invariants checked on the raw Map output leave the future to validation.
var fuzzFar = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// FuzzMapWorkout feeds arbitrary workout lines to Map the way the ETL hands
// them over (decoded into a map). Map must not panic, must be deterministic,
// must emit only finite numbers and a workout with an identity and a start,
// and a line validation would store keeps the canonical invariants.
func FuzzMapWorkout(f *testing.F) {
	for _, w := range suuntofake.Workouts() {
		f.Add(string(w))
	}
	for _, s := range []string{
		`{}`, `{"workoutKey":null,"workoutId":null,"startTime":null}`,
		`{"workoutKey":"k","startTime":0}`, `{"workoutKey":"k","startTime":-1}`,
		`{"workoutKey":"k","startTime":253402300799000,"stopTime":253402300799999}`,
		`{"workoutKey":"k","startTime":9223372036854775807,"totalTime":9999999}`,
		`{"workoutKey":"k","startTime":1772442000000,"stopTime":1772440000000,"totalTime":-5}`,
		`{"workoutKey":"k","startTime":1772442000000,"totalTime":1e308,"totalDistance":1e308,"energyConsumption":-1e308}`,
		`{"workoutKey":"k","startTime":1772442000000,"timeOffsetInMinutes":1440,"hrdata":{"workoutAvgHR":"NaN"}}`,
		`{"workoutKey":"k","startTime":1772442000000,"timeOffsetInMinutes":-720,"hrdata":{"workoutAvgHR":301,"avg":0,"hrmax":1e308,"min":-1}}`,
		`{"workoutId":12345678901234567890123,"startTime":1772442000000,"activityId":-1}`,
		`{"workoutId":1.5e3,"startTime":1772442000000,"gear":{"name":{"a":[[[[[]]]]]}}}`,
		`{"workoutKey":"k","startTime":"1772442000000"}`, `{"workoutKey":"k","startTime":1.5}`,
		`{"workoutKey":"","workoutId":"","startTime":1772442000000}`,
		`{"workoutKey":"k","startTime":1772442000000,"hrdata":[],"gear":[]}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		fuzzMap(t, DataTypeWorkout, line)
	})
}

// fuzzMap maps line as the ETL would and checks what a mapper must always
// hold.
func fuzzMap(t *testing.T, dataType, line string) {
	var fields map[string]any
	if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
		return
	}
	raw := connector.RawRecord{UserID: testUser, Provider: Slug, DataType: dataType, Fields: fields}
	a, errA := New(Options{}).Map(raw)
	b, errB := New(Options{}).Map(raw)
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
	for _, w := range a.Workouts {
		if w.UserID != testUser || w.Provider != Slug || w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" {
			t.Fatalf("workout without identity: %+v", w)
		}
		if w.StartDate.IsZero() || w.EndDate.IsZero() {
			t.Fatalf("workout without a start or end: %+v", w)
		}
		if w.DurationS != nil && *w.DurationS < 0 {
			t.Fatalf("negative duration %d", *w.DurationS)
		}
	}
	for _, r := range synthtest.Run(New(Options{}), []synth.Line{{DataType: dataType, JSON: []byte(line)}}, fakeNow) {
		if r.Outcome == synthtest.Stored && len(r.Broken) > 0 {
			t.Fatalf("stored a record breaking %v", r.Broken)
		}
	}
}

// FuzzFetchLines serves an arbitrary answer to the workout list and to a
// 24/7 pull. Neither may panic, and every line kept for the raw store is one
// JSON object on one line.
func FuzzFetchLines(f *testing.F) {
	f.Add([]byte(`{"error":null,"payload":` + string(suuntofake.File("workouts.json")) + `}`))
	for _, name := range []string{"workouts.json", "sleep.json", "recovery.json", "daily_activity.json"} {
		f.Add(suuntofake.File(name))
	}
	for _, s := range []string{`[]`, `null`, `[null]`, `{"error":null,"payload":[null]}`, `{"payload":[{},{}]}`,
		`[{"workoutKey":"a"},{"workoutKey":"a"}]`, `[{"startTime":"x"},{"startTime":1e400}]`, `[1,"x",true]`, `"x"`, `{"error":{}}`} {
		f.Add([]byte(s))
	}
	var answer atomic.Value
	answer.Store([]byte(nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(answer.Load().([]byte))
	}))
	f.Cleanup(srv.Close)
	c := New(Options{OAuthBase: srv.URL, APIBase: srv.URL, HTTPClient: srv.Client(),
		ClientID: "i", ClientSecret: "s", SubscriptionKey: "k",
		PageDelay: -1, PerPage: 1 << 20, Now: func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil }})
	from := fakeNow.Add(-24 * time.Hour)
	f.Fuzz(func(t *testing.T, body []byte) {
		answer.Store(body)
		check := func(lines []dayLine) {
			for _, l := range lines {
				if bytes.ContainsAny(l.line, "\n\r") || !json.Valid(l.line) || len(l.line) == 0 || l.line[0] != '{' {
					t.Fatalf("stored line is not one JSON object on one line: %q", l.line)
				}
			}
		}
		if lines, err := c.fetchWorkouts(context.Background(), "tok", from, fakeNow); err == nil {
			check(lines)
		}
		if lines, err := c.fetch247(context.Background(), "tok", DataTypeSleep, from, fakeNow); err == nil {
			check(lines)
		}
	})
}

// FuzzHandleInbound feeds arbitrary bodies, signed or not, to the
// notification handler. It must not panic, must refuse an unsigned body, and
// every event names a user, a known data type and a window that ends at now
// and starts no more than InboundLookback before it.
func FuzzHandleInbound(f *testing.F) {
	f.Add([]byte(`{"type":"WORKOUT_CREATED","username":"jane","workout":{"workoutKey":"k","startTime":1772539200000}}`), true)
	f.Add([]byte(`{"type":"WORKOUT_CREATED","username":"jane","workout":{"startTime":-9223372036854775808}}`), true)
	f.Add([]byte(`{"type":"WORKOUT_CREATED","username":"jane","workout":{"startTime":1e400}}`), true)
	f.Add([]byte(`username=jane&workoutid=900001`), true)
	f.Add([]byte(`{"type":"suunto_247_sleep_created","username":" jane "}`), true)
	f.Add([]byte(`{"username":"","workoutid":null}`), true)
	f.Add([]byte(`[]`), true)
	f.Add([]byte(`%zz`), true)
	f.Add([]byte(`{"type":"WORKOUT_CREATED","username":"jane"}`), false)

	c := testConnector()
	f.Fuzz(func(t *testing.T, body []byte, signed bool) {
		r := httptest.NewRequest(http.MethodPost, "/inbound/suunto", bytes.NewReader(body))
		if signed {
			r.Header.Set(HeaderSignature, Signature(testNotificationSecret, body))
		} else {
			r.Header.Set(HeaderSignature, Signature("not-the-secret", body))
		}
		events, err := c.HandleInbound(context.Background(), r)
		if !signed && err == nil {
			t.Fatal("an unsigned notification was accepted")
		}
		if err != nil {
			return
		}
		for _, e := range events {
			if strings.TrimSpace(e.ProviderUserID) == "" || e.ProviderUserID != strings.TrimSpace(e.ProviderUserID) {
				t.Fatalf("event user %q", e.ProviderUserID)
			}
			switch e.DataType {
			case DataTypeWorkout, DataTypeSleep, DataTypeRecovery, DataTypeDailyActivity:
			default:
				t.Fatalf("event of data type %q", e.DataType)
			}
			if !e.To.Equal(fakeNow) || e.From.After(e.To) || e.From.Before(fakeNow.Add(-InboundLookback)) {
				t.Fatalf("event window %v to %v", e.From, e.To)
			}
		}
	})
}
