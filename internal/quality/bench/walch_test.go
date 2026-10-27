// SPDX-License-Identifier: AGPL-3.0-only

//go:build bench

package bench

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeWalchFixture writes one invented night in the release's layout:
// wake for the first hour, asleep for six, wake for the last, with the
// wrist moving only while awake.
func writeWalchFixture(t *testing.T) string {
	dir := t.TempDir()
	root := filepath.Join(dir, "walch", "extracted", "release")
	for _, d := range []string{"labels", "motion", "heart_rate"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	var labels, motion strings.Builder
	for e := 0; e < 8*120; e++ {
		stage := 2
		if e < 120 || e >= 7*120 {
			stage = 0
		}
		fmt.Fprintf(&labels, "%d %d\n", e*30, stage)
	}
	for s := 0.0; s < 8*3600; s += 0.5 {
		z := -1.0
		if s < 3600 || s >= 7*3600 {
			z = -1.0 - 0.2*math.Abs(math.Sin(s))
		}
		fmt.Fprintf(&motion, "%.2f 0.01 0.0 %.4f\n", s, z)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "labels", "42_labeled_sleep.txt"), []byte(labels.String()), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "motion", "42_acceleration.txt"), []byte(motion.String()), 0o644))
	return dir
}

func TestLoadWalch_Fixture(t *testing.T) {
	recs, res, err := LoadWalch(writeWalchFixture(t))
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, 360.0, recs[0].Ref, "six hours of scored sleep")
	assert.InDelta(t, 360, recs[0].Value, 10, "the rule finds the still hours")
	assert.Equal(t, 1, res.Nights)
	assert.Contains(t, res.Summary, "1 nights")
}

func TestLoadWalch_Missing(t *testing.T) {
	_, _, err := LoadWalch(t.TempDir())
	assert.ErrorIs(t, err, ErrMissing)
}
