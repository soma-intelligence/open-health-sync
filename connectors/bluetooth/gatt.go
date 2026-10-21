// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrTruncated reports a value shorter than its flags say. The message
// names the characteristic, never a value.
var ErrTruncated = errors.New("bluetooth: value shorter than its flags say")

// reader reads the little-endian fields of one characteristic value, as the
// Bluetooth Core specification lays them out, and fails instead of reading
// past the end.
type reader struct {
	b   []byte
	off int
}

func (r *reader) need(n int) error {
	if r.off+n > len(r.b) {
		return ErrTruncated
	}
	return nil
}

func (r *reader) left() int { return len(r.b) - r.off }

func (r *reader) u8() (int, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	v := int(r.b[r.off])
	r.off++
	return v, nil
}

func (r *reader) u16() (int, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := int(r.b[r.off]) | int(r.b[r.off+1])<<8
	r.off += 2
	return v, nil
}

func (r *reader) s16() (int, error) {
	v, err := r.u16()
	return int(int16(uint16(v))), err
}

func (r *reader) skip(n int) error {
	if err := r.need(n); err != nil {
		return err
	}
	r.off += n
	return nil
}

// decimal is an IEEE 11073-20601 float kept as mantissa and base 10
// exponent, so a change of unit by a power of ten (kg/L to mg/dL) moves the
// exponent instead of adding binary rounding.
type decimal struct {
	m, e int
}

// float is the value times 10^shift.
func (d decimal) float(shift int) float64 {
	e := d.e + shift
	if e >= 0 {
		return float64(d.m) * math.Pow10(e)
	}
	return float64(d.m) / math.Pow10(-e)
}

// sfloatOf decodes a 16-bit SFLOAT: a 4-bit exponent over a 12-bit
// mantissa, both two's complement. NaN, NRes, +INF, -INF and the reserved
// value report false.
func sfloatOf(raw uint16) (decimal, bool) {
	m := int(raw & 0x0FFF)
	switch m {
	case 0x07FF, 0x0800, 0x07FE, 0x0802, 0x0801:
		return decimal{}, false
	}
	if m >= 0x0800 {
		m -= 0x1000
	}
	e := int(raw >> 12)
	if e >= 0x8 {
		e -= 0x10
	}
	return decimal{m: m, e: e}, true
}

// sfloat reads an SFLOAT. ok is false for a reserved value.
func (r *reader) sfloat() (d decimal, ok bool, err error) {
	v, err := r.u16()
	if err != nil {
		return decimal{}, false, err
	}
	d, ok = sfloatOf(uint16(v))
	return d, ok, nil
}

// float32 reads a 32-bit FLOAT: an 8-bit exponent over a 24-bit mantissa.
// ok is false for NaN, NRes, +INF, -INF and the reserved value.
func (r *reader) float32() (d decimal, ok bool, err error) {
	if err := r.need(4); err != nil {
		return decimal{}, false, err
	}
	m := int(r.b[r.off]) | int(r.b[r.off+1])<<8 | int(r.b[r.off+2])<<16
	e := int(int8(r.b[r.off+3]))
	r.off += 4
	switch m {
	case 0x007FFFFF, 0x00800000, 0x007FFFFE, 0x00800002, 0x00800001:
		return decimal{}, false, nil
	}
	if m >= 0x00800000 {
		m -= 0x01000000
	}
	return decimal{m: m, e: e}, true, nil
}

// wallClock is a Date Time field as the device sent it: its own clock, with
// no zone. A zero year, month or day means unknown.
type wallClock struct {
	year, month, day, hour, minute, second int
}

func (r *reader) dateTime() (wallClock, error) {
	if err := r.need(7); err != nil {
		return wallClock{}, err
	}
	b := r.b[r.off:]
	r.off += 7
	return wallClock{
		year: int(b[0]) | int(b[1])<<8, month: int(b[2]), day: int(b[3]),
		hour: int(b[4]), minute: int(b[5]), second: int(b[6]),
	}, nil
}

// earliestDeviceTime is the oldest device time trusted: earlier dates are
// clocks never set (many devices start at 2000 or 1970 after a battery change).
var earliestDeviceTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// deviceAhead is how far after the arrival a device time may be and still be
// a real reading time: clocks drift.
const deviceAhead = 10 * time.Minute

// in places the wall clock in loc, plus offset minutes (the glucose time
// offset field). ok is false for an incomplete or impossible date.
func (c wallClock) in(loc *time.Location, offset int) (time.Time, bool) {
	if c.year == 0 || c.month < 1 || c.month > 12 || c.day < 1 || c.day > 31 ||
		c.hour > 23 || c.minute > 59 || c.second > 59 {
		return time.Time{}, false
	}
	t := time.Date(c.year, time.Month(c.month), c.day, c.hour, c.minute, c.second, 0, loc)
	if t.Day() != c.day { // February 30 normalizes into March
		return time.Time{}, false
	}
	return t.Add(time.Duration(offset) * time.Minute), true
}

// truncated wraps ErrTruncated with the characteristic it happened in.
func truncated(char string, err error) error {
	if errors.Is(err, ErrTruncated) {
		return fmt.Errorf("%w: characteristic %s", ErrTruncated, char)
	}
	return err
}
