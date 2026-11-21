// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hourSeq(rows ...HourRow) iter.Seq2[HourRow, error] {
	return func(yield func(HourRow, error) bool) {
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func sampleSeq(rows ...SampleRow) iter.Seq2[SampleRow, error] {
	return func(yield func(SampleRow, error) bool) {
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}

func hourRow(at time.Time, n int64, sum, low, high float64) HourRow {
	return HourRow{Bucket: at, Provider: "oura", Source: "Ring", Slug: "oura", Worn: 1, StreamKey: "oura|Ring",
		N: n, HasValue: true, Sum: sum, Min: low, Max: high}
}

func sampleRow(at time.Time, v float64) SampleRow {
	return SampleRow{Start: at, Provider: "oura", Source: "Ring", Slug: "oura", Worn: 1, StreamKey: "oura|Ring", Value: &v}
}

// figures are what a bucket answers, for a comparison.
type figures struct {
	start         string
	n             int64
	sum, min, max float64
}

func figuresOf(bs []StatsBucket) []figures {
	out := make([]figures, len(bs))
	for i, b := range bs {
		out[i] = figures{b.Start.Format(time.RFC3339), b.Count, b.Sum, b.Min, b.Max}
	}
	return out
}

func TestFoldHoursUTC(t *testing.T) {
	d := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	rows := []HourRow{
		hourRow(d, 6, 488, 77, 84),
		hourRow(d.Add(time.Hour), 6, 496, 80, 86),
		hourRow(d.Add(25*time.Hour), 2, 100, 40, 60),
	}
	byHour, err := foldHours(hourSeq(rows...), time.Hour, time.UTC)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-09-24T00:00:00Z", 6, 488, 77, 84},
		{"2026-09-24T01:00:00Z", 6, 496, 80, 86},
		{"2026-09-25T01:00:00Z", 2, 100, 40, 60},
	}, figuresOf(byHour))

	byDay, err := foldHours(hourSeq(rows...), 24*time.Hour, time.UTC)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-09-24T00:00:00Z", 12, 984, 77, 86},
		{"2026-09-25T00:00:00Z", 2, 100, 40, 60},
	}, figuresOf(byDay))

	// A nil zone is UTC.
	again, err := foldHours(hourSeq(rows...), 24*time.Hour, nil)
	require.NoError(t, err)
	assert.Equal(t, figuresOf(byDay), figuresOf(again))
}

// Streams are never added up: one bucket per interval, provider, source,
// slug, wear and unit. A bucket with no value counts and has no figures.
func TestFoldKeepsStreamsApart(t *testing.T) {
	d := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	bpm, other := "bpm", "count/min"
	a := hourRow(d, 3, 180, 50, 70)
	b := a
	b.Source, b.StreamKey = "Watch", "oura|Watch"
	c := a
	c.Unit = &bpm
	e := a
	e.Unit = &other
	f := a
	f.Worn = 0
	empty := HourRow{Bucket: d, Provider: "fitbit", Slug: "fitbit", Worn: 1, N: 4}
	got, err := foldHours(hourSeq(a, b, c, e, f, a, empty, empty), time.Hour, time.UTC)
	require.NoError(t, err)
	require.Len(t, got, 6)
	assert.Equal(t, int64(6), got[0].Count, "the same stream twice is one bucket")
	assert.Equal(t, 360.0, got[0].Sum)
	assert.Equal(t, "Watch", got[1].Source)
	assert.Equal(t, &bpm, got[2].Unit)
	assert.Equal(t, &other, got[3].Unit)
	assert.Equal(t, figures{"2026-09-24T00:00:00Z", 8, 0, 0, 0}, figuresOf(got)[5], "no value: counted, no figure")
}

// America/Los_Angeles, across the change of 2026-11-01: the day is 25 hours
// long and starts at local midnight; the hour from 01:00 to 02:00 happens
// twice, and time_bucket with a zone keeps the two readings apart.
func TestFoldHoursAcrossTheDSTChange(t *testing.T) {
	la := zone(t, "America/Los_Angeles")
	// 07:00 UTC is midnight PDT; the clocks go back at 09:00 UTC.
	midnight := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	var rows []HourRow
	for h := range 26 {
		rows = append(rows, hourRow(midnight.Add(time.Duration(h)*time.Hour), 1, float64(h), float64(h), float64(h)))
	}
	byDay, err := foldHours(hourSeq(rows...), 24*time.Hour, la)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-11-01T00:00:00-07:00", 25, 300, 0, 24},
		{"2026-11-02T00:00:00-08:00", 1, 25, 25, 25},
	}, figuresOf(byDay), "a day of 25 hours")

	byHour, err := foldHours(hourSeq(rows[:4]...), time.Hour, la)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-11-01T00:00:00-07:00", 1, 0, 0, 0},
		{"2026-11-01T01:00:00-07:00", 1, 1, 1, 1},
		{"2026-11-01T01:00:00-08:00", 1, 2, 2, 2},
		{"2026-11-01T02:00:00-08:00", 1, 3, 3, 3},
	}, figuresOf(byHour), "the repeated hour is two buckets")

	// The samples of the same hours fold the same way.
	var ss []SampleRow
	for _, r := range rows {
		ss = append(ss, sampleRow(r.Bucket.Add(17*time.Minute), r.Sum))
	}
	sDay, err := foldSamples(sampleSeq(ss...), 24*time.Hour, la)
	require.NoError(t, err)
	assert.Equal(t, figuresOf(byDay), figuresOf(sDay))
	sHour, err := foldSamples(sampleSeq(ss[:4]...), time.Hour, la)
	require.NoError(t, err)
	assert.Equal(t, figuresOf(byHour), figuresOf(sHour))

	// Spring forward, 2026-03-08: a day of 23 hours.
	spring := time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC)
	rows = rows[:0]
	for h := range 24 {
		rows = append(rows, hourRow(spring.Add(time.Duration(h)*time.Hour), 1, 1, 1, 1))
	}
	byDay, err = foldHours(hourSeq(rows...), 24*time.Hour, la)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-03-08T00:00:00-08:00", 23, 23, 1, 1},
		{"2026-03-09T00:00:00-07:00", 1, 1, 1, 1},
	}, figuresOf(byDay))
}

// Review focus 1: a zone whose offset is not a whole number of hours. Its
// hours start at :30 UTC, so they are folded from the samples.
func TestFoldSamplesInHalfHourZones(t *testing.T) {
	d := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	ss := []SampleRow{
		sampleRow(d.Add(10*time.Minute), 60),              // 05:40 IST
		sampleRow(d.Add(29*time.Minute), 62),              // 05:59 IST
		sampleRow(d.Add(30*time.Minute), 70),              // 06:00 IST
		sampleRow(d.Add(18*time.Hour+29*time.Minute), 80), // 23:59 IST
		sampleRow(d.Add(18*time.Hour+30*time.Minute), 90), // 00:00 IST the next day
	}
	kolkata := zone(t, "Asia/Kolkata")
	byHour, err := foldSamples(sampleSeq(ss...), time.Hour, kolkata)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-09-24T05:00:00+05:30", 2, 122, 60, 62},
		{"2026-09-24T06:00:00+05:30", 1, 70, 70, 70},
		{"2026-09-24T23:00:00+05:30", 1, 80, 80, 80},
		{"2026-09-25T00:00:00+05:30", 1, 90, 90, 90},
	}, figuresOf(byHour))
	byDay, err := foldSamples(sampleSeq(ss...), 24*time.Hour, kolkata)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-09-24T00:00:00+05:30", 4, 272, 60, 80},
		{"2026-09-25T00:00:00+05:30", 1, 90, 90, 90},
	}, figuresOf(byDay))

	// Australia/Adelaide is at +09:30, and +10:30 from 2026-10-04 02:00.
	adelaide := zone(t, "Australia/Adelaide")
	change := time.Date(2026, 10, 3, 16, 30, 0, 0, time.UTC) // 02:00 ACST becomes 03:00 ACDT
	ss = []SampleRow{
		sampleRow(change.Add(-2*time.Hour), 1),                // 00:00 ACST
		sampleRow(change.Add(-time.Minute), 2),                // 01:59 ACST
		sampleRow(change, 3),                                  // 03:00 ACDT
		sampleRow(change.Add(20*time.Hour+59*time.Minute), 4), // 23:59 ACDT
		sampleRow(change.Add(21*time.Hour), 5),                // 00:00 the next day
	}
	byHour, err = foldSamples(sampleSeq(ss...), time.Hour, adelaide)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-10-04T00:00:00+09:30", 1, 1, 1, 1},
		{"2026-10-04T01:00:00+09:30", 1, 2, 2, 2},
		{"2026-10-04T03:00:00+10:30", 1, 3, 3, 3},
		{"2026-10-04T23:00:00+10:30", 1, 4, 4, 4},
		{"2026-10-05T00:00:00+10:30", 1, 5, 5, 5},
	}, figuresOf(byHour))
	byDay, err = foldSamples(sampleSeq(ss...), 24*time.Hour, adelaide)
	require.NoError(t, err)
	assert.Equal(t, []figures{
		{"2026-10-04T00:00:00+09:30", 4, 10, 1, 4},
		{"2026-10-05T00:00:00+10:30", 1, 5, 5, 5},
	}, figuresOf(byDay), "a day of 23 hours")
}

// A range of a day, a week and a month of hourly buckets folds into as many
// days, each the sum of its hours, and the samples of those hours fold into
// the same days; an empty range gives no bucket.
func TestFoldRanges(t *testing.T) {
	paris := zone(t, "Europe/Paris")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, paris)
	for _, days := range []int{1, 7, 30} {
		var rows []HourRow
		var ss []SampleRow
		for h := range days * 24 {
			at := start.Add(time.Duration(h) * time.Hour)
			rows = append(rows, hourRow(at, 2, float64(2*h+1), float64(h), float64(h+1)))
			ss = append(ss, sampleRow(at.Add(5*time.Minute), float64(h)), sampleRow(at.Add(50*time.Minute), float64(h+1)))
		}
		fromHours, err := foldHours(hourSeq(rows...), 24*time.Hour, paris)
		require.NoError(t, err)
		require.Len(t, fromHours, days)
		for i, b := range fromHours {
			assert.True(t, b.Start.Equal(start.AddDate(0, 0, i)), "day %d starts at %v", i, b.Start)
			assert.Equal(t, int64(48), b.Count)
			assert.Equal(t, float64(i*24), b.Min)
			assert.Equal(t, float64(i*24+24), b.Max)
		}
		fromSamples, err := foldSamples(sampleSeq(ss...), 24*time.Hour, paris)
		require.NoError(t, err)
		assert.Equal(t, figuresOf(fromHours), figuresOf(fromSamples), "%d days", days)

		hourly, err := foldSamples(sampleSeq(ss...), time.Hour, paris)
		require.NoError(t, err)
		assert.Len(t, hourly, days*24)
	}
	none, err := foldHours(hourSeq(), time.Hour, paris)
	require.NoError(t, err)
	assert.Empty(t, none)
	none, err = foldSamples(sampleSeq(), 24*time.Hour, paris)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// A row that fails to open ends the fold with its error: no bucket comes
// back short of it.
func TestFoldStopsOnAnError(t *testing.T) {
	boom := errors.New("row does not open")
	d := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	_, err := foldHours(func(yield func(HourRow, error) bool) {
		if yield(hourRow(d, 1, 1, 1, 1), nil) {
			yield(HourRow{}, boom)
		}
	}, time.Hour, time.UTC)
	assert.ErrorIs(t, err, boom)
	_, err = foldSamples(func(yield func(SampleRow, error) bool) { yield(SampleRow{}, boom) }, time.Hour, time.UTC)
	assert.ErrorIs(t, err, boom)
}

// The merged view keeps one stream per interval: by the order of sources,
// then worn on the body, then by stream key. The all view has every stream,
// the buckets of one provider, source and unit added up, in order.
func TestStatsView(t *testing.T) {
	d := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	row := func(at time.Time, provider, source, slug string, worn int, n int64) HourRow {
		return HourRow{Bucket: at, Provider: provider, Source: source, Slug: slug, Worn: worn,
			StreamKey: provider + "|" + source, N: n, HasValue: true, Sum: float64(n), Min: 1, Max: 1}
	}
	fold := func() []StatsBucket {
		bs, err := foldHours(hourSeq(
			row(d, "apple_health", "iPhone", "apple_health", 1, 5),
			row(d, "apple_health", "Watch", "apple_health", 0, 7),
			row(d, "oura", "Ring", "oura", 1, 9),
			row(d, "apple_health", "Band", "garmin", 1, 3),
			row(d.Add(time.Hour), "oura", "Ring", "oura", 1, 2),
			row(d.Add(time.Hour), "apple_health", "Band", "garmin", 0, 4),
		), time.Hour, time.UTC)
		require.NoError(t, err)
		return bs
	}
	rank := func(s string) int {
		return map[string]int{"Band": 0, "Ring": 1, "Watch": 2, "apple_health": 3, "iPhone": 4, "oura": 5,
			"apple_health|Band": 0, "apple_health|Watch": 1, "apple_health|iPhone": 2, "oura|Ring": 3}[s]
	}
	pick := func(bs []StatsBucket) []string {
		var out []string
		for _, b := range bs {
			out = append(out, b.Start.Format("15")+" "+b.Provider+" "+b.Source)
		}
		return out
	}

	assert.Equal(t, []string{"00 apple_health Watch", "01 oura Ring"},
		pick(statsView(fold(), ViewMerged, []string{"apple_health", "oura"}, rank)), "the watch before the phone")
	assert.Equal(t, []string{"00 oura Ring", "01 oura Ring"},
		pick(statsView(fold(), ViewMerged, []string{"oura", "apple_health"}, rank)), "the user's order of sources")
	assert.Equal(t, []string{"00 apple_health Band", "01 apple_health Band"},
		pick(statsView(fold(), ViewMerged, []string{"garmin"}, rank)), "ranked by the source, not the provider")
	// No source is in the order: worn first, then the stream key.
	assert.Equal(t, []string{"00 apple_health Watch", "01 apple_health Band"},
		pick(statsView(fold(), ViewMerged, nil, rank)))

	all := statsView(fold(), ViewAll, nil, rank)
	assert.Equal(t, []string{"00 apple_health Band", "00 apple_health Watch", "00 apple_health iPhone", "00 oura Ring",
		"01 apple_health Band", "01 oura Ring"}, pick(all))
	assert.Equal(t, int64(3), all[0].Count)

	// Two buckets of one provider, source and unit that differ in slug or
	// wear are one row of the all view.
	bs, err := foldHours(hourSeq(
		row(d, "apple_health", "Band", "garmin", 1, 3),
		row(d, "apple_health", "Band", "apple_health", 0, 4),
	), time.Hour, time.UTC)
	require.NoError(t, err)
	require.Len(t, bs, 2)
	merged := statsView(bs, ViewAll, nil, rank)
	require.Len(t, merged, 1)
	assert.Equal(t, int64(7), merged[0].Count)
	assert.Equal(t, 7.0, merged[0].Sum)

	// A missing unit sorts last.
	bpm := "bpm"
	with := row(d, "oura", "Ring", "oura", 1, 1)
	with.Unit = &bpm
	bs, err = foldHours(hourSeq(row(d, "oura", "Ring", "oura", 1, 1), with), time.Hour, time.UTC)
	require.NoError(t, err)
	sorted := statsView(bs, ViewAll, nil, func(string) int { return 0 })
	require.Len(t, sorted, 2)
	assert.NotNil(t, sorted[0].Unit)
	assert.Nil(t, sorted[1].Unit)
}

// wallInstant resolves a wall clock time as Postgres does: the second of
// two readings when the clocks go back, the offset before the change for a
// time the clocks skip.
func TestWallInstant(t *testing.T) {
	la := zone(t, "America/Los_Angeles")
	twice := wallInstant(2026, time.November, 1, 1, la)
	assert.True(t, twice.Equal(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)), "%v", twice)
	skipped := wallInstant(2026, time.March, 8, 2, la)
	assert.True(t, skipped.Equal(time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC)), "%v", skipped)
	plain := wallInstant(2026, time.June, 1, 12, la)
	assert.True(t, plain.Equal(time.Date(2026, 6, 1, 19, 0, 0, 0, time.UTC)), "%v", plain)
}
