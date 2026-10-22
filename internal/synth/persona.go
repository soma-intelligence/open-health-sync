// SPDX-License-Identifier: AGPL-3.0-only

package synth

import "fmt"

// Persona is an invented person. The figures are round values from public
// physiology references, not anyone's data.
type Persona string

// Personas.
const (
	PersonaActive       Persona = "active"       // runs and cycles, sleeps well, watch and ring
	PersonaSedentary    Persona = "sedentary"    // few steps, short nights, heavier
	PersonaNightShift   Persona = "night_shift"  // sleeps by day, nights across midnight the other way
	PersonaDiabetic     Persona = "diabetic"     // type 1, wears a CGM, logs meals
	PersonaHypertensive Persona = "hypertensive" // takes blood pressure twice a day
	PersonaCycle        Persona = "cycle"        // tracks a menstrual cycle and wrist temperature
)

// Personas lists every persona.
var Personas = []Persona{PersonaActive, PersonaSedentary, PersonaNightShift, PersonaDiabetic, PersonaHypertensive, PersonaCycle}

// ParsePersona returns the persona of a name.
func ParsePersona(name string) (Persona, error) {
	for _, p := range Personas {
		if string(p) == name {
			return p, nil
		}
	}
	return "", fmt.Errorf("synth: unknown persona %q", name)
}

// Profile is what a persona's body is like.
type Profile struct {
	Persona   Persona
	Name      string // invented display name
	Sex       string // "female", "male"
	BirthDate string // YYYY-MM-DD
	HeightCM  float64
	WeightKG  float64
	BodyFat   float64 // fraction
	RestingHR float64
	MaxHR     float64
	HRVms     float64 // nightly RMSSD
	VO2Max    float64 // mL/kg/min
	// SleepH is the usual time asleep, BedHour the usual local bedtime (24 may
	// exceed 24: 24.5 is half past midnight).
	SleepH  float64
	BedHour float64
	// StepsDay is the usual daily step count.
	StepsDay float64
	// Workouts per week and the sports the person does.
	WorkoutsWeek float64
	Sports       []Sport
	Systolic     float64
	Diastolic    float64
	// GlucoseMgdl is the fasting glucose; CGM says the person wears one.
	GlucoseMgdl float64
	CGM         bool
	// CycleDays is the menstrual cycle length, zero when not tracked.
	CycleDays int
	// Home is the IANA zone the person lives in.
	Home string
	// BPTwiceDaily says blood pressure is taken morning and evening.
	BPTwiceDaily bool
}

// ProfileOf returns the profile of a persona.
func ProfileOf(p Persona) Profile {
	switch p {
	case PersonaSedentary:
		return Profile{Persona: p, Name: "Sam Example", Sex: "male", BirthDate: "1968-06-15", HeightCM: 176, WeightKG: 98, BodyFat: 0.31,
			RestingHR: 72, MaxHR: 165, HRVms: 24, VO2Max: 29, SleepH: 6.1, BedHour: 23.75, StepsDay: 3500, WorkoutsWeek: 1,
			Sports: []Sport{SportWalk}, Systolic: 132, Diastolic: 85, GlucoseMgdl: 101, Home: "America/Chicago"}
	case PersonaNightShift:
		return Profile{Persona: p, Name: "Noa Example", Sex: "female", BirthDate: "1990-02-01", HeightCM: 168, WeightKG: 64, BodyFat: 0.27,
			RestingHR: 62, MaxHR: 185, HRVms: 45, VO2Max: 38, SleepH: 6.8, BedHour: 32.5, StepsDay: 9000, WorkoutsWeek: 2,
			Sports: []Sport{SportStrength, SportYoga, SportWalk}, Systolic: 114, Diastolic: 74, GlucoseMgdl: 88, Home: "Europe/London"}
	case PersonaDiabetic:
		return Profile{Persona: p, Name: "Dee Example", Sex: "female", BirthDate: "1995-09-30", HeightCM: 163, WeightKG: 58, BodyFat: 0.26,
			RestingHR: 66, MaxHR: 190, HRVms: 48, VO2Max: 39, SleepH: 7.4, BedHour: 23, StepsDay: 8000, WorkoutsWeek: 3,
			Sports: []Sport{SportRun, SportSwim, SportHIIT}, Systolic: 112, Diastolic: 72, GlucoseMgdl: 140, CGM: true, Home: "Europe/Berlin"}
	case PersonaHypertensive:
		return Profile{Persona: p, Name: "Hal Example", Sex: "male", BirthDate: "1958-11-11", HeightCM: 181, WeightKG: 91, BodyFat: 0.28,
			RestingHR: 70, MaxHR: 155, HRVms: 22, VO2Max: 27, SleepH: 6.6, BedHour: 22.5, StepsDay: 6000, WorkoutsWeek: 2,
			Sports: []Sport{SportWalk, SportCycle}, Systolic: 148, Diastolic: 94, GlucoseMgdl: 104, BPTwiceDaily: true, Home: "America/New_York"}
	case PersonaCycle:
		return Profile{Persona: p, Name: "Mia Example", Sex: "female", BirthDate: "1998-04-20", HeightCM: 170, WeightKG: 61, BodyFat: 0.24,
			RestingHR: 58, MaxHR: 192, HRVms: 62, VO2Max: 44, SleepH: 7.8, BedHour: 23.25, StepsDay: 11000, WorkoutsWeek: 4,
			Sports: []Sport{SportRun, SportYoga, SportRow, SportHike}, Systolic: 108, Diastolic: 68, GlucoseMgdl: 86, CycleDays: 28, Home: "Australia/Sydney"}
	default:
		return Profile{Persona: PersonaActive, Name: "Alex Example", Sex: "male", BirthDate: "1987-03-12", HeightCM: 180, WeightKG: 74, BodyFat: 0.16,
			RestingHR: 52, MaxHR: 188, HRVms: 70, VO2Max: 52, SleepH: 7.5, BedHour: 23, StepsDay: 12000, WorkoutsWeek: 5,
			Sports: []Sport{SportRun, SportCycle, SportSwim, SportStrength, SportWalk}, Systolic: 118, Diastolic: 76, GlucoseMgdl: 90, Home: "Europe/Paris"}
	}
}

// Sport is a workout kind in the generator's own words; renderers translate
// it to each vendor's enum.
type Sport string

// Sports.
const (
	SportRun      Sport = "run"
	SportWalk     Sport = "walk"
	SportCycle    Sport = "cycle"
	SportSwim     Sport = "swim"
	SportStrength Sport = "strength"
	SportYoga     Sport = "yoga"
	SportHIIT     Sport = "hiit"
	SportRow      Sport = "row"
	SportHike     Sport = "hike"
	SportOther    Sport = "other"
)

// Sports lists every sport.
var Sports = []Sport{SportRun, SportWalk, SportCycle, SportSwim, SportStrength, SportYoga, SportHIIT, SportRow, SportHike, SportOther}

// Moves says the sport covers a distance.
func (s Sport) Moves() bool {
	switch s {
	case SportRun, SportWalk, SportCycle, SportSwim, SportRow, SportHike:
		return true
	}
	return false
}

// Outdoor says the sport records a GPS route.
func (s Sport) Outdoor() bool {
	switch s {
	case SportRun, SportWalk, SportCycle, SportHike:
		return true
	}
	return false
}

// speed is a typical speed in m/s, intensity the fraction of heart rate
// reserve.
func (s Sport) speed() float64 {
	switch s {
	case SportRun:
		return 3.0
	case SportWalk:
		return 1.4
	case SportCycle:
		return 7.0
	case SportSwim:
		return 0.75
	case SportRow:
		return 3.6
	case SportHike:
		return 1.1
	}
	return 0
}

func (s Sport) intensity() float64 {
	switch s {
	case SportRun, SportHIIT, SportRow:
		return 0.75
	case SportCycle, SportSwim:
		return 0.65
	case SportStrength, SportHike:
		return 0.5
	case SportWalk:
		return 0.35
	case SportYoga:
		return 0.25
	}
	return 0.4
}
