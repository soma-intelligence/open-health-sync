// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/withings/withingsfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMap -fuzztime=60s ./connectors/withings/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzDataTypes are the data types FuzzMap picks from by index.
var fuzzDataTypes = []string{DataTypeMeasure, DataTypeActivity, DataTypeIntraday, DataTypeSleep, DataTypeWorkout, DataTypeECG, "unknown"}

// fuzzFar is a validation clock past any time a line can name, so the
// invariants checked on the raw Map output leave the future to validation.
var fuzzFar = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// fixtureLines returns the items of the recorded answers, by data type.
func fixtureLines(f *testing.F) map[string][][]byte {
	out := map[string][][]byte{}
	for _, s := range []struct{ dt, file, key string }{
		{DataTypeMeasure, "measure_page1.json", "measuregrps"},
		{DataTypeMeasure, "measure_page2.json", "measuregrps"},
		{DataTypeActivity, "activity.json", "activities"},
		{DataTypeSleep, "sleep_summary.json", "series"},
		{DataTypeWorkout, "workouts.json", "series"},
		{DataTypeECG, "heart_list.json", "series"},
	} {
		var p struct {
			Body map[string]json.RawMessage `json:"body"`
		}
		var items []json.RawMessage
		if err := json.Unmarshal(withingsfake.File(s.file), &p); err != nil {
			f.Fatal(err)
		}
		if err := json.Unmarshal(p.Body[s.key], &items); err != nil {
			f.Fatal(s.file, err)
		}
		for _, it := range items {
			out[s.dt] = append(out[s.dt], it)
		}
	}
	return out
}

// fetchedLines pulls every data type from the fake and returns the lines the
// raw store would keep (enriched as Fetch enriches them), by data type. A
// failed pull (a busy machine refusing local connections) gives no line:
// the fixtures still seed.
func fetchedLines(f *testing.F) map[string][][]byte {
	srv := withingsfake.New(f)
	c := New(Options{
		ClientID: "test-client-id", ClientSecret: "test-client-secret",
		AccountBase: srv.URL(), APIBase: srv.URL(), HTTPClient: srv.Client(),
		ChunkDelay: -1, PageDelay: -1,
		Now:   func() time.Time { return fakeNow },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	sink := &recordingSink{}
	if err := c.Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink); err != nil {
		f.Logf("fetch seeds skipped: %v", err)
		return nil
	}
	out := map[string][][]byte{}
	for _, dt := range AllDataTypes {
		out[dt] = sink.linesOf(dt)
	}
	return out
}

// FuzzMap feeds arbitrary lines of every data type to Map, the way the ETL
// hands them over (decoded into a map). Map must not panic or hang, must be
// deterministic, must emit only finite numbers and records with an identity,
// and a line validation would store keeps the canonical invariants.
func FuzzMap(f *testing.F) {
	for _, lines := range []map[string][][]byte{fixtureLines(f), fetchedLines(f)} {
		for i, dt := range fuzzDataTypes {
			for _, l := range lines[dt] {
				f.Add(uint8(i), string(l))
			}
		}
	}
	for _, s := range []struct {
		dt   int
		line string
	}{
		{0, `{"date":1772359200,"measures":[{"value":7234,"type":1,"unit":-2},{"value":1,"type":1,"unit":400}]}`},
		{0, `{"date":1772359200,"measures":[{"value":7234,"type":1,"unit":-400}]}`},
		{0, `{"date":0,"category":1,"measures":[{"value":-9223372036854775808,"type":9,"unit":0}]}`},
		{0, `{"date":253402300799,"timezone":"+14:00","measures":[]}`},
		{0, `{"date":null,"measures":null}`},
		{1, `{"date":"2026-03-01","timezone":"Pacific/Kiritimati","steps":1e308,"distance":-1,"calories":null}`},
		{1, `{"date":"9999-12-31","timezone":"Etc/GMT+12","moderate":-60,"intense":1e308}`},
		{1, `{"date":"2026-02-30","steps":1}`},
		{2, `{"timestamp":1772359200,"steps":10,"heart_rate":0,"duration":-60}`},
		{2, `{"timestamp":1772359200,"steps":10,"duration":18446744074}`},
		{2, `{"timestamp":-1,"heart_rate":1e308,"duration":1e308}`},
		{3, `{"id":1,"startdate":1772300000,"enddate":1772330000,"data":{"deepsleepduration":1e308,"remsleepduration":-5,"hr_min":"NaN"}}`},
		{3, `{"id":"x","startdate":1772330000,"enddate":1772300000,"sleep_phases":[{"state":1,"startdate":9223372036854775807,"enddate":-9223372036854775808}]}`},
		{3, `{"id":null,"startdate":0,"enddate":0,"data":{"a":[[[[[[[[[[]]]]]]]]]]}}`},
		{4, `{"id":7,"category":2,"startdate":1772359200,"enddate":1772362800,"data":{"calories":1e308,"distance":-1,"hr_average":"Inf"}}`},
		{4, `{"id":7,"startdate":-9223372036854775808,"enddate":9223372036854775807}`},
		{5, `{"timestamp":1772359200,"ecg":{"signalid":1,"afib":0},"signal":{"signal":[1,2,3],"sampling_frequency":1e-300}}`},
		{5, `{"timestamp":1772359200,"ecg":{"signalid":1,"afib":0},"signal":{"signal":[1e308,-1e308],"sampling_frequency":-500}}`},
		{5, `{"timestamp":1772359200,"heart_rate":1e308,"signal":null}`},
	} {
		f.Add(uint8(s.dt), s.line)
	}
	f.Fuzz(func(t *testing.T, kind uint8, line string) {
		fuzzMap(t, fuzzDataTypes[int(kind)%len(fuzzDataTypes)], line)
	})
}

// fuzzMap maps line as the ETL would and checks what a mapper must always
// hold.
func fuzzMap(t *testing.T, dataType, line string) {
	var fields map[string]any
	if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
		return
	}
	raw := connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields}
	done := make(chan struct{})
	var a, b connectorResult
	go func() {
		defer close(done)
		a.res, a.err = New(Options{}).Map(raw)
		b.res, b.err = New(Options{}).Map(raw)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Map did not return within 5 s")
	}
	if (a.err == nil) != (b.err == nil) || !reflect.DeepEqual(a.res, b.res) {
		t.Fatalf("Map is not deterministic: %v / %v", a.err, b.err)
	}
	if a.err != nil {
		return
	}
	res := a.res
	for _, msg := range synthtest.Invariants(&res, fuzzFar) {
		if strings.Contains(msg, "not finite") {
			t.Fatalf("Map emitted %s", msg)
		}
	}
	for _, s := range res.QuantitySamples {
		if s.UserID != testUserID || s.Provider != Slug || s.ContentHash == "" || s.ExternalID == "" || s.QuantityType == "" || s.Unit == "" {
			t.Fatalf("sample without identity: %+v", s)
		}
		if dataType == DataTypeIntraday && (s.EndDate.Before(s.StartDate) || s.EndDate.Sub(s.StartDate) > 24*time.Hour) {
			t.Fatalf("intraday interval %v to %v", s.StartDate, s.EndDate)
		}
	}
	for _, s := range res.SleepSessions {
		if s.ContentHash == "" {
			t.Fatalf("sleep without identity: %+v", s)
		}
	}
	for _, p := range res.SleepPhases {
		if p.ParentContentHash == "" || p.Phase == "" {
			t.Fatalf("phase without a session: %+v", p)
		}
	}
	for _, w := range res.Workouts {
		if w.ContentHash == "" || w.ActivityType == "" {
			t.Fatalf("workout without identity: %+v", w)
		}
	}
	for _, r := range res.ECGRecordings {
		if r.ContentHash == "" {
			t.Fatalf("ecg recording without identity: %+v", r)
		}
	}
	for _, r := range synthtest.Run(New(Options{}), []synth.Line{{DataType: dataType, JSON: []byte(line)}}, fakeNow) {
		if r.Outcome == synthtest.Stored && len(r.Broken) > 0 {
			t.Fatalf("stored a record breaking %v", r.Broken)
		}
	}
}

// FuzzFetchLines serves an arbitrary answer to the first request of every
// pull, and an empty page after it (so a "more" answer pages once, not up to
// maxPagesPerChunk times). None may panic, and every line kept for the raw
// store is one JSON object on one line.
func FuzzFetchLines(f *testing.F) {
	for _, name := range []string{"measure_page1.json", "activity.json", "intraday.json", "sleep_summary.json", "sleep_get.json", "workouts.json", "heart_list.json"} {
		f.Add(withingsfake.File(name))
	}
	for _, s := range []string{`{"status":0,"body":{"measuregrps":[null,{}],"activities":[null],"series":[null,{}]}}`,
		`{"status":0,"body":{"series":{"1":null,"x":{},"-1":{"timestamp":5}}}}`, `{"status":0,"body":null}`,
		`{"status":0,"body":{"series":[[],1,"x"],"more":true,"offset":-1}}`, `{"status":401}`, `[]`} {
		f.Add([]byte(s))
	}
	var answer atomic.Value
	answer.Store([]byte(nil))
	var served atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if served.Add(1) > 1 {
			_, _ = w.Write([]byte(`{"status":0,"body":{}}`))
			return
		}
		_, _ = w.Write(answer.Load().([]byte))
	}))
	f.Cleanup(srv.Close)
	c := New(Options{APIBase: srv.URL, HTTPClient: srv.Client(), ChunkDelay: -1, PageDelay: -1,
		Now: func() time.Time { return fakeNow }, Sleep: func(context.Context, time.Duration) error { return nil }})
	ctx := context.Background()
	f.Fuzz(func(t *testing.T, body []byte) {
		answer.Store(body)
		for _, fetch := range []func() ([]dayLine, error){
			func() ([]dayLine, error) { return c.fetchMeasure(ctx, "tok", fetchFrom, fetchTo) },
			func() ([]dayLine, error) { return c.fetchActivity(ctx, "tok", fetchFrom, fetchTo) },
			func() ([]dayLine, error) { return c.fetchIntraday(ctx, "tok", fetchFrom, fetchTo) },
			func() ([]dayLine, error) { return c.fetchSleep(ctx, "tok", fetchFrom, fetchTo) },
			func() ([]dayLine, error) { return c.fetchWorkouts(ctx, "tok", fetchFrom, fetchTo) },
			func() ([]dayLine, error) { return c.fetchHeart(ctx, "tok", fetchFrom, fetchTo) },
		} {
			served.Store(0)
			lines, err := fetch()
			if err != nil {
				continue
			}
			for _, l := range lines {
				if bytes.ContainsAny(l.line, "\n\r") || !json.Valid(l.line) || len(l.line) == 0 || l.line[0] != '{' {
					t.Fatalf("stored line is not one JSON object on one line: %q", l.line)
				}
			}
		}
	})
}

type connectorResult struct {
	res model.MapResult
	err error
}

// FuzzHandleInbound feeds arbitrary notification forms, signed for their
// user or not, to the notification handler. It must not panic, must refuse a
// wrong signature, and an event names the user, a pulled data type and a
// window ending at now no longer than InboundLookback.
func FuzzHandleInbound(f *testing.F) {
	f.Add("424242", "1", "1772359200", true)
	f.Add("424242", "44", "-9223372036854775808", true)
	f.Add("424242", "16", "9223372036854775807", true)
	f.Add("424242", "54", "", true)
	f.Add("", "1", "1", true)
	f.Add("424242", "x", "1", true)
	f.Add("424242", "1", "1", false)

	c := inboundConnector()
	f.Fuzz(func(t *testing.T, userID, appli, startdate string, signed bool) {
		form := url.Values{}
		form.Set("userid", userID)
		form.Set("appli", appli)
		form.Set("startdate", startdate)
		target := "/inbound/withings"
		if userID != "" {
			signFor := userID
			if !signed {
				signFor = userID + "-other"
			}
			cb, err := c.SignedCallbackURL("http://localhost:8080/inbound/withings", signFor)
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(cb)
			target = u.RequestURI()
		}
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		events, err := c.HandleInbound(context.Background(), r)
		if !signed && err == nil {
			t.Fatal("a notification signed for another user was accepted")
		}
		if err != nil {
			return
		}
		for _, e := range events {
			if e.ProviderUserID != userID || e.DataType == "" {
				t.Fatalf("event %+v", e)
			}
			if !e.To.Equal(inboundNow) || e.From.After(e.To) || e.From.Before(inboundNow.Add(-InboundLookback)) {
				t.Fatalf("event window %v to %v", e.From, e.To)
			}
		}
	})
}
