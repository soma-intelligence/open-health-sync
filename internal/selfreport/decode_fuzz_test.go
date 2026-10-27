// SPDX-License-Identifier: AGPL-3.0-only

package selfreport

import (
	"math"
	"testing"
	"time"
)

// FuzzDecode feeds arbitrary bodies of POST /v1/users/{id}/self-reports to
// Decode: no panic, and an accepted report has a known kind, a finite value
// and times the database can hold, and maps to a sample.
func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"id":"r1","kind":"mood","value":4,"start_at":"2026-03-01T08:00:00Z","timezone":"Europe/Paris"}`))
	f.Add([]byte(`{"id":"r1","kind":"rpe","value":7,"start_at":"2026-03-01T08:00:00Z","end_at":"2026-03-01T09:00:00Z"}`))
	f.Add([]byte(`{"id":"r1","kind":"mood","value":1e309,"start_at":"0001-01-01T00:00:00Z"}`))
	f.Add([]byte(`{"id":"r1","kind":"mood","value":4,"start_at":"2026-03-01T08:00:00Z","timezone":"../../etc/passwd"}`))
	f.Fuzz(func(t *testing.T, line []byte) {
		r, err := Decode(line)
		if err != nil {
			return
		}
		if _, ok := QuantityType(r.Kind); !ok {
			t.Fatalf("accepted unknown kind %q", r.Kind)
		}
		if math.IsNaN(r.Value) || math.IsInf(r.Value, 0) {
			t.Fatalf("accepted value %v", r.Value)
		}
		if r.StartAt.Year() < 1900 || r.StartAt.After(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("accepted start_at %v", r.StartAt)
		}
		_ = Sample("00000000-0000-4000-8000-000000000002", r)
	})
}
