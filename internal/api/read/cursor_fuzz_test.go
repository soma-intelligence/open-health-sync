// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"testing"
	"time"
)

// FuzzDecodeCursor feeds arbitrary ?cursor= values to every cursor decoder of
// the read API: none may panic, and whatever one accepts must encode back to
// a cursor that decodes to the same position.
func FuzzDecodeCursor(f *testing.F) {
	at := time.Date(2026, 3, 1, 9, 30, 0, 123456000, time.UTC)
	f.Add(EncodeCursor(at, 42))
	f.Add(encodeObsCursor(at, KindQuantity, 7))
	f.Add(encodeKeyCursor(at, "abc", "x|y"))
	f.Add("")
	f.Add("!!!")
	f.Add(EncodeCursor(time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), -1))
	f.Fuzz(func(t *testing.T, c string) {
		if ts, id, err := DecodeCursor(c); err == nil {
			t2, id2, err := DecodeCursor(EncodeCursor(ts, id))
			if err != nil || !t2.Equal(ts) || id2 != id {
				t.Fatalf("cursor %q: round trip gives %v %d %v, want %v %d", c, t2, id2, err, ts, id)
			}
		}
		if ts, k, id, err := decodeObsCursor(c); err == nil {
			t2, k2, id2, err := decodeObsCursor(encodeObsCursor(ts, k, id))
			if err != nil || !t2.Equal(ts) || k2 != k || id2 != id {
				t.Fatalf("obs cursor %q: round trip differs", c)
			}
		}
		for n := 1; n <= 2; n++ {
			if ts, key, err := decodeKeyCursor(c, n); err == nil {
				t2, key2, err := decodeKeyCursor(encodeKeyCursor(ts, key...), n)
				if err != nil || !t2.Equal(ts) || len(key2) != len(key) {
					t.Fatalf("key cursor %q (n=%d): round trip differs", c, n)
				}
			}
		}
	})
}
