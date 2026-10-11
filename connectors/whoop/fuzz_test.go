// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// fuzzDataTypes are the data types FuzzMap picks from by index.
var fuzzDataTypes = []string{DataTypeCycle, DataTypeRecovery, DataTypeSleep, DataTypeWorkout, DataTypeBody, DataTypeWebhook, "unknown"}

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map). Map must not panic, must be
// deterministic, and what it returns must be writable: every record has an
// identity, every deletion an id.
func FuzzMap(f *testing.F) {
	for i, file := range []string{"cycles.json", "recoveries.json", "sleeps.json", "workouts.json"} {
		var all struct {
			Records []json.RawMessage `json:"records"`
		}
		if err := json.Unmarshal(whoopfake.File(file), &all); err != nil {
			f.Fatal(err)
		}
		for _, r := range all.Records {
			f.Add(uint8(i), []byte(r))
		}
	}
	f.Add(uint8(4), []byte(`{"height_meter":1.8,"weight_kilogram":75.5,"max_heart_rate":190,"snapshot_date":"2026-03-03"}`))
	f.Add(uint8(5), []byte(`{"user_id":10001,"id":"aaaaaaaa-0000-4000-8000-000000000001","type":"recovery.deleted","trace_id":"t"}`))
	f.Add(uint8(0), []byte(`{"id":1,"start":"2026-03-01T00:00:00Z","timezone_offset":"+05:45","score_state":"SCORED","score":{"strain":1e308},"step_count":-0}`))
	f.Add(uint8(2), []byte(`{"id":"s","start":"2026-03-01T00:00:00Z","end":"2026-02-01T00:00:00Z","timezone_offset":"-+1:00","score_state":"SCORED","score":{"stage_summary":{"total_rem_sleep_time_milli":-9223372036854775808}}}`))

	c := New(Options{})
	f.Fuzz(func(t *testing.T, kind uint8, line []byte) {
		var fields map[string]any
		if json.Unmarshal(line, &fields) != nil || fields == nil {
			return
		}
		raw := connector.RawRecord{UserID: "u", Provider: Slug, DataType: fuzzDataTypes[int(kind)%len(fuzzDataTypes)], Fields: fields}
		a, errA := c.Map(raw)
		b, errB := c.Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
		}
		if errA != nil {
			return
		}
		for _, s := range a.QuantitySamples {
			if s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" || s.Provider != Slug {
				t.Fatalf("sample without identity: %+v", s)
			}
		}
		for _, s := range a.SleepSessions {
			if s.ContentHash == "" || s.ExternalID == "" {
				t.Fatalf("sleep without identity: %+v", s)
			}
		}
		for _, w := range a.Workouts {
			if w.ContentHash == "" || w.ExternalID == "" || w.ActivityType == "" {
				t.Fatalf("workout without identity: %+v", w)
			}
		}
		for _, d := range a.DailySummaries {
			if d.ContentHash == "" || d.SummaryDate.IsZero() || d.SummaryDate.Location() != time.UTC {
				t.Fatalf("summary without identity: %+v", d)
			}
		}
		for _, d := range a.Deletions {
			if d.ExternalID == "" {
				t.Fatal("deletion without id")
			}
		}
	})
}

// FuzzMapTimezoneOffset checks the offset reader on arbitrary strings: a
// reading is within ±14 h with the sign written, and a zone named for it has
// exactly that offset at the instant asked.
func FuzzMapTimezoneOffset(f *testing.F) {
	for _, s := range []string{"+01:00", "-03:30", "+05:45", "Z", "+-1:00", "+14:00", "-12:00", "+1:00", "+05:30"} {
		f.Add(s, int64(1772323200))
	}
	f.Fuzz(func(t *testing.T, s string, unix int64) {
		at := time.Unix(unix%(1<<34), 0)
		secs, ok := offsetSeconds(s)
		if !ok {
			if ianaForOffset(s, at) != "" {
				t.Fatalf("unreadable offset %q named a zone", s)
			}
			return
		}
		if secs > 14*3600 || secs < -14*3600 || (secs != 0 && (secs < 0) != (s[0] == '-')) {
			t.Fatalf("offset %q read as %d seconds", s, secs)
		}
		if name := ianaForOffset(s, at); name != "" {
			loc, err := time.LoadLocation(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, off := at.In(loc).Zone(); off != secs {
				t.Fatalf("%q at %v named %s with offset %d", s, at, name, off)
			}
		}
	})
}

// FuzzHandleInbound feeds arbitrary bodies, signed or not, to the webhook
// handler. It must not panic; an unsigned body is always refused; what it
// returns names a user, and stored lines are single line JSON objects.
func FuzzHandleInbound(f *testing.F) {
	f.Add([]byte(event(EventSleepUpdated)), true, int64(0))
	f.Add([]byte(event(EventWorkoutDeleted)), true, int64(-1000))
	f.Add([]byte(event(EventRecoveryDeleted)), false, int64(0))
	f.Add([]byte("{\n\"user_id\":1,\"id\":2,\"type\":\"sleep.deleted\"}"), true, int64(400000))
	f.Add([]byte(`{"user_id":"x","type":"x.y"}`), true, int64(0))
	f.Add([]byte(`[]`), true, int64(0))

	c := inboundConnector()
	f.Fuzz(func(t *testing.T, body []byte, signed bool, skewMillis int64) {
		ts := strconv.FormatInt(inboundNow.UnixMilli()+skewMillis, 10)
		r := httptest.NewRequest(http.MethodPost, "/inbound/whoop", bytes.NewReader(body))
		r.Header.Set(HeaderTimestamp, ts)
		if signed {
			r.Header.Set(HeaderSignature, Sign(testSecret, ts, body))
		} else {
			r.Header.Set(HeaderSignature, Sign("not-the-secret", ts, body))
		}
		events, err := c.HandleInbound(context.Background(), r)
		if !signed && err == nil {
			t.Fatal("an unsigned delivery was accepted")
		}
		if err != nil {
			return
		}
		for _, e := range events {
			if e.ProviderUserID == "" {
				t.Fatal("event without a user")
			}
			for _, l := range e.Lines {
				if bytes.ContainsAny(l, "\n\r") || !json.Valid(l) || len(l) == 0 || l[0] != '{' {
					t.Fatalf("stored line is not one JSON object on one line: %q", l)
				}
				var fields map[string]any
				if err := json.Unmarshal(l, &fields); err != nil {
					t.Fatal(err)
				}
				res, err := c.Map(connector.RawRecord{UserID: "u", Provider: Slug, DataType: e.DataType, Fields: fields})
				if err != nil || len(res.Deletions) != 1 {
					t.Fatalf("a stored deletion does not map to one deletion: %v %+v", err, res)
				}
			}
		}
	})
}
