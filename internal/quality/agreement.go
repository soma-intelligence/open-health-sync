// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"errors"
	"math"
	"sort"
	"time"
)

// Stage 3 of the spec: what two or more independent sources of the same user
// say about each other's error, without a reference. With two sources the
// paired differences bound the sum of the two error variances; with three,
// triple collocation gives each one; an anchor (a chest strap, a cuff)
// identifies bias as well. The per-user figure is then shrunk toward the
// prior, by an effective sample size corrected for autocorrelation.

// Methods of an agreement estimate.
const (
	MethodBlandAltman       = "bland_altman"
	MethodTripleCollocation = "triple_collocation"
)

// ErrDegenerate is returned when the sources carry no common signal (a
// constant series, a zero covariance), so the estimators are undefined.
var ErrDegenerate = errors.New("quality: degenerate series, no common signal")

// Agreement is the per-user, per-source estimate stage 3 hands the score.
// Values are in the metric's error space.
type Agreement struct {
	Method string
	// Sigma is the data's estimate of this source's random error.
	Sigma float64
	// NEff is the effective number of independent overlap observations.
	NEff float64
	// Bias and BiasSD are only meaningful when Anchored.
	Bias, BiasSD float64
	Anchored     bool
	Flags        []Flag
}

// BA is a Bland-Altman summary of paired differences a - b.
type BA struct {
	N               int
	Mean, SD        float64
	LoALow, LoAHigh float64
	Differences     []float64 `json:"-"`
	Lag1            float64   // lag-1 autocorrelation of the differences
}

// BlandAltman summarizes the paired differences a[i] - b[i].
func BlandAltman(a, b []float64) (BA, error) {
	if len(a) != len(b) {
		return BA{}, errors.New("quality: Bland-Altman needs paired series")
	}
	if len(a) < 2 {
		return BA{}, errors.New("quality: Bland-Altman needs at least two pairs")
	}
	d := make([]float64, len(a))
	for i := range a {
		d[i] = a[i] - b[i]
	}
	m, v := meanVar(d)
	sd := math.Sqrt(v)
	return BA{N: len(d), Mean: m, SD: sd, LoALow: m - 1.96*sd, LoAHigh: m + 1.96*sd, Differences: d, Lag1: lag1(d)}, nil
}

// meanVar returns the mean and the unbiased variance.
func meanVar(x []float64) (float64, float64) {
	var s float64
	for _, v := range x {
		s += v
	}
	m := s / float64(len(x))
	var ss float64
	for _, v := range x {
		ss += (v - m) * (v - m)
	}
	if len(x) < 2 {
		return m, 0
	}
	return m, ss / float64(len(x)-1)
}

// cov returns the unbiased covariance of two series of equal length.
func cov(x, y []float64) float64 {
	mx, _ := meanVar(x)
	my, _ := meanVar(y)
	var s float64
	for i := range x {
		s += (x[i] - mx) * (y[i] - my)
	}
	return s / float64(len(x)-1)
}

// TC is a triple collocation estimate.
type TC struct {
	N int
	// Var is each source's error variance, in its own units.
	Var [3]float64
	// Rho2 is each source's squared correlation with the unknown truth
	// (extended triple collocation, McColl et al. 2014).
	Rho2 [3]float64
	// Clipped says the raw estimate was negative and was set to zero: the
	// errors are probably not independent, or n is small.
	Clipped [3]bool
}

// TripleCollocation estimates the error variance of three collocated
// sources with independent errors, in the covariance form:
//
//	sigma_1^2 = C11 - C12 C13 / C23   (and cyclic)
//
// It needs no reference and allows each source its own offset and scale.
func TripleCollocation(y1, y2, y3 []float64) (TC, error) {
	n := len(y1)
	if len(y2) != n || len(y3) != n {
		return TC{}, errors.New("quality: triple collocation needs collocated series")
	}
	if n < 3 {
		return TC{}, errors.New("quality: triple collocation needs at least three points")
	}
	ys := [3][]float64{y1, y2, y3}
	var c [3][3]float64
	for i := range 3 {
		for j := i; j < 3; j++ {
			c[i][j] = cov(ys[i], ys[j])
			c[j][i] = c[i][j]
		}
	}
	scale := math.Max(c[0][0], math.Max(c[1][1], c[2][2]))
	if scale <= 0 {
		return TC{}, ErrDegenerate
	}
	for _, v := range []float64{c[0][1], c[0][2], c[1][2]} {
		if math.Abs(v) <= 1e-9*scale {
			return TC{}, ErrDegenerate
		}
	}
	tc := TC{N: n}
	for i := range 3 {
		j, k := (i+1)%3, (i+2)%3
		v := c[i][i] - c[i][j]*c[i][k]/c[j][k]
		if v < 0 {
			v, tc.Clipped[i] = 0, true
		}
		tc.Var[i] = v
		tc.Rho2[i] = math.Min(1, math.Max(0, c[i][j]*c[i][k]/(c[i][i]*c[j][k])))
	}
	return tc, nil
}

// lag1 is the lag-1 autocorrelation of a series.
func lag1(x []float64) float64 {
	if len(x) < 3 {
		return 0
	}
	m, v := meanVar(x)
	if v == 0 {
		return 0
	}
	var s float64
	for i := 1; i < len(x); i++ {
		s += (x[i] - m) * (x[i-1] - m)
	}
	return s / (float64(len(x)-1) * v)
}

// NEffAR1 is the effective sample size of an autocorrelated series under an
// AR(1) model, n (1 - r1) / (1 + r1). A negative r1 is taken as zero, so
// n_eff never exceeds n.
func NEffAR1(x []float64) float64 {
	n := float64(len(x))
	if n < 3 {
		return n
	}
	r := math.Max(0, math.Min(lag1(x), 0.999))
	return n * (1 - r) / (1 + r)
}

// ShrinkSigma combines a prior sigma with a data estimate in log space,
// precision weighted: the prior counts as w0 observations, the data as nEff.
// Without data (nEff or sigmaData not positive) it returns the prior.
func ShrinkSigma(sigmaPrior, sigmaData, nEff, w0 float64) float64 {
	if nEff <= 0 || sigmaData <= 0 || sigmaPrior <= 0 {
		return sigmaPrior
	}
	return math.Exp((w0*math.Log(sigmaPrior) + nEff*math.Log(sigmaData)) / (w0 + nEff))
}

// BiasPosterior is the normal update of a prior bias with the mean
// difference against an anchor, dbar with SD sd over nEff observations.
func BiasPosterior(prior, priorSD, dbar, sd, nEff float64) (float64, float64) {
	if nEff <= 0 || sd <= 0 || priorSD <= 0 {
		return prior, priorSD
	}
	pp, pd := 1/(priorSD*priorSD), nEff/(sd*sd)
	return (prior*pp + dbar*pd) / (pp + pd), math.Sqrt(1 / (pp + pd))
}

// SplitPairVariance splits the variance of the difference of two sources
// in proportion to their prior variances: with two sources only the sum is
// identifiable.
func SplitPairVariance(varDiff, sigma1, sigma2 float64) (float64, float64) {
	w := sigma1*sigma1 + sigma2*sigma2
	if w == 0 {
		return varDiff / 2, varDiff / 2
	}
	return varDiff * sigma1 * sigma1 / w, varDiff * sigma2 * sigma2 / w
}

// Point is one figure of a series.
type Point struct {
	T time.Time
	V float64
}

// bucketMeans averages a series per bucket of the grain.
func bucketMeans(grain time.Duration, s []Point) map[int64]float64 {
	sum := map[int64]float64{}
	n := map[int64]float64{}
	for _, p := range s {
		k := p.T.UnixNano() / int64(grain)
		sum[k] += p.V
		n[k]++
	}
	for k := range sum {
		sum[k] /= n[k]
	}
	return sum
}

// Align averages two series per bucket of the grain and returns the
// buckets both have, in time order.
func Align(grain time.Duration, a, b []Point) ([]float64, []float64) {
	ma, mb := bucketMeans(grain, a), bucketMeans(grain, b)
	keys := make([]int64, 0, len(ma))
	for k := range ma {
		if _, ok := mb[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	x, y := make([]float64, len(keys)), make([]float64, len(keys))
	for i, k := range keys {
		x[i], y[i] = ma[k], mb[k]
	}
	return x, y
}

// Align3 is Align for three series.
func Align3(grain time.Duration, a, b, c []Point) ([]float64, []float64, []float64) {
	ma, mb, mc := bucketMeans(grain, a), bucketMeans(grain, b), bucketMeans(grain, c)
	keys := make([]int64, 0, len(ma))
	for k := range ma {
		_, ok1 := mb[k]
		_, ok2 := mc[k]
		if ok1 && ok2 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	x, y, z := make([]float64, len(keys)), make([]float64, len(keys)), make([]float64, len(keys))
	for i, k := range keys {
		x[i], y[i], z[i] = ma[k], mb[k], mc[k]
	}
	return x, y, z
}

// PairAgreement estimates the error of target from its overlap with one
// other independent source. targetPrior and otherPrior are the two prior
// sigmas. Against an anchor the anchor's prior variance is taken out and
// the mean difference is the bias; otherwise the variance of the difference
// is split by the priors and the bias stays the prior's.
func PairAgreement(grain time.Duration, target, other []Point, targetPrior, otherPrior float64, otherIsAnchor bool) (Agreement, error) {
	x, y := Align(grain, target, other)
	ba, err := BlandAltman(x, y)
	if err != nil {
		return Agreement{}, err
	}
	ag := Agreement{Method: MethodBlandAltman, NEff: NEffAR1(ba.Differences)}
	v := ba.SD * ba.SD
	if otherIsAnchor {
		rest := v - otherPrior*otherPrior
		if rest <= 0 {
			rest, ag.Flags = 0, append(ag.Flags, FlagNegativeVariance)
		}
		ag.Sigma = math.Sqrt(rest)
		ag.Anchored = true
		ag.Bias = ba.Mean
		ag.BiasSD = ba.SD / math.Sqrt(math.Max(ag.NEff, 1))
		return ag, nil
	}
	v1, _ := SplitPairVariance(v, targetPrior, otherPrior)
	ag.Sigma = math.Sqrt(v1)
	return ag, nil
}

// TripleAgreement estimates the error of the first of three independent
// sources by triple collocation on their aligned overlap.
func TripleAgreement(grain time.Duration, target, b, c []Point) (Agreement, error) {
	x, y, z := Align3(grain, target, b, c)
	tc, err := TripleCollocation(x, y, z)
	if err != nil {
		return Agreement{}, err
	}
	ag := Agreement{Method: MethodTripleCollocation, Sigma: math.Sqrt(tc.Var[0])}
	d := make([]float64, len(x))
	for i := range x {
		d[i] = x[i] - (y[i]+z[i])/2
	}
	ag.NEff = NEffAR1(d)
	if tc.Clipped[0] {
		ag.Flags = append(ag.Flags, FlagNegativeVariance)
		ag.NEff = 0 // a clipped estimate carries no information
	}
	return ag, nil
}
