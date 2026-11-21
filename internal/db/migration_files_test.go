// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var migrationFileName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

// migrationFile is one parsed file name of the migrations directory.
type migrationFile struct {
	version   uint
	name, dir string // "quantity_compression", "up" or "down"
}

// parseMigrationNames parses the file names of a migrations directory.
func parseMigrationNames(names []string) ([]migrationFile, []string) {
	var files []migrationFile
	var problems []string
	for _, n := range names {
		m := migrationFileName.FindStringSubmatch(n)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s: not named NNNN_name.up.sql or NNNN_name.down.sql", n))
			continue
		}
		v, _ := strconv.ParseUint(m[1], 10, 32)
		if v == 0 {
			problems = append(problems, n+": version 0 means \"no migration\"")
			continue
		}
		files = append(files, migrationFile{version: uint(v), name: m[2], dir: m[3]})
	}
	return files, problems
}

// checkMigrationFiles returns what is wrong with a set of migration files,
// given the versions the latest release shipped:
//
//   - two migrations with one number (golang-migrate refuses to start);
//   - an up file with no down file of the same name, or the reverse;
//   - a migration numbered below the highest released one that the release
//     did not ship: golang-migrate would skip it on every database that ran
//     that release;
//   - a released migration that is gone.
func checkMigrationFiles(names []string, released []uint) []string {
	files, problems := parseMigrationNames(names)
	type pair struct{ up, down []string }
	byVersion := map[uint]*pair{}
	for _, f := range files {
		p := byVersion[f.version]
		if p == nil {
			p = &pair{}
			byVersion[f.version] = p
		}
		if f.dir == "up" {
			p.up = append(p.up, f.name)
		} else {
			p.down = append(p.down, f.name)
		}
	}
	var highestReleased uint
	wasReleased := map[uint]bool{}
	for _, v := range released {
		wasReleased[v] = true
		highestReleased = max(highestReleased, v)
	}
	versions := make([]uint, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for _, v := range versions {
		p := byVersion[v]
		switch {
		case len(p.up) > 1 || len(p.down) > 1:
			problems = append(problems, fmt.Sprintf("%04d: more than one migration has this number (%s)",
				v, strings.Join(append(append([]string{}, p.up...), p.down...), ", ")))
		case len(p.up) == 0:
			problems = append(problems, fmt.Sprintf("%04d_%s: a down file with no up file", v, p.down[0]))
		case len(p.down) == 0:
			problems = append(problems, fmt.Sprintf("%04d_%s: an up file with no down file", v, p.up[0]))
		case p.up[0] != p.down[0]:
			problems = append(problems, fmt.Sprintf("%04d: the up file is named %q, the down file %q", v, p.up[0], p.down[0]))
		}
		if v < highestReleased && !wasReleased[v] {
			problems = append(problems, fmt.Sprintf("%04d: numbered below %04d, the highest migration of the latest release, "+
				"which did not ship it: databases already past it would skip it. Give it the next free number above %04d",
				v, highestReleased, versions[len(versions)-1]))
		}
	}
	for _, v := range released {
		if p := byVersion[v]; p == nil || len(p.up) == 0 {
			problems = append(problems, fmt.Sprintf("%04d: shipped in the latest release, now missing: "+
				"a released migration is never removed or renumbered", v))
		}
	}
	return problems
}

// readReleased reads released_migrations.txt.
func readReleased(t *testing.T) []uint {
	t.Helper()
	f, err := os.Open("released_migrations.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []uint
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		v, err := strconv.ParseUint(line, 10, 32)
		if err != nil {
			t.Fatalf("released_migrations.txt: %q is not a migration number", line)
		}
		out = append(out, uint(v))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("released_migrations.txt lists no migration")
	}
	return out
}

// The migrations on disk: no duplicate number, every up with its down, and
// nothing new numbered below what the latest release shipped.
func TestMigrationFiles(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, p := range checkMigrationFiles(names, readReleased(t)) {
		t.Error(p)
	}
}

// checkMigrationFiles catches each mistake it is there for.
func TestCheckMigrationFiles(t *testing.T) {
	good := []string{
		"0001_baseline.up.sql", "0001_baseline.down.sql",
		"0002_seed.up.sql", "0002_seed.down.sql",
		"0005_index.up.sql", "0005_index.down.sql",
	}
	released := []uint{1, 2, 5}
	with := func(extra ...string) []string { return append(append([]string{}, good...), extra...) }
	without := func(drop string) []string {
		var out []string
		for _, n := range good {
			if n != drop {
				out = append(out, n)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		names    []string
		released []uint
		want     string // substring of the one problem expected; "" for none
	}{
		{"clean", good, released, ""},
		{"new migration above the release", with("0006_new.up.sql", "0006_new.down.sql"), released, ""},
		{"fills a gap below the release", with("0003_late.up.sql", "0003_late.down.sql"), released, "0003: numbered below 0005"},
		{"a gap above the release is free", with("0009_far.up.sql", "0009_far.down.sql", "0007_near.up.sql", "0007_near.down.sql"), released, ""},
		{"duplicate number", with("0002_other.up.sql", "0002_other.down.sql"), released, "0002: more than one migration"},
		{"up without down", with("0006_new.up.sql"), released, "0006_new: an up file with no down file"},
		{"down without up", with("0006_new.down.sql"), released, "0006_new: a down file with no up file"},
		{"names differ", with("0006_new.up.sql", "0006_old.down.sql"), released, "0006: the up file is named"},
		{"released migration removed", without("0002_seed.up.sql"), released, "0002"},
		{"bad name", with("6_new.up.sql"), released, "not named NNNN_name"},
		{"version zero", with("0000_none.up.sql"), released, "version 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkMigrationFiles(tc.names, tc.released)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("problems %q, want none", got)
				}
				return
			}
			if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Fatalf("problems %q, want one containing %q", got, tc.want)
			}
		})
	}
}
