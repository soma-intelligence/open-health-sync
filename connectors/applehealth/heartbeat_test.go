// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
)

func beat(at float64, gap bool) map[string]any {
	return map[string]any{"timeSinceSeriesStart": at, "precededByGap": gap}
}

// TestHeartbeatRRInterval: an RR interval is the difference between two
// consecutive beats, not a beat's offset from the series start.
func TestHeartbeatRRInterval(t *testing.T) {
	m := New()
	record := map[string]any{
		"startDate": "2025-03-01T14:30:00Z",
		"endDate":   "2025-03-01T14:30:03Z",
		"uuid":      "00000000-0000-4000-8000-000000000001",
		"heartbeats": []any{
			beat(0.0, false), beat(0.81, false), beat(1.60, false), beat(2.42, false),
		},
	}

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.HighFrequency, "heartbeat is high-frequency")
	require.Len(t, result.HeartbeatSamples, 3, "four beats give three intervals")

	start := time.Date(2025, 3, 1, 14, 30, 0, 0, time.UTC)
	want := []struct {
		rr float64
		at time.Duration
	}{{810, 810 * time.Millisecond}, {790, 1600 * time.Millisecond}, {820, 2420 * time.Millisecond}}
	for i, w := range want {
		hb := result.HeartbeatSamples[i]
		assert.Equal(t, w.rr, hb.RRMS, "interval %d", i)
		assert.Equal(t, start.Add(w.at), hb.Timestamp, "interval %d is stamped at its closing beat", i)
		assert.Equal(t, "user-123", hb.UserID)
		assert.Equal(t, "00000000-0000-4000-8000-000000000001", hb.SeriesID)
	}
}

// A beat preceded by a gap follows missing data: the time since the previous
// beat is not an RR interval.
func TestHeartbeatGapYieldsNoInterval(t *testing.T) {
	m := New()
	record := map[string]any{
		"startDate": "2025-03-01T14:30:00Z",
		"heartbeats": []any{
			beat(0.0, false), beat(0.81, false), beat(5.0, true), beat(5.8, false),
		},
	}

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.HeartbeatSamples, 2)
	assert.Equal(t, 810.0, result.HeartbeatSamples[0].RRMS)
	assert.Equal(t, 800.0, result.HeartbeatSamples[1].RRMS, "the gap beat starts a new run")
	assert.Equal(t, "2025-03-01T14:30:00Z", result.HeartbeatSamples[0].SeriesID,
		"without a uuid the series is named by its start, so a re-upload hashes the same")
}

func TestHeartbeatSeriesFixture(t *testing.T) {
	m := New()
	record := loadTestFixture(t, "heartbeat_series.json")

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.HeartbeatSamples, 3)
	assert.Equal(t, 856.0, result.HeartbeatSamples[0].RRMS)
}

func TestHeartbeatMissingStart(t *testing.T) {
	m := New()
	record := map[string]any{"heartbeats": []any{beat(0, false), beat(0.8, false)}}

	result, err := m.mapRecord(record, "user-123")
	assert.Error(t, err, "missing startDate should error")
	assert.Nil(t, result)
}

// The old one-beat-per-line shape cannot give an interval and is refused,
// rather than storing the beat's offset as if it were one.
func TestHeartbeatOneBeatLineRefused(t *testing.T) {
	m := New()
	record := map[string]any{
		"timestamp":            "2025-03-01T14:30:05Z",
		"timeSinceSeriesStart": 1.234,
		"precededByGap":        false,
	}

	result, err := m.mapRecord(record, "user-123")
	assert.ErrorIs(t, err, errOneBeatLine)
	assert.Nil(t, result)
}

func TestHeartbeatSingleBeatSeriesIsEmpty(t *testing.T) {
	m := New()
	record := map[string]any{"startDate": "2025-03-01T14:30:00Z", "heartbeats": []any{beat(0, false)}}

	result, err := m.mapRecord(record, "user-123")
	require.NoError(t, err)
	assert.Nil(t, result)
}

// Beats that do not come after their predecessor are dropped and counted;
// the log carries the count, never a beat time.
func TestHeartbeatNonIncreasingBeatsCounted(t *testing.T) {
	// Capture writes no timestamp, whose digits could contain a beat time by chance.
	buf := logtest.Capture(t, zerolog.DebugLevel)

	m := New()
	result, err := m.mapRecord(map[string]any{
		"startDate": "2025-03-01T14:30:00Z",
		"heartbeats": []any{
			beat(0.0, false), beat(0.81, false), beat(0.81, false), beat(0.5, false), beat(1.3, false),
		},
	}, "user-123")
	require.NoError(t, err)
	require.Len(t, result.HeartbeatSamples, 2, "0.81 and 1.3 close real intervals")
	assert.Equal(t, 810.0, result.HeartbeatSamples[0].RRMS)
	assert.Equal(t, 490.0, result.HeartbeatSamples[1].RRMS, "measured from the last kept beat, 0.81")
	assert.Contains(t, buf.String(), `"count":2`)
	assert.NotContains(t, buf.String(), "0.81")
}
