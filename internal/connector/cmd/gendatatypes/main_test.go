// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

const root = "../../../.."

// TestCommittedFilesAreCurrent fails when a mapper changed without the
// generated lists. Fix it with: go run ./internal/connector/cmd/gendatatypes
func TestCommittedFilesAreCurrent(t *testing.T) {
	types, err := scanAll(root)
	require.NoError(t, err)

	src, err := goFile(types)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(root, goPath))
	require.NoError(t, err)
	assert.Equal(t, string(src), string(got), "%s is out of date: run go run ./internal/connector/cmd/gendatatypes", goPath)

	doc, err := os.ReadFile(filepath.Join(root, readmePath))
	require.NoError(t, err)
	want, err := splice(doc, table(types))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(doc), "%s is out of date: run go run ./internal/connector/cmd/gendatatypes", readmePath)
}

// TestEveryConnectorSaysWhatItDelivers checks the lists the API serves:
// record kinds first, then quantity names, no repeat.
func TestEveryConnectorSaysWhatItDelivers(t *testing.T) {
	all := connector.All()
	require.NotEmpty(t, all)
	for _, c := range all {
		list := connector.DataTypesOf(c)
		require.NotEmpty(t, list, c.Slug())
		assert.True(t, slices.Contains(connector.Kinds, list[0]), "%s: starts with a record kind: %v", c.Slug(), list)
		seen := map[string]bool{}
		inKinds := true
		for _, v := range list {
			assert.False(t, seen[v], "%s: %s twice", c.Slug(), v)
			seen[v] = true
			isKind := slices.Contains(connector.Kinds, v)
			assert.False(t, isKind && !inKinds, "%s: kind %s after a quantity", c.Slug(), v)
			inKinds = inKinds && isKind
		}
	}
}

func TestScanReadsFieldsAndQuantities(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapper.go"), []byte(`package x

func m(res *model.MapResult) {
	res.Workouts = append(res.Workouts, model.Workout{})
	_ = model.MapResult{SleepSessions: nil}
	_ = sample(model.QTHeartRate, "body_mass", "not_a_quantity")
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mapper_test.go"), []byte(`package x

var _ = "step_count"
var _ = model.MapResult{DailySummaries: nil}
`), 0o644))
	r, err := Dir(dir, map[string]bool{"heart_rate": true, "body_mass": true, "step_count": true}, map[string]string{"QTHeartRate": "heart_rate"})
	require.NoError(t, err)
	assert.Equal(t, []string{"sleep", "workout"}, r.Kinds, "test files are not read")
	assert.Equal(t, []string{"body_mass", "heart_rate"}, r.Quantities)
}
