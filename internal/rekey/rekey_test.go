// SPDX-License-Identifier: AGPL-3.0-only

package rekey

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// notSealed are the bytea columns that hold no sealed secret.
var notSealed = map[string]bool{
	"response_body": true, // idempotent replay of an API response
}

// Every bytea column a migration adds is either a sealed column Run walks or
// listed in notSealed: a new sealed column left out would stay under the old
// key after a rotation.
func TestEveryByteaColumnIsClassified(t *testing.T) {
	files, err := filepath.Glob("../db/migrations/*.up.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	listed := map[string]bool{}
	for _, c := range Columns() {
		listed[c[strings.LastIndex(c, ".")+1:]] = true
	}
	col := regexp.MustCompile(`(?mi)^\s*(?:ADD COLUMN\s+(?:IF NOT EXISTS\s+)?)?([a-z_][a-z0-9_]*)\s+bytea\b`)
	found := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range col.FindAllStringSubmatch(string(b), -1) {
			name := strings.ToLower(m[1])
			found[name] = true
			require.True(t, listed[name] || notSealed[name],
				"%s: bytea column %s is neither in rekey.tables nor in notSealed", filepath.Base(f), name)
		}
	}
	for c := range listed {
		require.True(t, found[c], "rekey walks %s, which no migration creates", c)
	}
}

func TestKeyMatchAndDescribe(t *testing.T) {
	conn := tables[0]
	require.Equal(t, "tenant_id = $1::uuid AND user_id = $2::uuid AND provider = $3::text", keyMatch(conn, 1))
	require.Equal(t, "tenant_id=a user_id=b provider=oura", describe(conn, []string{"a", "b", "oura"}))
}
