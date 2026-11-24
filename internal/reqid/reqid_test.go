// SPDX-License-Identifier: AGPL-3.0-only

package reqid

import (
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	if got := Sanitize("abc-123_x.y"); got != "abc-123_x.y" {
		t.Fatalf("a valid id is kept: %q", got)
	}
	for _, in := range []string{"", "has space", "new\nline", strings.Repeat("a", 129), "é"} {
		got := Sanitize(in)
		if got == in || len(got) != 32 {
			t.Errorf("%q: got %q, want a fresh 32 character id", in, got)
		}
	}
	if a, b := Sanitize(""), Sanitize(""); a == b {
		t.Fatal("fresh ids differ")
	}
}
