// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
)

// numericCRPS integrates (F(z) - 1{z >= y})^2 on a fine grid.
func numericCRPS(y, mu, s, nu float64) float64 {
	const n = 400000
	lo, hi := mu-400*s, mu+400*s
	h := (hi - lo) / n
	var sum float64
	for i := range n {
		z := lo + (float64(i)+0.5)*h
		f := StudentTCDF((z-mu)/s, nu)
		if z >= y {
			f--
		}
		sum += f * f * h
	}
	return sum
}

func TestCRPSStudentT_MatchesIntegral(t *testing.T) {
	for _, c := range []struct{ y, mu, s, nu float64 }{
		{1, 0, 1, 4}, {-2.5, 1, 2, 3}, {0.3, 0, 0.5, 10}, {5, 0, 1, 2.5},
	} {
		assert.InDeltaf(t, numericCRPS(c.y, c.mu, c.s, c.nu), CRPSStudentT(c.y, c.mu, c.s, c.nu), 2e-3, "%+v", c)
	}
	// The normal limit, closed form at the mean: s (sqrt(2) - 1) / sqrt(pi).
	assert.InDelta(t, (math.Sqrt2-1)/math.Sqrt(math.Pi)*2, CRPSStudentT(0, 0, 2, math.Inf(1)), 1e-12)
	assert.InDelta(t, CRPSStudentT(0.7, 0, 1, math.Inf(1)), CRPSStudentT(0.7, 0, 1, 1e6), 1e-4)
}

func TestIntervalScore(t *testing.T) {
	assert.InDelta(t, 4, IntervalScore(-2, 2, 0, 0.1), 1e-12)
	assert.InDelta(t, 4+2/0.1*1, IntervalScore(-2, 2, 3, 0.1), 1e-12)
	assert.InDelta(t, 4+2/0.1*0.5, IntervalScore(-2, 2, -2.5, 0.1), 1e-12)
}

func TestReliability(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var q []float64
	var hit []bool
	for range 200000 {
		p := r.Float64()
		q = append(q, p)
		hit = append(hit, r.Float64() < p)
	}
	assert.Less(t, ECE(q, hit, 10), 0.01, "a calibrated forecast")
	// Always saying 0.9 when the truth is 0.5: ECE 0.4.
	q2 := make([]float64, 1000)
	h2 := make([]bool, 1000)
	for i := range q2 {
		q2[i], h2[i] = 0.9, i%2 == 0
	}
	assert.InDelta(t, 0.4, ECE(q2, h2, 10), 1e-12)
	assert.InDelta(t, (0.01+0.81)/2, Brier(q2, h2), 1e-12)
}

func TestPICPAndMPIW(t *testing.T) {
	lo := []float64{0, 0, 0, 0}
	hi := []float64{1, 2, 3, 4}
	r := []float64{0.5, 3, 2.9, -1}
	assert.InDelta(t, 0.5, PICP(lo, hi, r), 1e-12)
	assert.InDelta(t, 2.5, MPIW(lo, hi), 1e-12)
}

// Equal-mass bins hold the same number of forecasts each, so a cluster of
// similar q's is split rather than lumped.
func TestECEEqualMass(t *testing.T) {
	q := []float64{0.1, 0.2, 0.3, 0.4, 0.6, 0.7, 0.8, 0.9}
	hit := []bool{false, false, true, false, true, true, false, true}
	// Two bins of four: mean q 0.25 vs hit rate 0.25, mean q 0.75 vs 0.75.
	assert.InDelta(t, 0, ECEEqualMass(q, hit, 2), 1e-12)
	// Always 0.9 when the truth is 0.5, in any binning: 0.4.
	q2 := make([]float64, 1000)
	h2 := make([]bool, 1000)
	for i := range q2 {
		q2[i], h2[i] = 0.9, i%2 == 0
	}
	assert.InDelta(t, 0.4, ECEEqualMass(q2, h2, 20), 0.02)
	r := rand.New(rand.NewPCG(3, 4))
	var q3 []float64
	var h3 []bool
	for range 100000 {
		p := r.Float64()
		q3 = append(q3, p)
		h3 = append(h3, r.Float64() < p)
	}
	assert.Less(t, ECEEqualMass(q3, h3, 20), 0.01)
}
