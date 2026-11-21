// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// fuzzTables are the sealed tables in a fixed order: a fuzz input picks one
// by index, so an index in a saved crasher keeps meaning the same table.
func fuzzTables() []vault.Table {
	names := make([]string, 0, len(vault.Schema))
	for n := range vault.Schema {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]vault.Table, len(names))
	for i, n := range names {
		out[i] = vault.Schema[n]
	}
	return out
}

// fuzzRowValues are values a row of t can hold: one per sealed column, of
// its kind, every third one NULL.
func fuzzRowValues(t vault.Table) []any {
	vals := make([]any, len(t.Sealed))
	for i, c := range t.Sealed {
		if i%3 == 2 {
			continue
		}
		switch c.Kind {
		case vault.KindFloat64:
			vals[i] = 72.5 + float64(i)
		case vault.KindInt64:
			vals[i] = int64(-7 * (i + 1))
		case vault.KindBool:
			vals[i] = i%2 == 0
		case vault.KindText:
			vals[i] = "text " + c.Name
		case vault.KindJSON:
			vals[i] = json.RawMessage(`{"timezone":"Europe/Paris","n":[1,2.50,1e3]}`)
		case vault.KindTime:
			vals[i] = time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
		case vault.KindBytes:
			vals[i] = []byte{0, 1, 2}
		}
	}
	return vals
}

// FuzzOpenRow opens arbitrary bytes as the sealed value of a row, and
// decodes them as the plaintext of one (what a row holds once its seal has
// opened). No input may panic; a sealed value that does not open is ErrOpen
// and gives no value; a plaintext that decodes encodes back to a plaintext
// that decodes to the same row; and the text a JSON document is served as
// never panics either.
func FuzzOpenRow(f *testing.F) {
	tables := fuzzTables()
	tenant := uuid.MustParse("0192f3a4-5b6c-7d8e-9f00-112233445566")
	user := uuid.MustParse("0192f3a4-5b6c-7d8e-9f00-aabbccddeeff")
	uv := vault.UserVaultOf(tenant, user, fuzzObjectKeys(f))
	identity := []byte("content-hash\x00\x06\x4d")

	for i, t := range tables {
		vals := fuzzRowValues(t)
		sealed, err := uv.SealRow(t, identity, vals)
		if err != nil {
			f.Fatal(err)
		}
		plain, err := t.Encode(vals)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(uint8(i), sealed)
		f.Add(uint8(i), plain)
		// The sealed value of another table, and a truncated one.
		f.Add(uint8((i+1)%len(tables)), sealed)
		f.Add(uint8(i), sealed[:len(sealed)/2])
	}
	f.Add(uint8(0), []byte{})
	f.Add(uint8(0), []byte{0x02})
	f.Add(uint8(0), []byte{0x02, 0, 0, 0, 1})
	f.Add(uint8(0), []byte{0x01, 0x00})
	f.Add(uint8(3), []byte(`{"a":1e400,"a":[]}`))

	f.Fuzz(func(t *testing.T, table uint8, data []byte) {
		tab := tables[int(table)%len(tables)]

		vals, err := uv.OpenRow(tab, identity, data)
		if err != nil {
			if !errors.Is(err, vault.ErrOpen) {
				t.Fatalf("OpenRow: %v is not ErrOpen", err)
			}
			if vals != nil {
				t.Fatal("OpenRow returned values with an error")
			}
		} else if len(vals) != len(tab.Sealed) {
			t.Fatalf("OpenRow: %d values for %d sealed columns", len(vals), len(tab.Sealed))
		}

		if decoded, err := tab.Decode(data); err == nil {
			if len(decoded) != len(tab.Sealed) {
				t.Fatalf("Decode: %d values for %d sealed columns", len(decoded), len(tab.Sealed))
			}
			again, err := tab.Encode(decoded)
			if err != nil {
				t.Fatalf("a decoded row does not encode: %v", err)
			}
			back, err := tab.Decode(again)
			if err != nil {
				t.Fatalf("an encoded row does not decode: %v", err)
			}
			for i := range decoded {
				if !sameFuzzValue(decoded[i], back[i]) {
					t.Fatalf("%s.%s: %#v became %#v", tab.Name, tab.Sealed[i].Name, decoded[i], back[i])
				}
			}
		}

		// What a sealed JSON document is served as: stable once normalized.
		text := vault.JSONBText(data)
		if again := vault.JSONBText(text); json.Valid(data) && !bytes.Equal(text, again) {
			t.Fatalf("JSONBText is not stable: %q then %q", text, again)
		}
	})
}

// sameFuzzValue compares two decoded values; a float by its bits, so a NaN
// equals itself.
func sameFuzzValue(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || (x != x && y != y))
	case json.RawMessage:
		y, ok := b.(json.RawMessage)
		return ok && bytes.Equal(x, y)
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Equal(x, y)
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	}
	return a == b
}

// FuzzUnmarshalUserKeys reads arbitrary bytes as a user's unwrapped keys
// (what a wrapped key gives once its own seal has opened). No input may
// panic; an input that is accepted marshals back to the same bytes, and
// gives keys that can be used: a public key, and a row sealed then opened.
func FuzzUnmarshalUserKeys(f *testing.F) {
	valid := fuzzObjectKeys(f).Marshal()
	f.Add(valid)
	f.Add(valid[:len(valid)-1])
	f.Add(append(slices.Clone(valid), 0))
	f.Add([]byte{})
	f.Add([]byte{0x01})
	f.Add(bytes.Repeat([]byte{0xff}, len(valid)))
	f.Add(make([]byte, len(valid)))
	tab := vault.Schema["quantity_samples"]

	f.Fuzz(func(t *testing.T, data []byte) {
		k, err := vault.UnmarshalUserKeys(data)
		if err != nil {
			if k != nil {
				t.Fatal("UnmarshalUserKeys returned keys with an error")
			}
			return
		}
		if !bytes.Equal(k.Marshal(), data) {
			t.Fatal("accepted keys do not marshal back to their bytes")
		}
		// Any 32 bytes are an X25519 key and any 64 an ML-KEM seed: the
		// public half must derive, or fail, without a panic.
		_, _ = k.Public()
		uv := vault.UserVaultOf(uuid.Nil, uuid.Nil, k)
		vals := fuzzRowValues(tab)
		sealed, err := uv.SealRow(tab, []byte("id"), vals)
		if err != nil {
			t.Fatalf("SealRow under accepted keys: %v", err)
		}
		opened, err := uv.OpenRow(tab, []byte("id"), sealed)
		if err != nil {
			t.Fatalf("OpenRow under accepted keys: %v", err)
		}
		for i := range vals {
			if !sameFuzzValue(vals[i], opened[i]) {
				t.Fatalf("%s: %#v became %#v", tab.Sealed[i].Name, vals[i], opened[i])
			}
		}
	})
}
