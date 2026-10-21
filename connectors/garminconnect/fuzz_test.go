// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

import (
	"encoding/json"
	"testing"

	"github.com/soma-intelligence/open-health-sync/connectors/garminconnect/garminconnectfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// FuzzMap checks that no line makes Map panic, and that every sleep session
// it maps ends after it starts.
func FuzzMap(f *testing.F) {
	for _, name := range []string{"sleep.json", "hrv.json"} {
		f.Add(DataTypeSleeps, garminconnectfake.Fixture(f, name))
	}
	f.Add(DataTypeActivities, []byte(`{"activityId":1,"startTimeGMT":"2026-04-20 06:00:00","activityType":{"typeKey":"running"}}`))
	f.Add(DataTypeSleeps, []byte(`{"dailySleepDTO":{"calendarDate":"2026-03-01","sleepStartTimestampGMT":1,"sleepTimeSeconds":1e300}}`))
	c := New(Options{})
	f.Fuzz(func(t *testing.T, dataType string, line []byte) {
		var fields map[string]any
		if json.Unmarshal(line, &fields) != nil {
			return
		}
		res, err := c.Map(connector.RawRecord{UserID: testUserID, DataType: dataType, Fields: fields})
		if err != nil {
			return
		}
		for _, s := range res.SleepSessions {
			if !s.BedtimeEnd.After(s.BedtimeStart) {
				t.Fatalf("session %s ends before it starts", s.ExternalID)
			}
		}
	})
}
