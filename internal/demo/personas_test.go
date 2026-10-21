// SPDX-License-Identifier: AGPL-3.0-only

package demo_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// mixSeed1 is the SHA-256 of the JSON of 30 days of the mix persona, seed 1,
// generated on today: the records of every version before the personas.
const mixSeed1 = "cb437ec617eeacb05bd05b0e1a6e5e10981a3b0414dd539bad2aa987d1b20929"

func TestMixUnchanged(t *testing.T) {
	for _, persona := range []string{"", demo.PersonaMix} {
		batches, err := demo.Generate(demo.Options{Days: 30, Seed: 1, Today: today, Persona: persona})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(batches)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != mixSeed1 {
			t.Errorf("persona %q: the mix records changed: %s", persona, got)
		}
	}
}

func TestPersonaList(t *testing.T) {
	if demo.Personas[0].Name != demo.PersonaMix || !slices.Equal(demo.Personas[0].Providers, demo.Providers) ||
		demo.Personas[0].Seed != 1 {
		t.Fatalf("the first persona is not the mix of every provider, seed 1: %+v", demo.Personas[0])
	}
	seeds := map[int64]bool{}
	for _, p := range demo.Personas {
		if got, ok := demo.LookupPersona(p.Name); !ok || got.Name != p.Name {
			t.Errorf("%s: not found", p.Name)
		}
		if seeds[p.Seed] {
			t.Errorf("%s: seed %d is another persona's", p.Name, p.Seed)
		}
		seeds[p.Seed] = true
		if p.Description == "" || strings.Contains(p.Description, "\n") {
			t.Errorf("%s: description %q", p.Name, p.Description)
		}
		// Its providers, in write order.
		last := -1
		for _, slug := range p.Providers {
			i := slices.Index(demo.Providers, slug)
			if i <= last {
				t.Errorf("%s: %s out of order or not a demo provider", p.Name, slug)
			}
			last = i
			if _, ok := connector.Get(slug); !ok {
				t.Errorf("%s: %s is not a connector", p.Name, slug)
			}
		}
	}
	if p, ok := demo.LookupPersona(""); !ok || p.Name != demo.PersonaMix {
		t.Error(`"" is not the mix`)
	}
	_, err := demo.Generate(demo.Options{Days: 1, Today: today, Persona: "athlete"})
	if err == nil || !strings.Contains(err.Error(), demo.PersonaNames()) {
		t.Errorf("unknown persona: %v", err)
	}
	if demo.PersonaNames() != "mix, runner, sleep, weight" {
		t.Errorf("names %q", demo.PersonaNames())
	}
}

// Each persona writes its providers only, deterministic for a seed and a
// date: the same seed gives the same records, a run on a later day the
// same records for the days both cover, and the same seed in two personas
// two people.
func TestPersonasDeterministic(t *testing.T) {
	for _, p := range demo.Personas {
		a := generate(t, demo.Options{Days: 10, Seed: p.Seed, Persona: p.Name})
		names := make([]string, 0, len(a))
		for slug, mr := range a {
			names = append(names, slug)
			if mr.IsEmpty() {
				t.Errorf("%s: %s wrote nothing", p.Name, slug)
			}
		}
		slices.Sort(names)
		want := slices.Clone(p.Providers)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("%s: providers %v, want %v", p.Name, names, want)
		}
		if b := generate(t, demo.Options{Days: 10, Seed: p.Seed, Persona: p.Name}); !reflect.DeepEqual(a, b) {
			t.Errorf("%s: the same seed gave different records", p.Name)
		}
		later := generate(t, demo.Options{Days: 10, Seed: p.Seed, Persona: p.Name, Today: today.AddDate(0, 0, 3)})
		for slug, mr := range a {
			hashes := map[string]bool{}
			for _, s := range later[slug].QuantitySamples {
				hashes[s.ContentHash] = true
			}
			for _, s := range mr.QuantitySamples {
				if s.StartDate.After(today.AddDate(0, 0, -7)) && !hashes[s.ContentHash] {
					t.Errorf("%s: %s: a sample of a shared day differs between run days", p.Name, slug)
					break
				}
			}
		}
		other := generate(t, demo.Options{Days: 10, Seed: p.Seed + 100, Persona: p.Name})
		if reflect.DeepEqual(a, other) {
			t.Errorf("%s: another seed gave the same records", p.Name)
		}
	}
	// Seed 1 is another person as a runner than in the mix: the strap's
	// night heart rate differs at the same instants.
	mix := generate(t, demo.Options{Days: 3, Seed: 1})
	runner := generate(t, demo.Options{Days: 3, Seed: 1, Persona: demo.PersonaRunner})
	if reflect.DeepEqual(mix[demo.Whoop].SleepSessions, runner[demo.Whoop].SleepSessions) {
		t.Error("the runner of seed 1 sleeps like the mix person of seed 1")
	}
}

func TestPersonasPassTheValidator(t *testing.T) {
	for _, p := range demo.Personas[1:] {
		for slug, mr := range generate(t, demo.Options{Days: demo.DefaultDays, Seed: p.Seed, Persona: p.Name}) {
			if errs := model.CanonicalizeUnits(mr); len(errs) > 0 {
				t.Fatalf("%s %s: unit errors: %v", p.Name, slug, errs)
			}
			errs := etl.ValidateMapResultAt(mr, today)
			if r := etl.Rejections(errs); len(r) > 0 {
				t.Errorf("%s %s: %d rejections, first %s.%s: %s", p.Name, slug, len(r), r[0].RecordType, r[0].Field, r[0].Message)
			}
			if s := etl.Suspicions(errs); len(s) > 0 {
				t.Errorf("%s %s: %d suspect values, first %s: %s", p.Name, slug, len(s), s[0].Field, s[0].Message)
			}
			for _, s := range mr.QuantitySamples {
				if s.Metadata[demo.MetadataSynthetic] != true || s.Provider != slug || s.EndDate.After(today) {
					t.Fatalf("%s %s: sample %+v", p.Name, slug, s)
				}
			}
		}
	}
}

// stats are the figures a persona's description states, from its records.
type stats struct {
	values     map[string][]float64 // provider:type
	stepsByDay map[string]float64   // the step source's total per local day
	sleepHours []float64
	bedtimes   []float64 // minutes after noon
	runDays    int
}

func statsOf(recs map[string]*model.MapResult, stepSource string) stats {
	st := stats{values: map[string][]float64{}, stepsByDay: map[string]float64{}}
	runs := map[string]bool{}
	for slug, mr := range recs {
		for _, s := range mr.QuantitySamples {
			if s.QuantityType == model.QTStepCount {
				if slug == stepSource {
					st.stepsByDay[s.StartDate.In(paris).Format(time.DateOnly)] += s.Value
				}
				continue
			}
			st.values[slug+":"+s.QuantityType] = append(st.values[slug+":"+s.QuantityType], s.Value)
		}
		for _, s := range mr.SleepSessions {
			st.sleepHours = append(st.sleepHours, float64(*s.TotalSleepSeconds)/3600)
			l := s.BedtimeStart.In(paris)
			st.bedtimes = append(st.bedtimes, math.Mod(float64(l.Hour()*60+l.Minute())+12*60, 24*60))
		}
		for _, w := range mr.Workouts {
			if w.ActivityType == "running" {
				runs[w.StartDate.In(paris).Format(time.DateOnly)] = true
			}
		}
	}
	st.runDays = len(runs)
	return st
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func spread(v []float64) float64 {
	m, s := mean(v), 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)))
}

func steps(st stats) float64 {
	v := make([]float64, 0, len(st.stepsByDay))
	for _, n := range st.stepsByDay {
		v = append(v, n)
	}
	return mean(v)
}

// The baselines land about where Personas says, for the persona's seed and
// for another one, on two run days.
func TestPersonaBaselines(t *testing.T) {
	type band struct{ lo, hi float64 }
	check := func(t *testing.T, what string, got float64, b band) {
		t.Helper()
		if got < b.lo || got > b.hi {
			t.Errorf("%s = %.2f, want %.0f to %.0f", what, got, b.lo, b.hi)
		}
	}
	for _, when := range []time.Time{today, today.AddDate(0, 5, 0)} {
		mixRecs := generate(t, demo.Options{Days: demo.DefaultDays, Seed: 1, Today: when})
		mix := statsOf(map[string]*model.MapResult{demo.Oura: mixRecs[demo.Oura]}, "")
		for _, seed := range []int64{0, 31} {
			run := func(name string) map[string]*model.MapResult {
				p, _ := demo.LookupPersona(name)
				n := seed
				if n == 0 {
					n = p.Seed
				}
				return generate(t, demo.Options{Days: demo.DefaultDays, Seed: n, Persona: name, Today: when})
			}
			r := statsOf(run(demo.PersonaRunner), demo.AppleHealth)
			check(t, "runner resting heart rate", mean(r.values["whoop:resting_heart_rate"]), band{44, 48})
			check(t, "runner HRV", mean(r.values["whoop:hrv_rmssd"]), band{72, 84})
			check(t, "runner VO2 max", mean(r.values["polar:vo2_max"]), band{54, 58})
			check(t, "runner steps", steps(r), band{10500, 13500})
			if r.runDays < demo.DefaultDays/2 {
				t.Errorf("runner: runs on %d days of %d", r.runDays, demo.DefaultDays)
			}

			s := statsOf(run(demo.PersonaSleep), demo.AppleHealth)
			check(t, "sleep hours", mean(s.sleepHours), band{5.8, 6.6})
			check(t, "sleep resting heart rate", mean(s.values["oura:resting_heart_rate"]), band{61, 65})
			check(t, "sleep HRV", mean(s.values["oura:hrv_rmssd"]), band{26, 34})
			check(t, "sleep steps", steps(s), band{5200, 6800})
			if late := mean(s.bedtimes) - mean(mix.bedtimes); late < 45 {
				t.Errorf("sleep: to bed %.0f minutes after the mix person, want later", late)
			}
			if spread(s.bedtimes) < 1.5*spread(mix.bedtimes) {
				t.Errorf("sleep: bedtimes spread %.0f minutes, the mix person's %.0f", spread(s.bedtimes), spread(mix.bedtimes))
			}

			wrecs := run(demo.PersonaWeight)
			w := statsOf(wrecs, demo.Fitbit)
			check(t, "weight", mean(w.values["withings:body_mass"]), band{88, 96})
			check(t, "body fat", mean(w.values["withings:body_fat_percentage"]), band{25, 31})
			check(t, "weight steps", steps(w), band{4700, 6300})
			// The drift shows from the first fortnight to the last.
			var first, last []float64
			for _, smp := range wrecs[demo.Withings].QuantitySamples {
				if smp.QuantityType != "body_mass" {
					continue
				}
				switch {
				case smp.StartDate.Before(when.AddDate(0, 0, -demo.DefaultDays+14)):
					first = append(first, smp.Value)
				case smp.StartDate.After(when.AddDate(0, 0, -15)):
					last = append(last, smp.Value)
				}
			}
			if d := math.Abs(mean(last) - mean(first)); d < 1 {
				t.Errorf("weight: %.2f kg between the first and the last fortnight, want a visible drift", d)
			}
		}
	}
}
