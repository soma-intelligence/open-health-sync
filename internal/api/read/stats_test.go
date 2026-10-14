// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func TestFloorAndCeilInterval(t *testing.T) {
	paris := zone(t, "Europe/Paris")
	kolkata := zone(t, "Asia/Kolkata")
	at := time.Date(2026, 3, 10, 14, 37, 12, 0, time.UTC)

	cases := []struct {
		name        string
		t           time.Time
		interval    string
		loc         *time.Location
		floor, ceil time.Time
	}{
		{"hour UTC", at, IntervalHour, time.UTC,
			time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)},
		{"day UTC", at, IntervalDay, time.UTC,
			time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)},
		// 14:37 UTC is 15:37 in Paris (winter time).
		{"day Paris", at, IntervalDay, paris,
			time.Date(2026, 3, 10, 0, 0, 0, 0, paris), time.Date(2026, 3, 11, 0, 0, 0, 0, paris)},
		// 14:37 UTC is 20:07 in Kolkata: its hours start at :30 UTC.
		{"hour Kolkata", at, IntervalHour, kolkata,
			time.Date(2026, 3, 10, 20, 0, 0, 0, kolkata), time.Date(2026, 3, 10, 21, 0, 0, 0, kolkata)},
		// 23:30 UTC is already the next day in Kolkata.
		{"day across midnight", time.Date(2026, 3, 10, 23, 30, 0, 0, time.UTC), IntervalDay, kolkata,
			time.Date(2026, 3, 11, 0, 0, 0, 0, kolkata), time.Date(2026, 3, 12, 0, 0, 0, 0, kolkata)},
		// The spring forward day in Paris is 23 hours long.
		{"short DST day", time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC), IntervalDay, paris,
			time.Date(2026, 3, 29, 0, 0, 0, 0, paris), time.Date(2026, 3, 30, 0, 0, 0, 0, paris)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := floorInterval(c.t, c.interval, c.loc)
			assert.True(t, f.Equal(c.floor), "floor %v, want %v", f, c.floor)
			got := ceilInterval(c.t, c.interval, c.loc)
			assert.True(t, got.Equal(c.ceil), "ceil %v, want %v", got, c.ceil)
		})
	}
	short := ceilInterval(time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC), IntervalDay, paris).
		Sub(floorInterval(time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC), IntervalDay, paris))
	assert.Equal(t, 23*time.Hour, short, "a day bucket follows the wall clock, not 24 hours")
}

func TestCeilIntervalKeepsABoundary(t *testing.T) {
	paris := zone(t, "Europe/Paris")
	for _, c := range []struct {
		t        time.Time
		interval string
		loc      *time.Location
	}{
		{time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC), IntervalHour, time.UTC},
		{time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), IntervalDay, time.UTC},
		{time.Date(2026, 3, 10, 0, 0, 0, 0, paris), IntervalDay, paris},
	} {
		assert.True(t, ceilInterval(c.t, c.interval, c.loc).Equal(c.t), "%v %s", c.t, c.interval)
	}
}

func TestWholeHourOffsets(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.True(t, wholeHourOffsets(start, end, time.UTC))
	assert.True(t, wholeHourOffsets(start, end, zone(t, "Europe/Paris")), "DST moves by a whole hour")
	assert.True(t, wholeHourOffsets(start, end, zone(t, "America/New_York")))
	assert.False(t, wholeHourOffsets(start, end, zone(t, "Asia/Kolkata")), "UTC+05:30")
	assert.False(t, wholeHourOffsets(start, end, zone(t, "Asia/Kathmandu")), "UTC+05:45")
	assert.False(t, wholeHourOffsets(start, end, time.FixedZone("half", 30*60)))
	assert.True(t, wholeHourOffsets(start, end, time.FixedZone("plus2", 2*3600)))

	// Lord Howe Island moves its clock by 30 minutes: a window inside its
	// summer offset (+11:00) is whole hours, one that reaches the winter
	// offset (+10:30) is not.
	lordHowe := zone(t, "Australia/Lord_Howe")
	assert.True(t, wholeHourOffsets(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), lordHowe))
	assert.False(t, wholeHourOffsets(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), lordHowe))
	assert.True(t, wholeHourOffsets(end, start, zone(t, "Asia/Kolkata")), "an empty window has nothing to refuse")
}
