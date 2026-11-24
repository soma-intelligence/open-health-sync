// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// errOneBeatLine rejects the old one-beat-per-line shape. HealthKit gives a
// heartbeat series as beat times (timeSinceSeriesStart, seconds from the series
// start), not intervals: an RR interval is the difference between two
// consecutive beats, so one beat alone cannot yield one. The previous mapper
// stored timeSinceSeriesStart itself as the interval, which grows without
// bound across a series. Rejecting the line makes the failure visible instead
// of storing a wrong number.
var errOneBeatLine = errors.New(`heartbeat: a line must carry the whole series in "heartbeats"; one beat gives no RR interval`)

// mapHeartbeat maps one heartbeat series line:
//
//	{"startDate": "...", "uuid": "...", "metadata": {...},
//	 "heartbeats": [{"timeSinceSeriesStart": 0.0, "precededByGap": false}, ...]}
//
// Each beat after the first gives one RR interval, the difference between its
// time and the previous beat's, in milliseconds, stamped at the beat's own
// time. A beat with precededByGap true follows missing data, so the interval
// to the previous beat is not an RR interval and none is emitted for it.
func (m *mapper) mapHeartbeat(record map[string]any, userID string) (*model.MapResult, error) {
	beats, ok := record["heartbeats"].([]any)
	if !ok {
		return nil, errOneBeatLine
	}
	start, _, _, err := ParseTimestamp(record)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: %w", err)
	}

	// The series identity is part of every sample's content hash, so it must
	// come from the line itself: the same series re-uploaded must hash the same.
	seriesID := stringField(record, "uuid")
	if seriesID == "" {
		seriesID = start.Format(time.RFC3339Nano)
	}

	res := &model.MapResult{HighFrequency: true}
	prev := 0.0
	dropped := 0
	for i, b := range beats {
		beat, ok := b.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("heartbeat: beat %d is not an object", i)
		}
		at, err := extractFloat(beat, "timeSinceSeriesStart")
		if err != nil {
			return nil, fmt.Errorf("heartbeat: beat %d: %w", i, err)
		}
		gap, _ := beat["precededByGap"].(bool)
		if i > 0 && !gap {
			// Rounded to the microsecond: float noise in the subtraction
			// would otherwise change the content hash between re-uploads.
			rr := math.Round((at-prev)*1e6) / 1e3
			if rr <= 0 {
				// A beat not after the last kept one: out of order or
				// repeated. It is dropped and does not become the reference,
				// so out-of-order input never fabricates an interval.
				dropped++
				continue
			}
			res.HeartbeatSamples = append(res.HeartbeatSamples, model.HeartbeatSample{
				SeriesID:  seriesID,
				UserID:    userID,
				Timestamp: start.Add(time.Duration(math.Round(at * float64(time.Second)))).UTC(),
				RRMS:      rr,
			})
		}
		// The first beat and a beat after a gap start a run: no interval,
		// but the next beat measures from them.
		prev = at
	}
	if dropped > 0 {
		// Counts only: beat times are health values and never logged.
		modlog().Warn().Int("count", dropped).Msg("heartbeat_beats_not_after_predecessor_dropped")
	}
	if len(res.HeartbeatSamples) == 0 {
		return nil, nil
	}
	return res, nil
}
