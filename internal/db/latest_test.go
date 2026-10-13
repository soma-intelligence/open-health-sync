// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// LatestVersion must be the highest numbered up migration on disk.
func TestLatestVersion(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var want uint64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		n, err := strconv.ParseUint(name[:strings.IndexByte(name, '_')], 10, 32)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want = max(want, n)
	}
	got, err := LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	if want == 0 || uint64(got) != want {
		t.Fatalf("LatestVersion() = %d, want %d", got, want)
	}
}
