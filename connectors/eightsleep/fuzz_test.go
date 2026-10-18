// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"encoding/json"
	"testing"

	"github.com/soma-intelligence/open-health-sync/connectors/eightsleep/eightsleepfake"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// FuzzMap checks that no trends line makes Map panic, and that every
// session it maps ends after it starts.
func FuzzMap(f *testing.F) {
	for _, d := range eightsleepfake.Days() {
		f.Add(d)
	}
	f.Add([]byte(`{"day":"2026-03-01","sessions":[{"ts":"2026-03-01T00:00:00Z","stages":[{"stage":"deep","duration":1e300}]}]}`))
	c := New(Options{})
	f.Fuzz(func(t *testing.T, line []byte) {
		var fields map[string]any
		if json.Unmarshal(line, &fields) != nil {
			return
		}
		res, err := c.Map(connector.RawRecord{UserID: testUserID, DataType: DataTypeTrends, Fields: fields})
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
