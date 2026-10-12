// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMapSleep -fuzztime=30s ./connectors/oura/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// fuzzSeeds adds the documents of a recorded answer and a few shapes around
// them.
func fuzzSeeds(f *testing.F, dataType string) {
	var p struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(ourafake.File(dataType+".json"), &p); err != nil {
		f.Fatal(err)
	}
	for _, d := range p.Data {
		f.Add(string(d))
	}
	for _, s := range []string{
		`{}`, `{"id":""}`, `{"id":null,"day":null}`, `{"day":"2026-02-30"}`, `{"day":"99999-01-01"}`,
		`{"id":"x","day":"2026-03-01","timestamp":"2026-03-01T00:00:00+23:59"}`,
		`{"id":"x","type":"deleted"}`, `{"id":"x","bedtime_start":"2026-03-01T00:00:00Z","bedtime_end":"9999-12-31T23:59:59Z","sleep_phase_5_min":"1234"}`,
		`{"bpm":1e308,"timestamp":"2026-03-01T00:00:00Z"}`, `{"bpm":-1,"timestamp_unix":-1}`, `{"timestamp_unix":9223372036854775807,"bpm":60}`,
		`{"contributors":{"hrv_balance":"x"}}`, `{"start_datetime":"2026-03-01T00:00:00-12:00","end_datetime":"2026-03-01T00:00:00+14:00","id":"w"}`,
	} {
		f.Add(s)
	}
}

// fuzzMap maps line as the ETL would and checks what a mapper must always
// hold: no panic, determinism, records that name their user, provider and
// hash, phases inside their session.
func fuzzMap(t *testing.T, dataType, line string) {
	var fields map[string]any
	if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
		return
	}
	c := New(Options{})
	raw := connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dataType, Fields: fields}
	a, errA := c.Map(raw)
	b, errB := New(Options{}).Map(raw)
	if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
		t.Fatalf("Map is not deterministic: %v / %v", errA, errB)
	}
	if errA != nil {
		if len(errA.Error()) > 512 {
			t.Fatalf("error message of %d bytes", len(errA.Error()))
		}
		return
	}
	for _, s := range a.QuantitySamples {
		if s.UserID != testUserID || s.Provider != Slug || s.ContentHash == "" || s.QuantityType == "" {
			t.Fatalf("incomplete sample %+v", s)
		}
		if s.EndDate.Before(s.StartDate) {
			t.Fatalf("sample ends before it starts")
		}
	}
	for _, s := range a.SleepSessions {
		if s.ContentHash == "" || s.ExternalID == "" || !s.BedtimeEnd.After(s.BedtimeStart) {
			t.Fatalf("bad session %+v", s)
		}
		prev := s.BedtimeStart
		for _, p := range a.SleepPhases {
			if p.StartDate.Before(prev) || !p.EndDate.After(p.StartDate) || p.EndDate.After(s.BedtimeEnd) || p.ParentContentHash != s.ContentHash {
				t.Fatalf("bad phase %+v in %v..%v", p, s.BedtimeStart, s.BedtimeEnd)
			}
			prev = p.EndDate
		}
	}
	for _, w := range a.Workouts {
		if w.ContentHash == "" || w.ExternalID == "" || w.EndDate.Before(w.StartDate) {
			t.Fatalf("bad workout %+v", w)
		}
	}
	for _, cs := range a.CategorySamples {
		if cs.ContentHash == "" || cs.ExternalID == "" || cs.EndDate.Before(cs.StartDate) {
			t.Fatalf("bad category sample %+v", cs)
		}
	}
	for _, d := range a.Deletions {
		if d.ExternalID == "" || d.Provider != Slug {
			t.Fatalf("bad deletion %+v", d)
		}
	}
	if _, err := json.Marshal(a); err != nil {
		t.Fatalf("result does not encode: %v", err)
	}
	_ = model.CanonicalizeUnits(&a)
}

func FuzzMapDailyActivity(f *testing.F) {
	fuzzSeeds(f, DataTypeDailyActivity)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeDailyActivity, line) })
}

func FuzzMapSleep(f *testing.F) {
	fuzzSeeds(f, DataTypeSleep)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeSleep, line) })
}

func FuzzMapDailySleep(f *testing.F) {
	fuzzSeeds(f, DataTypeDailySleep)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeDailySleep, line) })
}

func FuzzMapDailyReadiness(f *testing.F) {
	fuzzSeeds(f, DataTypeDailyReadiness)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeDailyReadiness, line) })
}

func FuzzMapDailySpO2(f *testing.F) {
	fuzzSeeds(f, DataTypeDailySpO2)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeDailySpO2, line) })
}

func FuzzMapDailyStress(f *testing.F) {
	fuzzSeeds(f, DataTypeDailyStress)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeDailyStress, line) })
}

func FuzzMapWorkout(f *testing.F) {
	fuzzSeeds(f, DataTypeWorkout)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeWorkout, line) })
}

func FuzzMapSession(f *testing.F) {
	fuzzSeeds(f, DataTypeSession)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeSession, line) })
}

func FuzzMapVO2Max(f *testing.F) {
	fuzzSeeds(f, DataTypeVO2Max)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeVO2Max, line) })
}

func FuzzMapHeartRate(f *testing.F) {
	fuzzSeeds(f, DataTypeHeartRate)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeHeartRate, line) })
}

func FuzzMapWebhook(f *testing.F) {
	for _, dt := range []string{"sleep", "workout", "session", "daily_sleep", "SLEEP"} {
		f.Add(`{"event_type":"delete","data_type":"` + dt + `","object_id":"x","event_time":"2026-03-03T11:59:00+00:00","user_id":"u"}`)
	}
	f.Add(`{"event_type":"update","data_type":"sleep","user_id":"u"}`)
	f.Fuzz(func(t *testing.T, line string) { fuzzMap(t, DataTypeWebhook, line) })
}

// FuzzHandleInbound posts arbitrary bodies and headers, signed or not: no
// panic, and anything accepted is a well formed event whose stored line maps
// to a deletion.
func FuzzHandleInbound(f *testing.F) {
	ts := strconv.FormatInt(inboundNow.Unix(), 10)
	for _, ev := range []string{"create", "update", "delete", "DELETE", "archive"} {
		for _, dt := range []string{"sleep", "workout", "session", "daily_activity", "tag", "vo2_max"} {
			f.Add(`{"event_type":"`+ev+`","data_type":"`+dt+`","object_id":"o","event_time":"2026-03-03T11:59:00+00:00","user_id":"u"}`, ts, true)
		}
	}
	f.Add(`{"event_type":"delete"}`, ts, true)
	f.Add(`not json`, ts, true)
	f.Add(`{"event_type":"update","data_type":"sleep","user_id":"u"}`, "", false)
	f.Add(`{"event_type":"update","data_type":"sleep","user_id":"u"}`, "1772539200000", true)
	f.Add(`{"event_type":"update","data_type":"sleep","user_id":"u"}`, "2026-03-03T12:00:00Z", true)
	f.Fuzz(func(t *testing.T, body, timestamp string, sign bool) {
		c := inboundConnector(testVerify)
		r := httptest.NewRequest(http.MethodPost, "/inbound/oura", strings.NewReader(body))
		if timestamp != "" && !strings.ContainsAny(timestamp, "\r\n\x00") {
			r.Header.Set(HeaderTimestamp, timestamp)
		}
		sig := "00"
		if sign {
			sig = Sign(testSecret, strings.TrimSpace(r.Header.Get(HeaderTimestamp)), []byte(body))
		}
		r.Header.Set(HeaderSignature, sig)
		events, err := c.HandleInbound(context.Background(), r)
		if err != nil {
			if len(err.Error()) > 512 {
				t.Fatalf("error message of %d bytes", len(err.Error()))
			}
			return
		}
		if !sign {
			t.Fatalf("an unsigned body was accepted")
		}
		for _, ev := range events {
			if ev.ProviderUserID == "" || ev.DataType == "" {
				t.Fatalf("incomplete event %+v", ev)
			}
			if len(ev.Lines) == 0 {
				if !ev.To.After(ev.From) {
					t.Fatalf("empty pull window %+v", ev)
				}
				continue
			}
			if ev.DataType != DataTypeWebhook || len(ev.Lines) != 1 {
				t.Fatalf("unexpected stored event %+v", ev)
			}
			var fields map[string]any
			if err := json.Unmarshal(ev.Lines[0], &fields); err != nil {
				t.Fatalf("stored line is not JSON: %v", err)
			}
			res, err := New(Options{}).Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeWebhook, Fields: fields})
			if err != nil || len(res.Deletions) != 1 {
				t.Fatalf("stored line does not map to a deletion: %v %+v", err, res)
			}
		}
	})
}
