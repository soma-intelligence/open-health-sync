// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Stage 5 of the spec: split conformal calibration, Mondrian per
// calibration group (metric x device family x context). On held out
// subjects, each figure gives a normalized residual r = (e - bias) / s. A
// group's shift is the median r: the part of the bias the prior missed in
// that group, learned against the references. The conformity score is
// |r - shift|, and the group's factor gamma is the score quantile that makes
// the 90% interval cover 90% of them, divided by the t quantile. The
// degrees of freedom are chosen per group too, so the other levels (50, 68,
// 80, 95%) follow the tails the data actually have; a group with fewer than
// MinNuPoints points or MinNuSubjects subjects takes its parent's. A group
// with fewer than MinPoints points or MinSubjects subjects falls back to its
// parent: figures of one person are autocorrelated, so a few people with
// many figures are still a small group. Factors never cross metrics: a metric
// with no group of its own is not calibrated.

// CalPoint is one held out figure.
type CalPoint struct {
	// Path is the group from the coarsest to the finest:
	// metric, device family, context.
	Path []string
	// Residual is (error - bias) / s, with bias and s the uncalibrated
	// score's location and scale.
	Residual float64
	// Tol and Bias are the figure's tolerance and bias divided by s, for
	// the recalibration of q; a zero Tol leaves the point out of it.
	Tol, Bias float64
	// Subject is the person the figure belongs to. Figures of one person
	// are autocorrelated, so group sizes are counted in subjects as well
	// as in points. Left empty everywhere, only points count.
	Subject string
}

// QMap is a monotone map of q: knots of (raw q, calibrated q) joined by
// straight lines, and straight on to (0, 0) and (1, 1) outside them.
type QMap [][2]float64

// Apply maps a raw q. A nil map is the identity.
func (m QMap) Apply(q float64) float64 {
	if len(m) == 0 {
		return q
	}
	q = math.Min(1, math.Max(0, q))
	pts := make([][2]float64, 0, len(m)+2)
	if m[0][0] > 0 {
		pts = append(pts, [2]float64{0, 0})
	}
	pts = append(pts, m...)
	if m[len(m)-1][0] < 1 {
		pts = append(pts, [2]float64{1, 1})
	}
	for i := 1; i < len(pts); i++ {
		if q <= pts[i][0] {
			a, b := pts[i-1], pts[i]
			if b[0] == a[0] {
				return b[1]
			}
			return a[1] + (b[1]-a[1])*(q-a[0])/(b[0]-a[0])
		}
	}
	return pts[len(pts)-1][1]
}

// isotonic is the weighted least squares non-decreasing fit of y on x (x
// sorted ascending), by pool adjacent violators. Nil weights are ones.
func isotonic(x, y, w []float64) []float64 {
	type block struct {
		sum, wt float64
		n       int
	}
	var bs []block
	for i := range x {
		wi := 1.0
		if w != nil {
			wi = w[i]
		}
		bs = append(bs, block{y[i] * wi, wi, 1})
		for len(bs) > 1 && bs[len(bs)-2].sum/bs[len(bs)-2].wt > bs[len(bs)-1].sum/bs[len(bs)-1].wt {
			a, b := bs[len(bs)-2], bs[len(bs)-1]
			bs = append(bs[:len(bs)-2], block{a.sum + b.sum, a.wt + b.wt, a.n + b.n})
		}
	}
	out := make([]float64, 0, len(x))
	for _, b := range bs {
		for range b.n {
			out = append(out, b.sum/b.wt)
		}
	}
	return out
}

// MinQPoints and MinQSubjects are the fewest points and subjects a group
// fits a q map on.
const (
	MinQPoints   = 300
	MinQSubjects = 10
)

// maxKnots bounds the size of an exported q map.
const maxKnots = 25

// fitQMap fits the q map of a group from raw q's and outcomes. Raw q's
// equal to six decimals (the precision of the exported knots) are pooled
// first, so their order does not matter.
func fitQMap(q []float64, hit []bool) QMap {
	rq := make([]float64, len(q))
	idx := make([]int, len(q))
	for i := range idx {
		idx[i], rq[i] = i, round6(q[i])
	}
	sort.Slice(idx, func(a, b int) bool { return rq[idx[a]] < rq[idx[b]] })
	var x, y, w []float64
	for _, i := range idx {
		h := 0.0
		if hit[i] {
			h = 1
		}
		if n := len(x); n > 0 && rq[i] == x[n-1] {
			y[n-1] += h
			w[n-1]++
			continue
		}
		x, y, w = append(x, rq[i]), append(y, h), append(w, 1)
	}
	for i := range y {
		y[i] /= w[i]
	}
	fit := isotonic(x, y, w)
	var m QMap
	for k := 0; k < maxKnots; k++ {
		i := k * (len(x) - 1) / (maxKnots - 1)
		knot := [2]float64{x[i], round6(fit[i])}
		if len(m) > 0 && knot[0] <= m[len(m)-1][0] {
			continue
		}
		m = append(m, knot)
	}
	return m
}

// QMapFor returns the q map of the most specific group of path that has
// one, nil when none.
func (c *Calibration) QMapFor(path []string) QMap {
	if c == nil {
		return nil
	}
	keys := groupKeys(path)
	for i := len(keys) - 1; i >= 0; i-- {
		if m, ok := c.QMaps[keys[i]]; ok {
			return m
		}
	}
	return nil
}

// GroupCal is the calibration of one group.
type GroupCal struct {
	N        int     `json:"n"`
	Subjects int     `json:"subjects,omitempty"`
	Shift    float64 `json:"shift"`
	Gamma    float64 `json:"gamma"`
	Nu       float64 `json:"nu"`
}

// Calibration holds the factors of every group with enough points.
type Calibration struct {
	Version string
	// Level is the coverage gamma is fitted to.
	Level       float64
	MinPoints   int
	MinSubjects int
	Groups      map[string]GroupCal
	// QMaps recalibrate q per group: the t gives the right intervals once
	// gamma and nu are fitted, but its central mass, which q reads, can
	// still be off when the errors are not a t.
	QMaps map[string]QMap
}

// CalOptions are the fitting options.
type CalOptions struct {
	Version   string
	Level     float64 // 0.9 when zero
	MinPoints int     // 50 when zero
	// MinSubjects is DefaultMinSubjects when zero; it applies when the
	// points name their subjects.
	MinSubjects int
	NuGrid      []float64 // DefaultNuGrid when empty
}

// DefaultNuGrid lists the degrees of freedom a group may get; +Inf is the
// normal.
var DefaultNuGrid = []float64{2.5, 3, 4, 5, 7, 10, 15, 30, math.Inf(1)}

// checkLevels are the levels the degrees of freedom are chosen on.
var checkLevels = []float64{0.5, 0.68, 0.8, 0.9, 0.95}

// MinNuPoints and MinNuSubjects are the fewest points and subjects a
// group chooses its own nu on.
const (
	MinNuPoints   = 200
	MinNuSubjects = 10
)

// DefaultMinSubjects is the fewest subjects a group needs for its own
// shift and gamma.
const DefaultMinSubjects = 8

// groupKeys lists the groups of a path from the coarsest (the metric) to
// the finest.
func groupKeys(path []string) []string {
	var keys []string
	for i := range path {
		keys = append(keys, strings.Join(path[:i+1], "|"))
	}
	return keys
}

// FitCalibration fits gamma and nu for the global group, every prefix of
// every path, and keeps those with at least MinPoints points.
func FitCalibration(points []CalPoint, opt CalOptions) *Calibration {
	if opt.Level <= 0 || opt.Level >= 1 {
		opt.Level = 0.9
	}
	if opt.MinPoints <= 0 {
		opt.MinPoints = 50
	}
	if opt.MinSubjects <= 0 {
		opt.MinSubjects = DefaultMinSubjects
	}
	if len(opt.NuGrid) == 0 {
		opt.NuGrid = DefaultNuGrid
	}
	resid := map[string][]float64{}
	subjects := map[string]map[string]bool{}
	named := false
	var kept []CalPoint
	for _, p := range points {
		if math.IsNaN(p.Residual) || math.IsInf(p.Residual, 0) {
			continue
		}
		kept = append(kept, p)
		named = named || p.Subject != ""
		for _, k := range groupKeys(p.Path) {
			resid[k] = append(resid[k], p.Residual)
			if subjects[k] == nil {
				subjects[k] = map[string]bool{}
			}
			subjects[k][p.Subject] = true
		}
	}
	// enough says a group of n points from its subjects reaches both sizes.
	enough := func(k string, n, minPoints, minSubjects int) bool {
		return n >= minPoints && (!named || len(subjects[k]) >= minSubjects)
	}
	c := &Calibration{Version: opt.Version, Level: opt.Level, MinPoints: opt.MinPoints, MinSubjects: opt.MinSubjects, Groups: map[string]GroupCal{}, QMaps: map[string]QMap{}}
	// Parents first, so a small group can take its parent's nu.
	keys := make([]string, 0, len(resid))
	for k := range resid {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		di, dj := strings.Count(keys[i], "|"), strings.Count(keys[j], "|")
		if di != dj {
			return di < dj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		r := resid[k]
		if !enough(k, len(r), opt.MinPoints, opt.MinSubjects) {
			continue
		}
		sort.Float64s(r)
		shift := round6(r[len(r)/2])
		if len(r)%2 == 0 {
			shift = round6((r[len(r)/2-1] + r[len(r)/2]) / 2)
		}
		s := make([]float64, len(r))
		for i, v := range r {
			s[i] = math.Abs(v - shift)
		}
		sort.Float64s(s)
		grid := opt.NuGrid
		if !enough(k, len(s), MinNuPoints, MinNuSubjects) {
			grid = []float64{4}
			if i := strings.LastIndex(k, "|"); i >= 0 {
				if p, ok := c.Groups[k[:i]]; ok {
					grid = []float64{p.Nu}
				}
			}
		}
		g := fitGroup(s, opt.Level, grid)
		g.Shift = shift
		if named {
			g.Subjects = len(subjects[k])
		}
		c.Groups[k] = g
	}
	// The q maps, on the raw q each point gets from its own group.
	qs, hits := map[string][]float64{}, map[string][]bool{}
	for _, p := range kept {
		if p.Tol <= 0 {
			continue
		}
		g, _, ok := c.Lookup(p.Path)
		if !ok {
			continue
		}
		q := ProbWithin(p.Tol, p.Bias+g.Shift, g.Gamma, g.Nu)
		hit := math.Abs(p.Residual+p.Bias) <= p.Tol
		for _, k := range groupKeys(p.Path) {
			qs[k], hits[k] = append(qs[k], q), append(hits[k], hit)
		}
	}
	for k, q := range qs {
		if enough(k, len(q), MinQPoints, MinQSubjects) {
			c.QMaps[k] = fitQMap(q, hits[k])
		}
	}
	return c
}

// conformalQuantile is the finite sample split conformal quantile of sorted
// scores: the ceil((n+1) level)-th smallest.
func conformalQuantile(sorted []float64, level float64) float64 {
	n := len(sorted)
	k := int(math.Ceil(float64(n+1)*level)) - 1
	if k >= n {
		k = n - 1
	}
	if k < 0 {
		k = 0
	}
	return sorted[k]
}

func fitGroup(sorted []float64, level float64, grid []float64) GroupCal {
	qhat := conformalQuantile(sorted, level)
	best := GroupCal{N: len(sorted)}
	bestLoss := math.Inf(1)
	for _, nu := range grid {
		gamma := qhat / StudentTQuantile((1+level)/2, nu)
		var loss float64
		for _, l := range checkLevels {
			r := gamma * StudentTQuantile((1+l)/2, nu)
			cov := float64(sort.SearchFloat64s(sorted, math.Nextafter(r, math.Inf(1)))) / float64(len(sorted))
			loss += math.Abs(cov - l)
		}
		// Ties go to the larger nu, the simpler tail.
		if loss <= bestLoss+1e-12 {
			best.Gamma, best.Nu, bestLoss = gamma, nu, loss
		}
	}
	if best.Gamma <= 0 || math.IsNaN(best.Gamma) {
		best.Gamma = 1
	}
	// Six decimals, the precision of the exported file.
	best.Gamma = round6(best.Gamma)
	return best
}

// Lookup returns the calibration of the most specific group of path that
// has enough points, and its key.
func (c *Calibration) Lookup(path []string) (GroupCal, string, bool) {
	if c == nil {
		return GroupCal{}, "", false
	}
	keys := groupKeys(path)
	for i := len(keys) - 1; i >= 0; i-- {
		if g, ok := c.Groups[keys[i]]; ok {
			return g, keys[i], true
		}
	}
	return GroupCal{}, "", false
}

type calibrationJSON struct {
	Version     string              `json:"version"`
	Level       float64             `json:"level"`
	MinPoints   int                 `json:"min_points"`
	MinSubjects int                 `json:"min_subjects,omitempty"`
	Groups      map[string]groupOut `json:"groups"`
	QMaps       map[string]QMap     `json:"q_maps,omitempty"`
}

// groupOut spells an infinite nu as 0, since JSON has no infinity.
type groupOut struct {
	N        int     `json:"n"`
	Subjects int     `json:"subjects,omitempty"`
	Shift    float64 `json:"shift"`
	Gamma    float64 `json:"gamma"`
	Nu       float64 `json:"nu"`
}

// MarshalJSON writes the calibration with sorted keys; a normal (nu = +Inf)
// is written as nu 0.
func (c *Calibration) MarshalJSON() ([]byte, error) {
	out := calibrationJSON{Version: c.Version, Level: c.Level, MinPoints: c.MinPoints, MinSubjects: c.MinSubjects, Groups: map[string]groupOut{}, QMaps: c.QMaps}
	for k, g := range c.Groups {
		nu := g.Nu
		if math.IsInf(nu, 1) {
			nu = 0
		}
		out.Groups[k] = groupOut{N: g.N, Subjects: g.Subjects, Shift: g.Shift, Gamma: g.Gamma, Nu: nu}
	}
	return json.MarshalIndent(out, "", " ")
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// ParseCalibration reads and checks an exported calibration.
func ParseCalibration(b []byte) (*Calibration, error) {
	var in calibrationJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	if in.Version == "" {
		return nil, errors.New("calibration: no version")
	}
	c := &Calibration{Version: in.Version, Level: in.Level, MinPoints: in.MinPoints, MinSubjects: in.MinSubjects, Groups: map[string]GroupCal{}, QMaps: in.QMaps}
	for k, m := range in.QMaps {
		for i := 1; i < len(m); i++ {
			if m[i][0] < m[i-1][0] || m[i][1] < m[i-1][1] {
				return nil, fmt.Errorf("calibration: q map %q is not monotone", k)
			}
		}
	}
	for k, g := range in.Groups {
		if g.Gamma <= 0 || g.Nu < 0 || (g.Nu > 0 && g.Nu < 2) || g.N <= 0 {
			return nil, fmt.Errorf("calibration: group %q: gamma must be positive, nu 0 or at least 2", k)
		}
		nu := g.Nu
		if nu == 0 {
			nu = math.Inf(1)
		}
		c.Groups[k] = GroupCal{N: g.N, Subjects: g.Subjects, Shift: g.Shift, Gamma: g.Gamma, Nu: nu}
	}
	return c, nil
}
