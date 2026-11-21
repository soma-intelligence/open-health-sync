// SPDX-License-Identifier: AGPL-3.0-only

package series

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Format is the version of the blob layout, stored in the format column of
// ohs_data.series_blobs and in the blob's first byte. A new layout takes a
// new value; a reader keeps decoding every format still stored.
const Format = 1

// MaxPoints bounds the points of one blob a decoder accepts, and
// maxPlain the bytes a blob may decompress to: a corrupt or hostile blob
// cannot make a reader allocate without limit.
const (
	MaxPoints = 50_000_000
	maxPlain  = 1 << 30
)

// ErrCorrupt is a blob that does not decode.
var ErrCorrupt = errors.New("series: blob does not decode")

// A blob is
//
//	Format (1 byte) || zstd frame of the plain encoding
//
// and the plain encoding (format 1) is
//
//	uvarint point count n
//	time column           (timed kinds only)  int column
//	value columns         one float column per column of the kind
//	capture column        int column
//
// An int column is a byte naming its transform (1: deltas, 2: deltas of
// deltas), then n zigzag varints. A float column is a presence byte (0: every
// value present, 1: a bitmap of ceil(n/8) bytes follows, bit set when present,
// 2: every value NULL) and, unless every value is NULL, a mode byte for the
// m present values:
//
//	0  decimal: a byte d, then an int column of the m integers k with
//	   value = float64(k) / 10^d, bit for bit (the encoder checks every one)
//	1  raw: the IEEE 754 bits of each value XOR those of the previous one (0
//	   before the first), as 8 byte planes of m bytes, most significant first
//
// Columnar deltas leave long runs of small numbers that zstd compresses far
// below the 8 bytes of a double.
const (
	transDelta  = 1
	transDelta2 = 2

	presentAll    = 0
	presentBitmap = 1
	presentNone   = 2

	modeDecimal = 0
	modeRaw     = 1
)

// pow10 are the scales the decimal mode tries; each is exact in a float64.
var pow10 = [...]float64{1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15}

var (
	encOnce sync.Once
	enc     *zstd.Encoder
	decOnce sync.Once
	dec     *zstd.Decoder
)

func encoder() *zstd.Encoder {
	encOnce.Do(func() {
		var err error
		enc, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
		if err != nil {
			panic("series: zstd encoder: " + err.Error())
		}
	})
	return enc
}

func decoder() *zstd.Decoder {
	decOnce.Do(func() {
		var err error
		dec, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(maxPlain))
		if err != nil {
			panic("series: zstd decoder: " + err.Error())
		}
	})
	return dec
}

// Encode returns the blob of pts, a series of kind k in series order (Sort).
func Encode(k Kind, pts []Point) ([]byte, error) {
	s, err := k.spec()
	if err != nil {
		return nil, err
	}
	plain := binary.AppendUvarint(nil, uint64(len(pts)))
	ints := make([]int64, len(pts))
	if s.timed {
		for i, p := range pts {
			ints[i] = p.At
		}
		plain = appendInts(plain, ints)
	}
	vals := make([]float64, 0, len(pts))
	for c := 0; c < s.cols; c++ {
		vals = vals[:0]
		present := make([]bool, len(pts))
		for i, p := range pts {
			if !p.IsNull(c) {
				present[i] = true
				vals = append(vals, p.V[c])
			}
		}
		plain = appendFloats(plain, present, vals)
	}
	for i, p := range pts {
		ints[i] = p.Capture
	}
	plain = appendInts(plain, ints)
	out := []byte{Format}
	return encoder().EncodeAll(plain, out), nil
}

// Decode reverses Encode.
func Decode(k Kind, blob []byte) ([]Point, error) {
	s, err := k.spec()
	if err != nil {
		return nil, err
	}
	if len(blob) < 1 || blob[0] != Format {
		return nil, fmt.Errorf("%w: unknown format", ErrCorrupt)
	}
	plain, err := decoder().DecodeAll(blob[1:], nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	r := reader{b: plain}
	n64 := r.uvarint()
	if r.err != nil || n64 > MaxPoints || n64 > uint64(len(plain)) {
		return nil, fmt.Errorf("%w: point count", ErrCorrupt)
	}
	n := int(n64)
	pts := make([]Point, n)
	if s.timed {
		ats := r.ints(n)
		for i := range pts {
			pts[i].At = ats[i]
		}
	}
	for c := 0; c < s.cols; c++ {
		present, vals := r.floats(n)
		j := 0
		for i := range pts {
			if present != nil && !present[i] {
				pts[i].Null |= 1 << c
				continue
			}
			pts[i].V[c] = vals[j]
			j++
		}
	}
	caps := r.ints(n)
	if r.err != nil {
		return nil, r.err
	}
	if len(r.b) != 0 {
		return nil, fmt.Errorf("%w: bytes after the last column", ErrCorrupt)
	}
	for i := range pts {
		pts[i].Capture = caps[i]
	}
	return pts, nil
}

func zigzag(v int64) uint64   { return uint64(v<<1) ^ uint64(v>>63) }
func unzigzag(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

// appendInts appends the int column of v, with whichever transform encodes
// shorter.
func appendInts(out []byte, v []int64) []byte {
	d1 := make([]byte, 0, len(v)+1)
	d1 = append(d1, transDelta)
	d2 := make([]byte, 0, len(v)+1)
	d2 = append(d2, transDelta2)
	var prev, prevDelta int64
	for _, x := range v {
		delta := x - prev
		d1 = binary.AppendUvarint(d1, zigzag(delta))
		d2 = binary.AppendUvarint(d2, zigzag(delta-prevDelta))
		prev, prevDelta = x, delta
	}
	if len(d2) < len(d1) {
		return append(out, d2...)
	}
	return append(out, d1...)
}

// decimalScale returns the smallest d for which every value of v is
// float64(k)/10^d for an integer k of at most 53 bits, bit for bit, and the
// integers; ok is false when there is none (a NaN, an infinity, a negative
// zero, or more digits than a double holds).
func decimalScale(v []float64) (int, []int64, bool) {
	ks := make([]int64, len(v))
next:
	for d, scale := range pow10 {
		for i, x := range v {
			if math.IsNaN(x) || math.IsInf(x, 0) || (x == 0 && math.Signbit(x)) {
				return 0, nil, false
			}
			k := math.Round(x * scale)
			if math.Abs(k) > 1<<53 {
				continue next
			}
			if math.Float64bits(k/scale) != math.Float64bits(x) {
				continue next
			}
			ks[i] = int64(k)
		}
		return d, ks, true
	}
	return 0, nil, false
}

// appendFloats appends the float column of the present values vals.
func appendFloats(out []byte, present []bool, vals []float64) []byte {
	switch {
	case len(vals) == 0 && len(present) > 0:
		return append(out, presentNone)
	case len(vals) == len(present):
		out = append(out, presentAll)
	default:
		out = append(out, presentBitmap)
		bm := make([]byte, (len(present)+7)/8)
		for i, p := range present {
			if p {
				bm[i/8] |= 1 << (i % 8)
			}
		}
		out = append(out, bm...)
	}
	if d, ks, ok := decimalScale(vals); ok {
		out = append(out, modeDecimal, byte(d))
		return appendInts(out, ks)
	}
	out = append(out, modeRaw)
	// Byte planes: the first (most significant) byte of every value, then
	// the second, and so on, so the bytes that barely change sit together.
	m := len(vals)
	base := len(out)
	out = append(out, make([]byte, 8*m)...)
	var prev uint64
	for i, x := range vals {
		b := math.Float64bits(x)
		xor := b ^ prev
		prev = b
		for j := range 8 {
			out[base+j*m+i] = byte(xor >> (56 - 8*j))
		}
	}
	return out
}

// reader reads a plain encoding; the first error sticks.
type reader struct {
	b   []byte
	err error
}

func (r *reader) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: %s", ErrCorrupt, what)
	}
	r.b = nil
}

func (r *reader) byte1() byte {
	if r.err != nil || len(r.b) < 1 {
		r.fail("truncated")
		return 0
	}
	c := r.b[0]
	r.b = r.b[1:]
	return c
}

func (r *reader) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.fail("bad varint")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) ints(n int) []int64 {
	out := make([]int64, n)
	trans := r.byte1()
	if trans != transDelta && trans != transDelta2 {
		r.fail("unknown int transform")
		return out
	}
	var prev, prevDelta int64
	for i := range out {
		v := unzigzag(r.uvarint())
		if r.err != nil {
			return out
		}
		delta := v
		if trans == transDelta2 {
			delta = prevDelta + v
		}
		prev += delta
		prevDelta = delta
		out[i] = prev
	}
	return out
}

// floats reads a float column of n points: the presence of each point (nil
// when every one is present) and the present values.
func (r *reader) floats(n int) ([]bool, []float64) {
	var present []bool
	m := n
	switch r.byte1() {
	case presentAll:
	case presentNone:
		return make([]bool, n), nil
	case presentBitmap:
		size := (n + 7) / 8
		if r.err != nil || len(r.b) < size {
			r.fail("truncated bitmap")
			return make([]bool, n), nil
		}
		present = make([]bool, n)
		m = 0
		for i := range present {
			if r.b[i/8]&(1<<(i%8)) != 0 {
				present[i] = true
				m++
			}
		}
		r.b = r.b[size:]
	default:
		r.fail("unknown presence")
		return make([]bool, n), nil
	}
	vals := make([]float64, m)
	switch r.byte1() {
	case modeDecimal:
		d := int(r.byte1())
		if d >= len(pow10) {
			r.fail("decimal scale")
			return present, vals
		}
		ks := r.ints(m)
		for i, k := range ks {
			vals[i] = float64(k) / pow10[d]
		}
	case modeRaw:
		if r.err != nil || len(r.b) < 8*m {
			r.fail("truncated raw column")
			return present, vals
		}
		var prev uint64
		for i := range vals {
			var xor uint64
			for j := range 8 {
				xor |= uint64(r.b[j*m+i]) << (56 - 8*j)
			}
			b := xor ^ prev
			vals[i] = math.Float64frombits(b)
			prev = b
		}
		r.b = r.b[8*m:]
	default:
		r.fail("unknown float mode")
	}
	return present, vals
}
