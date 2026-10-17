// SPDX-License-Identifier: AGPL-3.0-only

// Package demo generates synthetic health data for one invented person, so a
// developer can try an instance (the REST API, FHIR, Open mHealth, exports,
// MCP) without a wearable account: `ohs seed --demo`. The data is plausible
// but made up, deterministic for a seed, and spread over six providers that
// overlap the way real ones do (two straps both report heart rate, two
// sources report sleep, steps and weight). Generate has no I/O: it returns
// canonical records per provider. Seed writes them for one user of a tenant
// through the ETL writer (etl.WriteSynthetic), for `ohs seed --demo` and for
// the admin API's demo user alike. Every record that has metadata carries
// {"synthetic": true}.
package demo

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"time"
	_ "time/tzdata" // the demo zone must resolve on a host without a zone database

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Provider slugs the demo writes, in write order. Each is a connector of the
// repository; none needs to be configured for its data to be stored or read.
const (
	Oura        = "oura"
	Whoop       = "whoop"
	Withings    = "withings"
	Fitbit      = "fitbit"
	Polar       = "polar"
	AppleHealth = "apple_health"
)

// Providers lists the providers Generate fills, in write order.
var Providers = []string{Oura, Whoop, Withings, Fitbit, Polar, AppleHealth}

// Defaults and limits of Options.
const (
	DefaultDays = 90
	MaxDays     = 365
	// DefaultZone is the zone the demo person lives in.
	DefaultZone = "Europe/Paris"
)

// MetadataSynthetic is the metadata key every generated record carries, set
// to true.
const MetadataSynthetic = "synthetic"

// Options selects what Generate produces.
type Options struct {
	// Days is the number of complete local days generated, ending the day
	// before Today. 0 means DefaultDays.
	Days int
	// Today is the day after the last generated day (its date in Location
	// counts). The zero value means the current day. The records of a given
	// date do not depend on Today, so a run on a later day adds the new days
	// and dedups the others.
	Today time.Time
	// Seed selects the person and their data. The same seed gives the same
	// records.
	Seed int64
	// Location is the person's zone. nil means DefaultZone.
	Location *time.Location
}

// Batch is the records of one provider.
type Batch struct {
	Provider string
	Records  *model.MapResult
}

// Generate returns the records of every provider in Providers, in that order.
func Generate(o Options) ([]Batch, error) {
	if o.Days == 0 {
		o.Days = DefaultDays
	}
	if o.Days < 1 || o.Days > MaxDays {
		return nil, fmt.Errorf("demo: days must be between 1 and %d", MaxDays)
	}
	if o.Location == nil {
		loc, err := time.LoadLocation(DefaultZone)
		if err != nil {
			return nil, fmt.Errorf("demo: load zone: %w", err)
		}
		o.Location = loc
	}
	if o.Today.IsZero() {
		o.Today = time.Now()
	}
	g := newGen(o.Seed, o.Location)
	t := o.Today.In(o.Location)
	last := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, o.Location).AddDate(0, 0, -1)
	if last.Before(time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC)) {
		return nil, errors.New("demo: today is too early")
	}

	builders := map[string]func(*day, *model.MapResult){
		Oura: g.oura, Whoop: g.whoop, Withings: g.withings,
		Fitbit: g.fitbit, Polar: g.polar, AppleHealth: g.appleHealth,
	}
	out := make([]Batch, 0, len(Providers))
	for _, p := range Providers {
		mr := &model.MapResult{}
		for i := o.Days - 1; i >= 0; i-- {
			builders[p](g.day(last.AddDate(0, 0, -i)), mr)
		}
		out = append(out, Batch{Provider: p, Records: mr})
	}
	return out, nil
}

// profile is the invented person: the baselines every day varies around.
type profile struct {
	restingHR float64 // bpm
	hrv       float64 // RMSSD, ms
	weight    float64 // kg
	bodyFat   float64 // percent
	height    float64 // m
	stepGoal  float64 // steps on an ordinary weekday
	vo2max    float64 // mL/kg/min
	phase     float64 // phase of the slow fitness cycle, radians
	home      [2]float64
}

// gen holds the person and the per-date truth, computed once per date.
type gen struct {
	seed int64
	loc  *time.Location
	p    profile
	days map[string]*day
}

func newGen(seed int64, loc *time.Location) *gen {
	r := rng(seed, "profile")
	return &gen{
		seed: seed,
		loc:  loc,
		days: map[string]*day{},
		p: profile{
			restingHR: 52 + 8*r.Float64(),
			hrv:       42 + 25*r.Float64(),
			weight:    68 + 12*r.Float64(),
			bodyFat:   15 + 7*r.Float64(),
			height:    1.70 + 0.15*r.Float64(),
			stepGoal:  7500 + 3000*r.Float64(),
			vo2max:    44 + 8*r.Float64(),
			phase:     2 * math.Pi * r.Float64(),
			// A park in Paris, where the demo zone is.
			home: [2]float64{48.8462 + 0.002*r.Float64(), 2.3371 + 0.002*r.Float64()},
		},
	}
}

// rng returns a generator for one (seed, stream): the same stream always
// draws the same numbers, whatever was drawn elsewhere.
func rng(seed int64, stream string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(stream))
	return rand.New(rand.NewPCG(uint64(seed), h.Sum64()))
}

// dayRNG is the generator of one stream of one date.
func (g *gen) dayRNG(stream string, date time.Time) *rand.Rand {
	return rng(g.seed, stream+"|"+date.Format(time.DateOnly))
}

// norm draws from a normal distribution, clamped to mean +- 3 sd.
func norm(r *rand.Rand, mean, sd float64) float64 {
	v := r.NormFloat64()
	return mean + sd*math.Max(-3, math.Min(3, v))
}

// round keeps d decimals.
func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

// metadata is the metadata every generated record starts from.
func (g *gen) metadata() map[string]any {
	return map[string]any{MetadataSynthetic: true, "timezone": g.loc.String()}
}

// dateOnlyUTC is the calendar date of t as midnight UTC (a summary date).
func dateOnlyUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// sample builds one quantity sample. Its hash names the provider, type,
// window and value: the same sample generated again dedups.
func (g *gen) sample(provider, qt string, v float64, unit string, start, end time.Time, extra map[string]any) model.QuantitySample {
	md := g.metadata()
	for k, val := range extra {
		md[k] = val
	}
	s := model.QuantitySample{
		Provider:        provider,
		SourceSlug:      provider,
		SourceName:      sourceNames[provider],
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start.UTC(),
		EndDate:         end.UTC(),
		Metadata:        md,
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"demo":          "1",
		"provider":      provider,
		"quantity_type": qt,
		"start_date":    start.UTC().Format(time.RFC3339),
		"end_date":      end.UTC().Format(time.RFC3339),
		"value":         model.CanonicalFloat(v),
	})
	return s
}

// sourceNames are the device names shown as source_name.
var sourceNames = map[string]string{
	Oura:        "Demo Ring",
	Whoop:       "Demo Strap",
	Withings:    "Demo Scale",
	Fitbit:      "Demo Band",
	Polar:       "Demo Sports Watch",
	AppleHealth: "Demo Phone",
}

// externalID is the provider record id of a generated record.
func externalID(provider, kind string, date time.Time) string {
	return "demo-" + provider + "-" + kind + "-" + date.Format(time.DateOnly)
}
