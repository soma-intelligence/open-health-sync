// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Goldens from closed forms (nu = 1 is Cauchy, nu = 2 has an algebraic CDF)
// and from printed t tables.
func TestStudentTCDF_Goldens(t *testing.T) {
	assert.InDelta(t, 0.75, StudentTCDF(1, 1), 1e-12)
	assert.InDelta(t, 0.5+math.Atan(-3)/math.Pi, StudentTCDF(-3, 1), 1e-12)
	for _, x := range []float64{-4, -1.5, 0, 0.3, 2, 7} {
		want := 0.5 + x/(2*math.Sqrt(2+x*x))
		assert.InDeltaf(t, want, StudentTCDF(x, 2), 1e-12, "nu=2 x=%v", x)
	}
	assert.InDelta(t, 0.975, StudentTCDF(2.776445105, 4), 1e-8)
	assert.InDelta(t, 0.95, StudentTCDF(1.812461123, 10), 1e-8)
	assert.InDelta(t, 0.975, StudentTCDF(1.959963985, math.Inf(1)), 1e-9)
}

func TestStudentTQuantile_Goldens(t *testing.T) {
	assert.InDelta(t, 2.776445105, StudentTQuantile(0.975, 4), 1e-7)
	assert.InDelta(t, 1.812461123, StudentTQuantile(0.95, 10), 1e-7)
	assert.InDelta(t, 2.042272456, StudentTQuantile(0.975, 30), 1e-7)
	assert.InDelta(t, 12.70620474, StudentTQuantile(0.975, 1), 1e-6)
	assert.InDelta(t, 1.959963985, StudentTQuantile(0.975, math.Inf(1)), 1e-8)
	assert.InDelta(t, 0, StudentTQuantile(0.5, 3), 1e-12)
	assert.InDelta(t, -StudentTQuantile(0.9, 5), StudentTQuantile(0.1, 5), 1e-10)
}

// The quantile inverts the CDF everywhere, including far tails and small nu.
func TestStudentTQuantile_InvertsCDF(t *testing.T) {
	for _, nu := range []float64{1, 1.5, 2, 3, 4, 7, 15, 60} {
		for _, p := range []float64{1e-6, 0.001, 0.025, 0.2, 0.5, 0.68, 0.9, 0.995, 1 - 1e-6} {
			x := StudentTQuantile(p, nu)
			assert.InDeltaf(t, p, StudentTCDF(x, nu), 1e-9, "nu=%v p=%v", nu, p)
		}
	}
}

func TestRegIncBeta_Goldens(t *testing.T) {
	// I_x(1, 1) = x; I_x(a, 1) = x^a; I_x(1, b) = 1 - (1-x)^b.
	assert.InDelta(t, 0.3, RegIncBeta(0.3, 1, 1), 1e-14)
	assert.InDelta(t, math.Pow(0.6, 2.5), RegIncBeta(0.6, 2.5, 1), 1e-13)
	assert.InDelta(t, 1-0.6*0.6*0.6, RegIncBeta(0.4, 1, 3), 1e-13)
	// Symmetry I_x(a, b) = 1 - I_{1-x}(b, a).
	assert.InDelta(t, 1-RegIncBeta(0.8, 4, 2.5), RegIncBeta(0.2, 2.5, 4), 1e-13)
	assert.Equal(t, 0.0, RegIncBeta(0, 2, 3))
	assert.Equal(t, 1.0, RegIncBeta(1, 2, 3))
}

func TestStudentTPDF_IntegratesToCDF(t *testing.T) {
	// Trapezoid from -40 to x against the CDF.
	for _, nu := range []float64{2, 5} {
		const n = 41300
		h, sum := 41.3/n, 0.0
		for i := range n {
			v := -40 + float64(i)*h
			sum += h * (StudentTPDF(v, nu) + StudentTPDF(v+h, nu)) / 2
		}
		assert.InDelta(t, StudentTCDF(1.3, nu)-StudentTCDF(-40, nu), sum, 1e-5)
	}
}

func TestDigamma_Goldens(t *testing.T) {
	assert.InDelta(t, -0.5772156649, Digamma(1), 1e-9)
	assert.InDelta(t, -1.9635100260, Digamma(0.5), 1e-9)
	assert.InDelta(t, 1.2561176684, Digamma(4), 1e-9)
}

// E log|T| has a closed form; check it against a Monte Carlo free numeric
// integral for one nu and against the normal limit.
func TestMeanLogAbsT(t *testing.T) {
	assert.InDelta(t, -(0.5772156649+math.Ln2)/2, MeanLogAbsT(math.Inf(1)), 1e-9)
	nu, h, sum := 4.0, 1e-4, 0.0
	for v := h / 2; v < 400; v += h {
		sum += 2 * h * math.Log(v) * StudentTPDF(v, nu)
	}
	assert.InDelta(t, sum, MeanLogAbsT(nu), 2e-3)
}
