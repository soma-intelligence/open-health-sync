// SPDX-License-Identifier: AGPL-3.0-only

package quality

import "math"

// Scoring rules the backtest reports (research note, section 5.2). They are
// here rather than in the bench so they are unit tested with the rest.

// PICP is the fraction of references inside their interval.
func PICP(lo, hi, ref []float64) float64 {
	var n float64
	for i := range ref {
		if ref[i] >= lo[i] && ref[i] <= hi[i] {
			n++
		}
	}
	return n / float64(len(ref))
}

// MPIW is the mean interval width.
func MPIW(lo, hi []float64) float64 {
	var s float64
	for i := range lo {
		s += hi[i] - lo[i]
	}
	return s / float64(len(lo))
}

// IntervalScore is the Winkler score of one central (1 - alpha) interval:
// its width plus 2/alpha times the distance by which it misses the
// reference. Lower is better; it is a proper scoring rule.
func IntervalScore(lo, hi, ref, alpha float64) float64 {
	s := hi - lo
	if ref < lo {
		s += 2 / alpha * (lo - ref)
	}
	if ref > hi {
		s += 2 / alpha * (ref - hi)
	}
	return s
}

// CRPSStudentT is the continuous ranked probability score of a location
// scale Student t forecast (mu, s, nu > 1) for the outcome y, in closed form
// (Jordan, Kruger and Lerch, scoringRules). nu = +Inf is the normal.
func CRPSStudentT(y, mu, s, nu float64) float64 {
	z := (y - mu) / s
	if math.IsInf(nu, 1) {
		return s * (z*(2*NormalCDF(z)-1) + 2*StudentTPDF(z, nu) - 1/math.Sqrt(math.Pi))
	}
	lb1 := lbeta(0.5, nu-0.5)
	lb2 := lbeta(0.5, nu/2)
	c := 2 * math.Sqrt(nu) / (nu - 1) * math.Exp(lb1-2*lb2)
	return s * (z*(2*StudentTCDF(z, nu)-1) + 2*StudentTPDF(z, nu)*(nu+z*z)/(nu-1) - c)
}

func lbeta(a, b float64) float64 {
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	return la + lb - lab
}

// ECE is the expected calibration error of probabilities q against
// outcomes, over equal width bins: the bin-weighted mean of |mean q - hit
// rate|.
func ECE(q []float64, hit []bool, bins int) float64 {
	sq := make([]float64, bins)
	sh := make([]float64, bins)
	n := make([]float64, bins)
	for i, p := range q {
		b := int(p * float64(bins))
		if b >= bins {
			b = bins - 1
		}
		if b < 0 {
			b = 0
		}
		sq[b] += p
		if hit[i] {
			sh[b]++
		}
		n[b]++
	}
	var e float64
	for b := range bins {
		if n[b] > 0 {
			e += n[b] / float64(len(q)) * math.Abs(sq[b]/n[b]-sh[b]/n[b])
		}
	}
	return e
}

// Brier is the mean squared error of probabilities against outcomes.
func Brier(q []float64, hit []bool) float64 {
	var s float64
	for i, p := range q {
		o := 0.0
		if hit[i] {
			o = 1
		}
		s += (p - o) * (p - o)
	}
	return s / float64(len(q))
}
