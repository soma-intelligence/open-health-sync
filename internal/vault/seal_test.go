// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"strings"
	"testing"
)

// The object that replaces a raw object must not have the old key as a
// prefix: the raw store deletes by prefix, and deleting the old one would
// take the new one with it.
func TestSealedKeyIsNotUnderTheOldKey(t *testing.T) {
	const key = "raw/t/u/apple_health/2026-10-01/b1_0.ndjson.gz"
	got := sealedKey(key, 1)
	if got != "raw/t/u/apple_health/2026-10-01/v1.b1_0.ndjson.gz" {
		t.Fatalf("sealedKey = %q", got)
	}
	if strings.HasPrefix(got, key) {
		t.Fatal("the new key starts with the old one")
	}
	if v, ok := sealedKeyVersion(got); !ok || v != 1 {
		t.Fatalf("sealedKeyVersion(%q) = %d, %v", got, v, ok)
	}
	if _, ok := sealedKeyVersion(key); ok {
		t.Fatal("an object collect wrote carries no version in its name")
	}
	// A reseal replaces the mark, it does not stack another.
	again := sealedKey(got, 2)
	if again != "raw/t/u/apple_health/2026-10-01/v2.b1_0.ndjson.gz" || strings.HasPrefix(again, got) {
		t.Fatalf("sealedKey of a sealed key = %q", again)
	}
	if v, ok := sealedKeyVersion(again); !ok || v != 2 {
		t.Fatalf("sealedKeyVersion(%q) = %d, %v", again, v, ok)
	}
}

func TestClearRowsGauge(t *testing.T) {
	m := NewMetrics()
	var b bytes.Buffer
	m.Write(&b)
	if strings.Contains(b.String(), "ohs_vault_clear_rows") {
		t.Fatal("the gauge is absent until a status was read")
	}
	m.SetClearRows(StatusReport{Tables: map[string]int64{"workouts": 3, "quantity_samples": 0}, RawObjects: 2})
	b.Reset()
	m.Write(&b)
	for _, want := range []string{
		`ohs_vault_clear_rows{table="quantity_samples"} 0`,
		`ohs_vault_clear_rows{table="workouts"} 3`,
		`ohs_vault_clear_raw_objects 2`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("metrics lack %q:\n%s", want, b.String())
		}
	}
}
