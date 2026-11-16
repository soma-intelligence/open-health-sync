// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// A database at the binary's version passes; any other state is refused with
// a message naming both versions and the ohs migrate command to run.
func TestSchemaStateErr(t *testing.T) {
	if err := (SchemaState{Database: 49, Binary: 49}).Err(); err != nil {
		t.Fatalf("matching versions: %v", err)
	}
	for _, tc := range []struct {
		name  string
		state SchemaState
		is    error
		want  []string
	}{
		{"behind", SchemaState{Database: 44, Binary: 49}, ErrSchemaBehind, []string{"44", "49", "`ohs migrate up`"}},
		{"never migrated", SchemaState{Database: 0, Binary: 49}, ErrSchemaBehind, []string{"version 0", "49", "`ohs migrate up`"}},
		{"ahead", SchemaState{Database: 52, Binary: 49}, ErrSchemaAhead, []string{"52", "49", "`ohs migrate version`"}},
		{"dirty", SchemaState{Database: 49, Binary: 49, Dirty: true}, ErrSchemaDirty, []string{"49", "`ohs migrate force <version> --yes`"}},
		{"dirty wins over behind", SchemaState{Database: 45, Binary: 49, Dirty: true}, ErrSchemaDirty, []string{"45", "49"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.state.Err()
			if !errors.Is(err, tc.is) {
				t.Fatalf("Err() = %v, want %v", err, tc.is)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("message %q lacks %q", err, w)
				}
			}
		})
	}
}

// Force refuses a version this binary has no migration for, before it
// touches the database.
func TestForceUnknownVersion(t *testing.T) {
	latest, err := LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	err = Force("postgres://nobody@127.0.0.1:1/none?connect_timeout=1", latest+1000)
	if err == nil || !strings.Contains(err.Error(), "has no migration") {
		t.Fatalf("Force(unknown) = %v, want a refusal naming the version", err)
	}
	for _, v := range []uint{1, latest} {
		if ok, err := embedsVersion(v); err != nil || !ok {
			t.Fatalf("embedsVersion(%d) = %v, %v", v, ok, err)
		}
	}
}

// A dirty schema may only be forced to the dirty version or one of its two
// neighbours among the migrations of the binary.
func TestForceTargets(t *testing.T) {
	versions := []uint{1, 2, 15, 18}
	for _, tc := range []struct {
		dirty uint
		want  []uint
	}{
		{18, []uint{15, 18}},    // a failed up to 18, or a failed down of the next one
		{15, []uint{2, 15, 18}}, // a failed up to 15, or a failed down of 18
		{1, []uint{0, 1, 2}},
		{0, []uint{0, 1}}, // a failed down of the first migration
	} {
		got, err := forceTargets(tc.dirty, versions)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("forceTargets(%d) = %v, %v, want %v", tc.dirty, got, err, tc.want)
		}
	}
	if _, err := forceTargets(16, versions); err == nil {
		t.Error("a dirty version the binary does not have must be refused")
	}
	all, err := embeddedVersions()
	if err != nil || len(all) < 2 || !slices.IsSorted(all) {
		t.Fatalf("embeddedVersions() = %v, %v", all, err)
	}
	if latest, _ := LatestVersion(); all[len(all)-1] != latest {
		t.Fatalf("last embedded version %d, latest %d", all[len(all)-1], latest)
	}
}
