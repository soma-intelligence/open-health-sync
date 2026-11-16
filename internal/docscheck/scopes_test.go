// SPDX-License-Identifier: AGPL-3.0-only

package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
)

// TestKeyScopesAreDocumented keeps the lists of API key scopes in step with
// auth.KeyScopes: the table of docs/api.md has one row per scope, in the
// same order, and the KeyScope enum of api/openapi.yaml holds the same
// values.
func TestKeyScopesAreDocumented(t *testing.T) {
	root := filepath.Join("..", "..")
	guide, err := os.ReadFile(filepath.Join(root, "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(guide), "\n### Key scopes\n")
	if !ok {
		t.Fatal("docs/api.md has no section \"### Key scopes\"")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	var rows []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([^`]+)` \\|").FindAllStringSubmatch(section, -1) {
		rows = append(rows, m[1])
	}
	if !slices.Equal(rows, auth.KeyScopes) {
		t.Errorf("the scope table of docs/api.md lists\n%v\nauth.KeyScopes is\n%v", rows, auth.KeyScopes)
	}

	spec, err := os.ReadFile(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, schema, ok := strings.Cut(string(spec), "\n    KeyScope:\n")
	if !ok {
		t.Fatal("api/openapi.yaml has no KeyScope schema")
	}
	schema, _, _ = strings.Cut(schema, "\n\n")
	var enum []string
	for _, m := range regexp.MustCompile(`(?m)^        - "([^"]+)"$`).FindAllStringSubmatch(schema, -1) {
		enum = append(enum, m[1])
	}
	if !slices.Equal(enum, auth.KeyScopes) {
		t.Errorf("the KeyScope enum of api/openapi.yaml is\n%v\nauth.KeyScopes is\n%v", enum, auth.KeyScopes)
	}
}
