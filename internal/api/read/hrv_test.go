// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"encoding/base64"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 3, 1, 7, 0, 0, 0, time.UTC)

// beatsOf stamps each interval at the end of the one before it, as the Apple
// Health mapper does; a negative value is a gap of that many milliseconds
// with no interval stored.
func beatsOf(rrs ...float64) []Beat {
	at := t0
	var out []Beat
	for _, rr := range rrs {
		if rr < 0 {
			at = at.Add(time.Duration(-rr * float64(time.Millisecond)))
			continue
		}
		at = at.Add(time.Duration(rr * float64(time.Millisecond)))
		out = append(out, Beat{At: at, RRMs: rr})
	}
	return out
}

func repeat(n int, rrs ...float64) []float64 {
	var out []float64
	for range n {
		out = append(out, rrs...)
	}
	return out
}

func val(t *testing.T, p *float64) float64 {
	t.Helper()
	require.NotNil(t, p)
	return *p
}

func TestSummarizeBeatsSteady(t *testing.T) {
	// 790, 810, 790, ...: every successive difference is 20 ms.
	s := SummarizeBeats(beatsOf(repeat(10, 790, 810)...))
	assert.Equal(t, 20, s.Intervals)
	assert.Equal(t, 20, s.Accepted)
	assert.Equal(t, 19, s.Pairs)
	assert.InDelta(t, 800, val(t, s.MeanRRMs), 1e-9)
	assert.InDelta(t, 75, val(t, s.MeanHR), 1e-9)
	assert.InDelta(t, 20, val(t, s.RMSSDMs), 1e-9)
	assert.InDelta(t, 0, val(t, s.PNN50), 1e-9)
	// Sample SD of ten 790s and ten 810s: sqrt(20*100/19).
	assert.InDelta(t, math.Round(math.Sqrt(2000.0/19)*1000)/1000, val(t, s.SDNNMs), 1e-9)
}

func TestSummarizeBeatsPNN50(t *testing.T) {
	// Differences alternate 60 and -60: every pair is over 50 ms.
	s := SummarizeBeats(beatsOf(repeat(8, 770, 830)...))
	assert.InDelta(t, 100, val(t, s.PNN50), 1e-9)
	assert.InDelta(t, 60, val(t, s.RMSSDMs), 1e-9)
}

func TestSummarizeBeatsRejectsImplausibleAndEctopic(t *testing.T) {
	rrs := append(repeat(12, 800), 250, 800) // 250 is out of range
	rrs = append(rrs, 500, 1100, 800, 800)   // a premature beat and its pause
	rrs = append(rrs, repeat(10, 800)...)
	s := SummarizeBeats(beatsOf(rrs...))
	assert.Equal(t, len(rrs), s.Intervals)
	// Rejected: 250; 500 (37 % shorter); 1100 (out of 20 % of 500); the 800
	// after 1100 (27 % shorter). The 800 after 250 has no plausible
	// predecessor to compare with, so it is kept, but forms no pair.
	assert.Equal(t, len(rrs)-4, s.Accepted)
	// Pairs: 11 in the first run, none across the rejections, then the 800
	// after the rejected 800 starts a run of 11 accepted beats: 10 pairs.
	assert.Equal(t, 11+10, s.Pairs)
	assert.InDelta(t, 0, val(t, s.RMSSDMs), 1e-9)
	assert.InDelta(t, 800, val(t, s.MeanRRMs), 1e-9)
}

func TestSummarizeBeatsGapBreaksPairs(t *testing.T) {
	// A gap of two seconds between two runs of 800 and 900: the 100 ms step
	// across it is not a successive difference.
	rrs := append(repeat(11, 800), -2000)
	rrs = append(rrs, repeat(11, 900)...)
	s := SummarizeBeats(beatsOf(rrs...))
	assert.Equal(t, 22, s.Accepted)
	assert.Equal(t, 20, s.Pairs)
	assert.InDelta(t, 0, val(t, s.RMSSDMs), 1e-9)
	assert.InDelta(t, 0, val(t, s.PNN50), 1e-9)
}

func TestSummarizeBeatsTooShort(t *testing.T) {
	s := SummarizeBeats(beatsOf(repeat(10, 800)...))
	assert.Equal(t, 9, s.Pairs)
	assert.Nil(t, s.RMSSDMs)
	assert.Nil(t, s.SDNNMs)
	assert.Nil(t, s.PNN50)
	assert.InDelta(t, 800, val(t, s.MeanRRMs), 1e-9)

	empty := SummarizeBeats(nil)
	assert.Zero(t, empty.Intervals)
	assert.Nil(t, empty.MeanRRMs)
	assert.Nil(t, empty.MeanHR)
}

func TestSummarizeBeatsRepeatedBeat(t *testing.T) {
	b := beatsOf(repeat(12, 800)...)
	b = append(b[:5], append([]Beat{b[4]}, b[5:]...)...)
	s := SummarizeBeats(b)
	assert.Equal(t, 12, s.Intervals)
	assert.Equal(t, 11, s.Pairs)
}

func TestKeyCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 3, 1, 9, 30, 0, 123456000, time.UTC)
	c := encodeKeyCursor(at, "apple_health", "series|with|bars")
	gotT, key, err := decodeKeyCursor(c, 2)
	require.NoError(t, err)
	assert.True(t, at.Equal(gotT))
	assert.Equal(t, []string{"apple_health", "series|with|bars"}, key)

	for _, bad := range []string{"%%", encodeKeyCursor(at, "only-one"),
		base64.RawURLEncoding.EncodeToString([]byte("soon|a|b")),
		base64.RawURLEncoding.EncodeToString([]byte("2026-03-01T00:00:00Z||b"))} {
		_, _, err := decodeKeyCursor(bad, 2)
		assert.ErrorIs(t, err, ErrInvalidCursor, bad)
	}
}
