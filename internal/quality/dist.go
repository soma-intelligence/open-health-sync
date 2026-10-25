// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"sync"
)

// The score's predictive error is a Student t: wearable errors have heavier
// tails than a normal (motion artifacts, cadence lock), and a t with a few
// degrees of freedom is the standard way to say so. The functions below are
// written out here rather than taken from a library: the repository adds no
// dependency for them, and each one is checked against closed forms and
// printed tables in dist_test.go.

// RegIncBeta returns the regularized incomplete beta function I_x(a, b), for
// a, b > 0 and x in [0, 1]. It evaluates the continued fraction of Numerical
// Recipes (modified Lentz), on whichever side of the mean converges fast.
func RegIncBeta(x, a, b float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	front := math.Exp(lab - la - lb + a*math.Log(x) + b*math.Log1p(-x))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(x, a, b) / a
	}
	return 1 - front*betaCF(1-x, b, a)/b
}

// betaCF is the continued fraction of the incomplete beta function.
func betaCF(x, a, b float64) float64 {
	const (
		maxIter = 500
		eps     = 1e-16
		tiny    = 1e-300
	)
	qab, qap, qam := a+b, a+1, a-1
	c, d := 1.0, 1-qab*x/qap
	if math.Abs(d) < tiny {
		d = tiny
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIter; m++ {
		fm := float64(m)
		m2 := 2 * fm
		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		h *= d * c
		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = 1 + aa/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}

// NormalCDF is the standard normal distribution function.
func NormalCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// NormalQuantile is the inverse of NormalCDF.
func NormalQuantile(p float64) float64 { return -math.Sqrt2 * math.Erfcinv(2*p) }

// tTail returns P(T > x) for x >= 0, computed directly so the upper tail
// keeps its relative precision.
func tTail(x, nu float64) float64 {
	if math.IsInf(nu, 1) {
		return 0.5 * math.Erfc(x/math.Sqrt2)
	}
	return 0.5 * RegIncBeta(nu/(nu+x*x), nu/2, 0.5)
}

// StudentTCDF is the distribution function of a standard Student t with nu
// degrees of freedom. nu = +Inf is the standard normal.
func StudentTCDF(x, nu float64) float64 {
	if x >= 0 {
		return 1 - tTail(x, nu)
	}
	return tTail(-x, nu)
}

// StudentTPDF is the density of a standard Student t.
func StudentTPDF(x, nu float64) float64 {
	if math.IsInf(nu, 1) {
		return math.Exp(-x*x/2) / math.Sqrt(2*math.Pi)
	}
	l1, _ := math.Lgamma((nu + 1) / 2)
	l2, _ := math.Lgamma(nu / 2)
	return math.Exp(l1 - l2 - 0.5*math.Log(nu*math.Pi) - (nu+1)/2*math.Log1p(x*x/nu))
}

type quantileKey struct{ p, nu float64 }

// quantiles caches StudentTQuantile: the score asks for the same few levels
// and degrees of freedom millions of times in a backtest.
var quantiles sync.Map

// StudentTQuantile is the inverse of StudentTCDF, for p in (0, 1).
func StudentTQuantile(p, nu float64) float64 {
	switch {
	case p <= 0:
		return math.Inf(-1)
	case p >= 1:
		return math.Inf(1)
	case p == 0.5:
		return 0
	}
	k := quantileKey{p, nu}
	if v, ok := quantiles.Load(k); ok {
		return v.(float64)
	}
	var x float64
	if math.IsInf(nu, 1) {
		x = NormalQuantile(p)
	} else {
		// Solve tTail(x) = tail for x >= 0, then restore the sign.
		tail := math.Min(p, 1-p)
		x = tInvTail(tail, nu)
		if p < 0.5 {
			x = -x
		}
	}
	quantiles.Store(k, x)
	return x
}

// tInvTail solves P(T > x) = tail for x >= 0, tail in (0, 0.5), by a
// safeguarded Newton iteration on a bracket.
func tInvTail(tail, nu float64) float64 {
	lo, hi := 0.0, 1.0
	for tTail(hi, nu) > tail {
		lo, hi = hi, hi*2
		if hi > 1e300 {
			return math.Inf(1)
		}
	}
	x := (lo + hi) / 2
	for range 200 {
		f := tTail(x, nu) - tail
		if math.Abs(f) <= 1e-15*tail {
			return x
		}
		if f > 0 {
			lo = x
		} else {
			hi = x
		}
		// tTail decreases with slope -pdf.
		next := x + f/StudentTPDF(x, nu)
		if next <= lo || next >= hi || math.IsNaN(next) {
			next = (lo + hi) / 2
		}
		if math.Abs(next-x) <= 1e-14*math.Max(1, x) {
			return next
		}
		x = next
	}
	return x
}

// Digamma is the logarithmic derivative of the gamma function, for x > 0:
// the recurrence psi(x) = psi(x+1) - 1/x up to x >= 6, then the asymptotic
// series.
func Digamma(x float64) float64 {
	var r float64
	for x < 6 {
		r -= 1 / x
		x++
	}
	f := 1 / (x * x)
	return r + math.Log(x) - 0.5/x - f*(1.0/12-f*(1.0/120-f*(1.0/252-f*(1.0/240-f/132))))
}

// eulerGamma is the Euler-Mascheroni constant.
const eulerGamma = 0.57721566490153286

// MeanLogAbsT returns E[log |T|] for a standard Student t with nu degrees of
// freedom. T = Z / sqrt(V/nu) with V chi-squared, so it is E log|Z| minus
// half of E log(V/nu). The residual model predicts the mean log absolute
// error; this turns that prediction into a scale.
func MeanLogAbsT(nu float64) float64 {
	z := -(eulerGamma + math.Ln2) / 2
	if math.IsInf(nu, 1) {
		return z
	}
	return z - 0.5*(Digamma(nu/2)+math.Ln2-math.Log(nu))
}
