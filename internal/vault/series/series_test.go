// SPDX-License-Identifier: AGPL-3.0-only

package series

import (
	"bytes"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ptr(v float64) *float64 { return &v }

// sameBits fails unless want and got hold the same points, bit for bit.
func sameBits(t *testing.T, want, got []Point) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].At, got[i].At, i)
		require.Equal(t, want[i].Null, got[i].Null, i)
		require.Equal(t, want[i].Capture, got[i].Capture, i)
		for c := range MaxCols {
			require.Equal(t, math.Float64bits(want[i].V[c]), math.Float64bits(got[i].V[c]), "point %d column %d", i, c)
		}
	}
}

func roundTrip(t *testing.T, k Kind, pts []Point) []byte {
	t.Helper()
	blob, err := Encode(k, pts)
	require.NoError(t, err)
	got, err := Decode(k, blob)
	require.NoError(t, err)
	sameBits(t, pts, got)
	return blob
}

func TestRoundTripECG(t *testing.T) {
	// An Apple Watch recording: offsets k/512, volts times a million (not a
	// short decimal), and a Withings one: offsets k/300, whole microvolts.
	var apple, withings []Point
	for k := range 15360 {
		v := math.Round(0.0004*math.Sin(float64(k)/40)*1e9) / 1e9 * 1_000_000
		apple = append(apple, Point{V: [MaxCols]float64{float64(k) / 512, v}})
	}
	for k := range 9000 {
		withings = append(withings, Point{V: [MaxCols]float64{float64(k) / 300, float64(k%400 - 200)}})
	}
	blob := roundTrip(t, ECG, apple)
	t.Logf("apple ecg: %d points, %d bytes, %.2f bytes/point", len(apple), len(blob), float64(len(blob))/float64(len(apple)))
	require.Less(t, len(blob), 9*len(apple))
	blob = roundTrip(t, ECG, withings)
	t.Logf("withings ecg: %d points, %d bytes, %.2f bytes/point", len(withings), len(blob), float64(len(blob))/float64(len(withings)))
	require.Less(t, len(blob), 3*len(withings))
}

func TestRoundTripRoute(t *testing.T) {
	start := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
	var pts []Point
	lat, lon := 48.8566, 2.3522
	for k := range 7200 {
		lat += 0.00002 * math.Cos(float64(k)/300)
		lon += 0.00003 * math.Sin(float64(k)/400)
		p := Point{At: Micro(start.Add(time.Duration(k) * time.Second)), Capture: Micro(start)}
		p.V[RouteLatitude], p.V[RouteLongitude] = lat, lon
		p.Set(RouteAltitude, ptr(35+float64(k%50)/10))
		if k%3 == 0 {
			p.Set(RouteSpeed, nil)
		} else {
			p.Set(RouteSpeed, ptr(3.25))
		}
		p.Set(RouteCourse, nil)
		pts = append(pts, p)
	}
	blob := roundTrip(t, Route, pts)
	t.Logf("route: %d points, %d bytes, %.2f bytes/point", len(pts), len(blob), float64(len(blob))/float64(len(pts)))
	require.Less(t, len(blob), 24*len(pts))
}

func TestRoundTripOddValues(t *testing.T) {
	odd := []float64{0, math.Copysign(0, -1), math.NaN(), math.Inf(1), math.Inf(-1), math.SmallestNonzeroFloat64,
		math.MaxFloat64, 0.1 + 0.2, 1e300, -1e-300, 123.456, 1 << 60}
	var pts []Point
	for i, v := range odd {
		pts = append(pts, Point{At: int64(i) - 3, V: [MaxCols]float64{v}, Capture: int64(-i)})
	}
	roundTrip(t, Heartbeat, pts)
	// A column of short decimals with one negative zero keeps the zero's sign.
	pts = []Point{{V: [MaxCols]float64{1.5, 2}}, {V: [MaxCols]float64{math.Copysign(0, -1), 3}}}
	roundTrip(t, ECG, pts)
	roundTrip(t, Route, nil)
	roundTrip(t, Heartbeat, []Point{{At: math.MinInt64}, {At: math.MaxInt64}})
}

func TestRoundTripRandom(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		n := r.IntN(300)
		pts := make([]Point, n)
		for i := range pts {
			pts[i].At = r.Int64N(1<<50) - 1<<49
			pts[i].Capture = r.Int64N(1 << 52)
			for c := range 5 {
				switch r.IntN(4) {
				case 0:
					pts[i].Set(c, nil)
				case 1:
					pts[i].V[c] = float64(r.IntN(100000)) / 1000
				default:
					pts[i].V[c] = math.Float64frombits(r.Uint64())
				}
			}
		}
		roundTrip(t, Route, pts)
	}
}

func TestDecodeRejects(t *testing.T) {
	blob, err := Encode(Heartbeat, []Point{{At: 1, V: [MaxCols]float64{800}}, {At: 2, V: [MaxCols]float64{810}}})
	require.NoError(t, err)
	_, err = Decode(Heartbeat, nil)
	require.ErrorIs(t, err, ErrCorrupt)
	_, err = Decode(Heartbeat, append([]byte{9}, blob[1:]...))
	require.ErrorIs(t, err, ErrCorrupt)
	_, err = Decode(Heartbeat, blob[:len(blob)-1])
	require.ErrorIs(t, err, ErrCorrupt)
	_, err = Decode(Kind(99), blob)
	require.Error(t, err)
	// Trailing bytes inside the frame.
	plain, err := decoder().DecodeAll(blob[1:], nil)
	require.NoError(t, err)
	_, err = Decode(Heartbeat, encoder().EncodeAll(append(plain, 0), []byte{Format}))
	require.ErrorIs(t, err, ErrCorrupt)
}

func FuzzDecode(f *testing.F) {
	blob, _ := Encode(Route, []Point{{At: 5, V: [MaxCols]float64{1, 2, 3, 4, 5}, Null: 1 << 4}})
	f.Add(blob)
	f.Add([]byte{Format})
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, k := range []Kind{ECG, Route, Heartbeat} {
			pts, err := Decode(k, b)
			if err != nil {
				continue
			}
			again, err := Encode(k, pts)
			require.NoError(t, err)
			back, err := Decode(k, again)
			require.NoError(t, err)
			require.Len(t, back, len(pts))
		}
	})
}

func TestMergeUnionAndDedup(t *testing.T) {
	a := []Point{{At: 1, V: [MaxCols]float64{800}}, {At: 3, V: [MaxCols]float64{820}}}
	b := []Point{{At: 2, V: [MaxCols]float64{810}}, {At: 3, V: [MaxCols]float64{820}}, {At: 3, V: [MaxCols]float64{821}}}
	got, st, err := Merge(Heartbeat, a, b)
	require.NoError(t, err)
	require.Equal(t, MergeStats{Added: 2, Skipped: 1}, st)
	require.Equal(t, []int64{1, 2, 3, 3}, []int64{got[0].At, got[1].At, got[2].At, got[3].At})
	require.Equal(t, 820.0, got[2].V[0], "equal times keep the order they came in")
	// The stored list is not modified.
	require.Len(t, a, 2)

	// Merging in the other order gives the same series.
	other, _, err := Merge(Heartbeat, b, a)
	require.NoError(t, err)
	encA, _ := Encode(Heartbeat, got)
	encB, _ := Encode(Heartbeat, other)
	require.True(t, bytes.Equal(encA, encB))
}

func TestMergeCaptureGuard(t *testing.T) {
	fix := func(speed float64, capture int64) Point {
		p := Point{At: 10, Capture: capture}
		p.V[RouteLatitude], p.V[RouteLongitude] = 1, 2
		p.Set(RouteAltitude, nil)
		p.Set(RouteSpeed, &speed)
		p.Set(RouteCourse, nil)
		return p
	}
	cases := []struct {
		name       string
		stored, in Point
		want       float64
		replaced   int
	}{
		{"newer capture replaces", fix(1, 100), fix(2, 200), 2, 1},
		{"older capture is held back", fix(1, 200), fix(2, 100), 1, 0},
		{"equal capture keeps the first", fix(1, 100), fix(2, 100), 1, 0},
		{"unknown incoming never replaces", fix(1, 100), fix(2, 0), 1, 0},
		{"known replaces unknown", fix(1, 0), fix(2, 100), 2, 1},
	}
	for _, c := range cases {
		got, st, err := Merge(Route, []Point{c.stored}, []Point{c.in})
		require.NoError(t, err, c.name)
		require.Len(t, got, 1, c.name)
		require.Equal(t, c.want, got[0].V[RouteSpeed], c.name)
		require.Equal(t, c.replaced, st.Replaced, c.name)
	}
	// Three parts in any order end with the newest copy.
	parts := [][]Point{{fix(1, 100)}, {fix(3, 300)}, {fix(2, 200)}}
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		var s []Point
		for _, i := range order {
			s, _, _ = Merge(Route, s, parts[i])
		}
		require.Equal(t, 3.0, s[0].V[RouteSpeed], order)
		require.EqualValues(t, 300, s[0].Capture, order)
	}
}

func TestMergeDropsStoredTwins(t *testing.T) {
	stored := []Point{{V: [MaxCols]float64{0, 5}}, {V: [MaxCols]float64{0, 5}}, {V: [MaxCols]float64{1.0 / 512, 6}}}
	got, st, err := Merge(ECG, stored, nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, MergeStats{}, st)
}
