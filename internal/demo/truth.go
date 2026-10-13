// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// The truth of one day: what the person did and how their body responded.
// Each provider reports it through its own sensor, with its own noise and
// coverage. Every figure of a date depends only on the seed and the date.

// phase is one stretch of one sleep stage.
type phase struct {
	stage      string
	start, end time.Time
}

// workoutPlan is the workout of one day.
type workoutPlan struct {
	kind       string // canonical activity type
	start, end time.Time
	intensity  float64 // 0 (stroll) to 1 (race)
	speed      float64 // m/s, 0 for an indoor session
	elevation  float64 // m gained
}

func (w *workoutPlan) minutes() float64 { return w.end.Sub(w.start).Minutes() }

func (w *workoutPlan) distance() float64 { return w.speed * w.end.Sub(w.start).Seconds() }

// cadence is the steps per minute the workout adds.
func (w *workoutPlan) cadence() float64 {
	switch w.kind {
	case "running":
		return 165
	case "walking":
		return 105
	}
	return 0
}

// minutesIn is the number of workout minutes in local hour h of its day.
func (w *workoutPlan) minutesIn(loc *time.Location, h int) float64 {
	n := 0.0
	for t := w.start; t.Before(w.end); t = t.Add(time.Minute) {
		if t.In(loc).Hour() == h {
			n++
		}
	}
	return n
}

// day is the truth of one local calendar day.
type day struct {
	date    time.Time // local midnight
	trend   float64   // slow fitness cycle, -1 (tired) to 1 (fresh)
	weekend bool

	// The night that ends this morning.
	sleepStart, sleepEnd time.Time
	phases               []phase

	restingHR, hrv, resp, spo2, tempDev float64
	weight, bodyFat                     float64

	steps   [24]float64 // per local hour
	workout *workoutPlan
}

// day returns the truth of the local date of d, computed once.
func (g *gen) day(d time.Time) *day {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, g.loc)
	key := d.Format(time.DateOnly)
	if t, ok := g.days[key]; ok {
		return t
	}
	t := g.makeDay(d)
	g.days[key] = t
	return t
}

func (g *gen) makeDay(d time.Time) *day {
	r := g.dayRNG("truth", d)
	n := float64(d.Unix()) / 86400 // days since the epoch, as a smooth clock
	t := &day{
		date:    d,
		trend:   math.Sin(2*math.Pi*n/41 + g.p.phase),
		weekend: d.Weekday() == time.Saturday || d.Weekday() == time.Sunday,
	}
	t.workout = g.planWorkout(d)

	// Sleep: to bed later before a weekend day, longer at the weekend.
	eve := d.AddDate(0, 0, -1)
	bed := time.Date(eve.Year(), eve.Month(), eve.Day(), 22, 50, 0, 0, g.loc)
	if eve.Weekday() == time.Friday || eve.Weekday() == time.Saturday {
		bed = bed.Add(50 * time.Minute)
	}
	bed = bed.Add(time.Duration(norm(r, 0, 25)) * time.Minute).Truncate(time.Minute)
	hours := norm(r, 7.3, 0.55)
	if t.weekend {
		hours += 0.4
	}
	t.sleepStart = bed
	t.sleepEnd = bed.Add(time.Duration(hours * float64(time.Hour))).Truncate(time.Minute)
	t.phases = hypnogram(r, t.sleepStart, t.sleepEnd)

	// A hard session the day before costs some recovery.
	strain := 0.0
	if prev := g.planWorkout(eve); prev != nil {
		strain = prev.intensity * prev.minutes() / 60
	}
	t.restingHR = norm(r, g.p.restingHR-1.5*t.trend+1.2*strain, 1.2)
	t.hrv = math.Max(15, norm(r, g.p.hrv+6*t.trend-5*strain, 4))
	t.resp = norm(r, 14.5, 0.6)
	t.spo2 = math.Min(99.5, norm(r, 96.6, 0.7))
	t.tempDev = norm(r, 0, 0.18)
	t.weight = g.p.weight + 0.9*math.Sin(2*math.Pi*n/67+g.p.phase) + norm(r, 0, 0.25)
	t.bodyFat = g.p.bodyFat + 0.6*math.Sin(2*math.Pi*n/67+g.p.phase) + norm(r, 0, 0.3)

	// Steps per hour: a commute, lunch and evening shape on weekdays, a
	// late and flatter one at the weekend.
	shape := [24]float64{7: 0.6, 8: 1.4, 9: 0.8, 10: 0.5, 11: 0.5, 12: 1.2, 13: 0.9, 14: 0.4, 15: 0.4,
		16: 0.5, 17: 0.9, 18: 1.3, 19: 0.8, 20: 0.5, 21: 0.3, 22: 0.15}
	if t.weekend {
		shape = [24]float64{8: 0.3, 9: 0.6, 10: 1.0, 11: 1.2, 12: 0.9, 13: 0.7, 14: 0.9, 15: 1.1,
			16: 1.1, 17: 0.9, 18: 0.7, 19: 0.6, 20: 0.4, 21: 0.3, 22: 0.2}
	}
	total := g.p.stepGoal * math.Max(0.45, norm(r, 1, 0.22))
	sum := 0.0
	for _, w := range shape {
		sum += w
	}
	for h, w := range shape {
		if w > 0 {
			t.steps[h] = math.Round(total * w / sum * math.Max(0.3, norm(r, 1, 0.25)))
		}
	}
	if w := t.workout; w != nil && w.cadence() > 0 {
		for m := w.start; m.Before(w.end); m = m.Add(time.Minute) {
			t.steps[m.Hour()] += w.cadence()
		}
	}
	return t
}

// planWorkout is the workout of date d, or nil. It has its own random
// stream, so the day after can look at it without drawing the whole day.
func (g *gen) planWorkout(d time.Time) *workoutPlan {
	r := g.dayRNG("workout", d)
	at := func(h, m int) time.Time {
		t := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, g.loc)
		return t.Add(time.Duration(norm(r, 0, 12)) * time.Minute).Truncate(time.Minute)
	}
	p := r.Float64()
	var w *workoutPlan
	switch d.Weekday() {
	case time.Monday:
		if p < 0.5 {
			w = &workoutPlan{kind: "strength_training", start: at(18, 45), intensity: 0.4 + 0.1*r.Float64()}
			w.end = w.start.Add(time.Duration(40+r.IntN(20)) * time.Minute)
		}
	case time.Tuesday:
		if p < 0.7 {
			w = &workoutPlan{kind: "running", start: at(18, 30), intensity: 0.65 + 0.15*r.Float64(),
				speed: 2.8 + 0.4*r.Float64(), elevation: 20 + 40*r.Float64()}
			w.end = w.start.Add(time.Duration(35+r.IntN(16)) * time.Minute)
		}
	case time.Thursday:
		if p < 0.6 {
			w = &workoutPlan{kind: "cycling", start: at(18, 0), intensity: 0.5 + 0.2*r.Float64(),
				speed: 6.6 + 1.4*r.Float64(), elevation: 150 + 250*r.Float64()}
			w.end = w.start.Add(time.Duration(60+r.IntN(31)) * time.Minute)
		}
	case time.Saturday:
		if p < 0.8 {
			w = &workoutPlan{kind: "running", start: at(9, 30), intensity: 0.55 + 0.1*r.Float64(),
				speed: 2.7 + 0.3*r.Float64(), elevation: 40 + 80*r.Float64()}
			w.end = w.start.Add(time.Duration(60+r.IntN(36)) * time.Minute)
		}
	case time.Sunday:
		switch {
		case p < 0.55:
			w = &workoutPlan{kind: "walking", start: at(15, 0), intensity: 0.15 + 0.1*r.Float64(),
				speed: 1.3 + 0.2*r.Float64(), elevation: 10 + 30*r.Float64()}
			w.end = w.start.Add(time.Duration(45+r.IntN(46)) * time.Minute)
		case p < 0.8:
			w = &workoutPlan{kind: "cycling", start: at(10, 0), intensity: 0.45 + 0.15*r.Float64(),
				speed: 6.5 + 1.2*r.Float64(), elevation: 200 + 300*r.Float64()}
			w.end = w.start.Add(time.Duration(75+r.IntN(46)) * time.Minute)
		}
	}
	return w
}

// hypnogram splits a night into sleep cycles of about 90 minutes: light,
// deep, light, REM, sometimes a short wake. Deep sleep shrinks and REM grows
// as the night goes on, as in a real night. It starts with a few minutes awake
// (falling asleep).
func hypnogram(r *rand.Rand, start, end time.Time) []phase {
	var out []phase
	at := start
	add := func(stage string, minutes float64) {
		d := time.Duration(math.Round(minutes)) * time.Minute
		if d <= 0 || !at.Before(end) {
			return
		}
		next := at.Add(d)
		if next.After(end) {
			next = end
		}
		if n := len(out); n > 0 && out[n-1].stage == stage {
			out[n-1].end = next
		} else {
			out = append(out, phase{stage: stage, start: at, end: next})
		}
		at = next
	}
	add(model.SleepStageAwake, 6+10*r.Float64())
	cycles := math.Max(1, math.Round(end.Sub(at).Minutes()/90))
	length := end.Sub(at).Minutes() / cycles
	for i := 0.0; i < cycles; i++ {
		deep := math.Max(0.04, 0.32-0.08*i+norm(r, 0, 0.03))
		rem := math.Min(0.4, 0.12+0.06*i+norm(r, 0, 0.03))
		awake := 0.0
		if r.Float64() < 0.45 {
			awake = 2 + 6*r.Float64()
		}
		light := length*(1-deep-rem) - awake
		add(model.SleepStageLight, light/2)
		add(model.SleepStageDeep, length*deep)
		add(model.SleepStageLight, light/2)
		add(model.SleepStageREM, length*rem)
		add(model.SleepStageAwake, awake)
	}
	// Rounding leaves a few minutes: the last stage takes them.
	if n := len(out); n > 0 {
		out[n-1].end = end
	}
	return out
}

// stageAt is the sleep stage at t, or "" when the person is awake out of bed.
func (g *gen) stageAt(t time.Time) string {
	local := t.In(g.loc)
	for _, d := range []*day{g.day(local), g.day(local.AddDate(0, 0, 1))} {
		if t.Before(d.sleepStart) || !t.Before(d.sleepEnd) {
			continue
		}
		for _, p := range d.phases {
			if !t.Before(p.start) && t.Before(p.end) {
				return p.stage
			}
		}
	}
	return ""
}

// hrAt is the true heart rate at t, before any sensor noise.
func (g *gen) hrAt(t time.Time) float64 {
	local := t.In(g.loc)
	d := g.day(local)
	switch g.stageAt(t) {
	case model.SleepStageDeep:
		return d.restingHR + 1
	case model.SleepStageLight:
		return d.restingHR + 4
	case model.SleepStageREM:
		return d.restingHR + 8
	case model.SleepStageAwake:
		return d.restingHR + 12
	}
	if w := d.workout; w != nil && !t.Before(w.start) && t.Before(w.end) {
		elapsed := t.Sub(w.start).Minutes()
		warm := math.Min(1, elapsed/8)
		return d.restingHR + 20 + warm*(20+90*w.intensity+6*math.Sin(elapsed/4))
	}
	// Awake: higher in active hours, a little higher in the afternoon.
	h := local.Hour()
	activity := math.Min(1, d.steps[h]/1500)
	return d.restingHR + 18 + 20*activity + 4*math.Sin(math.Pi*float64(h-8)/14)
}
