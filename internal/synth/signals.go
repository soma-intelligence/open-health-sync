// SPDX-License-Identifier: AGPL-3.0-only

package synth

import (
	"math"
	"time"
)

// The signals below are functions of time, so a device sampling every second
// and another sampling every ten minutes read the same body, and a year at one
// sample a second is streamed without holding it in memory.

// HeartRate returns the heart rate at t, in beats per minute.
func (tl *Timeline) HeartRate(t time.Time) float64 {
	p := tl.P
	seed := tl.Opt.Seed
	n := valueNoise(seed, "hr", t, 7*time.Minute)*3 + valueNoise(seed, "hr-fast", t, 40*time.Second)*1.2
	if w, ok := tl.WorkoutAt(t); ok {
		el := t.Sub(w.Start)
		ramp := clamp(el.Minutes()/4, 0, 1)
		drift := clamp(el.Minutes()/120, 0, 1) * 6
		target := w.AvgHR - 3 + drift
		if w.Sport == SportHIIT {
			// Intervals: 1 minute hard, 1 minute easy.
			if int(el.Minutes())%2 == 0 {
				target = w.MaxHR - 4
			} else {
				target = w.AvgHR - 12
			}
		}
		if w.Paused(t) {
			target = p.RestingHR + 30
		}
		hr := p.RestingHR + 15 + (target-p.RestingHR-15)*ramp + n
		if tl.ExtremeDay == w.Day && el > w.End.Sub(w.Start)-3*time.Minute {
			return w.MaxHR
		}
		return round(clamp(hr, 30, w.MaxHR), 0)
	}
	if s, ok := tl.SleepAt(t); ok {
		dip := 0.0
		switch s.StageAt(t) {
		case StageDeep:
			dip = -3
		case StageREM:
			dip = 2
		case StageAwake:
			dip = 6
		}
		hr := s.AvgHR + dip + n*0.6
		if tl.ExtremeDay == s.Day && !s.Nap {
			hr = math.Min(hr, 35+math.Abs(n))
		}
		return round(math.Max(hr, s.LowestHR), 0)
	}
	act := tl.cadence(t) / 115
	circ := 4 * math.Sin(2*math.Pi*(float64(tl.Local(t).Hour())-9)/24)
	return round(clamp(p.RestingHR+10+circ+30*act+n, 35, 205), 0)
}

// cadence returns the steps per minute at t.
func (tl *Timeline) cadence(t time.Time) float64 {
	if w, ok := tl.WorkoutAt(t); ok {
		if w.Paused(t) || w.AvgCadence == 0 || w.Steps == 0 {
			return 0
		}
		return w.AvgCadence + valueNoise(tl.Opt.Seed, "cad", t, 30*time.Second)*3
	}
	if b, ok := tl.boutAt(t); ok {
		return b.Cadence + valueNoise(tl.Opt.Seed, "cad", t, 30*time.Second)*4
	}
	if tl.asleep(t) {
		return 0
	}
	// Pottering about: a few steps most minutes of the day.
	x := hash01(tl.Opt.Seed, "potter", t.Unix()/60)
	if x < 0.7 {
		return 0
	}
	return math.Floor((x - 0.7) * 40)
}

// Steps returns the steps taken in [a, b), counted minute by minute.
func (tl *Timeline) Steps(a, b time.Time) float64 {
	if !b.After(a) {
		return 0
	}
	var n float64
	for m := a.Truncate(time.Minute); m.Before(b); m = m.Add(time.Minute) {
		s, e := m, m.Add(time.Minute)
		if s.Before(a) {
			s = a
		}
		if e.After(b) {
			e = b
		}
		if !tl.Worn(m) {
			continue
		}
		n += tl.cadence(m) * e.Sub(s).Minutes()
	}
	return math.Round(n)
}

// Distance returns the metres covered in [a, b): workouts at their speed,
// walks at a stride from the height.
func (tl *Timeline) Distance(a, b time.Time) float64 {
	stride := tl.P.HeightCM * 0.415 / 100
	var m float64
	for t := a.Truncate(time.Minute); t.Before(b); t = t.Add(time.Minute) {
		if w, ok := tl.WorkoutAt(t); ok && w.DistanceM > 0 {
			if !w.Paused(t) {
				m += w.DistanceM / w.Moving().Minutes()
			}
			continue
		}
		m += tl.cadence(t) * stride
	}
	return round(m, 1)
}

// ActiveEnergy returns the active kilocalories burned in [a, b).
func (tl *Timeline) ActiveEnergy(a, b time.Time) float64 {
	var k float64
	for t := a.Truncate(time.Minute); t.Before(b); t = t.Add(time.Minute) {
		if w, ok := tl.WorkoutAt(t); ok {
			k += w.EnergyKcal / w.End.Sub(w.Start).Minutes()
			continue
		}
		k += tl.cadence(t) * 0.04 * tl.P.WeightKG / 70
	}
	return round(k, 2)
}

// BasalEnergy returns the resting kilocalories of [a, b), from a
// Mifflin-St Jeor estimate.
func (tl *Timeline) BasalEnergy(a, b time.Time) float64 {
	p := tl.P
	age := float64(a.Year() - atoi(p.BirthDate[:4]))
	bmr := 10*p.WeightKG + 6.25*p.HeightCM - 5*age
	if p.Sex == "male" {
		bmr += 5
	} else {
		bmr -= 161
	}
	return round(bmr*b.Sub(a).Hours()/24, 2)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// Glucose returns the blood glucose at t in mg/dL: meals raise it, workouts
// lower it, a type 1 persona swings more.
func (tl *Timeline) Glucose(t time.Time) float64 {
	p := tl.P
	g := p.GlucoseMgdl + 8*valueNoise(tl.Opt.Seed, "glu", t, 50*time.Minute)
	amp := 45.0
	if p.CGM {
		amp = 110
	}
	for _, m := range tl.Meals {
		dt := t.Sub(m.At).Minutes()
		if dt < 0 || dt > 240 {
			continue
		}
		g += amp * (m.CarbsG / 80) * math.Exp(-math.Pow(dt-50, 2)/(2*35*35))
	}
	if w, ok := tl.WorkoutAt(t); ok {
		g -= clamp(t.Sub(w.Start).Minutes()/2, 0, 25)
	}
	if tl.ExtremeDay >= 0 {
		d := tl.Days[tl.ExtremeDay]
		switch {
		case Interval{at(d, 3), at(d, 3.4)}.Contains(t):
			return 45
		case Interval{at(d, 13), at(d, 13.5)}.Contains(t):
			return 400
		}
	}
	return round(clamp(g, 40, 400), 0)
}

// SpO2 returns the blood oxygen saturation at t, as a fraction.
func (tl *Timeline) SpO2(t time.Time) float64 {
	base := 0.975
	if s, ok := tl.SleepAt(t); ok {
		base = s.SpO2/100 - 0.005
	}
	v := base + 0.012*valueNoise(tl.Opt.Seed, "spo2", t, 20*time.Minute)
	if tl.ExtremeDay >= 0 && (Interval{at(tl.Days[tl.ExtremeDay], 2), at(tl.Days[tl.ExtremeDay], 2.1)}).Contains(t) {
		v = 0.86
	}
	return round(clamp(v, 0.7, 1), 3)
}

// Respiration returns the breaths per minute at t.
func (tl *Timeline) Respiration(t time.Time) float64 {
	base := 15.0
	if s, ok := tl.SleepAt(t); ok {
		base = s.Resp
	} else if _, ok := tl.WorkoutAt(t); ok {
		base = 28
	}
	return round(base+1.2*valueNoise(tl.Opt.Seed, "resp", t, 15*time.Minute), 1)
}

// HRVSDNN returns an SDNN reading at t, in milliseconds.
func (tl *Timeline) HRVSDNN(t time.Time) float64 {
	return round(clamp(tl.P.HRVms*0.85+12*valueNoise(tl.Opt.Seed, "sdnn", t, 6*time.Hour), 8, 200), 3)
}

// Route returns the position, altitude and speed at t of an outdoor workout:
// a loop of the workout's distance around its center.
func (tl *Timeline) Route(w Workout, t time.Time) (lat, lon, ele, speed float64) {
	if w.DistanceM == 0 || w.Moving() <= 0 {
		return w.CenterLat, w.CenterLon, 35, 0
	}
	moved := t.Sub(w.Start)
	for _, p := range w.Pauses {
		if t.After(p.End) {
			moved -= p.Duration()
		} else if t.After(p.Start) {
			moved -= t.Sub(p.Start)
		}
	}
	frac := clamp(moved.Seconds()/w.Moving().Seconds(), 0, 1)
	// A loop of 2 km circumference repeated as many times as the distance
	// needs.
	const loop = 2000.0
	radius := loop / (2 * math.Pi)
	ang := 2 * math.Pi * frac * w.DistanceM / loop
	dLat := radius * math.Sin(ang) / 111320
	dLon := radius * (1 - math.Cos(ang)) / (111320 * math.Cos(w.CenterLat*math.Pi/180))
	lat = round(w.CenterLat+dLat, 7)
	lon = round(w.CenterLon+dLon, 7)
	ele = round(35+8*math.Sin(ang/2)+0.5*valueNoise(tl.Opt.Seed, "ele", t, time.Minute), 2)
	if w.Paused(t) {
		return lat, lon, ele, 0
	}
	speed = round(w.DistanceM/w.Moving().Seconds()*(1+0.05*valueNoise(tl.Opt.Seed, "spd", t, 45*time.Second)), 3)
	return lat, lon, ele, speed
}

// ecgTrace draws secs seconds of a single lead ECG at hz samples a second, in
// microvolts: a P wave, a QRS complex and a T wave per beat.
func ecgTrace(seed uint64, start time.Time, hz, secs int, bpm float64) []float64 {
	n := hz * secs
	out := make([]float64, n)
	rr := 60 / bpm
	gauss := func(x, mu, sd, a float64) float64 { return a * math.Exp(-math.Pow(x-mu, 2)/(2*sd*sd)) }
	for i := range out {
		tt := float64(i) / float64(hz)
		ph := math.Mod(tt, rr) / rr
		v := gauss(ph, 0.18, 0.025, 110) - gauss(ph, 0.29, 0.008, 120) + gauss(ph, 0.31, 0.012, 1100) -
			gauss(ph, 0.33, 0.01, 250) + gauss(ph, 0.55, 0.045, 300)
		v += 15 * valueNoise(seed, "ecg", start.Add(time.Duration(tt*float64(time.Second))), 80*time.Millisecond)
		out[i] = round(v, 3)
	}
	return out
}
