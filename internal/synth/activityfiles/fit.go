// SPDX-License-Identifier: AGPL-3.0-only

package activityfiles

import (
	"bytes"
	"encoding/binary"
	"math"
	"time"
)

// The FIT encoder follows the FIT Protocol (developer.garmin.com/fit/protocol):
// a 14 byte header (or the legacy 12 byte one), definition messages before
// the data messages they describe, little or big endian per definition, and
// the CRC-16 of the protocol after the data. Message and field numbers, base
// types, scales and enum values come from the FIT Profile (Profile.xlsx of
// the FIT SDK 21.218).

// Base types (Profile, fit_base_type).
const (
	btEnum    = 0x00
	btUint8   = 0x02
	btUint16  = 0x84
	btSint32  = 0x85
	btUint32  = 0x86
	btUint32z = 0x8C
)

// Global message numbers (Profile, mesg_num).
const (
	mesgFileID   = 0
	mesgSession  = 18
	mesgLap      = 19
	mesgRecord   = 20
	mesgEvent    = 21
	mesgActivity = 34
)

// fitEpoch is the FIT date_time origin, 1989-12-31T00:00:00Z.
var fitEpoch = time.Date(1989, 12, 31, 0, 0, 0, 0, time.UTC)

func fitTime(t time.Time) uint32 { return uint32(t.UTC().Sub(fitEpoch) / time.Second) }

// semicircles converts degrees to the sint32 semicircles of the Profile.
func semicircles(deg float64) int32 { return int32(math.Round(deg * (1 << 31) / 180)) }

// field is one field of a message: its number, base type and value. A value
// of nil writes the base type's invalid value.
type field struct {
	num  byte
	base byte
	v    any
}

func size(base byte) int {
	switch base {
	case btEnum, btUint8:
		return 1
	case btUint16:
		return 2
	default:
		return 4
	}
}

// invalid is the invalid value of each base type (FIT Protocol, Table 7).
func invalid(base byte) uint64 {
	switch base {
	case btEnum, btUint8:
		return 0xFF
	case btUint16:
		return 0xFFFF
	case btSint32:
		return 0x7FFFFFFF
	case btUint32z:
		return 0
	default:
		return 0xFFFFFFFF
	}
}

// fitWriter builds a FIT file.
type fitWriter struct {
	data bytes.Buffer
	// order is the byte order of every message, big says it is big endian.
	order binary.ByteOrder
	big   bool
	// defined maps a local message type to the signature it was defined with.
	defined map[byte]string
}

func newFIT(big bool) *fitWriter {
	w := &fitWriter{order: binary.LittleEndian, big: big, defined: map[byte]string{}}
	if big {
		w.order = binary.BigEndian
	}
	return w
}

// msg writes a data message, preceded by its definition when the local type
// last held another layout.
func (w *fitWriter) msg(local byte, global uint16, fields []field) {
	sig := string([]byte{byte(global), byte(global >> 8)})
	for _, f := range fields {
		sig += string([]byte{f.num, f.base})
	}
	if w.defined[local] != sig {
		w.define(local, global, fields)
		w.defined[local] = sig
	}
	w.data.WriteByte(local & 0x0F)
	for _, f := range fields {
		w.value(f)
	}
}

func (w *fitWriter) define(local byte, global uint16, fields []field) {
	w.data.WriteByte(0x40 | local&0x0F)
	w.data.WriteByte(0) // reserved
	if w.big {
		w.data.WriteByte(1)
	} else {
		w.data.WriteByte(0)
	}
	var g [2]byte
	w.order.PutUint16(g[:], global)
	w.data.Write(g[:])
	w.data.WriteByte(byte(len(fields)))
	for _, f := range fields {
		w.data.Write([]byte{f.num, byte(size(f.base)), f.base})
	}
}

func (w *fitWriter) value(f field) {
	var u uint64
	switch v := f.v.(type) {
	case nil:
		u = invalid(f.base)
	case int:
		u = uint64(v)
	case int32:
		u = uint64(uint32(v))
	case uint32:
		u = uint64(v)
	case float64:
		u = uint64(int64(math.Round(v)))
	}
	switch size(f.base) {
	case 1:
		w.data.WriteByte(byte(u))
	case 2:
		var b [2]byte
		w.order.PutUint16(b[:], uint16(u))
		w.data.Write(b[:])
	default:
		var b [4]byte
		w.order.PutUint32(b[:], uint32(u))
		w.data.Write(b[:])
	}
}

// bytes returns the file: header, data and CRC. header12 writes the legacy
// 12 byte header without its CRC.
func (w *fitWriter) bytes(header12 bool) []byte {
	var out bytes.Buffer
	hs := byte(14)
	if header12 {
		hs = 12
	}
	out.WriteByte(hs)
	out.WriteByte(0x20) // protocol 2.0
	var b [4]byte
	binary.LittleEndian.PutUint16(b[:2], 21218) // profile 21.218
	out.Write(b[:2])
	binary.LittleEndian.PutUint32(b[:], uint32(w.data.Len()))
	out.Write(b[:])
	out.WriteString(".FIT")
	if !header12 {
		binary.LittleEndian.PutUint16(b[:2], crc16(out.Bytes()))
		out.Write(b[:2])
	}
	out.Write(w.data.Bytes())
	binary.LittleEndian.PutUint16(b[:2], crc16(out.Bytes()))
	out.Write(b[:2])
	return out.Bytes()
}

// crc16 is the CRC of the FIT Protocol, nibble by nibble.
func crc16(data []byte) uint16 {
	table := [16]uint16{0x0000, 0xCC01, 0xD801, 0x1400, 0xF001, 0x3C00, 0x2800, 0xE401,
		0xA001, 0x6C00, 0x7800, 0xB401, 0x5000, 0x9C01, 0x8801, 0x4400}
	var crc uint16
	for _, b := range data {
		tmp := table[crc&0xF]
		crc = (crc >> 4) & 0x0FFF
		crc = crc ^ tmp ^ table[b&0xF]
		tmp = table[crc&0xF]
		crc = (crc >> 4) & 0x0FFF
		crc = crc ^ tmp ^ table[(b>>4)&0xF]
	}
	return crc
}
