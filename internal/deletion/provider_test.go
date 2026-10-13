// SPDX-License-Identifier: AGPL-3.0-only

package deletion

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	createTable = regexp.MustCompile(`(?s)CREATE TABLE (ohs_\w+\.\w+) \((.*?)\n\);`)
	alterTable  = regexp.MustCompile(`(?s)ALTER TABLE (ohs_\w+\.\w+)\b(.*?);`)
	dropTable   = regexp.MustCompile(`DROP TABLE (?:IF EXISTS )?(ohs_\w+\.\w+)`)
	addColumn   = regexp.MustCompile(`ADD COLUMN (?:IF NOT EXISTS )?(\w+)`)
	dropColumn  = regexp.MustCompile(`DROP COLUMN (?:IF EXISTS )?(\w+)`)
)

// migratedColumns replays the up migrations' CREATE, ALTER and DROP TABLE
// statements and returns the columns of every ohs_* table.
func migratedColumns(t *testing.T) map[string]map[string]bool {
	t.Helper()
	files, err := filepath.Glob("../db/migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	tables := map[string]map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(b)
		for _, m := range createTable.FindAllStringSubmatch(sql, -1) {
			cols := map[string]bool{}
			for _, line := range strings.Split(m[2], "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 || strings.HasPrefix(fields[0], "--") {
					continue
				}
				switch strings.ToUpper(fields[0]) {
				case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN":
					continue
				}
				cols[strings.Trim(fields[0], `"`)] = true
			}
			tables[m[1]] = cols
		}
		for _, m := range alterTable.FindAllStringSubmatch(sql, -1) {
			cols, ok := tables[m[1]]
			if !ok {
				continue
			}
			for _, c := range addColumn.FindAllStringSubmatch(m[2], -1) {
				cols[c[1]] = true
			}
			for _, c := range dropColumn.FindAllStringSubmatch(m[2], -1) {
				delete(cols, c[1])
			}
		}
		for _, m := range dropTable.FindAllStringSubmatch(sql, -1) {
			delete(tables, m[1])
		}
	}
	return tables
}

// Every table that holds user rows is erased by a provider deletion: either
// it has a provider column, or it is a child table whose parent has one. A
// new table that is neither fails here (and the worker refuses to run).
func TestEveryUserTableIsCoveredByProviderErasure(t *testing.T) {
	tables := migratedColumns(t)
	for _, want := range []string{"ohs_data.quantity_samples", "ohs_data.sleep_phases", "ohs_sync.raw_objects", "ohs_sync.connections"} {
		if !tables[want]["user_id"] {
			t.Fatalf("migration parser lost %s", want)
		}
	}
	for name, cols := range tables {
		schema, _, _ := strings.Cut(name, ".")
		if (schema != "ohs_sync" && schema != "ohs_data") || !cols["user_id"] {
			continue
		}
		if cols["provider"] {
			if _, child := childTables[name]; child {
				t.Errorf("%s has a provider column and is also listed in childTables", name)
			}
			continue
		}
		if _, child := childTables[name]; !child {
			t.Errorf("%s holds user rows but has no provider column: add it to childTables (internal/deletion/provider_worker.go) with its parent", name)
		}
	}
	for name, c := range childTables {
		cols, ok := tables[name]
		if !ok || !cols["user_id"] || !cols["tenant_id"] || !cols[c.fk] {
			t.Errorf("childTables names %s.%s, which the migrations do not define with tenant_id and user_id", name, c.fk)
		}
		parent := tables[c.parent]
		if !parent["provider"] || !parent["id"] {
			t.Errorf("parent %s of %s needs id and provider columns", c.parent, name)
		}
	}
}

func TestValidProvider(t *testing.T) {
	for s, want := range map[string]bool{
		"apple_health": true, "withings": true, "intervals-icu": true, "x1": true,
		"": false, "..": false, "a/b": false, "Oura": false, "a.b": false, "-a": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
	} {
		if got := ValidProvider(s); got != want {
			t.Errorf("ValidProvider(%q) = %v, want %v", s, got, want)
		}
	}
}
