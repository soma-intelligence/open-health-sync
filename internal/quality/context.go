// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"time"
)

// Context is the situation a figure was measured in. Wearable error depends
// on it more than on anything else: a wrist heart rate is a couple of bpm off
// at rest and several times that while running.
type Context string

// Contexts.
const (
	ContextRest     Context = "rest"
	ContextExercise Context = "exercise"
	ContextSleep    Context = "sleep"
	ContextDay      Context = "day" // awake, moving about, not exercising
	ContextUnknown  Context = "unknown"
)

// Thresholds of the context classifier. Steps are per minute, motion is the
// mean acceleration magnitude above gravity (ENMO) in g over the minute.
const (
	exerciseCadence = 100  // brisk walking and above
	dayCadence      = 10   // a few steps: moving about
	exerciseMotion  = 0.1  // vigorous wrist movement
	restMotion      = 0.02 // still
	exerciseHRR     = 0.4  // fraction of heart rate reserve
	restHRMargin    = 15   // bpm above resting
	defaultMaxHR    = 190
)

// ContextInput is what is known around a figure. A NaN means unknown;
// RestingHR and MaxHR may be zero when unknown.
type ContextInput struct {
	HR, RestingHR, MaxHR float64
	Steps                float64 // per minute
	Motion               float64 // ENMO, g
	Asleep               bool    // inside a sleep window of any source
	InWorkout            bool    // inside a recorded workout
}

// ClassifyContext puts a figure in rest, exercise, sleep or day from the
// heart rate, the steps and the motion around it, and the sleep and workout
// windows. Sleep and workouts win; then intensity (cadence, motion, or heart
// rate reserve when the wrist barely moves, as on a bike) says exercise;
// stillness near the resting heart rate says rest; the rest is day.
func ClassifyContext(c ContextInput) Context {
	known := func(v float64) bool { return !math.IsNaN(v) }
	switch {
	case c.Asleep:
		return ContextSleep
	case c.InWorkout:
		return ContextExercise
	}
	if !known(c.HR) && !known(c.Steps) && !known(c.Motion) {
		return ContextUnknown
	}
	if known(c.Steps) && c.Steps >= exerciseCadence {
		return ContextExercise
	}
	if known(c.Motion) && c.Motion >= exerciseMotion {
		return ContextExercise
	}
	if known(c.HR) && c.RestingHR > 0 {
		maxHR := c.MaxHR
		if maxHR <= c.RestingHR {
			maxHR = defaultMaxHR
		}
		if (c.HR-c.RestingHR)/(maxHR-c.RestingHR) >= exerciseHRR {
			return ContextExercise
		}
	}
	still := (!known(c.Steps) || c.Steps < dayCadence) && (!known(c.Motion) || c.Motion < restMotion)
	calm := !known(c.HR) || c.RestingHR <= 0 || c.HR <= c.RestingHR+restHRMargin
	if still && calm {
		return ContextRest
	}
	return ContextDay
}

// Window is a stretch of time, a sleep or a workout.
type Window struct{ Start, End time.Time }

// InWindows reports whether t falls in one of the windows, [Start, End).
func InWindows(t time.Time, ws []Window) bool {
	for _, w := range ws {
		if !t.Before(w.Start) && t.Before(w.End) {
			return true
		}
	}
	return false
}
