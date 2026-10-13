// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// One builder per provider: each reads the truth of a day through its own
// sensor and appends that day's records to mr. Coverage overlaps on purpose:
// heart rate comes from five sources, sleep from two, steps from three,
// weight and workouts from two.

func ptr[T any](v T) *T { return &v }

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// every calls fn at each step from start (included) to end (excluded).
func every(start, end time.Time, step time.Duration, fn func(time.Time)) {
	for t := start; t.Before(end); t = t.Add(step) {
		fn(t)
	}
}

// heartRate appends the heart rate samples a sensor reads every step over
// [start, end), skipping the instants keep refuses.
func (g *gen) heartRate(mr *model.MapResult, r *rand.Rand, provider string, start, end time.Time, step time.Duration, sd float64, keep func(time.Time) bool) {
	every(start, end, step, func(t time.Time) {
		if keep != nil && !keep(t) {
			return
		}
		v := math.Round(clamp(g.hrAt(t)+norm(r, 0, sd), 35, 200))
		mr.QuantitySamples = append(mr.QuantitySamples, g.sample(provider, model.QTHeartRate, v, "bpm", t, t, nil))
	})
}

// sleep builds the session of the night ending on d, as a sensor whose clock
// is shift off, with its phases. The caller fills the provider's own figures.
func (g *gen) sleep(provider string, d *day, shift time.Duration) (model.SleepSession, []model.SleepPhase) {
	start, end := d.sleepStart.Add(shift), d.sleepEnd.Add(shift)
	s := model.SleepSession{
		Provider:     provider,
		ExternalID:   externalID(provider, "sleep", d.date),
		BedtimeStart: start.UTC(),
		BedtimeEnd:   end.UTC(),
		Metadata:     g.metadata(),
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"demo":          "1",
		"provider":      provider,
		"external_id":   s.ExternalID,
		"bedtime_start": s.BedtimeStart.Format(time.RFC3339),
	})
	totals := map[string]int{}
	phases := make([]model.SleepPhase, 0, len(d.phases))
	for _, p := range d.phases {
		ps, pe := p.start.Add(shift).UTC(), p.end.Add(shift).UTC()
		dur := int(pe.Sub(ps).Seconds())
		totals[p.stage] += dur
		phases = append(phases, model.SleepPhase{
			Phase: p.stage, StartDate: ps, EndDate: pe, DurationS: dur, ParentContentHash: s.ContentHash,
		})
	}
	s.DeepSleepSeconds = ptr(totals[model.SleepStageDeep])
	s.REMSleepSeconds = ptr(totals[model.SleepStageREM])
	s.LightSleepSeconds = ptr(totals[model.SleepStageLight])
	s.AwakeSeconds = ptr(totals[model.SleepStageAwake])
	s.TotalSleepSeconds = ptr(totals[model.SleepStageDeep] + totals[model.SleepStageREM] + totals[model.SleepStageLight])
	return s, phases
}

// sleepScore rates a night from its length and deep and REM shares.
func sleepScore(s model.SleepSession) float64 {
	total := float64(*s.TotalSleepSeconds)
	if total <= 0 {
		return 0
	}
	hours := total / 3600
	restorative := float64(*s.DeepSleepSeconds+*s.REMSleepSeconds) / total
	return math.Round(clamp(52+6*(hours-6)+40*(restorative-0.35), 35, 98))
}

// workoutKcal is the active energy of a workout.
func (g *gen) workoutKcal(w *workoutPlan) float64 {
	return w.minutes() * (3.5 + 9*w.intensity) * g.p.weight / 70
}

// oura: a ring, worn at night. Sleep with stages, night heart rate, resting
// heart rate, HRV, breathing, temperature deviation, and a daily summary
// with its scores.
func (g *gen) oura(d *day, mr *model.MapResult) {
	r := g.dayRNG(Oura, d.date)
	s, phases := g.sleep(Oura, d, time.Duration(r.IntN(5)-2)*time.Minute)
	start, end := s.BedtimeStart, s.BedtimeEnd
	s.RestingHR = ptr(math.Round(d.restingHR - 1))
	s.HRVAvgMs = ptr(math.Round(d.hrv + norm(r, 0, 2)))
	s.RespiratoryRate = ptr(round(d.resp+norm(r, 0, 0.2), 1))
	s.SleepScore = ptr(sleepScore(s))
	mr.SleepSessions = append(mr.SleepSessions, s)
	mr.SleepPhases = append(mr.SleepPhases, phases...)

	g.heartRate(mr, r, Oura, start, end, 10*time.Minute, 1.5, nil)
	mr.QuantitySamples = append(mr.QuantitySamples,
		g.sample(Oura, model.QTRestingHeartRate, *s.RestingHR, "bpm", start, end, nil),
		g.sample(Oura, model.QTHRVRMSSD, *s.HRVAvgMs, "ms", start, end, nil),
		g.sample(Oura, model.QTRespiratoryRate, *s.RespiratoryRate, "bpm", start, end, nil),
		g.sample(Oura, model.QTBodyTemperatureDeviation, round(d.tempDev, 2), "celsius", start, end, nil),
	)

	steps := 0.0
	for _, v := range d.steps {
		steps += v
	}
	steps = math.Round(steps * norm(r, 0.95, 0.03))
	active := math.Round(steps * 0.04)
	if d.workout != nil {
		active += math.Round(g.workoutKcal(d.workout) * 0.8)
	}
	sum := model.DailySummary{
		Provider:       Oura,
		SummaryDate:    dateOnlyUTC(d.date),
		ReadinessScore: ptr(math.Round(clamp(74+10*d.trend+(d.hrv-g.p.hrv)/2-(d.restingHR-g.p.restingHR), 40, 99))),
		SleepScore:     s.SleepScore,
		ActivityScore:  ptr(math.Round(clamp(55+30*steps/g.p.stepGoal/1.4, 30, 99))),
		StepCount:      ptr(int(steps)),
		ActiveKcal:     ptr(active),
		RestingHR:      s.RestingHR,
		HRVAvgMs:       s.HRVAvgMs,
		Metadata:       g.metadata(),
	}
	sum.ContentHash = model.ComputeContentHash(map[string]string{
		"demo": "1", "provider": Oura, "summary_date": sum.SummaryDate.Format(time.DateOnly), "kind": "daily",
	})
	mr.DailySummaries = append(mr.DailySummaries, sum)
}

// whoop: a strap, worn day and night. Heart rate around the clock, sleep with
// stages and SpO2, HRV, recovery and strain, and the workouts it detects.
func (g *gen) whoop(d *day, mr *model.MapResult) {
	r := g.dayRNG(Whoop, d.date)
	g.heartRate(mr, r, Whoop, d.date, d.date.AddDate(0, 0, 1), 10*time.Minute, 2, nil)

	s, phases := g.sleep(Whoop, d, time.Duration(r.IntN(7)-3)*time.Minute)
	s.RestingHR = ptr(math.Round(d.restingHR + norm(r, 0, 0.7)))
	s.HRVAvgMs = ptr(round(d.hrv+norm(r, 0, 3), 1))
	s.RespiratoryRate = ptr(round(d.resp+norm(r, 0, 0.3), 1))
	s.SpO2Avg = ptr(round(d.spo2+norm(r, 0, 0.4), 1))
	s.SleepScore = ptr(math.Round(clamp(sleepScore(s)+norm(r, 3, 3), 30, 100)))
	mr.SleepSessions = append(mr.SleepSessions, s)
	mr.SleepPhases = append(mr.SleepPhases, phases...)
	mr.QuantitySamples = append(mr.QuantitySamples,
		g.sample(Whoop, model.QTHRVRMSSD, *s.HRVAvgMs, "ms", s.BedtimeStart, s.BedtimeEnd, nil),
		g.sample(Whoop, model.QTRestingHeartRate, *s.RestingHR, "bpm", s.BedtimeStart, s.BedtimeEnd, nil),
	)

	strain := 6 + 3*r.Float64()
	if w := d.workout; w != nil {
		strain += w.minutes() / 60 * (4 + 8*w.intensity)
		if w.kind != "walking" {
			ws := w.start.Add(time.Duration(1+r.IntN(3)) * time.Minute)
			we := w.end.Add(time.Duration(r.IntN(4)-1) * time.Minute)
			avg, max := g.workoutHR(w, r)
			wo := model.Workout{
				Provider:         Whoop,
				ExternalID:       externalID(Whoop, "workout", d.date),
				ActivityType:     w.kind,
				StartDate:        ws.UTC(),
				EndDate:          we.UTC(),
				DurationS:        ptr(int(we.Sub(ws).Seconds())),
				ActiveEnergyKcal: ptr(math.Round(g.workoutKcal(w) * norm(r, 1.05, 0.05))),
				AvgHeartRate:     ptr(avg + 1),
				MaxHeartRate:     ptr(max),
				Metadata:         g.metadata(),
			}
			wo.ContentHash = model.ComputeContentHash(map[string]string{
				"demo": "1", "provider": Whoop, "external_id": wo.ExternalID,
			})
			mr.Workouts = append(mr.Workouts, wo)
		}
	}
	sum := model.DailySummary{
		Provider:      Whoop,
		SummaryDate:   dateOnlyUTC(d.date),
		RecoveryScore: ptr(math.Round(clamp(60+25*d.trend+1.5*(d.hrv-g.p.hrv), 5, 99))),
		StrainScore:   ptr(round(clamp(strain, 0, 21), 1)),
		RestingHR:     s.RestingHR,
		HRVAvgMs:      s.HRVAvgMs,
		Metadata:      g.metadata(),
	}
	sum.ContentHash = model.ComputeContentHash(map[string]string{
		"demo": "1", "provider": Whoop, "summary_date": sum.SummaryDate.Format(time.DateOnly), "kind": "cycle",
	})
	mr.DailySummaries = append(mr.DailySummaries, sum)
}

// workoutHR is the average and maximum heart rate of a workout, read every
// minute.
func (g *gen) workoutHR(w *workoutPlan, r *rand.Rand) (avg, max float64) {
	n := 0.0
	every(w.start, w.end, time.Minute, func(t time.Time) {
		v := g.hrAt(t)
		avg += v
		max = math.Max(max, v)
		n++
	})
	return math.Round(avg / n), math.Round(max + 3 + 4*r.Float64())
}

// withings: a scale most mornings, a blood pressure cuff on Sundays.
func (g *gen) withings(d *day, mr *model.MapResult) {
	r := g.dayRNG(Withings, d.date)
	if r.Float64() < 0.75 {
		at := d.sleepEnd.Add(time.Duration(10+r.IntN(25)) * time.Minute)
		weight := round(d.weight, 2)
		fat := round(d.bodyFat, 1)
		mr.QuantitySamples = append(mr.QuantitySamples,
			g.sample(Withings, "body_mass", weight, "kg", at, at, nil),
			g.sample(Withings, "body_fat_percentage", fat, "percent", at, at, nil),
			g.sample(Withings, "lean_body_mass", round(weight*(1-fat/100), 2), "kg", at, at, nil),
			g.sample(Withings, "body_mass_index", round(weight/(g.p.height*g.p.height), 1), "kg/m2", at, at, nil),
		)
	}
	if d.date.Weekday() == time.Sunday {
		at := time.Date(d.date.Year(), d.date.Month(), d.date.Day(), 9, 0, 0, 0, g.loc).
			Add(time.Duration(r.IntN(40)) * time.Minute)
		mr.QuantitySamples = append(mr.QuantitySamples,
			g.sample(Withings, "blood_pressure_systolic", math.Round(norm(r, 118, 5)), "mmHg", at, at, nil),
			g.sample(Withings, "blood_pressure_diastolic", math.Round(norm(r, 76, 4)), "mmHg", at, at, nil),
			g.sample(Withings, model.QTHeartRate, math.Round(d.restingHR+norm(r, 9, 2)), "bpm", at, at, nil),
		)
	}
}

// fitbit: a band worn in the day. Hourly steps, distance, active energy and
// floors, heart rate every 15 minutes, a daily resting heart rate, and the
// night's SpO2.
func (g *gen) fitbit(d *day, mr *model.MapResult) {
	r := g.dayRNG(Fitbit, d.date)
	for h, steps := range d.steps {
		start := time.Date(d.date.Year(), d.date.Month(), d.date.Day(), h, 0, 0, 0, g.loc)
		end := start.Add(time.Hour)
		// Energy: the workout's minutes of this hour, plus the other steps.
		kcal, other := 0.0, steps
		if w := d.workout; w != nil {
			mins := w.minutesIn(g.loc, h)
			kcal = mins * (3.5 + 9*w.intensity) * g.p.weight / 70
			other = math.Max(0, steps-w.cadence()*mins)
		}
		if steps == 0 && kcal == 0 {
			continue
		}
		n := math.Round(steps * norm(r, 1, 0.03))
		kcal = math.Round(kcal + other*0.042)
		if n > 0 {
			mr.QuantitySamples = append(mr.QuantitySamples,
				g.sample(Fitbit, model.QTStepCount, n, "count", start, end, nil),
				g.sample(Fitbit, model.QTDistanceWalkingRunning, round(n*0.76, 1), "m", start, end, nil),
			)
		}
		if kcal > 0 {
			mr.QuantitySamples = append(mr.QuantitySamples,
				g.sample(Fitbit, model.QTActiveEnergyBurned, kcal, "kcal", start, end, nil))
		}
		if n > 700 && r.Float64() < 0.5 {
			mr.QuantitySamples = append(mr.QuantitySamples,
				g.sample(Fitbit, model.QTFlightsClimbed, float64(1+r.IntN(3)), "count", start, end, nil))
		}
	}
	day0 := d.date
	g.heartRate(mr, r, Fitbit, day0.Add(7*time.Hour), day0.Add(23*time.Hour), 15*time.Minute, 2.5,
		func(t time.Time) bool { return g.stageAt(t) == "" })
	mr.QuantitySamples = append(mr.QuantitySamples,
		g.sample(Fitbit, model.QTRestingHeartRate, math.Round(d.restingHR+norm(r, 1, 0.8)), "bpm",
			day0, day0.AddDate(0, 0, 1), nil),
		g.sample(Fitbit, model.QTOxygenSaturation, round(d.spo2+norm(r, -0.3, 0.3), 1), "percent",
			d.sleepStart, d.sleepEnd, nil),
	)
}

// polar: a sports watch, worn for workouts. Workouts with distance, heart
// rate, energy and a GPS route, heart rate every minute while training, and
// a VO2 max estimate after each run.
func (g *gen) polar(d *day, mr *model.MapResult) {
	w := d.workout
	if w == nil {
		return
	}
	r := g.dayRNG(Polar, d.date)
	avg, max := g.workoutHR(w, r)
	wo := model.Workout{
		Provider:         Polar,
		ExternalID:       externalID(Polar, "exercise", d.date),
		ActivityType:     w.kind,
		StartDate:        w.start.UTC(),
		EndDate:          w.end.UTC(),
		DurationS:        ptr(int(w.end.Sub(w.start).Seconds())),
		ActiveEnergyKcal: ptr(math.Round(g.workoutKcal(w))),
		AvgHeartRate:     ptr(avg),
		MaxHeartRate:     ptr(max),
		Metadata:         g.metadata(),
	}
	if w.speed > 0 {
		wo.DistanceM = ptr(math.Round(w.distance()))
		wo.AvgSpeedMps = ptr(round(w.speed, 2))
		wo.ElevationGainM = ptr(math.Round(w.elevation))
	}
	vo2 := round(g.p.vo2max+1.5*d.trend, 1)
	if w.kind == "running" {
		wo.VO2Max = ptr(vo2)
	}
	wo.ContentHash = model.ComputeContentHash(map[string]string{
		"demo": "1", "provider": Polar, "external_id": wo.ExternalID,
	})
	mr.Workouts = append(mr.Workouts, wo)
	if w.speed > 0 {
		mr.RoutePoints = append(mr.RoutePoints, g.route(w, wo.ContentHash)...)
	}
	g.heartRate(mr, r, Polar, w.start, w.end, time.Minute, 1, nil)
	if w.kind == "running" {
		mr.QuantitySamples = append(mr.QuantitySamples, g.sample(Polar, "vo2_max", vo2, "mL/kg/min", w.end, w.end, nil))
	}
}

// route is a loop around the person's park, one point every 30 seconds.
func (g *gen) route(w *workoutPlan, parent string) []model.RoutePoint {
	radius := 600.0 // m
	if w.kind == "cycling" {
		radius = 2500
	}
	const mPerDegLat = 111320.0
	lat0, lon0 := g.p.home[0], g.p.home[1]
	mPerDegLon := mPerDegLat * math.Cos(lat0*math.Pi/180)
	var out []model.RoutePoint
	every(w.start, w.end.Add(time.Second), 30*time.Second, func(t time.Time) {
		angle := w.speed * t.Sub(w.start).Seconds() / radius
		alt := round(35+8*math.Sin(2*angle), 1)
		out = append(out, model.RoutePoint{
			Timestamp:         t.UTC(),
			Latitude:          round(lat0+radius*math.Sin(angle)/mPerDegLat, 6),
			Longitude:         round(lon0+radius*(1-math.Cos(angle))/mPerDegLon, 6),
			AltitudeM:         ptr(alt),
			SpeedMps:          ptr(round(w.speed, 2)),
			ParentContentHash: parent,
		})
	})
	return out
}

// appleHealth: the phone in a pocket. Hourly steps, distance and floors (it
// stays home during runs), and a weight typed in by hand on Sundays.
func (g *gen) appleHealth(d *day, mr *model.MapResult) {
	r := g.dayRNG(AppleHealth, d.date)
	carried := norm(r, 0.82, 0.05)
	for h, steps := range d.steps {
		start := time.Date(d.date.Year(), d.date.Month(), d.date.Day(), h, 0, 0, 0, g.loc)
		if w := d.workout; w != nil && w.kind == "running" {
			steps -= w.cadence() * w.minutesIn(g.loc, h)
		}
		n := math.Round(steps * carried)
		if n <= 0 {
			continue
		}
		end := start.Add(time.Hour)
		mr.QuantitySamples = append(mr.QuantitySamples,
			g.sample(AppleHealth, model.QTStepCount, n, "count", start, end, nil),
			g.sample(AppleHealth, model.QTDistanceWalkingRunning, round(n*0.72, 1), "m", start, end, nil),
		)
		if n > 600 && r.Float64() < 0.4 {
			mr.QuantitySamples = append(mr.QuantitySamples,
				g.sample(AppleHealth, model.QTFlightsClimbed, float64(1+r.IntN(3)), "count", start, end, nil))
		}
	}
	if d.date.Weekday() == time.Sunday {
		at := time.Date(d.date.Year(), d.date.Month(), d.date.Day(), 10, 30, 0, 0, g.loc)
		s := g.sample(AppleHealth, "body_mass", round(d.weight+0.4, 1), "kg", at, at, nil)
		s.WasUserEntered = ptr(true)
		mr.QuantitySamples = append(mr.QuantitySamples, s)
	}
}
