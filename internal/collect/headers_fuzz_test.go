// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"net/http"
	"strings"
	"testing"
)

// FuzzParseHeaders feeds arbitrary X-OHS-Type, X-OHS-Day, X-OHS-Batch and
// X-OHS-Part values to the upload route's header check: no panic, and what
// it accepts is safe inside a raw store key (no separator, no dot, no
// traversal) and a part number in range.
func FuzzParseHeaders(f *testing.F) {
	f.Add("HKQuantityTypeIdentifierHeartRate", "2026-01-10", "b1", "0")
	f.Add("../x", "2026-02-30", "b/1", "-1")
	f.Add("t", "0000-01-01", "b", "99999999999999999999")
	f.Add("t", "9999-12-31", "b", "+1")
	f.Fuzz(func(t *testing.T, typ, day, batch, part string) {
		hd := http.Header{}
		hd.Set(HeaderType, typ)
		hd.Set(HeaderDay, day)
		hd.Set(HeaderBatch, batch)
		hd.Set(HeaderPart, part)
		q, msg := parseHeaders(hd)
		if msg != "" {
			return
		}
		for _, s := range []string{q.dataType, q.batch} {
			if s == "" || strings.ContainsAny(s, "/\\.\x00") {
				t.Fatalf("accepted segment %q", s)
			}
		}
		if q.part < 0 || q.part > maxPart {
			t.Fatalf("accepted part %d", q.part)
		}
		if q.day.IsZero() && day != "0001-01-01" {
			t.Fatalf("accepted day %q as zero", day)
		}
	})
}
