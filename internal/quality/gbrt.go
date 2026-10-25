// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Gradient boosted regression trees, the residual model of stage 4. They
// are fitted offline on reference datasets (the bench) to the log absolute
// error of a figure, and at run time scale its sigma. Squared loss, depth
// limited trees grown on quantile bins, no row or column sampling: the same
// data give the same trees on every machine, so the exported JSON is
// reproducible.

// GBRTParams are the fitting parameters.
type GBRTParams struct {
	Trees        int
	LearningRate float64
	MaxDepth     int
	// MinLeaf is the fewest training rows a leaf may hold.
	MinLeaf int
	// Lambda is the L2 penalty on leaf values.
	Lambda float64
	// Bins is the number of quantile bins per feature, 64 when zero.
	Bins int
}

// TreeNode is one node of a tree. A split sends a row left when its feature
// is at most Threshold, and a missing (NaN) feature the way MissingLeft says.
type TreeNode struct {
	Leaf        bool    `json:"leaf,omitempty"`
	Value       float64 `json:"v,omitempty"`
	Feature     int     `json:"f,omitempty"`
	Threshold   float64 `json:"t,omitempty"`
	MissingLeft bool    `json:"m,omitempty"`
	Left        int     `json:"l,omitempty"`
	Right       int     `json:"r,omitempty"`
}

// Tree is a regression tree; node 0 is the root.
type Tree struct {
	Nodes []TreeNode `json:"nodes"`
}

// Ensemble is a fitted model: Base plus LearningRate times the sum of the
// trees.
type Ensemble struct {
	Features     []string `json:"features"`
	Base         float64  `json:"base"`
	LearningRate float64  `json:"learning_rate"`
	Trees        []Tree   `json:"trees"`
}

// Predict returns the model's value for a feature vector in the order of
// Features.
func (e *Ensemble) Predict(x []float64) float64 {
	s := 0.0
	for _, t := range e.Trees {
		s += t.predict(x)
	}
	return e.Base + e.LearningRate*s
}

func (t Tree) predict(x []float64) float64 {
	i := 0
	for {
		n := t.Nodes[i]
		if n.Leaf {
			return n.Value
		}
		v := x[n.Feature]
		switch {
		case math.IsNaN(v):
			if n.MissingLeft {
				i = n.Left
			} else {
				i = n.Right
			}
		case v <= n.Threshold:
			i = n.Left
		default:
			i = n.Right
		}
	}
}

// ParseEnsemble reads and checks an exported model.
func ParseEnsemble(b []byte) (*Ensemble, error) {
	var e Ensemble
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	for ti, t := range e.Trees {
		if len(t.Nodes) == 0 {
			return nil, fmt.Errorf("gbrt: tree %d is empty", ti)
		}
		for ni, n := range t.Nodes {
			if n.Leaf {
				continue
			}
			if n.Feature < 0 || n.Feature >= len(e.Features) {
				return nil, fmt.Errorf("gbrt: tree %d node %d: feature %d out of range", ti, ni, n.Feature)
			}
			// Children come after their parent, which rules out cycles.
			for _, c := range []int{n.Left, n.Right} {
				if c <= ni || c >= len(t.Nodes) {
					return nil, fmt.Errorf("gbrt: tree %d node %d: child %d out of order", ti, ni, c)
				}
			}
		}
	}
	return &e, nil
}

// missingBin marks a NaN feature in the binned matrix.
const missingBin = math.MaxUint16

// binner holds the thresholds of every feature.
type binner struct {
	edges [][]float64 // per feature, ascending; bin k is (edges[k-1], edges[k]]
}

func newBinner(X [][]float64, nf, bins int) binner {
	b := binner{edges: make([][]float64, nf)}
	vals := make([]float64, 0, len(X))
	for f := range nf {
		vals = vals[:0]
		for _, row := range X {
			if !math.IsNaN(row[f]) {
				vals = append(vals, row[f])
			}
		}
		sort.Float64s(vals)
		distinct := vals[:0:0]
		for i, v := range vals {
			if i == 0 || v != vals[i-1] {
				distinct = append(distinct, v)
			}
		}
		var edges []float64
		if len(distinct) <= bins {
			// Midpoints between distinct values.
			for i := 0; i+1 < len(distinct); i++ {
				edges = append(edges, (distinct[i]+distinct[i+1])/2)
			}
		} else {
			for k := 1; k < bins; k++ {
				v := vals[k*len(vals)/bins]
				if len(edges) == 0 || v > edges[len(edges)-1] {
					edges = append(edges, v)
				}
			}
		}
		b.edges[f] = edges
	}
	return b
}

// code returns the bin of v: the number of edges below v.
func (b binner) code(f int, v float64) uint16 {
	if math.IsNaN(v) {
		return missingBin
	}
	return uint16(sort.SearchFloat64s(b.edges[f], v))
}

// FitGBRT fits an ensemble to y on the rows of X, whose columns are named
// by names.
func FitGBRT(X [][]float64, y []float64, names []string, p GBRTParams) (*Ensemble, error) {
	n := len(X)
	if n == 0 || len(y) != n {
		return nil, errors.New("gbrt: need as many targets as rows, and at least one")
	}
	nf := len(names)
	for i, row := range X {
		if len(row) != nf {
			return nil, fmt.Errorf("gbrt: row %d has %d features, want %d", i, len(row), nf)
		}
	}
	if p.Trees <= 0 || p.LearningRate <= 0 {
		return nil, errors.New("gbrt: trees and learning rate must be positive")
	}
	if p.MaxDepth <= 0 {
		p.MaxDepth = 3
	}
	if p.MinLeaf <= 0 {
		p.MinLeaf = 1
	}
	if p.Bins <= 1 {
		p.Bins = 64
	}
	if p.Bins > 4096 {
		p.Bins = 4096
	}
	bn := newBinner(X, nf, p.Bins)
	codes := make([][]uint16, nf)
	for f := range nf {
		codes[f] = make([]uint16, n)
		for i, row := range X {
			codes[f][i] = bn.code(f, row[f])
		}
	}
	var base float64
	for _, v := range y {
		base += v
	}
	base /= float64(n)
	pred := make([]float64, n)
	for i := range pred {
		pred[i] = base
	}
	g := &grower{p: p, bn: bn, codes: codes, grad: make([]float64, n)}
	e := &Ensemble{Features: append([]string(nil), names...), Base: base, LearningRate: p.LearningRate}
	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	for range p.Trees {
		for i := range n {
			g.grad[i] = y[i] - pred[i]
		}
		g.nodes = nil
		g.grow(all, 0)
		t := Tree{Nodes: g.nodes}
		for i := range n {
			pred[i] += p.LearningRate * t.predict(X[i])
		}
		e.Trees = append(e.Trees, t)
	}
	return e, nil
}

type grower struct {
	p     GBRTParams
	bn    binner
	codes [][]uint16
	grad  []float64
	nodes []TreeNode
}

type split struct {
	gain        float64
	feature     int
	bin         int
	missingLeft bool
}

// grow adds the subtree of rows to the node list and returns its index.
func (g *grower) grow(rows []int, depth int) int {
	idx := len(g.nodes)
	var sum float64
	for _, i := range rows {
		sum += g.grad[i]
	}
	lam := g.p.Lambda
	leaf := TreeNode{Leaf: true, Value: sum / (float64(len(rows)) + lam)}
	g.nodes = append(g.nodes, leaf)
	if depth >= g.p.MaxDepth || len(rows) < 2*g.p.MinLeaf {
		return idx
	}
	best := split{gain: 1e-12, feature: -1}
	parentScore := sum * sum / (float64(len(rows)) + lam)
	for f := range g.codes {
		nb := len(g.bn.edges[f]) + 1
		if nb < 2 {
			continue
		}
		hs := make([]float64, nb)
		hc := make([]int, nb)
		var ms float64
		var mc int
		col := g.codes[f]
		for _, i := range rows {
			c := col[i]
			if c == missingBin {
				ms += g.grad[i]
				mc++
				continue
			}
			hs[c] += g.grad[i]
			hc[c]++
		}
		var ls float64
		var lc int
		for k := 0; k < nb-1; k++ {
			ls += hs[k]
			lc += hc[k]
			for _, ml := range []bool{true, false} {
				L, LC := ls, lc
				if ml {
					L, LC = ls+ms, lc+mc
				}
				R, RC := sum-L, len(rows)-LC
				if LC < g.p.MinLeaf || RC < g.p.MinLeaf {
					continue
				}
				gain := L*L/(float64(LC)+lam) + R*R/(float64(RC)+lam) - parentScore
				if gain > best.gain {
					best = split{gain: gain, feature: f, bin: k, missingLeft: ml}
				}
				if mc == 0 {
					break // without missing rows the two directions are one split
				}
			}
		}
	}
	if best.feature < 0 {
		return idx
	}
	var left, right []int
	col := g.codes[best.feature]
	for _, i := range rows {
		c := col[i]
		if (c == missingBin && best.missingLeft) || (c != missingBin && int(c) <= best.bin) {
			left = append(left, i)
		} else {
			right = append(right, i)
		}
	}
	l := g.grow(left, depth+1)
	r := g.grow(right, depth+1)
	g.nodes[idx] = TreeNode{Feature: best.feature, Threshold: g.bn.edges[best.feature][best.bin],
		MissingLeft: best.missingLeft, Left: l, Right: r}
	return idx
}
