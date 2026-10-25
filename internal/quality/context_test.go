// SPDX-License-Identifier: AGPL-3.0-only

package quality

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClassifyContext(t *testing.T) {
	nan := math.NaN()
	cases := []struct {
		name string
		in   ContextInput
		want Context
	}{
		{"asleep wins", ContextInput{HR: 50, RestingHR: 55, Steps: 0, Motion: nan, Asleep: true}, ContextSleep},
		{"workout", ContextInput{HR: 90, RestingHR: 55, Steps: nan, Motion: nan, InWorkout: true}, ContextExercise},
		{"running cadence", ContextInput{HR: 150, RestingHR: 55, Steps: 160, Motion: nan}, ContextExercise},
		{"cycling: high HR, no steps", ContextInput{HR: 140, RestingHR: 55, Steps: 0, Motion: 0.02}, ContextExercise},
		{"strong motion", ContextInput{HR: nan, Steps: nan, Motion: 0.25}, ContextExercise},
		{"sitting", ContextInput{HR: 62, RestingHR: 55, Steps: 0, Motion: 0.005}, ContextRest},
		{"walking about", ContextInput{HR: 85, RestingHR: 55, Steps: 40, Motion: nan}, ContextDay},
		{"nothing known", ContextInput{HR: nan, Steps: nan, Motion: nan}, ContextUnknown},
		{"HR only, near rest", ContextInput{HR: 60, RestingHR: 58, Steps: nan, Motion: nan}, ContextRest},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, ClassifyContext(c.in), c.name)
	}
}

func TestInWindows(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	w := []Window{{t0, t0.Add(8 * time.Hour)}}
	assert.True(t, InWindows(t0.Add(time.Hour), w))
	assert.False(t, InWindows(t0.Add(-time.Minute), w))
	assert.False(t, InWindows(t0.Add(8*time.Hour), w))
}
