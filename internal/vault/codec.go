// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// Kind is the type of a sealed column.
type Kind uint8

const (
	KindFloat64 Kind = iota + 1
	KindInt64
	KindBool
	KindText
	// KindJSON is a JSON document. The caller passes canonical JSON (keys in one
	// fixed order) as []byte or json.RawMessage: Encode stores the bytes as they
	// are, so the same document must always arrive as the same bytes, or the
	// row's content MAC changes without the content changing.
	KindJSON
	// KindTime is an instant, kept to the microsecond (UnixMicro). Postgres
	// timestamptz has no finer grain.
	KindTime
	KindBytes
)

// Column is one sealed column of a table. Tag identifies it inside the encoding
// and is never reused for another column of the same table, even after the
// column is dropped. Tag 0 is reserved as the end marker.
type Column struct {
	Name string
	Tag  uint8
	Kind Kind
}

// Table describes the sealed part of one table of ohs_data.
type Table struct {
	Name string
	// Sealed lists the columns encoded into the sealed bytes, in the order Encode
	// and Decode take and return their values.
	Sealed []Column
	// Identity lists the clear columns that identify the row, in order. They form
	// the tail of the row's AAD, so a sealed value moved to another row fails to
	// open.
	Identity []string
	// MAC is true when the table is written with an upsert that compares the old
	// and the new row: it then carries a content_mac column (see the spec,
	// "Keyed hashes").
	MAC bool
}

// formatByte starts every encoding. A new layout takes a new value.
const formatByte = 0x01

// endTag closes the encoding. Without it, a copy cut at a field boundary would
// read as a valid row with fewer fields.
const endTag = 0

// Encode returns the plaintext of a sealed row. vals are in Sealed order and nil
// is SQL NULL. A NULL field is left out of the encoding, so an empty string and a
// NULL stay apart. The accepted Go types per kind are float64, int64 (or int),
// bool, string, []byte or json.RawMessage (KindJSON and KindBytes) and time.Time.
func (t Table) Encode(vals []any) ([]byte, error) {
	if len(vals) != len(t.Sealed) {
		return nil, fmt.Errorf("vault: %s: %d values for %d sealed columns", t.Name, len(vals), len(t.Sealed))
	}
	out := []byte{formatByte}
	var scratch [binary.MaxVarintLen64]byte
	for i, c := range t.Sealed {
		if vals[i] == nil {
			continue
		}
		body, err := encodeValue(c.Kind, vals[i])
		if err != nil {
			return nil, fmt.Errorf("vault: %s.%s: %w", t.Name, c.Name, err)
		}
		out = append(out, c.Tag)
		n := binary.PutUvarint(scratch[:], uint64(len(body)))
		out = append(out, scratch[:n]...)
		out = append(out, body...)
	}
	return append(out, endTag), nil
}

// Decode is the inverse of Encode: values in Sealed order, nil for a NULL.
// Floats come back as float64, integers as int64, JSON as json.RawMessage,
// bytes as []byte and instants as UTC time.Time.
func (t Table) Decode(b []byte) ([]any, error) {
	if len(b) == 0 || b[0] != formatByte {
		return nil, fmt.Errorf("vault: %s: unknown row format", t.Name)
	}
	vals := make([]any, len(t.Sealed))
	seen := make([]bool, len(t.Sealed))
	rest := b[1:]
	for {
		if len(rest) == 0 {
			return nil, fmt.Errorf("vault: %s: truncated row", t.Name)
		}
		tag := rest[0]
		rest = rest[1:]
		if tag == endTag {
			if len(rest) != 0 {
				return nil, fmt.Errorf("vault: %s: bytes after the end of the row", t.Name)
			}
			return vals, nil
		}
		idx := -1
		for i, c := range t.Sealed {
			if c.Tag == tag {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("vault: %s: unknown tag %d", t.Name, tag)
		}
		if seen[idx] {
			return nil, fmt.Errorf("vault: %s: tag %d twice", t.Name, tag)
		}
		n, w := binary.Uvarint(rest)
		if w <= 0 {
			return nil, fmt.Errorf("vault: %s: bad length for tag %d", t.Name, tag)
		}
		rest = rest[w:]
		if n > uint64(len(rest)) {
			return nil, fmt.Errorf("vault: %s: tag %d overruns the row", t.Name, tag)
		}
		v, err := decodeValue(t.Sealed[idx].Kind, rest[:n])
		if err != nil {
			return nil, fmt.Errorf("vault: %s.%s: %w", t.Name, t.Sealed[idx].Name, err)
		}
		vals[idx], seen[idx] = v, true
		rest = rest[n:]
	}
}

func encodeValue(k Kind, v any) ([]byte, error) {
	switch k {
	case KindFloat64:
		f, ok := v.(float64)
		if !ok {
			return nil, wrongType(k, v)
		}
		return binary.BigEndian.AppendUint64(nil, math.Float64bits(f)), nil
	case KindInt64:
		switch x := v.(type) {
		case int64:
			return binary.AppendVarint(nil, x), nil
		case int:
			return binary.AppendVarint(nil, int64(x)), nil
		}
		return nil, wrongType(k, v)
	case KindBool:
		b, ok := v.(bool)
		if !ok {
			return nil, wrongType(k, v)
		}
		if b {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case KindText:
		s, ok := v.(string)
		if !ok {
			return nil, wrongType(k, v)
		}
		return []byte(s), nil
	case KindJSON, KindBytes:
		switch x := v.(type) {
		case []byte:
			return x, nil
		case json.RawMessage:
			return x, nil
		}
		return nil, wrongType(k, v)
	case KindTime:
		t, ok := v.(time.Time)
		if !ok {
			return nil, wrongType(k, v)
		}
		return binary.AppendVarint(nil, t.UnixMicro()), nil
	}
	return nil, fmt.Errorf("unknown kind %d", k)
}

func decodeValue(k Kind, b []byte) (any, error) {
	switch k {
	case KindFloat64:
		if len(b) != 8 {
			return nil, errors.New("float64 is not 8 bytes")
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case KindInt64:
		x, err := readVarint(b)
		if err != nil {
			return nil, err
		}
		return x, nil
	case KindBool:
		if len(b) != 1 || b[0] > 1 {
			return nil, errors.New("bad bool")
		}
		return b[0] == 1, nil
	case KindText:
		return string(b), nil
	case KindJSON:
		return json.RawMessage(append([]byte{}, b...)), nil
	case KindBytes:
		return append([]byte{}, b...), nil
	case KindTime:
		x, err := readVarint(b)
		if err != nil {
			return nil, err
		}
		return time.UnixMicro(x).UTC(), nil
	}
	return nil, fmt.Errorf("unknown kind %d", k)
}

// readVarint reads a zigzag varint that must fill b exactly.
func readVarint(b []byte) (int64, error) {
	x, w := binary.Varint(b)
	if w <= 0 || w != len(b) {
		return 0, errors.New("bad varint")
	}
	return x, nil
}

func wrongType(k Kind, v any) error {
	return fmt.Errorf("kind %d does not take %T", k, v)
}
