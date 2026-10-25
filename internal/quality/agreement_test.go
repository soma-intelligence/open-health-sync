// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlandAltman_Golden(t *testing.T) {
	a := []float64{10, 12, 14, 16}
	b := []float64{9, 12, 12, 15}
	ba, err := BlandAltman(a, b)
	require.NoError(t, err)
	// d = 1, 0, 2, 1: mean 1, SD sqrt(2/3).
	assert.Equal(t, 4, ba.N)
	assert.InDelta(t, 1, ba.Mean, 1e-12)
	assert.InDelta(t, math.Sqrt(2.0/3), ba.SD, 1e-12)
	assert.InDelta(t, 1-1.96*math.Sqrt(2.0/3), ba.LoALow, 1e-12)
	assert.InDelta(t, 1+1.96*math.Sqrt(2.0/3), ba.LoAHigh, 1e-12)

	_, err = BlandAltman([]float64{1}, []float64{2})
	assert.Error(t, err)
	_, err = BlandAltman([]float64{1, 2}, []float64{2})
	assert.Error(t, err)
}

// simulateSources draws n true values and three linear sources with known
// error SDs.
func simulateSources(seed uint64, n int, sig [3]float64) (x [3][]float64) {
	r := rand.New(rand.NewPCG(seed, 7))
	a := [3]float64{0, 3, -2}
	b := [3]float64{1, 0.9, 1.1}
	for range n {
		truth := 70 + 12*r.NormFloat64()
		for i := range 3 {
			x[i] = append(x[i], a[i]+b[i]*truth+sig[i]*r.NormFloat64())
		}
	}
	return x
}

// Triple collocation recovers each source's error variance, in its own
// units, without a reference.
func TestTripleCollocation_RecoversVariances(t *testing.T) {
	sig := [3]float64{2, 4, 6}
	for seed := range uint64(5) {
		x := simulateSources(seed, 20000, sig)
		tc, err := TripleCollocation(x[0], x[1], x[2])
		require.NoError(t, err)
		for i := range 3 {
			assert.InDeltaf(t, 1, math.Sqrt(tc.Var[i])/sig[i], 0.05, "seed %d source %d", seed, i)
			assert.False(t, tc.Clipped[i])
		}
		// rho^2 with the truth: b^2 var(x) / var(y).
		want0 := 144.0 / (144 + 4)
		assert.InDelta(t, want0, tc.Rho2[0], 0.01)
	}
}

// Two sources with correlated errors break the independence assumption.
// With y2 and y3 erring in opposite directions, C23 shrinks and the
// variance of y1 comes out negative: the guard clips it to zero and says so.
func TestTripleCollocation_NegativeVarianceGuard(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	var y1, y2, y3 []float64
	for range 5000 {
		truth := 60 + 10*r.NormFloat64()
		common := 5 * r.NormFloat64()
		y1 = append(y1, truth+0.5*r.NormFloat64())
		y2 = append(y2, truth+common+0.5*r.NormFloat64())
		y3 = append(y3, truth-common+0.5*r.NormFloat64())
	}
	tc, err := TripleCollocation(y1, y2, y3)
	require.NoError(t, err)
	assert.True(t, tc.Clipped[0], "the correlated errors of y2 and y3 drive the variance of y1 negative")
	assert.Equal(t, 0.0, tc.Var[0])
}

func TestTripleCollocation_Degenerate(t *testing.T) {
	c := []float64{5, 5, 5, 5, 5}
	v := []float64{1, 2, 3, 4, 5}
	_, err := TripleCollocation(c, v, v)
	assert.ErrorIs(t, err, ErrDegenerate)
	_, err = TripleCollocation(v[:2], v[:2], v[:2])
	assert.Error(t, err)
}

func TestNEffAR1(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 1))
	n := 50000
	ar := make([]float64, n)
	white := make([]float64, n)
	for i := 1; i < n; i++ {
		ar[i] = 0.8*ar[i-1] + r.NormFloat64()
		white[i] = r.NormFloat64()
	}
	assert.InDelta(t, float64(n)*0.2/1.8, NEffAR1(ar), float64(n)*0.2/1.8*0.1)
	assert.InDelta(t, float64(n), NEffAR1(white), float64(n)*0.05)
	// Alternating series: negative r1 is clipped, n_eff never exceeds n.
	alt := make([]float64, 100)
	for i := range alt {
		alt[i] = float64(i % 2)
	}
	assert.Equal(t, 100.0, NEffAR1(alt))
	assert.Equal(t, 1.0, NEffAR1([]float64{3}))
}

func TestShrinkSigma(t *testing.T) {
	assert.InDelta(t, math.Sqrt(8), ShrinkSigma(2, 4, 10, 10), 1e-12)
	assert.Equal(t, 2.0, ShrinkSigma(2, 4, 0, 10))
	assert.Equal(t, 2.0, ShrinkSigma(2, 0, 100, 10))
	// More data moves it toward the data, monotonically.
	prev := 2.0
	for _, n := range []float64{1, 10, 100, 1000} {
		s := ShrinkSigma(2, 4, n, 20)
		assert.Greater(t, s, prev)
		prev = s
	}
	assert.Less(t, prev, 4.0)
}

func TestBiasPosterior(t *testing.T) {
	// Equal precision: the midpoint, variance halved.
	m, sd := BiasPosterior(0, 1, 2, 10, 100)
	assert.InDelta(t, 1, m, 1e-12)
	assert.InDelta(t, math.Sqrt(0.5), sd, 1e-12)
	m, sd = BiasPosterior(-1, 2, 5, 3, 0)
	assert.Equal(t, -1.0, m)
	assert.Equal(t, 2.0, sd)
}

func TestSplitPairVariance(t *testing.T) {
	v1, v2 := SplitPairVariance(25, 3, 4)
	assert.InDelta(t, 9, v1, 1e-12)
	assert.InDelta(t, 16, v2, 1e-12)
}

func TestAlign(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	a := []Point{{t0, 60}, {t0.Add(time.Minute), 62}, {t0.Add(6 * time.Minute), 70}, {t0.Add(20 * time.Minute), 80}}
	b := []Point{{t0.Add(2 * time.Minute), 59}, {t0.Add(7 * time.Minute), 71}, {t0.Add(30 * time.Minute), 90}}
	x, y := Align(5*time.Minute, a, b)
	assert.Equal(t, []float64{61, 70}, x)
	assert.Equal(t, []float64{59, 71}, y)
	x, y, z := Align3(5*time.Minute, a, b, b)
	assert.Len(t, x, 2)
	assert.Equal(t, y, z)
}

// A pair against an anchor identifies the bias and the error of the other
// source: the anchor's prior variance is taken out of the difference.
func TestPairAgreement_Anchored(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var dev, ref []Point
	for i := range 3000 {
		truth := 70 + 10*math.Sin(float64(i)/30)
		at := t0.Add(time.Duration(i) * 5 * time.Minute)
		dev = append(dev, Point{at, truth + 4 + 3*r.NormFloat64()})
		ref = append(ref, Point{at, truth + 1*r.NormFloat64()})
	}
	ag, err := PairAgreement(5*time.Minute, dev, ref, 3, 1, true)
	require.NoError(t, err)
	assert.True(t, ag.Anchored)
	assert.InDelta(t, 4, ag.Bias, 0.2)
	assert.InDelta(t, 3, ag.Sigma, 0.15)
	assert.Equal(t, MethodBlandAltman, ag.Method)
	assert.Greater(t, ag.NEff, 1000.0)

	// Without an anchor the bias is not identifiable and the variance of the
	// difference is split by the priors.
	ag, err = PairAgreement(5*time.Minute, dev, ref, 3, 1, false)
	require.NoError(t, err)
	assert.False(t, ag.Anchored)
	assert.InDelta(t, math.Sqrt(10*9.0/10), ag.Sigma, 0.2)
}
