// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// calData draws normalized scores |e - b| / s for three groups whose model
// scale s is right, twice too small, and half too large, with t errors.
func calData(seed uint64, perGroup map[string]int, nu float64) []CalPoint {
	r := rand.New(rand.NewPCG(seed, 5))
	miss := map[string]float64{"right": 1, "small": 2, "large": 0.5}
	var pts []CalPoint
	for _, g := range []string{"right", "small", "large"} {
		for range perGroup[g] {
			e := miss[g] * drawT(r, nu)
			pts = append(pts, CalPoint{Path: []string{"heart_rate", "dev", g}, Score: math.Abs(e)})
		}
	}
	return pts
}

// drawT draws a standard t by inversion.
func drawT(r *rand.Rand, nu float64) float64 {
	return StudentTQuantile(r.Float64()*0.999998+0.000001, nu)
}

func coverage(c *Calibration, pts []CalPoint, level float64) map[string]float64 {
	hit, n := map[string]float64{}, map[string]float64{}
	for _, p := range pts {
		g, _, ok := c.Lookup(p.Path)
		if !ok {
			continue
		}
		k := p.Path[2]
		n[k]++
		if p.Score <= g.Gamma*StudentTQuantile((1+level)/2, g.Nu) {
			hit[k]++
		}
	}
	for k := range hit {
		hit[k] /= n[k]
	}
	return hit
}

// Split conformal reaches the nominal coverage in every Mondrian group on
// fresh data, however wrong the model's scale was.
func TestConformal_NominalCoveragePerGroup(t *testing.T) {
	per := map[string]int{"right": 2000, "small": 2000, "large": 2000}
	c := FitCalibration(calData(1, per, 4), CalOptions{Version: "test"})
	test := calData(2, map[string]int{"right": 20000, "small": 20000, "large": 20000}, 4)
	for g, cov := range coverage(c, test, 0.9) {
		assert.InDeltaf(t, 0.9, cov, 0.015, "group %s", g)
	}
	small, key, _ := c.Lookup([]string{"heart_rate", "dev", "small"})
	assert.Equal(t, "heart_rate|dev|small", key)
	right, _, _ := c.Lookup([]string{"heart_rate", "dev", "right"})
	assert.InDelta(t, 2, small.Gamma/right.Gamma, 0.2)
	// The other levels follow when the chosen nu matches the tails.
	for _, lvl := range []float64{0.5, 0.8, 0.95} {
		for g, cov := range coverage(c, test, lvl) {
			assert.InDeltaf(t, lvl, cov, 0.03, "group %s level %v", g, lvl)
		}
	}
}

// A group with fewer than 50 points borrows its parent's factor.
func TestConformal_ParentFallback(t *testing.T) {
	pts := calData(3, map[string]int{"right": 3000, "small": 30}, 6)
	c := FitCalibration(pts, CalOptions{Version: "test"})
	_, key, ok := c.Lookup([]string{"heart_rate", "dev", "small"})
	require.True(t, ok)
	assert.Equal(t, "heart_rate|dev", key)
	_, key, _ = c.Lookup([]string{"heart_rate", "other_device", "rest"})
	assert.Equal(t, "heart_rate", key)
	_, key, _ = c.Lookup([]string{"glucose", "x", "y"})
	assert.Equal(t, "*", key)
	assert.Equal(t, 50, c.MinPoints)
}

// The degrees of freedom follow the tails: heavy for t errors, light for
// normal ones.
func TestConformal_ChoosesNu(t *testing.T) {
	heavy := FitCalibration(calData(4, map[string]int{"right": 20000}, 3), CalOptions{Version: "t"})
	light := FitCalibration(calData(5, map[string]int{"right": 20000}, math.Inf(1)), CalOptions{Version: "n"})
	h, _, _ := heavy.Lookup([]string{"heart_rate", "dev", "right"})
	l, _, _ := light.Lookup([]string{"heart_rate", "dev", "right"})
	assert.LessOrEqual(t, h.Nu, 4.0)
	assert.GreaterOrEqual(t, l.Nu, 15.0)
}

func TestCalibration_JSONRoundTrip(t *testing.T) {
	c := FitCalibration(calData(6, map[string]int{"right": 500}, 4), CalOptions{Version: "v1"})
	b, err := c.MarshalJSON()
	require.NoError(t, err)
	back, err := ParseCalibration(b)
	require.NoError(t, err)
	assert.Equal(t, c.Groups, back.Groups)
	_, err = ParseCalibration([]byte(`{"version":"x","groups":{"a":{"n":10,"gamma":-1,"nu":4}}}`))
	assert.Error(t, err)
}
