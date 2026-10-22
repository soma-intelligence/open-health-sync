// SPDX-License-Identifier: AGPL-3.0-only

package synth

import (
	"math"
	"time"
)

// Totals is what a device sums over one local day, the figures every daily
// summary endpoint reports in its own words.
type Totals struct {
	Steps      float64
	DistanceM  float64
	ActiveKcal float64
	BasalKcal  float64
	FloorsUp   float64
	// Minutes by intensity, from the step cadence and the workouts.
	SedentaryMin, LightMin, ModerateMin, VigorousMin float64
	RestingHR, AvgHR, MinHR, MaxHR                   float64
	// WornMin is the minutes the wrist device was on.
	WornMin float64
}

// Totals sums a day. Days the device was off sum to zero; a day partly off
// sums what was recorded.
func (tl *Timeline) Totals(d Day) Totals {
	if tl.totals == nil {
		tl.totals = map[int]Totals{}
	}
	if t, ok := tl.totals[d.Index]; ok {
		return t
	}
	var t Totals
	if d.Missing {
		tl.totals[d.Index] = t
		return t
	}
	stride := tl.P.HeightCM * 0.415 / 100
	var hrSum, hrN float64
	t.MinHR = math.Inf(1)
	for m := d.Start; m.Before(d.End); m = m.Add(time.Minute) {
		if !tl.Worn(m) {
			continue
		}
		t.WornMin++
		c := tl.cadence(m)
		t.Steps += c
		w, inW := tl.WorkoutAt(m)
		switch {
		case inW && w.DistanceM > 0 && !w.Paused(m):
			t.DistanceM += w.DistanceM / w.Moving().Minutes()
		case !inW:
			t.DistanceM += c * stride
		}
		if inW {
			t.ActiveKcal += w.EnergyKcal / w.End.Sub(w.Start).Minutes()
		} else {
			t.ActiveKcal += c * 0.04 * tl.P.WeightKG / 70
		}
		switch {
		case inW && w.Sport.intensity() >= 0.75:
			t.VigorousMin++
		case inW || c >= 100:
			t.ModerateMin++
		case c > 0:
			t.LightMin++
		case !tl.asleep(m):
			t.SedentaryMin++
		}
		if m.Minute()%5 == 0 {
			hr := tl.HeartRate(m)
			hrSum += hr
			hrN++
			t.MinHR = math.Min(t.MinHR, hr)
			t.MaxHR = math.Max(t.MaxHR, hr)
		}
	}
	for _, w := range tl.WorkoutsOn(d.Index) {
		t.FloorsUp += math.Floor(w.AscentM / 3)
	}
	t.FloorsUp += float64(int(hash01(tl.Opt.Seed, "floors", int64(d.Index))*12) + 2)
	if t.WornMin == 0 {
		t.FloorsUp = 0
	}
	t.Steps = math.Round(t.Steps)
	t.DistanceM = round(t.DistanceM, 1)
	t.ActiveKcal = round(t.ActiveKcal, 1)
	t.BasalKcal = tl.BasalEnergy(d.Start, d.End)
	if hrN > 0 {
		t.AvgHR = round(hrSum/hrN, 0)
	} else {
		t.MinHR = 0
	}
	t.RestingHR = round(tl.P.RestingHR+valueNoise(tl.Opt.Seed, "rhr", d.Start, 3*24*time.Hour)*2, 0)
	if s, ok := tl.NightOf(d.Index); ok {
		t.RestingHR = s.LowestHR + 3
	}
	tl.totals[d.Index] = t
	return t
}

// NightOf returns the main sleep that ends on day i.
func (tl *Timeline) NightOf(i int) (Sleep, bool) {
	for _, s := range tl.Sleeps {
		if s.Day == i && !s.Nap {
			return s, true
		}
	}
	return Sleep{}, false
}

// SleepsOn returns the sleeps, naps included, that end on day i.
func (tl *Timeline) SleepsOn(i int) []Sleep {
	var out []Sleep
	for _, s := range tl.Sleeps {
		if s.Day == i {
			out = append(out, s)
		}
	}
	return out
}

// WorkoutsOn returns the workouts of day i.
func (tl *Timeline) WorkoutsOn(i int) []Workout {
	var out []Workout
	for _, w := range tl.Workouts {
		if w.Day == i {
			out = append(out, w)
		}
	}
	return out
}

// Recorded returns the workouts a wrist device saw.
func (tl *Timeline) Recorded() []Workout {
	var out []Workout
	for _, w := range tl.Workouts {
		if tl.Worn(w.Start) {
			out = append(out, w)
		}
	}
	return out
}

// Age returns the persona's age in whole years at t.
func (tl *Timeline) Age(t time.Time) int {
	b, err := time.Parse(time.DateOnly, tl.P.BirthDate)
	if err != nil {
		return 0
	}
	age := t.Year() - b.Year()
	if t.YearDay() < b.YearDay() {
		age--
	}
	return age
}

// LocalISO writes t in the zone the person was in, with its offset, as most
// vendors write a local timestamp: 2026-03-29T03:30:00+02:00.
func (tl *Timeline) LocalISO(t time.Time) string { return tl.Local(t).Format(time.RFC3339) }

// Intensity is how hard a sport is, 0..1.
func (s Sport) Intensity() float64 { return s.intensity() }

// Every returns the instants from a to b, step apart, that a worn device saw.
func (tl *Timeline) Every(a, b time.Time, step time.Duration) []time.Time {
	var out []time.Time
	for t := a; t.Before(b); t = t.Add(step) {
		if tl.Worn(t) {
			out = append(out, t)
		}
	}
	return out
}
