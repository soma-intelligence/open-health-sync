// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Evidence tags of a prior, as the research note uses them.
const (
	TagVerified    = "V" // checked against the paper, an open full text or an abstract
	TagSecondary   = "S" // seen only in secondary sources
	TagUnconfirmed = "U" // not rechecked: a default synthesized from the literature
)

// Levels of the fallback chain, most specific first.
const (
	LevelModel   = "model"
	LevelFamily  = "family"
	LevelSensor  = "sensor"
	LevelGeneric = "generic"
)

var levels = []string{LevelModel, LevelFamily, LevelSensor, LevelGeneric}

// Prior is one published error figure for a metric, a device and a
// context: the predictive error of a reading is bias plus a Student t of
// scale sigma. Bias is device minus reference.
type Prior struct {
	Metric string `json:"metric"`
	Level  string `json:"level"`
	// Key is the model, family or sensor the entry is for, empty at the
	// generic level.
	Key string `json:"key,omitempty"`
	// Context restricts the entry, empty for any context.
	Context Context `json:"context,omitempty"`

	// Bias and its uncertainty, absolute plus relative to the value.
	Bias      float64 `json:"bias"`
	BiasRel   float64 `json:"bias_rel,omitempty"`
	BiasSD    float64 `json:"bias_sd"`
	BiasSDRel float64 `json:"bias_sd_rel,omitempty"`
	// Sigma is the random error (1 SD), absolute plus relative to the value;
	// the two add in quadrature.
	Sigma    float64 `json:"sigma"`
	SigmaRel float64 `json:"sigma_rel,omitempty"`
	// Nu is the degrees of freedom of the t.
	Nu float64 `json:"nu"`

	Citation string `json:"citation"`
	Tag      string `json:"tag"`
	Note     string `json:"note,omitempty"`
}

// BiasAt returns the bias for a figure of value v (error space).
func (p Prior) BiasAt(v float64) float64 { return p.Bias + p.BiasRel*math.Abs(v) }

// BiasSDAt returns the bias uncertainty at v.
func (p Prior) BiasSDAt(v float64) float64 { return math.Hypot(p.BiasSD, p.BiasSDRel*math.Abs(v)) }

// SigmaAt returns the random error at v.
func (p Prior) SigmaAt(v float64) float64 { return math.Hypot(p.Sigma, p.SigmaRel*math.Abs(v)) }

// Priors is the versioned prior table.
type Priors struct {
	Version string  `json:"version"`
	Entries []Prior `json:"entries"`

	index map[priorKey]int
}

type priorKey struct {
	metric, level, key string
	ctx                Context
}

//go:embed priors.json
var priorsJSON []byte

var (
	defaultPriors     *Priors
	defaultPriorsOnce sync.Once
)

// DefaultPriors returns the embedded table. It panics if the embedded file
// is invalid, which the package tests rule out.
func DefaultPriors() *Priors {
	defaultPriorsOnce.Do(func() {
		p, err := ParsePriors(priorsJSON)
		if err != nil {
			panic("quality: embedded priors.json: " + err.Error())
		}
		defaultPriors = p
	})
	return defaultPriors
}

// ParsePriors reads and checks a prior table.
func ParsePriors(b []byte) (*Priors, error) {
	var p Priors
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.Version == "" {
		return nil, errors.New("priors: no version")
	}
	p.index = make(map[priorKey]int, len(p.Entries))
	for i, e := range p.Entries {
		where := fmt.Sprintf("priors: entry %d (%s %s %q %s)", i, e.Metric, e.Level, e.Key, e.Context)
		if _, ok := LookupMetric(e.Metric); !ok {
			return nil, errors.New(where + ": unknown metric")
		}
		switch e.Level {
		case LevelGeneric:
			if e.Key != "" {
				return nil, errors.New(where + ": a generic entry has no key")
			}
		case LevelModel, LevelFamily, LevelSensor:
			if e.Key == "" {
				return nil, errors.New(where + ": no key")
			}
		default:
			return nil, errors.New(where + ": unknown level")
		}
		switch e.Tag {
		case TagVerified, TagSecondary, TagUnconfirmed:
		default:
			return nil, errors.New(where + ": tag must be V, S or U")
		}
		if e.Citation == "" {
			return nil, errors.New(where + ": no citation")
		}
		if e.Sigma < 0 || e.SigmaRel < 0 || e.BiasSD < 0 || e.BiasSDRel < 0 || e.Sigma+e.SigmaRel == 0 {
			return nil, errors.New(where + ": sigma and bias_sd must be non-negative, and sigma set")
		}
		if e.Nu < 2 {
			return nil, errors.New(where + ": nu must be at least 2")
		}
		k := priorKey{e.Metric, e.Level, e.Key, e.Context}
		if _, dup := p.index[k]; dup {
			return nil, errors.New(where + ": duplicate")
		}
		p.index[k] = i
	}
	return &p, nil
}

// Lookup returns the most specific prior for a figure: the device model,
// then its family, then its sensor kind, then the generic entry. At each
// level an entry for the context wins over an entry for any context.
func (p *Priors) Lookup(metric string, d Device, ctx Context) (Prior, string, bool) {
	keys := map[string]string{LevelModel: d.Model, LevelFamily: d.Family, LevelSensor: d.Sensor, LevelGeneric: ""}
	for _, lvl := range levels {
		k := keys[lvl]
		if lvl != LevelGeneric && k == "" {
			continue
		}
		for _, c := range []Context{ctx, ""} {
			if i, ok := p.index[priorKey{metric, lvl, k, c}]; ok {
				return p.Entries[i], lvl, true
			}
		}
	}
	return Prior{}, "", false
}

// W0 returns the prior's strength in pseudo-observations for the shrinkage
// of agreement estimates, by evidence tag (spec, stage 3).
func W0(tag string) float64 {
	switch tag {
	case TagVerified:
		return 50
	case TagSecondary:
		return 20
	}
	return 10
}
