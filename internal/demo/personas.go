// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

// Persona names. Mix is the default: one person seen through every
// provider. The others are each a person of their own, with their own
// providers and habits, for a demo of one kind of user.
const (
	PersonaMix    = "mix"
	PersonaRunner = "runner"
	PersonaSleep  = "sleep"
	PersonaWeight = "weight"
)

// PersonaInfo describes one persona.
type PersonaInfo struct {
	Name string
	// Description is one line on who the person is.
	Description string
	// Seed is the persona's own seed: `ohs seed --demo --persona` and the
	// admin API's demo user use it.
	Seed int64
	// Providers are the providers it writes, in write order.
	Providers []string
}

// Personas lists the personas, the default first.
var Personas = []PersonaInfo{
	{PersonaMix, "one person seen through six sources that overlap like real devices", 1, Providers},
	{PersonaRunner, "a trained runner: resting heart rate about 46, HRV about 78, VO2 max about 56, " +
		"about 12,000 steps, runs on most days", 2, []string{Whoop, Polar, AppleHealth}},
	{PersonaSleep, "a short sleeper: about 6.2 h a night with late, irregular bedtimes, resting heart rate " +
		"about 63, HRV about 30, about 6,000 steps, few workouts", 3, []string{Oura, AppleHealth}},
	{PersonaWeight, "someone watching their weight: about 92 kg with a slow drift, body fat about 28 percent, " +
		"about 5,500 steps, few workouts", 4, []string{Withings, Fitbit}},
}

// LookupPersona returns the persona of that name; "" is PersonaMix.
func LookupPersona(name string) (PersonaInfo, bool) {
	if name == "" {
		name = PersonaMix
	}
	i := slices.IndexFunc(Personas, func(p PersonaInfo) bool { return p.Name == name })
	if i < 0 {
		return PersonaInfo{}, false
	}
	return Personas[i], true
}

// DefaultExternalRef is the external_ref of the demo user of a persona
// when none is given: demo-user for the mix, demo-<name> for the others, so
// each persona fills its own user and two never mix in one.
func DefaultExternalRef(persona string) string {
	if persona == "" || persona == PersonaMix {
		return "demo-user"
	}
	return "demo-" + persona
}

// PersonaNames lists the persona names, comma separated, for a message.
func PersonaNames() string {
	names := make([]string, 0, len(Personas))
	for _, p := range Personas {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// habits are how a persona lives: the parts of a day that differ between
// personas. The mix person's are the values the generator always had.
type habits struct {
	// bedHour and bedMinute are the usual bedtime on the eve of a weekday,
	// weekendLater how much later before a Saturday or a Sunday, bedSD the
	// spread in minutes.
	bedHour, bedMinute int
	weekendLater       time.Duration
	bedSD              float64
	// sleepHours and sleepSD are the time in bed on a weekday night.
	sleepHours, sleepSD float64
	// carried is the share of the steps the phone counts, and phoneOnRuns
	// whether it counts the runs too.
	carried     float64
	phoneOnRuns bool
	// drift makes the weight follow a slow cycle of loss and regain instead
	// of the mix person's gentle wave.
	drift bool
	// plan is the workout of a date, from the date's draw p and its random
	// stream; nil is the mix person's week.
	plan func(d time.Time, p float64, r *rand.Rand, at func(h, m int) time.Time) *workoutPlan
}

var mixHabits = habits{bedHour: 22, bedMinute: 50, weekendLater: 50 * time.Minute, bedSD: 25,
	sleepHours: 7.3, sleepSD: 0.55, carried: 0.82}

// persona is a generator's person: how it is drawn and how it lives.
type persona struct {
	name    string
	profile func(r *rand.Rand) profile
	habits  habits
}

func personaOf(name string) persona {
	switch name {
	case PersonaRunner:
		return persona{name: name, profile: runnerProfile, habits: habits{bedHour: 22, bedMinute: 30,
			weekendLater: 20 * time.Minute, bedSD: 20, sleepHours: 7.9, sleepSD: 0.45, carried: 0.95,
			phoneOnRuns: true, plan: runnerWeek}}
	case PersonaSleep:
		return persona{name: name, profile: sleepProfile, habits: habits{bedHour: 23, bedMinute: 50,
			weekendLater: 70 * time.Minute, bedSD: 55, sleepHours: 6.45, sleepSD: 0.75, carried: 0.95,
			plan: easyWeek}}
	case PersonaWeight:
		return persona{name: name, profile: weightProfile, habits: habits{bedHour: 23, bedMinute: 0,
			weekendLater: 40 * time.Minute, bedSD: 30, sleepHours: 7.1, sleepSD: 0.6, carried: 0.95,
			drift: true, plan: easyWeek}}
	}
	return persona{name: PersonaMix, profile: mixProfile, habits: mixHabits}
}

// mixProfile is the person the generator always drew: the draws and their
// order are those of every earlier version, so a seed gives the same data.
func mixProfile(r *rand.Rand) profile {
	return profile{
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
	}
}

// The baselines below are a little under the figures of Personas where a
// day adds to them: the run of the day before raises the resting heart
// rate and lowers HRV, and the day's runs or walks add steps.

func runnerProfile(r *rand.Rand) profile {
	return profile{
		restingHR: 44.5 + 1.5*r.Float64(),
		hrv:       78 + 4*r.Float64(),
		weight:    64 + 6*r.Float64(),
		bodyFat:   10 + 3*r.Float64(),
		height:    1.68 + 0.14*r.Float64(),
		stepGoal:  6300 + 500*r.Float64(),
		vo2max:    55.5 + 2*r.Float64(),
		phase:     2 * math.Pi * r.Float64(),
		home:      [2]float64{48.8462 + 0.002*r.Float64(), 2.3371 + 0.002*r.Float64()},
	}
}

func sleepProfile(r *rand.Rand) profile {
	return profile{
		restingHR: 62.7 + 1.5*r.Float64(),
		hrv:       27.5 + 3*r.Float64(),
		weight:    70 + 10*r.Float64(),
		bodyFat:   20 + 5*r.Float64(),
		height:    1.65 + 0.15*r.Float64(),
		stepGoal:  5600 + 400*r.Float64(),
		vo2max:    36 + 4*r.Float64(),
		phase:     2 * math.Pi * r.Float64(),
		home:      [2]float64{48.8462 + 0.002*r.Float64(), 2.3371 + 0.002*r.Float64()},
	}
}

func weightProfile(r *rand.Rand) profile {
	return profile{
		restingHR: 66 + 3*r.Float64(),
		hrv:       30 + 6*r.Float64(),
		weight:    91 + 2*r.Float64(),
		bodyFat:   27 + 2*r.Float64(),
		height:    1.74 + 0.08*r.Float64(),
		stepGoal:  4800 + 400*r.Float64(),
		vo2max:    31 + 4*r.Float64(),
		phase:     2 * math.Pi * r.Float64(),
		home:      [2]float64{48.8462 + 0.002*r.Float64(), 2.3371 + 0.002*r.Float64()},
	}
}

// Weight drift: a cycle of a year, as for many people. From mid February
// (the seed moves it by up to ten days) the weight falls by driftKg over
// driftDown days, then the rest of the year, the holidays, brings it back.
// A function of the date only.
const (
	driftYear  = 365.2425
	driftStart = 45.0 // days after January 1
	driftDown  = 300.0
	driftKg    = 6.0
)

// drift is the weight above the baseline on day n (days since the epoch),
// between +driftKg/2 and -driftKg/2.
func drift(n, phase float64) float64 {
	start := driftStart + 20*(phase/(2*math.Pi)-0.5)
	x := math.Mod(n-start, driftYear)
	if x < 0 {
		x += driftYear
	}
	if x < driftDown {
		return driftKg/2 - driftKg*x/driftDown
	}
	return -driftKg/2 + driftKg*(x-driftDown)/(driftYear-driftDown)
}

// runnerWeek runs on most days: easy runs, intervals and a tempo run in the
// week, a long run on Saturday, and some strength work.
func runnerWeek(d time.Time, p float64, r *rand.Rand, at func(h, m int) time.Time) *workoutPlan {
	run := func(h, m int, minutes, spread int, intensity, speed float64) *workoutPlan {
		w := &workoutPlan{kind: "running", start: at(h, m), intensity: intensity + 0.08*r.Float64(),
			speed: speed + 0.3*r.Float64(), elevation: 20 + 60*r.Float64()}
		w.end = w.start.Add(time.Duration(minutes+r.IntN(spread)) * time.Minute)
		return w
	}
	switch d.Weekday() {
	case time.Monday:
		if p < 0.6 {
			return run(7, 0, 30, 11, 0.45, 3.1)
		}
	case time.Tuesday:
		if p < 0.9 {
			return run(18, 30, 45, 16, 0.78, 3.6)
		}
	case time.Wednesday:
		if p < 0.8 {
			return run(7, 0, 40, 11, 0.5, 3.2)
		}
	case time.Thursday:
		if p < 0.85 {
			return run(18, 30, 45, 11, 0.7, 3.6)
		}
	case time.Friday:
		if p < 0.4 {
			w := &workoutPlan{kind: "strength_training", start: at(18, 0), intensity: 0.4 + 0.1*r.Float64()}
			w.end = w.start.Add(time.Duration(35+r.IntN(16)) * time.Minute)
			return w
		}
	case time.Saturday:
		if p < 0.9 {
			return run(8, 30, 80, 31, 0.58, 3.2)
		}
	case time.Sunday:
		if p < 0.6 {
			return run(9, 30, 35, 11, 0.42, 3.0)
		}
	}
	return nil
}

// easyWeek is a walk now and then, more often on Sunday.
func easyWeek(d time.Time, p float64, r *rand.Rand, at func(h, m int) time.Time) *workoutPlan {
	walk := func(h, m, minutes, spread int) *workoutPlan {
		w := &workoutPlan{kind: "walking", start: at(h, m), intensity: 0.15 + 0.1*r.Float64(),
			speed: 1.25 + 0.2*r.Float64(), elevation: 10 + 20*r.Float64()}
		w.end = w.start.Add(time.Duration(minutes+r.IntN(spread)) * time.Minute)
		return w
	}
	switch {
	case d.Weekday() == time.Wednesday && p < 0.2:
		return walk(18, 30, 30, 16)
	case d.Weekday() == time.Sunday && p < 0.4:
		return walk(15, 0, 40, 31)
	}
	return nil
}
