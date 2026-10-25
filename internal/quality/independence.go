// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"sort"
	"time"
)

// Stage 0 of the spec: copies are one observation, not two. A Garmin heart
// rate relayed through Apple Health has the same error as the one read from
// Garmin; counting both shrinks intervals falsely and breaks triple
// collocation. Copies are found by provenance first (the producer and the
// original id), then, when provenance is missing, by an equal value at the
// same start.

// Obs is one stored figure with its provenance.
type Obs struct {
	ID string
	// Provider is the pipe the figure came through (apple_health, garmin).
	Provider string
	// Source is the upstream that produced it (the sample's source_slug),
	// empty when the pipe does not say.
	Source string
	// Bundle is the producing app's bundle id when the pipe gives one.
	Bundle string
	// OriginalID is the producer's own id of the figure, when known.
	OriginalID string
	Start      time.Time
	Value      float64
}

// producer is the best name of who produced the figure.
func (o Obs) producer() string {
	switch {
	case o.Source != "":
		return o.Source
	case o.Bundle != "":
		return o.Bundle
	}
	return ""
}

// direct says the figure was read from its producer, not through a relay.
func (o Obs) direct() bool { return o.Source != "" && o.Source == o.Provider }

// Reasons a group of copies was formed.
const (
	ReasonSingle     = "single"
	ReasonProvenance = "provenance"
	ReasonValueTime  = "value_time"
)

// CopyGroup is a set of figures that are one observation.
type CopyGroup struct {
	Members []int // indexes into the input, ascending
	// Representative is the member to keep: a direct read when there is one,
	// else the earliest.
	Representative int
	Reason         string
}

// GroupCopies groups obs into observations. Two figures through different
// pipes are copies when they share a producer and an original id, or share
// a producer and start within window; or, when either lacks provenance,
// when their values differ by at most valueTol and they start within
// window. Figures of one pipe are never copies of each other.
func GroupCopies(obs []Obs, window time.Duration, valueTol float64) []CopyGroup {
	n := len(obs)
	parent := make([]int, n)
	reason := make([]string, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(i, j int, why string) {
		ri, rj := find(i), find(j)
		if ri == rj {
			return
		}
		if rj < ri {
			ri, rj = rj, ri
		}
		parent[rj] = ri
		if reason[ri] != ReasonProvenance {
			reason[ri] = why
		}
		if reason[rj] == ReasonProvenance {
			reason[ri] = ReasonProvenance
		}
	}
	// Sort by start so only neighbours within the window are compared.
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return obs[idx[a]].Start.Before(obs[idx[b]].Start) })
	byOriginal := map[[2]string]int{}
	for _, i := range idx {
		o := obs[i]
		if o.OriginalID != "" && o.producer() != "" {
			k := [2]string{o.producer(), o.OriginalID}
			if j, ok := byOriginal[k]; ok && obs[j].Provider != o.Provider {
				union(j, i, ReasonProvenance)
			} else if !ok {
				byOriginal[k] = i
			}
		}
	}
	for a := range idx {
		i := idx[a]
		for b := a + 1; b < n; b++ {
			j := idx[b]
			if obs[j].Start.Sub(obs[i].Start) > window {
				break
			}
			oi, oj := obs[i], obs[j]
			if oi.Provider == oj.Provider {
				continue
			}
			pi, pj := oi.producer(), oj.producer()
			switch {
			case pi != "" && pi == pj:
				union(i, j, ReasonProvenance)
			case (pi == "" || pj == "") && math.Abs(oi.Value-oj.Value) <= valueTol:
				// A relay that does not say who produced the figure: only an
				// equal value at the same start marks a copy.
				union(i, j, ReasonValueTime)
			}
		}
	}
	groups := map[int]*CopyGroup{}
	var roots []int
	for i := range obs {
		r := find(i)
		g, ok := groups[r]
		if !ok {
			g = &CopyGroup{Representative: -1, Reason: ReasonSingle}
			groups[r] = g
			roots = append(roots, r)
		}
		g.Members = append(g.Members, i)
	}
	out := make([]CopyGroup, 0, len(roots))
	for _, r := range roots {
		g := groups[r]
		if len(g.Members) > 1 {
			g.Reason = reason[r]
		}
		g.Representative = g.Members[0]
		for _, m := range g.Members {
			if obs[m].direct() {
				g.Representative = m
				break
			}
		}
		if !obs[g.Representative].direct() {
			for _, m := range g.Members {
				if obs[m].Start.Before(obs[g.Representative].Start) {
					g.Representative = m
				}
			}
		}
		out = append(out, *g)
	}
	return out
}

// sharedFamilies lists device families whose metrics come from one
// algorithm or one sensor, so their errors are correlated: one error family
// for the agreement estimators.
var sharedFamilies = map[string]map[string]string{
	"apple_watch": {MetricHeartRate: "ppg", MetricHRVSDNN: "ppg", MetricRestingHeartRate: "ppg"},
	"garmin":      {MetricHeartRate: "hr", MetricActiveEnergy: "hr", MetricRestingHeartRate: "hr"},
	"fitbit":      {MetricHeartRate: "hr", MetricActiveEnergy: "hr"},
	"oura":        {MetricHeartRate: "ppg", MetricRestingHeartRate: "ppg", MetricHRVRMSSD: "ppg"},
	"whoop":       {MetricHeartRate: "ppg", MetricRestingHeartRate: "ppg", MetricHRVRMSSD: "ppg"},
}

// ErrorFamily returns the error family of a device family's metric: equal
// for metrics that share an algorithm, distinct otherwise.
func ErrorFamily(family, metric string) string {
	if m, ok := sharedFamilies[family]; ok {
		if f, ok := m[metric]; ok {
			return family + "/" + f
		}
	}
	return family + "/" + metric
}

// IndependentSources counts the distinct error families among the device
// families of a metric's sources.
func IndependentSources(families []string, metric string) int {
	seen := map[string]bool{}
	for _, f := range families {
		seen[ErrorFamily(f, metric)] = true
	}
	return len(seen)
}

// Definition is what a figure measures: the metric and, when the source
// says, the kind (asleep vs in_bed, active vs total).
type Definition struct {
	Metric string
	Kind   string
}

// Comparable reports whether two figures measure the same thing. SDNN and
// RMSSD are different metrics; asleep and in bed are different kinds; a
// kind left empty matches any.
func Comparable(a, b Definition) bool {
	if a.Metric != b.Metric {
		return false
	}
	return a.Kind == "" || b.Kind == "" || a.Kind == b.Kind
}
