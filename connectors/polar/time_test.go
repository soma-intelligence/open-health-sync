// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseISODuration(t *testing.T) {
	ok := map[string]time.Duration{
		"PT2H44M":    2*time.Hour + 44*time.Minute,
		"PT210.026S": 210*time.Second + 26*time.Millisecond,
		"PT4S":       4 * time.Second,
		"PT0S":       0,
		"P1DT2H":     26 * time.Hour,
		"PT1H0M30S":  time.Hour + 30*time.Second,
	}
	for in, want := range ok {
		got, err := parseISODuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "P", "PT", "2H", "PT2X", "P1Y", "PT1H2", "PTT1H", "P1M"} {
		_, err := parseISODuration(in)
		assert.Error(t, err, in)
	}
}

func TestOffsetZone(t *testing.T) {
	cases := map[int]string{0: "Etc/UTC", 60: "Etc/GMT-1", -300: "Etc/GMT+5", 330: "", 840: "Etc/GMT-14", 900: "", -720: "Etc/GMT+12"}
	for in, want := range cases {
		assert.Equal(t, want, offsetZone(in), in)
		if want != "" {
			_, err := time.LoadLocation(want)
			assert.NoError(t, err, want)
		}
	}
}

func TestParseTime(t *testing.T) {
	tm, zoned, err := parseTime("2026-03-05T07:30:00")
	require.NoError(t, err)
	assert.False(t, zoned)
	assert.Equal(t, time.Date(2026, 3, 5, 7, 30, 0, 0, time.UTC), tm)
	tm, zoned, err = parseTime("2026-03-05T07:30:00+01:00")
	require.NoError(t, err)
	assert.True(t, zoned)
	assert.Equal(t, 60, offsetMinutes(tm))
	_, _, err = parseTime("2026-03-05T07:30:00.000Z")
	require.NoError(t, err)
	_, _, err = parseTime("2025-08-14T00:02")
	require.NoError(t, err)
	_, _, err = parseTime("nope")
	require.Error(t, err)
}

func TestYmdChunks(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got := ymdChunks(from, to, 28)
	assert.Equal(t, []ymdSpan{{"2026-01-01", "2026-01-28"}, {"2026-01-29", "2026-02-25"}, {"2026-02-26", "2026-02-28"}}, got)
	assert.Empty(t, ymdChunks(to, from, 28))
}

func TestPullStateOffsetFor(t *testing.T) {
	s := &pullState{offsets: map[string]int{"2026-03-04": 60, "2026-03-10": 120}}
	off, ok := s.offsetFor("2026-03-04")
	assert.True(t, ok)
	assert.Equal(t, 60, off)
	off, ok = s.offsetFor("2026-03-06")
	assert.True(t, ok)
	assert.Equal(t, 60, off)
	off, ok = s.offsetFor("2026-03-08")
	assert.True(t, ok)
	assert.Equal(t, 120, off)
	_, ok = s.offsetFor("2026-04-01")
	assert.False(t, ok)
}
