// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestScrubText(t *testing.T) {
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	name := "Watch\x00 7"
	mr := &MapResult{
		QuantitySamples: []QuantitySample{{
			QuantityType: "heart_rate", Value: 72, StartDate: at,
			SourceName: "Wat\x00ch", DeviceModel: "bad \xff utf8",
			Metadata: map[string]any{"k\x00ey": "v\x00al", "nested": map[string]any{"list": []any{"a\x00", 1.5}}},
		}},
		Workouts:      []Workout{{StartDate: at, ExternalID: name}},
		NutritionLogs: []NutritionLog{{StartDate: at, NutrientsJSON: map[string]any{"iron\x00": 2.0}}},
		ECGSamples:    make([]ECGVoltageSample, 3),
	}
	ScrubText(mr)
	s := mr.QuantitySamples[0]
	assert.Equal(t, "Watch", s.SourceName)
	assert.Equal(t, "bad � utf8", s.DeviceModel)
	assert.Equal(t, map[string]any{"key": "val", "nested": map[string]any{"list": []any{"a", 1.5}}}, s.Metadata)
	assert.Equal(t, "Watch 7", mr.Workouts[0].ExternalID)
	assert.Equal(t, map[string]any{"iron": 2.0}, mr.NutritionLogs[0].NutrientsJSON)
	assert.Equal(t, "Watch\x00 7", name, "the input string is not changed in place")

	clean := &MapResult{QuantitySamples: []QuantitySample{{SourceName: "Watch", Metadata: map[string]any{"a": "b"}}}}
	ScrubText(clean)
	assert.Equal(t, "Watch", clean.QuantitySamples[0].SourceName)
	ScrubText(nil)
}

func TestScrubJSON(t *testing.T) {
	in := map[string]any{"a\x00": []any{"x\x00y", map[string]any{"b": "\x00"}}, "n": 3.0}
	out := ScrubJSON(in)
	assert.Equal(t, map[string]any{"a": []any{"xy", map[string]any{"b": ""}}, "n": 3.0}, out)
	assert.Contains(t, in, "a\x00", "the input is left as it was")
}
