// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"os"
	"strings"
	"testing"
)

// The React console retries once on exactly this detail (CSRF_REFUSED in
// web/console/src/api/client.ts): the two strings must not drift apart.
func TestDetailCSRFMatchesTheConsoleClient(t *testing.T) {
	src, err := os.ReadFile("../../web/console/src/api/client.ts")
	if err != nil {
		t.Fatalf("read the console client: %v", err)
	}
	want := `export const CSRF_REFUSED = "` + DetailCSRF + `";`
	if !strings.Contains(string(src), want) {
		t.Errorf("web/console/src/api/client.ts has no %s", want)
	}
}
