// SPDX-License-Identifier: AGPL-3.0-only

package synth

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"time"
)

// Rand is a deterministic random source for one named stream of one seed: the
// same seed and name give the same numbers whatever else was drawn before, so
// adding a provider or a scenario never changes the bytes of another.
type Rand struct{ r *rand.Rand }

// NewRand returns the stream name of seed.
func NewRand(seed uint64, name string) *Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return &Rand{r: rand.New(rand.NewPCG(seed, h.Sum64()))}
}

// Float returns a number in [0, 1).
func (r *Rand) Float() float64 { return r.r.Float64() }

// Range returns a number in [lo, hi).
func (r *Rand) Range(lo, hi float64) float64 { return lo + (hi-lo)*r.r.Float64() }

// Norm returns a normal number of mean m and standard deviation sd.
func (r *Rand) Norm(m, sd float64) float64 { return m + sd*r.r.NormFloat64() }

// Intn returns an integer in [0, n).
func (r *Rand) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return r.r.IntN(n)
}

// Chance reports true with probability p.
func (r *Rand) Chance(p float64) bool { return r.r.Float64() < p }

// Pick returns one of xs.
func Pick[T any](r *Rand, xs ...T) T { return xs[r.Intn(len(xs))] }

// Hex returns n random hexadecimal characters.
func (r *Rand) Hex(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[r.Intn(16)]
	}
	return string(b)
}

// UUID returns a random version 4 UUID in upper case, as HealthKit and most
// vendors print them.
func (r *Rand) UUID() string {
	h := []byte(r.Hex(32))
	h[12] = '4'
	h[16] = "89ab"[r.Intn(4)]
	s := string(h)
	return upper(s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32])
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// hash01 maps (seed, stream, i) to [0, 1) without state, for series sampled
// at any rate: a device reading heart rate every second and another every five
// minutes see the same underlying signal.
func hash01(seed uint64, stream string, i int64) float64 {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], seed)
	binary.LittleEndian.PutUint64(b[8:], uint64(i))
	h := fnv.New64a()
	_, _ = h.Write([]byte(stream))
	_, _ = h.Write(b[:])
	x := h.Sum64()
	// splitmix64 finaliser: fnv alone is poorly mixed in the low bits.
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / float64(1<<53)
}

// valueNoise is smooth noise in [-1, 1] over time: random values every period,
// cosine interpolated between them.
func valueNoise(seed uint64, stream string, t time.Time, period time.Duration) float64 {
	p := int64(period)
	n := t.UnixNano()
	i := n / p
	if n < 0 && n%p != 0 {
		i--
	}
	f := float64(n-i*p) / float64(p)
	a := hash01(seed, stream, i)*2 - 1
	b := hash01(seed, stream, i+1)*2 - 1
	w := (1 - math.Cos(f*math.Pi)) / 2
	return a*(1-w) + b*w
}

// round returns x rounded to n decimals.
func round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}

// Round is round for renderers.
func Round(x float64, n int) float64 { return round(x, n) }

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }
