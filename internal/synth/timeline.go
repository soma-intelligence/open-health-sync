// SPDX-License-Identifier: AGPL-3.0-only

package synth

import (
	"math"
	"sort"
	"time"
)

// Timeline is what an invented person lived: the ground truth every provider
// renderer samples with its own devices, rates and formats.
type Timeline struct {
	Opt  Options
	P    Profile
	Home *time.Location
	Legs []Leg
	Days []Day

	Sleeps   []Sleep
	Workouts []Workout
	Bouts    []Bout // walks outside workouts
	Weighs   []Weigh
	BPs      []BP
	ECGs     []ECG
	Meals    []Meal
	Drinks   []Drink
	Mindful  []Interval
	Flows    []Flow
	// Offline lists the stretches a wearable was off the wrist (Gaps).
	Offline []Interval
	// ExtremeDay is the index of the day of the Extremes scenario, -1 without
	// it.
	ExtremeDay int
	// built says the lists are sorted, so lookups can search them.
	built bool
}

// Interval is a stretch of time.
type Interval struct{ Start, End time.Time }

// Contains reports whether t is in [Start, End).
func (i Interval) Contains(t time.Time) bool { return !t.Before(i.Start) && t.Before(i.End) }

// Duration is End minus Start.
func (i Interval) Duration() time.Duration { return i.End.Sub(i.Start) }

// Day is one local calendar day of the person. Start and End are the UTC
// instants of its local midnights; a day may last 23, 25 or, when the person
// flies, any number of hours, and two days may share a date across the date
// line.
type Day struct {
	Index      int
	Date       string // YYYY-MM-DD, local
	Loc        *time.Location
	Start, End time.Time
	// Missing says nothing was recorded that day (Gaps).
	Missing bool
}

// Interval returns the day as an interval.
func (d Day) Interval() Interval { return Interval{d.Start, d.End} }

// Sleep is one main sleep or nap.
type Sleep struct {
	ID         string
	Day        int // the day the person wakes up on
	Start, End time.Time
	Nap        bool
	Stages     []Stage
	// Figures over the night.
	AvgHR, LowestHR, HRV, Resp, SpO2, TempDelta float64
	Score                                       int
}

// Stage is one sleep stage segment.
type Stage struct {
	Stage      SleepStage
	Start, End time.Time
}

// SleepStage is a sleep stage in the generator's words.
type SleepStage string

// Sleep stages.
const (
	StageAwake SleepStage = "awake"
	StageLight SleepStage = "light"
	StageDeep  SleepStage = "deep"
	StageREM   SleepStage = "rem"
)

// Asleep returns the time spent in a stage other than awake.
func (s Sleep) Asleep() time.Duration {
	var d time.Duration
	for _, st := range s.Stages {
		if st.Stage != StageAwake {
			d += st.End.Sub(st.Start)
		}
	}
	return d
}

// In returns the time spent in a stage.
func (s Sleep) In(stage SleepStage) time.Duration {
	var d time.Duration
	for _, st := range s.Stages {
		if st.Stage == stage {
			d += st.End.Sub(st.Start)
		}
	}
	return d
}

// StageAt returns the stage at t, "" outside the sleep.
func (s Sleep) StageAt(t time.Time) SleepStage {
	for _, st := range s.Stages {
		if !t.Before(st.Start) && t.Before(st.End) {
			return st.Stage
		}
	}
	return ""
}

// Workout is one workout.
type Workout struct {
	ID         string
	Day        int
	Sport      Sport
	Start, End time.Time
	Pauses     []Interval
	DistanceM  float64
	EnergyKcal float64
	AvgHR      float64
	MaxHR      float64
	Steps      float64
	AscentM    float64
	AvgPowerW  float64
	AvgCadence float64
	Indoor     bool
	// Center is the middle of the route, for outdoor sports.
	CenterLat, CenterLon float64
}

// Moving returns the time not paused.
func (w Workout) Moving() time.Duration {
	d := w.End.Sub(w.Start)
	for _, p := range w.Pauses {
		d -= p.End.Sub(p.Start)
	}
	return d
}

// Paused reports whether t falls in a pause.
func (w Workout) Paused(t time.Time) bool {
	for _, p := range w.Pauses {
		if p.Contains(t) {
			return true
		}
	}
	return false
}

// Bout is a walk outside workouts.
type Bout struct {
	Start, End time.Time
	Cadence    float64 // steps per minute
}

// Weigh is a weigh-in on a scale.
type Weigh struct {
	At        time.Time
	WeightKG  float64
	BodyFat   float64 // fraction
	LeanKG    float64
	MuscleKG  float64
	BoneKG    float64
	WaterKG   float64
	BMI       float64
	VisceralF float64
}

// BP is a blood pressure reading.
type BP struct {
	At        time.Time
	Systolic  float64
	Diastolic float64
	Pulse     float64
	Irregular bool
}

// ECG is a single lead recording.
type ECG struct {
	ID             string
	Start          time.Time
	HZ             int
	Classification string // HealthKit's spelling: sinusRhythm, atrialFibrillation, inconclusiveLowHeartRate...
	AvgHR          float64
	MicroVolts     []float64
}

// Meal is a meal with its nutrients.
type Meal struct {
	ID       string
	At       time.Time
	Kind     string // breakfast, lunch, dinner, snack
	Name     string
	EnergyKc float64
	ProteinG float64
	CarbsG   float64
	FatG     float64
	SatFatG  float64
	FiberG   float64
	SugarG   float64
	SodiumMg float64
	CaffMg   float64
}

// Drink is water taken.
type Drink struct {
	At time.Time
	ML float64
}

// Flow is a menstrual flow day.
type Flow struct {
	Day   int
	Start time.Time
	Level string // light, medium, heavy
	First bool   // first day of the cycle
}

// Build lives the timeline of opts.
func Build(opts Options) *Timeline {
	o := opts.withDefaults()
	tl := &Timeline{Opt: o, P: ProfileOf(o.Persona), ExtremeDay: -1}
	tl.Home = mustZone(tl.P.Home)
	tl.legs()
	tl.days()
	if tl.Opt.Now.IsZero() {
		tl.Opt.Now = tl.Days[len(tl.Days)-1].End.Add(48 * time.Hour).UTC()
	}
	r := NewRand(o.Seed, "timeline")
	if o.Has(Gaps) && len(tl.Days) > 3 {
		tl.Days[len(tl.Days)/2].Missing = true
	}
	if o.Has(Extremes) && len(tl.Days) > 2 {
		tl.ExtremeDay = 2
	}
	for i := range tl.Days {
		if tl.Days[i].Missing {
			continue
		}
		tl.sleep(i, r)
		tl.workouts(i, r)
		tl.bouts(i, r)
		tl.body(i, r)
		tl.food(i, r)
	}
	if o.Has(Gaps) {
		for _, d := range tl.Days {
			if d.Index == 1 && !d.Missing {
				tl.Offline = append(tl.Offline, Interval{d.Start.Add(9 * time.Hour), d.Start.Add(15 * time.Hour)})
			}
		}
	}
	sort.Slice(tl.Sleeps, func(i, j int) bool { return tl.Sleeps[i].Start.Before(tl.Sleeps[j].Start) })
	sort.Slice(tl.Workouts, func(i, j int) bool { return tl.Workouts[i].Start.Before(tl.Workouts[j].Start) })
	sort.Slice(tl.Bouts, func(i, j int) bool { return tl.Bouts[i].Start.Before(tl.Bouts[j].Start) })
	tl.built = true
	return tl
}

// Window returns the first and last instants of the timeline.
func (tl *Timeline) Window() Interval {
	return Interval{tl.Days[0].Start.Add(-12 * time.Hour), tl.Days[len(tl.Days)-1].End}
}

func (tl *Timeline) legs() {
	start := time.Date(tl.Opt.Start.Year(), tl.Opt.Start.Month(), tl.Opt.Start.Day(), 0, 0, 0, 0, tl.Home)
	tl.Legs = []Leg{{From: start.Add(-48 * time.Hour), Zone: tl.Home}}
	if !tl.Opt.Has(Travel) {
		return
	}
	// One zone every two days from the second day, flights at 10:00 local.
	at := start.AddDate(0, 0, 1)
	for _, name := range travelZones {
		z := mustZone(name)
		dep := time.Date(at.Year(), at.Month(), at.Day(), 10, 0, 0, 0, tl.ZoneAt(at))
		tl.Legs = append(tl.Legs, Leg{From: dep, Zone: z})
		at = dep.Add(48 * time.Hour)
	}
	dep := at
	tl.Legs = append(tl.Legs, Leg{From: dep, Zone: tl.Home})
}

// days walks local midnights from the start: a flight that changes zone
// during a day ends that day at the next midnight of the new zone.
func (tl *Timeline) days() {
	add := func(from time.Time, n int) {
		s := from
		for k := 0; k < n; k++ {
			loc := tl.ZoneAt(s)
			ls := s.In(loc)
			e := time.Date(ls.Year(), ls.Month(), ls.Day()+1, 0, 0, 0, 0, loc)
			for _, l := range tl.Legs {
				if l.From.After(s) && l.From.Before(e) {
					nl := l.From.In(l.Zone)
					e = time.Date(nl.Year(), nl.Month(), nl.Day()+1, 0, 0, 0, 0, l.Zone)
					if !e.After(l.From) {
						e = e.AddDate(0, 0, 1)
					}
				}
			}
			tl.Days = append(tl.Days, Day{Index: len(tl.Days), Date: ls.Format(time.DateOnly), Loc: loc, Start: s, End: e})
			s = e
		}
	}
	start := time.Date(tl.Opt.Start.Year(), tl.Opt.Start.Month(), tl.Opt.Start.Day(), 0, 0, 0, 0, tl.Home)
	add(start, tl.Opt.Days)
	if !tl.Opt.Has(DST) {
		return
	}
	// Three days around each DST change of the next year not inside the
	// window yet.
	end := tl.Days[len(tl.Days)-1].End
	for _, t := range transitions(tl.Home, start, start.AddDate(1, 0, 0)) {
		if t.Before(end) {
			continue
		}
		lt := t.In(tl.Home)
		from := time.Date(lt.Year(), lt.Month(), lt.Day()-1, 0, 0, 0, 0, tl.Home)
		if from.Before(end) {
			from = end
		}
		add(from, 3)
		end = tl.Days[len(tl.Days)-1].End
	}
}

// at returns local wall time h hours after midnight of the local date of d,
// h may exceed 24. A wall time skipped by a DST change moves forward, as
// time.Date does.
func at(d Day, h float64) time.Time {
	l := d.Start.In(d.Loc)
	hh := int(math.Floor(h))
	mm := int(math.Round((h - float64(hh)) * 60))
	return time.Date(l.Year(), l.Month(), l.Day(), hh, mm, 0, 0, d.Loc)
}

// AtLocal is at for renderers.
func AtLocal(d Day, h float64) time.Time { return at(d, h) }

func (tl *Timeline) sleep(i int, r *Rand) {
	d := tl.Days[i]
	p := tl.P
	if tl.ExtremeDay >= 0 && i == tl.ExtremeDay+1 {
		// The ultra ran through the night: no night, a 10 minute nap.
		tl.Sleeps = append(tl.Sleeps, tl.buildSleep(i, at(d, 14.5), 10.0/60, true, r))
		return
	}
	need := r.Norm(p.SleepH, 0.45)
	if i == tl.ExtremeDay {
		need = 13
	}
	// Bedtime relative to this day's midnight. A night across a DST change
	// lasts its elapsed time, so its wall clock wake time moves.
	bed := p.BedHour - 24 + r.Norm(0, 0.35)
	start := at(d, 0).Add(time.Duration(bed * float64(time.Hour)))
	tl.Sleeps = append(tl.Sleeps, tl.buildSleep(i, start, need, false, r))
}

func (tl *Timeline) buildSleep(i int, start time.Time, needH float64, nap bool, r *Rand) Sleep {
	p := tl.P
	s := Sleep{ID: r.UUID(), Day: i, Start: start.Truncate(30 * time.Second), Nap: nap}
	t := s.Start
	add := func(st SleepStage, d time.Duration) {
		d = d.Round(30 * time.Second)
		if d <= 0 {
			return
		}
		if n := len(s.Stages); n > 0 && s.Stages[n-1].Stage == st {
			s.Stages[n-1].End = s.Stages[n-1].End.Add(d)
		} else {
			s.Stages = append(s.Stages, Stage{st, t, t.Add(d)})
		}
		t = t.Add(d)
	}
	if nap {
		add(StageAwake, 2*time.Minute)
		add(StageLight, time.Duration(needH*float64(time.Hour)))
	} else {
		add(StageAwake, time.Duration(r.Range(4, 22)*float64(time.Minute)))
		asleep := time.Duration(0)
		target := time.Duration(needH * float64(time.Hour))
		for c := 0; asleep < target; c++ {
			cycle := time.Duration(r.Norm(92, 10) * float64(time.Minute))
			deepF := clamp(0.28-0.07*float64(c), 0.02, 0.3)
			remF := clamp(0.1+0.06*float64(c), 0.08, 0.4)
			lightF := 1 - deepF - remF
			add(StageLight, time.Duration(float64(cycle)*lightF*0.55))
			add(StageDeep, time.Duration(float64(cycle)*deepF))
			add(StageLight, time.Duration(float64(cycle)*lightF*0.45))
			add(StageREM, time.Duration(float64(cycle)*remF))
			asleep += cycle
			if r.Chance(0.35) {
				add(StageAwake, time.Duration(r.Range(1, 6)*float64(time.Minute)))
			}
		}
		add(StageAwake, time.Duration(r.Range(2, 12)*float64(time.Minute)))
	}
	s.End = t
	s.LowestHR = round(p.RestingHR-r.Range(3, 7), 0)
	if i == tl.ExtremeDay {
		s.LowestHR = 35
	}
	s.AvgHR = round(p.RestingHR+r.Range(-1, 4), 0)
	s.HRV = round(p.HRVms*r.Range(0.8, 1.2), 0)
	s.Resp = round(r.Range(12.5, 16.5), 1)
	s.SpO2 = round(r.Range(95, 98.5), 1)
	s.TempDelta = round(r.Norm(0, 0.25), 2)
	if p.CycleDays > 0 {
		s.TempDelta = round(tl.cycleTemp(i)+r.Norm(0, 0.1), 2)
	}
	eff := float64(s.Asleep()) / float64(s.End.Sub(s.Start))
	s.Score = int(clamp(math.Round(40+45*eff+5*float64(s.Asleep().Hours()-6)), 1, 100))
	return s
}

// cycleTemp is the wrist temperature shift of a cycle day: about +0.3 C
// after ovulation.
func (tl *Timeline) cycleTemp(i int) float64 {
	if tl.P.CycleDays == 0 {
		return 0
	}
	if i%tl.P.CycleDays >= tl.P.CycleDays/2 {
		return 0.3
	}
	return 0
}

func (tl *Timeline) workouts(i int, r *Rand) {
	d := tl.Days[i]
	p := tl.P
	if i == tl.ExtremeDay {
		// A 20 hour ultra from 14:00, after a 13 hour night.
		tl.Workouts = append(tl.Workouts, tl.buildWorkout(i, SportRun, at(d, 14), 20*time.Hour, r))
		return
	}
	if tl.ExtremeDay >= 0 && i == tl.ExtremeDay+1 {
		return
	}
	if !r.Chance(p.WorkoutsWeek / 7) {
		return
	}
	sport := Pick(r, p.Sports...)
	hour := Pick(r, 7.0, 12.25, 18.0)
	var dur float64 // minutes
	switch sport {
	case SportRun:
		dur = r.Range(28, 70)
	case SportWalk:
		dur = r.Range(30, 80)
	case SportCycle:
		dur = r.Range(45, 150)
	case SportSwim:
		dur = r.Range(30, 60)
	case SportStrength:
		dur = r.Range(40, 70)
	case SportYoga:
		dur = r.Range(30, 60)
	case SportHIIT:
		dur = r.Range(20, 35)
	case SportRow:
		dur = r.Range(20, 45)
	case SportHike:
		dur = r.Range(120, 300)
	default:
		dur = r.Range(20, 60)
	}
	start := at(d, hour).Add(time.Duration(r.Range(0, 40) * float64(time.Minute)))
	tl.Workouts = append(tl.Workouts, tl.buildWorkout(i, sport, start, time.Duration(dur*float64(time.Minute)), r))
	if tl.Opt.Has(MultiDevice) && r.Chance(0.3) && tl.Workouts[len(tl.Workouts)-1].End.Before(at(d, 20)) {
		// A second, short session the same day.
		tl.Workouts = append(tl.Workouts, tl.buildWorkout(i, SportWalk, at(d, 20.5), 25*time.Minute, r))
	}
}

func (tl *Timeline) buildWorkout(i int, sport Sport, start time.Time, dur time.Duration, r *Rand) Workout {
	p := tl.P
	start = start.Truncate(time.Second)
	w := Workout{ID: r.UUID(), Day: i, Sport: sport, Start: start, End: start.Add(dur.Truncate(time.Second))}
	if sport == SportRun || sport == SportCycle || sport == SportHike {
		if dur > 25*time.Minute && r.Chance(0.6) {
			ps := start.Add(time.Duration(r.Range(0.3, 0.7) * float64(dur)))
			w.Pauses = append(w.Pauses, Interval{ps, ps.Add(time.Duration(r.Range(30, 240)) * time.Second)})
		}
	}
	w.Indoor = !sport.Outdoor() || r.Chance(0.15)
	moving := w.Moving().Hours()
	if sport.Moves() {
		v := sport.speed() * r.Range(0.9, 1.1)
		if i == tl.ExtremeDay {
			v = 2.0
		}
		w.DistanceM = round(v*moving*3600, 1)
	}
	inten := sport.intensity()
	w.AvgHR = round(p.RestingHR+inten*(p.MaxHR-p.RestingHR)+r.Norm(0, 3), 0)
	w.MaxHR = round(math.Min(p.MaxHR, w.AvgHR+r.Range(8, 22)), 0)
	if i == tl.ExtremeDay {
		w.MaxHR = 205
	}
	met := 3 + 9*inten
	w.EnergyKcal = round(met*p.WeightKG*moving, 0)
	switch sport {
	case SportRun:
		w.AvgCadence = round(r.Range(160, 176), 0)
		w.Steps = round(w.AvgCadence*moving*60, 0)
	case SportWalk, SportHike:
		w.AvgCadence = round(r.Range(100, 118), 0)
		w.Steps = round(w.AvgCadence*moving*60, 0)
	case SportCycle:
		w.AvgCadence = round(r.Range(78, 92), 0)
		w.AvgPowerW = round(r.Range(140, 230), 0)
	case SportRow:
		w.AvgCadence = round(r.Range(22, 30), 0)
		w.AvgPowerW = round(r.Range(120, 200), 0)
	}
	if sport.Outdoor() && !w.Indoor {
		w.AscentM = round(w.DistanceM*r.Range(0.004, 0.02), 0)
		w.CenterLat, w.CenterLon = center(tl.ZoneAt(start).String())
		w.CenterLat += r.Range(-0.01, 0.01)
		w.CenterLon += r.Range(-0.01, 0.01)
	}
	return w
}

// center returns a public place in or near a zone's city, so a route lies on
// land where the zone is. None of them is anyone's address.
func center(zone string) (float64, float64) {
	switch zone {
	case "America/New_York":
		return 40.7829, -73.9654 // Central Park
	case "America/Chicago":
		return 41.8826, -87.6226 // Grant Park
	case "Europe/London":
		return 51.5073, -0.1657 // Hyde Park
	case "Europe/Berlin":
		return 52.5145, 13.3501 // Tiergarten
	case "Australia/Sydney":
		return -33.8908, 151.2743 // Centennial Park
	case "Asia/Kolkata":
		return 22.5448, 88.3426 // Maidan
	case "Asia/Kathmandu":
		return 27.7172, 85.3240
	case "Pacific/Chatham":
		return -43.9535, -176.5597
	case "Pacific/Kiritimati":
		return 1.8721, -157.4278
	case "Pacific/Honolulu":
		return 21.2690, -157.8120 // Kapiolani Park
	case "Australia/Lord_Howe":
		return -31.5553, 159.0821
	}
	return 48.8462, 2.3371 // Jardin du Luxembourg
}

func (tl *Timeline) bouts(i int, r *Rand) {
	d := tl.Days[i]
	p := tl.P
	// About 2500 steps a day come from pottering about (cadence), the rest
	// from workouts and walks.
	target := r.Norm(p.StepsDay, p.StepsDay*0.15) - 2500
	for _, w := range tl.Workouts {
		if w.Day == i {
			target -= w.Steps
		}
	}
	// Fixed walks, then short random ones until the day's target.
	hours := []float64{8.3, 12.5, 17.8}
	if p.Persona == PersonaNightShift {
		hours = []float64{17.5, 21.0, 2.5 + 24}
	}
	var got float64
	add := func(h, mins float64) {
		s := at(d, h)
		if h >= 24 {
			s = at(d, 0).Add(time.Duration(h * float64(time.Hour)))
		}
		b := Bout{Start: s.Truncate(time.Minute), Cadence: round(r.Range(95, 118), 0)}
		b.End = b.Start.Add(time.Duration(mins * float64(time.Minute)))
		if tl.asleep(b.Start) || tl.inWorkout(b.Start) || tl.inWorkout(b.End) {
			return
		}
		tl.Bouts = append(tl.Bouts, b)
		got += b.Cadence * mins
	}
	for _, h := range hours {
		add(h+r.Range(-0.2, 0.2), r.Range(8, 20))
	}
	for n := 0; got < target && n < 60; n++ {
		add(r.Range(7.5, 21.5), r.Range(2, 12))
	}
}

func (tl *Timeline) body(i int, r *Rand) {
	d := tl.Days[i]
	p := tl.P
	trend := 0.6 * valueNoise(tl.Opt.Seed, "weight", d.Start, 10*24*time.Hour)
	if r.Chance(0.85) {
		wkg := round(p.WeightKG+trend+r.Norm(0, 0.25), 2)
		fat := clamp(p.BodyFat+r.Norm(0, 0.006), 0.04, 0.6)
		bmi := wkg / math.Pow(p.HeightCM/100, 2)
		w := Weigh{At: at(d, 7.15).Add(time.Duration(r.Range(0, 600)) * time.Second), WeightKG: wkg, BodyFat: round(fat, 3),
			LeanKG: round(wkg*(1-fat), 2), BoneKG: round(wkg*0.04, 2), WaterKG: round(wkg*(1-fat)*0.73, 2), BMI: round(bmi, 1), VisceralF: round(clamp(fat*30, 1, 20), 0)}
		w.MuscleKG = round(w.LeanKG-w.BoneKG, 2)
		tl.Weighs = append(tl.Weighs, w)
	}
	bp := func(h float64) {
		s := r.Norm(p.Systolic, 6)
		di := r.Norm(p.Diastolic, 4)
		tl.BPs = append(tl.BPs, BP{At: at(d, h).Add(time.Duration(r.Range(0, 900)) * time.Second), Systolic: round(s, 0), Diastolic: round(di, 0),
			Pulse: round(p.RestingHR+r.Range(2, 10), 0), Irregular: r.Chance(0.03)})
	}
	if p.BPTwiceDaily {
		bp(7.5)
		bp(21)
	} else if i%7 == 3 {
		bp(8)
	}
	if i%4 == 1 {
		e := ECG{ID: r.UUID(), Start: at(d, r.Range(10, 20)).Truncate(time.Second), HZ: 512, Classification: "sinusRhythm", AvgHR: round(p.RestingHR+r.Range(5, 15), 0)}
		if i == tl.ExtremeDay {
			e.Classification = "inconclusiveHighHeartRate"
			e.AvgHR = 128
		}
		e.MicroVolts = ecgTrace(tl.Opt.Seed, e.Start, e.HZ, 30, e.AvgHR)
		tl.ECGs = append(tl.ECGs, e)
	}
	if r.Chance(0.5) {
		s := at(d, r.Range(12, 22)).Truncate(time.Minute)
		tl.Mindful = append(tl.Mindful, Interval{s, s.Add(time.Duration(Pick(r, 5, 10, 15)) * time.Minute)})
	}
	if p.CycleDays > 0 {
		cd := i % p.CycleDays
		if cd < 5 {
			lvl := []string{"medium", "heavy", "heavy", "medium", "light"}[cd]
			tl.Flows = append(tl.Flows, Flow{Day: i, Start: at(d, 0), Level: lvl, First: cd == 0})
		}
	}
}

func (tl *Timeline) food(i int, r *Rand) {
	d := tl.Days[i]
	kinds := []struct {
		kind  string
		h     float64
		kcal  float64
		names []string
	}{
		{"breakfast", 7.6, 450, []string{"Oatmeal with berries", "Eggs and toast", "Yogurt and granola"}},
		{"lunch", 12.6, 700, []string{"Chicken salad", "Lentil soup", "Rice bowl"}},
		{"snack", 16.2, 200, []string{"Apple", "Almonds", "Banana"}},
		{"dinner", 19.5, 750, []string{"Salmon and potatoes", "Pasta with vegetables", "Tofu stir fry"}},
	}
	for _, k := range kinds {
		if k.kind == "snack" && r.Chance(0.4) {
			continue
		}
		kc := round(k.kcal*r.Range(0.75, 1.25), 0)
		carbs := round(kc*r.Range(0.4, 0.55)/4, 1)
		prot := round(kc*r.Range(0.15, 0.25)/4, 1)
		fat := round((kc-carbs*4-prot*4)/9, 1)
		m := Meal{ID: r.UUID(), At: at(d, k.h).Add(time.Duration(r.Range(0, 1800)) * time.Second).Truncate(time.Minute), Kind: k.kind,
			Name: Pick(r, k.names...), EnergyKc: kc, ProteinG: prot, CarbsG: carbs, FatG: math.Max(fat, 0), SatFatG: round(math.Max(fat, 0)*0.3, 1),
			FiberG: round(carbs*0.08, 1), SugarG: round(carbs*0.25, 1), SodiumMg: round(kc*1.3, 0)}
		if k.kind == "breakfast" {
			m.CaffMg = 95
		}
		tl.Meals = append(tl.Meals, m)
	}
	for n := 0; n < 6+r.Intn(4); n++ {
		tl.Drinks = append(tl.Drinks, Drink{At: at(d, r.Range(7.5, 21.5)).Truncate(time.Minute), ML: Pick(r, 200.0, 250, 330, 500)})
	}
}

// asleep reports whether the person sleeps at t.
func (tl *Timeline) asleep(t time.Time) bool { _, ok := tl.SleepAt(t); return ok }

// SleepAt returns the sleep holding t.
func (tl *Timeline) SleepAt(t time.Time) (Sleep, bool) {
	if i, ok := find(len(tl.Sleeps), tl.built, t, func(i int) (time.Time, time.Time) { return tl.Sleeps[i].Start, tl.Sleeps[i].End }); ok {
		return tl.Sleeps[i], true
	}
	return Sleep{}, false
}

func (tl *Timeline) inWorkout(t time.Time) bool { _, ok := tl.WorkoutAt(t); return ok }

// WorkoutAt returns the workout holding t.
func (tl *Timeline) WorkoutAt(t time.Time) (Workout, bool) {
	if i, ok := find(len(tl.Workouts), tl.built, t, func(i int) (time.Time, time.Time) { return tl.Workouts[i].Start, tl.Workouts[i].End }); ok {
		return tl.Workouts[i], true
	}
	return Workout{}, false
}

// boutAt returns the walk holding t.
func (tl *Timeline) boutAt(t time.Time) (Bout, bool) {
	if i, ok := find(len(tl.Bouts), tl.built, t, func(i int) (time.Time, time.Time) { return tl.Bouts[i].Start, tl.Bouts[i].End }); ok {
		return tl.Bouts[i], true
	}
	return Bout{}, false
}

// find returns the index of the interval holding t among n non-overlapping
// intervals, by binary search once they are sorted, by a scan before.
func find(n int, sorted bool, t time.Time, iv func(int) (time.Time, time.Time)) (int, bool) {
	if !sorted {
		for i := 0; i < n; i++ {
			if s, e := iv(i); !t.Before(s) && t.Before(e) {
				return i, true
			}
		}
		return 0, false
	}
	i := sort.Search(n, func(i int) bool { _, e := iv(i); return e.After(t) })
	for ; i < n; i++ {
		s, e := iv(i)
		if s.After(t) {
			break
		}
		if t.Before(e) {
			return i, true
		}
	}
	return 0, false
}

// Worn reports whether a wrist device records at t (Gaps takes it off).
func (tl *Timeline) Worn(t time.Time) bool {
	for _, o := range tl.Offline {
		if o.Contains(t) {
			return false
		}
	}
	for _, d := range tl.Days {
		if d.Missing && d.Interval().Contains(t) {
			return false
		}
	}
	return true
}

// DayOf returns the day holding t.
func (tl *Timeline) DayOf(t time.Time) (Day, bool) {
	for _, d := range tl.Days {
		if d.Interval().Contains(t) {
			return d, true
		}
	}
	return Day{}, false
}
