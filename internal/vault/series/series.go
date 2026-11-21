// SPDX-License-Identifier: AGPL-3.0-only

// Package series stores a whole series (the voltages of one ECG recording,
// the route of one workout, the beats of one heartbeat series) as one blob
// per parent instead of one row per point (docs/data-model.md, "Series
// blobs"). A blob is a versioned columnar encoding, compressed with zstd; the
// ETL then stores it in ohs_data.series_blobs, sealed under the user's data
// key like any sealed row (vault.Schema["series_blobs"]) or, without keys,
// as it is.
//
// Every value comes back bit for bit: a float column is stored as scaled
// integers only when every value of it round-trips exactly, as the raw IEEE
// 754 bits otherwise, and an instant to the microsecond Postgres kept.
package series

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"
)

// Kind is the kind of a series, the kind column of ohs_data.series_blobs.
type Kind int16

const (
	// ECG is the voltages of one ECG recording: columns offset (seconds
	// from the recording's start) and voltage (microvolts). No time column:
	// a sample's time is its recording's start plus its offset.
	ECG Kind = 1
	// Route is the GPS fixes of one workout: columns latitude, longitude,
	// altitude (m), speed (m/s) and course (degrees), the last three
	// nullable.
	Route Kind = 2
	// Heartbeat is the beats of one heartbeat series: column rr (ms).
	Heartbeat Kind = 3
)

// Column indexes of each kind.
const (
	ECGOffset  = 0
	ECGVoltage = 1

	RouteLatitude  = 0
	RouteLongitude = 1
	RouteAltitude  = 2
	RouteSpeed     = 3
	RouteCourse    = 4

	HeartbeatRR = 0
)

// MaxCols is the most value columns a kind has.
const MaxCols = 5

// spec describes a kind: its value columns, whether its points carry a
// time, and how many leading columns (after the time) identify a point.
type spec struct {
	cols, identity int
	timed          bool
	name           string
}

var specs = map[Kind]spec{
	ECG:       {cols: 2, identity: 2, timed: false, name: "ecg"},
	Route:     {cols: 5, identity: 3, timed: true, name: "route"},
	Heartbeat: {cols: 1, identity: 1, timed: true, name: "heartbeat"},
}

func (k Kind) spec() (spec, error) {
	s, ok := specs[k]
	if !ok {
		return spec{}, fmt.Errorf("series: unknown kind %d", k)
	}
	return s, nil
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { _, ok := specs[k]; return ok }

// String is the kind's name.
func (k Kind) String() string {
	if s, ok := specs[k]; ok {
		return s.name
	}
	return fmt.Sprintf("kind(%d)", k)
}

// Point is one point of a series.
type Point struct {
	// At is the point's time in microseconds since the Unix epoch (UTC), the
	// grain Postgres keeps; 0 for an ECG sample.
	At int64
	// V holds the kind's value columns in order; a NULL value is 0 with its
	// bit set in Null.
	V    [MaxCols]float64
	Null uint8
	// Capture is when the raw object that brought the point was captured,
	// in microseconds since the epoch, 0 when unknown. It settles which copy
	// of a point wins when two copies differ in a column that does not
	// identify the point (see Merge).
	Capture int64
}

// IsNull reports whether column c of p is NULL.
func (p Point) IsNull(c int) bool { return p.Null&(1<<c) != 0 }

// Value returns column c of p, nil when NULL.
func (p Point) Value(c int) *float64 {
	if p.IsNull(c) {
		return nil
	}
	v := p.V[c]
	return &v
}

// Set sets column c of p to v, NULL when v is nil.
func (p *Point) Set(c int, v *float64) {
	if v == nil {
		p.V[c] = 0
		p.Null |= 1 << c
		return
	}
	p.V[c] = *v
	p.Null &^= 1 << c
}

// Time is p.At as a UTC time.
func (p Point) Time() time.Time { return time.UnixMicro(p.At).UTC() }

// Micro is the microsecond value Postgres stores for t (pgx truncates the
// nanoseconds the same way).
func Micro(t time.Time) int64 { return t.UnixMicro() }

// key is the identity of a point: its time and its identifying columns, bit
// for bit, with their NULL bits.
type key struct {
	at   int64
	v    [3]uint64
	null uint8
}

func (s spec) key(p Point) key {
	k := key{null: p.Null & (1<<s.identity - 1)}
	if s.timed {
		k.at = p.At
	}
	for c := 0; c < s.identity; c++ {
		k.v[c] = math.Float64bits(p.V[c])
	}
	return k
}

// sameRest reports whether a and b agree on every column that does not
// identify a point.
func (s spec) sameRest(a, b Point) bool {
	for c := s.identity; c < s.cols; c++ {
		if a.IsNull(c) != b.IsNull(c) || math.Float64bits(a.V[c]) != math.Float64bits(b.V[c]) {
			return false
		}
	}
	return true
}

// MergeStats is what Merge did with the incoming points.
type MergeStats struct {
	// Added is the points that were not stored, Replaced the stored points
	// whose other columns a newer copy changed, Skipped the copies of stored
	// points that changed nothing.
	Added, Replaced, Skipped int
}

// newer reports whether a copy captured at incoming replaces one captured at
// stored: a known capture time after the stored one, or after an unknown one.
// An equal time is not newer, and an unknown time never replaces, so a
// point's first copy stays unless a later capture says otherwise, as the
// per-point rows of earlier releases kept the first copy (ON CONFLICT DO
// NOTHING).
func newer(stored, incoming int64) bool {
	return incoming != 0 && (stored == 0 || incoming > stored)
}

// Merge returns stored with the points of in added: the union of the two,
// each point once, sorted (Sort). A point of in whose identity (time and
// identifying columns, see Kind) is stored already replaces the stored copy
// only when it differs in another column (the speed or course of a route
// fix) and comes from a newer capture (newer). Merging is order independent
// for points of known, distinct capture times. stored is not modified.
func Merge(k Kind, stored, in []Point) ([]Point, MergeStats, error) {
	s, err := k.spec()
	if err != nil {
		return nil, MergeStats{}, err
	}
	out := make([]Point, len(stored), len(stored)+len(in))
	copy(out, stored)
	index := make(map[key]int, len(stored)+len(in))
	for i, p := range out {
		if _, dup := index[s.key(p)]; !dup {
			index[s.key(p)] = i
		}
	}
	var st MergeStats
	for _, p := range in {
		kk := s.key(p)
		i, ok := index[kk]
		if !ok {
			index[kk] = len(out)
			out = append(out, p)
			st.Added++
			continue
		}
		if s.sameRest(out[i], p) || !newer(out[i].Capture, p.Capture) {
			st.Skipped++
			continue
		}
		out[i] = p
		st.Replaced++
	}
	// A stored list with the same point twice (rows stored clear and sealed
	// before a conversion) keeps its first copy.
	if len(index) < len(out) {
		seen := make(map[key]bool, len(index))
		kept := out[:0]
		for _, p := range out {
			kk := s.key(p)
			if seen[kk] {
				continue
			}
			seen[kk] = true
			kept = append(kept, p)
		}
		out = kept
	}
	sortPoints(s, out)
	return out, st, nil
}

// Sort puts pts in series order: by time, by offset for an ECG, the order
// they came in among equals.
func Sort(k Kind, pts []Point) error {
	s, err := k.spec()
	if err != nil {
		return err
	}
	sortPoints(s, pts)
	return nil
}

func sortPoints(s spec, pts []Point) {
	slices.SortStableFunc(pts, func(a, b Point) int {
		if s.timed {
			return cmp.Compare(a.At, b.At)
		}
		return cmp.Compare(a.V[ECGOffset], b.V[ECGOffset])
	})
}

// Span is the first and last time of pts, in series order (zero times for
// no point, or an ECG, whose points carry no time).
func Span(pts []Point) (first, last time.Time) {
	if len(pts) == 0 || (pts[0].At == 0 && pts[len(pts)-1].At == 0) {
		return time.Time{}, time.Time{}
	}
	return pts[0].Time(), pts[len(pts)-1].Time()
}
