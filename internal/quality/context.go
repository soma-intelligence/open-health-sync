// SPDX-License-Identifier: AGPL-3.0-only

package quality

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
