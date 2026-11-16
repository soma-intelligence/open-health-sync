// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"iter"
	"math"
	"slices"
	"time"
)

// Statistics computed in Go (docs/specs/vault.md, "Aggregates move to Go").
//
// SQL cannot sum a sealed value. It selects, on clear columns, either the
// sealed hourly buckets of the range (ohs_data.quantity_hourly_sealed, which
// the ETL keeps exact) or, in a zone whose offset is not a whole number of
// hours, the samples; the rows are opened and folded here into the intervals
// of the request, in its time zone. The fold gives one bucket per interval
// and stream, as the inner GROUP BY of the SQL path did; statsView then
// keeps what the view asks for.

// HourRow is one opened hourly bucket: the statistics of the samples of one
// stream that start in [Bucket, Bucket + 1 hour).
type HourRow struct {
	Bucket   time.Time
	Provider string
	// Source is the stream's source string, Slug its source for the merged
	// view's order (coalesce(source_slug, provider)).
	Source, Slug string
	// Worn is 0 for a stream worn on the body and 1 otherwise.
	Worn int
	// StreamKey breaks a tie between two streams that rank alike.
	StreamKey string
	Unit      *string
	N         int64
	// HasValue is false for a bucket whose samples carry no value: Sum, Min
	// and Max are then not set.
	HasValue      bool
	Sum, Min, Max float64
}

// SampleRow is one opened quantity sample, with the keys of its stream.
type SampleRow struct {
	Start        time.Time
	Provider     string
	Source, Slug string
	Worn         int
	StreamKey    string
	Unit         *string
	Value        *float64
}

// statGroup identifies one bucket of the fold: an interval and a stream.
type statGroup struct {
	start    int64 // UnixMicro of the interval's start
	provider string
	source   string
	slug     string
	worn     int
	unit     string
	hasUnit  bool
}

type folder struct {
	interval time.Duration
	zone     *time.Location
	index    map[statGroup]int
	out      []StatsBucket
	// lastStart and lastEnd remember the interval of the last row, up to
	// the next change of the zone's offset: rows arrive in time order, so
	// most fall in it.
	lastStart, lastEnd time.Time
	hasLast            bool
}

func newFolder(interval time.Duration, zone *time.Location) *folder {
	if zone == nil {
		zone = time.UTC
	}
	return &folder{interval: interval, zone: zone, index: map[statGroup]int{}}
}

// start is the start of the interval holding t.
func (f *folder) start(t time.Time) time.Time {
	if f.hasLast && !t.Before(f.lastStart) && t.Before(f.lastEnd) {
		return f.lastStart
	}
	s := intervalStart(t, f.interval, f.zone)
	e := s.Add(f.interval)
	// Inside one offset of the zone the wall clock runs with the instants,
	// so every instant from s to the end of the interval, or to the next
	// change of offset, starts at s.
	if _, change := s.ZoneBounds(); !change.IsZero() && change.Before(e) {
		e = change
	}
	f.lastStart, f.lastEnd, f.hasLast = s, e, true
	return s
}

// bucket returns the bucket of a row, made when new.
func (f *folder) bucket(at time.Time, provider, source, slug string, worn int, key string, unit *string) *StatsBucket {
	start := f.start(at)
	g := statGroup{start: start.UnixMicro(), provider: provider, source: source, slug: slug, worn: worn}
	if unit != nil {
		g.unit, g.hasUnit = *unit, true
	}
	i, ok := f.index[g]
	if !ok {
		i = len(f.out)
		f.index[g] = i
		f.out = append(f.out, StatsBucket{Start: start.In(f.zone), Provider: provider, Source: source, Unit: unit,
			slug: slug, worn: worn, key: key})
	}
	return &f.out[i]
}

// add folds n samples with their sum, minimum and maximum into b; has is
// false when the samples carry no value.
func (b *StatsBucket) add(n int64, has bool, sum, low, high float64) {
	b.Count += n
	if !has {
		return
	}
	if !b.valued {
		b.Min, b.Max, b.valued = low, high, true
	} else {
		b.Min, b.Max = math.Min(b.Min, low), math.Max(b.Max, high)
	}
	b.Sum += sum
}

// result returns the buckets by interval, in the order their streams were
// first met inside it.
func (f *folder) result() []StatsBucket {
	slices.SortStableFunc(f.out, func(a, b StatsBucket) int { return a.Start.Compare(b.Start) })
	return f.out
}

// foldHours folds hourly buckets into intervals of interval (time.Hour or
// 24 hours, a calendar day) in zone: one StatsBucket per interval and stream
// (provider, source, slug, worn and unit), by interval. The hours must be
// whole hours of zone (see wholeHourOffsets): an hour is put in the interval
// its start falls in.
func foldHours(rows iter.Seq2[HourRow, error], interval time.Duration, zone *time.Location) ([]StatsBucket, error) {
	f := newFolder(interval, zone)
	for r, err := range rows {
		if err != nil {
			return nil, err
		}
		f.bucket(r.Bucket, r.Provider, r.Source, r.Slug, r.Worn, r.StreamKey, r.Unit).add(r.N, r.HasValue, r.Sum, r.Min, r.Max)
	}
	return f.result(), nil
}

// foldSamples folds samples into intervals of interval in zone, as
// foldHours does with hours: a sample is put in the interval its start
// falls in, whatever the zone's offset.
func foldSamples(rows iter.Seq2[SampleRow, error], interval time.Duration, zone *time.Location) ([]StatsBucket, error) {
	f := newFolder(interval, zone)
	for r, err := range rows {
		if err != nil {
			return nil, err
		}
		b := f.bucket(r.Start, r.Provider, r.Source, r.Slug, r.Worn, r.StreamKey, r.Unit)
		if r.Value == nil {
			b.add(1, false, 0, 0, 0)
		} else {
			b.add(1, true, *r.Value, *r.Value, *r.Value)
		}
	}
	return f.result(), nil
}

// intervalStart is the start of the interval of length interval (an hour, or
// 24 hours for a calendar day) that holds t in zone, as time_bucket with a
// time zone gives it.
//
// A day starts at the midnight of t's wall clock in zone, so a day is 23 or
// 25 hours long when the clocks change. An hour is counted on the instants,
// from the wall clock at the offset in force at t: an hour starts where the
// wall clock reads a whole hour, also in a zone at a half hour offset, and
// the hour that reads the same twice when the clocks go back is two
// intervals, one per reading.
func intervalStart(t time.Time, interval time.Duration, zone *time.Location) time.Time {
	if zone == time.UTC {
		return t.UTC().Truncate(interval)
	}
	l := t.In(zone)
	if interval >= 24*time.Hour {
		return wallInstant(l.Year(), l.Month(), l.Day(), 0, zone)
	}
	_, offset := l.Zone()
	shift := time.Duration(offset) * time.Second
	return t.Add(shift).Truncate(interval).Add(-shift).In(zone)
}

// wallInstant is the instant at which the wall clock of zone reads the given
// hour of the given day. A wall clock time that happens twice (clocks going
// back) is the second one; one that never happens (clocks going forward) is
// read with the offset in force before the change. Both are what Postgres
// does for timestamp AT TIME ZONE.
func wallInstant(year int, month time.Month, day, hour int, zone *time.Location) time.Time {
	wall := time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	guess := time.Date(year, month, day, hour, 0, 0, 0, zone)
	start, end := guess.ZoneBounds()
	_, cur := guess.Zone()
	offsets := []int{cur}
	prev := cur
	if !start.IsZero() {
		_, prev = start.Add(-time.Nanosecond).In(zone).Zone()
		offsets = append(offsets, prev)
	}
	if !end.IsZero() {
		_, next := end.In(zone).Zone()
		offsets = append(offsets, next)
	}
	var best time.Time
	found := false
	for _, off := range offsets {
		c := wall.Add(-time.Duration(off) * time.Second)
		if _, o := c.In(zone).Zone(); o != off {
			continue
		}
		if !found || c.After(best) {
			best, found = c, true
		}
	}
	if found {
		return best.In(zone)
	}
	// The wall clock skips this time. The change is the one next to guess:
	// read the time with the offset before it.
	if !start.IsZero() && !wall.Add(-time.Duration(prev)*time.Second).Before(start) &&
		wall.Add(-time.Duration(cur)*time.Second).Before(start) {
		return wall.Add(-time.Duration(prev) * time.Second).In(zone)
	}
	return wall.Add(-time.Duration(cur) * time.Second).In(zone)
}

// statsView keeps of the fold what view asks for, in the order of the SQL
// path. The all view has one bucket per interval, provider, source and unit,
// ordered by them. The merged view has one per interval: the stream the
// merged reads rank first, by the order of sources, then worn on the body,
// then by stream key. rank orders text as the database does.
func statsView(buckets []StatsBucket, view View, order []string, rank func(string) int) []StatsBucket {
	if view == ViewAll {
		type key struct {
			start    int64
			provider string
			source   string
			unit     string
			hasUnit  bool
		}
		index := map[key]int{}
		var out []StatsBucket
		for _, b := range buckets {
			k := key{start: b.Start.UnixMicro(), provider: b.Provider, source: b.Source}
			if b.Unit != nil {
				k.unit, k.hasUnit = *b.Unit, true
			}
			i, ok := index[k]
			if !ok {
				index[k] = len(out)
				nb := b
				nb.Count, nb.Sum, nb.Min, nb.Max, nb.valued = 0, 0, 0, 0, false
				out = append(out, nb)
				i = len(out) - 1
			}
			out[i].add(b.Count, b.valued, b.Sum, b.Min, b.Max)
		}
		slices.SortStableFunc(out, func(a, b StatsBucket) int {
			if c := a.Start.Compare(b.Start); c != 0 {
				return c
			}
			if c := rank(a.Provider) - rank(b.Provider); c != 0 {
				return c
			}
			if c := rank(a.Source) - rank(b.Source); c != 0 {
				return c
			}
			// A missing unit sorts last, as NULL does.
			switch {
			case a.Unit == nil && b.Unit == nil:
				return 0
			case a.Unit == nil:
				return 1
			case b.Unit == nil:
				return -1
			}
			return rank(*a.Unit) - rank(*b.Unit)
		})
		return out
	}
	var out []StatsBucket
	better := func(a, b *StatsBucket) bool {
		if ra, rb := sourceRank(order, a.slug), sourceRank(order, b.slug); ra != rb {
			return ra < rb
		}
		if a.worn != b.worn {
			return a.worn < b.worn
		}
		return rank(a.key) < rank(b.key)
	}
	for i := range buckets {
		b := &buckets[i]
		if n := len(out); n > 0 && out[n-1].Start.Equal(b.Start) {
			if better(b, &out[n-1]) {
				out[n-1] = *b
			}
			continue
		}
		out = append(out, *b)
	}
	return out
}
