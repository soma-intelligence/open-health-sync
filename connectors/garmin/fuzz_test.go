// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/garmin/garminfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Fuzz targets. Run one for a while with
//
//	go test -run='^$' -fuzz=FuzzMap -fuzztime=30s ./connectors/garmin/
//
// A crasher lands in testdata/fuzz/<target>/ and is replayed by every plain
// go test from then on.

// FuzzMap maps any line under any data type and checks what a mapper must
// always hold: no panic, determinism, records that name their user,
// provider and hash, phases inside their session.
func FuzzMap(f *testing.F) {
	for i, dt := range AllDataTypes {
		for _, it := range garminfake.Summaries(dt) {
			f.Add(uint8(i), string(it))
		}
	}
	for _, s := range []string{
		`{}`, `{"startTimeInSeconds":-1}`, `{"startTimeInSeconds":1e308}`, `{"calendarDate":"2026-02-30"}`,
		`{"startTimeInSeconds":1772404200,"durationInSeconds":-5,"sleepLevelsMap":{"deep":[{"startTimeInSeconds":1772404200,"endTimeInSeconds":1}]}}`,
		`{"startTimeInSeconds":1772404200,"startTimeOffsetInSeconds":99999999,"timeOffsetHeartRateSamples":{"-1":60,"x":70,"999999999":80}}`,
		`{"activityId":-1,"summaryId":"","startTimeInSeconds":1772404200}`,
		`{"measurementTimeInSeconds":1772404200,"weightInGrams":1e300,"bodyFatInPercent":-3}`,
	} {
		for i := range AllDataTypes {
			f.Add(uint8(i), s)
		}
	}
	f.Fuzz(func(t *testing.T, idx uint8, line string) {
		dt := AllDataTypes[int(idx)%len(AllDataTypes)]
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
			return
		}
		c := New(Options{})
		raw := connector.RawRecord{UserID: testUserID, Provider: Slug, DataType: dt, Fields: fields}
		a, errA := c.Map(raw)
		b, errB := c.Map(raw)
		if (errA == nil) != (errB == nil) || !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: Map is not deterministic", dt)
		}
		if errA != nil {
			return
		}
		for _, s := range a.QuantitySamples {
			if s.UserID != testUserID || s.Provider != Slug || s.ContentHash == "" || s.Unit == "" {
				t.Fatalf("%s: incomplete sample %+v", dt, s)
			}
			if s.EndDate.Before(s.StartDate) {
				t.Fatalf("%s: sample ends before it starts", dt)
			}
		}
		for _, s := range a.SleepSessions {
			if s.ContentHash == "" || !s.BedtimeEnd.After(s.BedtimeStart) {
				t.Fatalf("%s: bad session %+v", dt, s)
			}
		}
		for _, p := range a.SleepPhases {
			if !p.EndDate.After(p.StartDate) || p.DurationS <= 0 || p.EndDate.Sub(p.StartDate) > 24*time.Hour {
				t.Fatalf("%s: bad phase %+v", dt, p)
			}
		}
		for _, w := range a.Workouts {
			if w.ContentHash == "" || w.ExternalID == "" || w.EndDate.Before(w.StartDate) {
				t.Fatalf("%s: bad workout %+v", dt, w)
			}
		}
		for _, d := range a.DailySummaries {
			if d.ContentHash == "" || d.SummaryDate.IsZero() {
				t.Fatalf("%s: bad summary %+v", dt, d)
			}
		}
	})
}

// FuzzInbound feeds any body to the notification parser: no panic, and every
// event names a user and a data type.
func FuzzInbound(f *testing.F) {
	f.Add(`{"dailies":[{"userId":"u","callbackURL":"https://x?uploadStartTimeInSeconds=1772406000&uploadEndTimeInSeconds=1772406300"}]}`)
	f.Add(`{"sleeps":[` + string(garminfake.Summaries(DataTypeSleeps)[0]) + `]}`)
	f.Add(`{"deregistrations":[{"userId":"u"}]}`)
	f.Add(`{"dailies":[{"userId":"u","callbackURL":"%zz"}]}`)
	f.Fuzz(func(t *testing.T, body string) {
		evs, err := parseNotification([]byte(body), fakeNow)
		if err != nil {
			return
		}
		for _, ev := range evs {
			if ev.ProviderUserID == "" || ev.DataType == "" {
				t.Fatalf("event without user or type: %+v", ev)
			}
			if len(ev.Lines) == 0 && ev.To.After(fakeNow) {
				t.Fatalf("pull past now: %+v", ev)
			}
		}
	})
}
