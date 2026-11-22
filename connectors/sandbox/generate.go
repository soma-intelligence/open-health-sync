// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/demo"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// The sandbox API's wire format: what Fetch stores as raw lines and Map
// reads. It is the sandbox's own, documented in docs/connectors/sandbox.md.

// Sleep is one night, its stages listed.
type Sleep struct {
	ID              string   `json:"id"`
	Persona         string   `json:"persona"`
	Start           string   `json:"start"`
	End             string   `json:"end"`
	Timezone        string   `json:"timezone,omitempty"`
	TotalSleepS     *int     `json:"total_sleep_s,omitempty"`
	DeepS           *int     `json:"deep_s,omitempty"`
	REMS            *int     `json:"rem_s,omitempty"`
	LightS          *int     `json:"light_s,omitempty"`
	AwakeS          *int     `json:"awake_s,omitempty"`
	Score           *float64 `json:"score,omitempty"`
	HRVms           *float64 `json:"hrv_ms,omitempty"`
	RestingHR       *float64 `json:"resting_hr,omitempty"`
	RespiratoryRate *float64 `json:"respiratory_rate,omitempty"`
	Stages          []Stage  `json:"stages"`
}

// Stage is one sleep stage segment: awake, light, deep or rem.
type Stage struct {
	Stage string `json:"stage"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// HeartRate is one heart rate reading.
type HeartRate struct {
	Persona  string  `json:"persona"`
	Time     string  `json:"time"`
	BPM      float64 `json:"bpm"`
	Timezone string  `json:"timezone,omitempty"`
}

// Steps is the step count of one interval.
type Steps struct {
	Persona  string  `json:"persona"`
	Start    string  `json:"start"`
	End      string  `json:"end"`
	Count    float64 `json:"count"`
	Timezone string  `json:"timezone,omitempty"`
}

// Workout is one workout.
type Workout struct {
	ID         string   `json:"id"`
	Persona    string   `json:"persona"`
	Sport      string   `json:"sport"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	DurationS  *int     `json:"duration_s,omitempty"`
	DistanceM  *float64 `json:"distance_m,omitempty"`
	EnergyKcal *float64 `json:"energy_kcal,omitempty"`
	AvgHR      *float64 `json:"avg_hr,omitempty"`
	MaxHR      *float64 `json:"max_hr,omitempty"`
	Timezone   string   `json:"timezone,omitempty"`
}

// dataset is what the sandbox serves for one persona over one window.
type dataset struct {
	Sleep     []Sleep
	HeartRate []HeartRate
	Steps     []Steps
	Workouts  []Workout
}

// maxWindowDays bounds the days one request can generate.
const maxWindowDays = demo.MaxDays

// generate builds the persona's data that starts in [from, to), from the demo
// generator (internal/demo): the same persona and dates give the same data on
// every call and every instance, whatever the window, so a pull read again
// stores nothing new. Only complete days up to yesterday (in the persona's
// zone, at now) exist. Of the providers a persona is seen through, each kind
// of record comes from the first that has it, so nothing is served twice.
func generate(persona string, from, to, now time.Time) (dataset, error) {
	pi, ok := demo.LookupPersona(persona)
	if !ok {
		return dataset{}, fmt.Errorf("sandbox: unknown persona %q", persona)
	}
	loc, err := time.LoadLocation(demo.DefaultZone)
	if err != nil {
		return dataset{}, err
	}
	if to.After(now) {
		to = now
	}
	if !from.Before(to) {
		return dataset{}, nil
	}
	// Today is the day after the last generated day: the day after to's,
	// at most today. A night that starts the evening before from is
	// generated on from's day and filtered out by its start.
	day := func(t time.Time) time.Time {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	today := day(to).AddDate(0, 0, 1)
	if nowDay := day(now); today.After(nowDay) {
		today = nowDay
	}
	first := day(from)
	days := 0
	for d := first; d.Before(today); d = d.AddDate(0, 0, 1) {
		days++
	}
	if days < 1 {
		return dataset{}, nil
	}
	if days > maxWindowDays {
		return dataset{}, fmt.Errorf("sandbox: a window holds at most %d days", maxWindowDays)
	}
	batches, err := demo.Generate(demo.Options{Days: days, Today: today, Seed: pi.Seed, Location: loc, Persona: pi.Name})
	if err != nil {
		return dataset{}, err
	}
	in := func(t time.Time) bool { return !t.Before(from) && t.Before(to) }
	tz := loc.String()
	var out dataset
	var haveSleep, haveHR, haveSteps, haveWorkouts bool
	for _, b := range batches {
		mr := b.Records
		if !haveSleep && len(mr.SleepSessions) > 0 {
			haveSleep = true
			for _, s := range mr.SleepSessions {
				if !in(s.BedtimeStart) {
					continue
				}
				out.Sleep = append(out.Sleep, sleepOf(pi.Name, tz, s, mr.SleepPhases))
			}
		}
		var hr, steps []model.QuantitySample
		for _, q := range mr.QuantitySamples {
			switch q.QuantityType {
			case model.QTHeartRate:
				hr = append(hr, q)
			case model.QTStepCount:
				steps = append(steps, q)
			}
		}
		if !haveHR && len(hr) > 0 {
			haveHR = true
			for _, q := range hr {
				if in(q.StartDate) {
					out.HeartRate = append(out.HeartRate, HeartRate{Persona: pi.Name, Time: stamp(q.StartDate), BPM: q.Value, Timezone: tz})
				}
			}
		}
		if !haveSteps && len(steps) > 0 {
			haveSteps = true
			for _, q := range steps {
				if in(q.StartDate) {
					out.Steps = append(out.Steps, Steps{Persona: pi.Name, Start: stamp(q.StartDate), End: stamp(q.EndDate), Count: q.Value, Timezone: tz})
				}
			}
		}
		if !haveWorkouts && len(mr.Workouts) > 0 {
			haveWorkouts = true
			for _, w := range mr.Workouts {
				if !in(w.StartDate) {
					continue
				}
				out.Workouts = append(out.Workouts, Workout{
					ID: recordID(pi.Name, "workout", w.StartDate), Persona: pi.Name, Sport: w.ActivityType,
					Start: stamp(w.StartDate), End: stamp(w.EndDate), DurationS: w.DurationS,
					DistanceM: w.DistanceM, EnergyKcal: w.ActiveEnergyKcal, AvgHR: w.AvgHeartRate, MaxHR: w.MaxHeartRate,
					Timezone: tz,
				})
			}
		}
	}
	return out, nil
}

// sleepOf renders one demo night with its phases.
func sleepOf(persona, tz string, s model.SleepSession, phases []model.SleepPhase) Sleep {
	out := Sleep{
		ID: recordID(persona, "sleep", s.BedtimeStart), Persona: persona,
		Start: stamp(s.BedtimeStart), End: stamp(s.BedtimeEnd), Timezone: tz,
		TotalSleepS: s.TotalSleepSeconds, DeepS: s.DeepSleepSeconds, REMS: s.REMSleepSeconds,
		LightS: s.LightSleepSeconds, AwakeS: s.AwakeSeconds, Score: s.SleepScore,
		HRVms: s.HRVAvgMs, RestingHR: s.RestingHR, RespiratoryRate: s.RespiratoryRate,
		Stages: []Stage{},
	}
	for _, p := range phases {
		if p.ParentContentHash == s.ContentHash {
			out.Stages = append(out.Stages, Stage{Stage: p.Phase, Start: stamp(p.StartDate), End: stamp(p.EndDate)})
		}
	}
	return out
}

// recordID is the stable id of one record: the same persona, kind and start
// give the same id on every instance.
func recordID(persona, kind string, start time.Time) string {
	sum := sha256.Sum256([]byte(persona + "|" + kind + "|" + start.UTC().Format(time.RFC3339)))
	return "sbx-" + kind + "-" + hex.EncodeToString(sum[:8])
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
