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
// subjects, the normalized error |e - bias| / s of each figure is a
// conformity score; a group's factor gamma is the score quantile that makes
// the 90% interval cover 90% of them, divided by the t quantile. The
// degrees of freedom are chosen per group too, so the other levels (50, 68,
// 80, 95%) follow the tails the data actually have. A group with fewer than
// MinPoints points falls back to its parent.

// CalPoint is one held out figure.
type CalPoint struct {
	// Path is the group from the coarsest to the finest:
	// metric, device family, context.
	Path []string
	// Score is |error - bias| / s, with s the model's scale.
	Score float64
}

// GroupCal is the calibration of one group.
type GroupCal struct {
	N     int     `json:"n"`
	Gamma float64 `json:"gamma"`
	Nu    float64 `json:"nu"`
}

// Calibration holds the factors of every group with enough points.
type Calibration struct {
	Version string
	// Level is the coverage gamma is fitted to.
	Level     float64
	MinPoints int
	Groups    map[string]GroupCal
}

// CalOptions are the fitting options.
type CalOptions struct {
	Version   string
	Level     float64   // 0.9 when zero
	MinPoints int       // 50 when zero
	NuGrid    []float64 // DefaultNuGrid when empty
}

// DefaultNuGrid lists the degrees of freedom a group may get; +Inf is the
// normal.
var DefaultNuGrid = []float64{2.5, 3, 4, 5, 7, 10, 15, 30, math.Inf(1)}

// checkLevels are the levels the degrees of freedom are chosen on.
var checkLevels = []float64{0.5, 0.68, 0.8, 0.9, 0.95}

// GlobalGroup is the root of every path.
const GlobalGroup = "*"

func groupKeys(path []string) []string {
	keys := []string{GlobalGroup}
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
	if len(opt.NuGrid) == 0 {
		opt.NuGrid = DefaultNuGrid
	}
	scores := map[string][]float64{}
	for _, p := range points {
		if math.IsNaN(p.Score) || math.IsInf(p.Score, 0) {
			continue
		}
		for _, k := range groupKeys(p.Path) {
			scores[k] = append(scores[k], p.Score)
		}
	}
	c := &Calibration{Version: opt.Version, Level: opt.Level, MinPoints: opt.MinPoints, Groups: map[string]GroupCal{}}
	for k, s := range scores {
		if len(s) < opt.MinPoints {
			continue
		}
		sort.Float64s(s)
		c.Groups[k] = fitGroup(s, opt.Level, opt.NuGrid)
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
	Version   string              `json:"version"`
	Level     float64             `json:"level"`
	MinPoints int                 `json:"min_points"`
	Groups    map[string]groupOut `json:"groups"`
}

// groupOut spells an infinite nu as 0, since JSON has no infinity.
type groupOut struct {
	N     int     `json:"n"`
	Gamma float64 `json:"gamma"`
	Nu    float64 `json:"nu"`
}

// MarshalJSON writes the calibration with sorted keys; a normal (nu = +Inf)
// is written as nu 0.
func (c *Calibration) MarshalJSON() ([]byte, error) {
	out := calibrationJSON{Version: c.Version, Level: c.Level, MinPoints: c.MinPoints, Groups: map[string]groupOut{}}
	for k, g := range c.Groups {
		nu := g.Nu
		if math.IsInf(nu, 1) {
			nu = 0
		}
		out.Groups[k] = groupOut{N: g.N, Gamma: g.Gamma, Nu: nu}
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
	c := &Calibration{Version: in.Version, Level: in.Level, MinPoints: in.MinPoints, Groups: map[string]GroupCal{}}
	for k, g := range in.Groups {
		if g.Gamma <= 0 || g.Nu < 0 || (g.Nu > 0 && g.Nu < 2) || g.N <= 0 {
			return nil, fmt.Errorf("calibration: group %q: gamma must be positive, nu 0 or at least 2", k)
		}
		nu := g.Nu
		if nu == 0 {
			nu = math.Inf(1)
		}
		c.Groups[k] = GroupCal{N: g.N, Gamma: g.Gamma, Nu: nu}
	}
	return c, nil
}
