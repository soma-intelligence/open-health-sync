// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// calData draws normalized residuals (e - b) / s for three groups whose
// model scale s is right, twice too small, and half too large, with t
// errors.
func calData(seed uint64, perGroup map[string]int, nu float64) []CalPoint {
	r := rand.New(rand.NewPCG(seed, 5))
	miss := map[string]float64{"right": 1, "small": 2, "large": 0.5}
	var pts []CalPoint
	for _, g := range []string{"right", "small", "large"} {
		for range perGroup[g] {
			e := miss[g] * drawT(r, nu)
			pts = append(pts, CalPoint{Path: []string{"heart_rate", "dev", g}, Residual: e})
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
		if math.Abs(p.Residual-g.Shift) <= g.Gamma*StudentTQuantile((1+level)/2, g.Nu) {
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
	// Factors never cross metrics: a metric with no group has none.
	_, _, ok = c.Lookup([]string{"glucose", "x", "y"})
	assert.False(t, ok)
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

// A group whose errors sit off the model's bias gets that offset as a shift
// (the median normalized residual), and its intervals are centred on it.
func TestConformal_LearnsShift(t *testing.T) {
	pts := calData(7, map[string]int{"right": 5000}, 4)
	for i := range pts {
		pts[i].Residual += 1.5
	}
	c := FitCalibration(pts, CalOptions{Version: "s"})
	g, _, _ := c.Lookup([]string{"heart_rate", "dev", "right"})
	assert.InDelta(t, 1.5, g.Shift, 0.05)
	assert.InDelta(t, 1, g.Gamma, 0.06)
	test := calData(8, map[string]int{"right": 20000}, 4)
	for i := range test {
		test[i].Residual += 1.5
	}
	assert.InDelta(t, 0.9, coverage(c, test, 0.9)["right"], 0.015)
}

// A small group takes its parent's degrees of freedom, so nu is never
// chosen on a handful of tails.
func TestConformal_SmallGroupInheritsNu(t *testing.T) {
	pts := calData(9, map[string]int{"right": 20000}, 3)
	small := calData(10, map[string]int{"small": 120}, math.Inf(1))
	c := FitCalibration(append(pts, small...), CalOptions{Version: "n"})
	parent, _, _ := c.Lookup([]string{"heart_rate", "dev"})
	g, key, _ := c.Lookup([]string{"heart_rate", "dev", "small"})
	assert.Equal(t, "heart_rate|dev|small", key)
	assert.Equal(t, parent.Nu, g.Nu)
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

func TestIsotonic_Golden(t *testing.T) {
	// Pool adjacent violators: (1, 0), (2, 1), (3, 0), (4, 1) -> 0, 0.5, 0.5, 1.
	x := []float64{1, 2, 3, 4}
	y := []float64{0, 1, 0, 1}
	got := isotonic(x, y, nil)
	assert.Equal(t, []float64{0, 0.5, 0.5, 1}, got)
	// Ties are one point: whatever order their outcomes come in, a raw q
	// shared by many figures maps to their hit rate.
	m0 := fitQMap([]float64{0.5, 0.5, 0.5, 0.5, 0.2}, []bool{false, false, true, true, false})
	assert.Equal(t, QMap{{0.2, 0}, {0.5, 0.5}}, m0)
	// Raw q's that differ only by rounding noise are ties too.
	m1 := fitQMap([]float64{0.5 + 1e-12, 0.5 - 1e-12, 0.5 + 2e-12, 0.5}, []bool{false, true, true, false})
	assert.Equal(t, QMap{{0.5, 0.5}}, m1)
	m := QMap{{0, 0}, {0.5, 0.2}, {1, 1}}
	assert.InDelta(t, 0.1, m.Apply(0.25), 1e-12)
	assert.InDelta(t, 0.6, m.Apply(0.75), 1e-12)
	assert.Equal(t, 0.0, m.Apply(-1))
	assert.Equal(t, 1.0, m.Apply(2))
	// Outside its knots a map runs straight to (0, 0) and (1, 1): a raw q a
	// hair below the only knot maps next to it, not to a cliff.
	one := QMap{{0.55, 0.6}}
	assert.InDelta(t, 0.6, one.Apply(0.5499), 1e-3)
	assert.InDelta(t, 0.6, one.Apply(0.5501), 1e-3)
	assert.InDelta(t, 0.3, one.Apply(0.275), 1e-12)
	assert.Equal(t, 0.42, QMap(nil).Apply(0.42))
}

// mixtureData draws residuals a Student t fits badly (mostly tight, some
// very wide), with a tolerance spread over the scale, so the raw q is off;
// the q map fitted on one draw calibrates q on another.
func mixtureData(seed uint64, n int) []CalPoint {
	r := rand.New(rand.NewPCG(seed, 13))
	pts := make([]CalPoint, n)
	for i := range pts {
		e := 0.4 * r.NormFloat64()
		if r.Float64() < 0.15 {
			e = 4 * r.NormFloat64()
		}
		pts[i] = CalPoint{Path: []string{"heart_rate", "dev", "rest"}, Residual: e, Tol: 0.2 + 2*r.Float64()}
	}
	return pts
}

func qOf(c *Calibration, p CalPoint) (float64, bool) {
	g, _, _ := c.Lookup(p.Path)
	q := ProbWithin(p.Tol, p.Bias+g.Shift, g.Gamma, g.Nu)
	q = c.QMapFor(p.Path).Apply(q)
	return q, math.Abs(p.Residual+p.Bias) <= p.Tol
}

func TestConformal_QMapCalibratesQ(t *testing.T) {
	c := FitCalibration(mixtureData(1, 20000), CalOptions{Version: "q"})
	require.NotEmpty(t, c.QMapFor([]string{"heart_rate", "dev", "rest"}))
	test := mixtureData(2, 40000)
	var raw, cal []float64
	var hit []bool
	noMap := *c
	noMap.QMaps = nil
	for _, p := range test {
		q, h := qOf(c, p)
		q0, _ := qOf(&noMap, p)
		cal, raw, hit = append(cal, q), append(raw, q0), append(hit, h)
	}
	assert.Greater(t, ECE(raw, hit, 10), 0.03, "the t alone is off on this mixture")
	assert.Less(t, ECE(cal, hit, 10), 0.015)
}
