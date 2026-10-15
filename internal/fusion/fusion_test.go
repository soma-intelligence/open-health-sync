// SPDX-License-Identifier: AGPL-3.0-only

package fusion

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderResolvesMostSpecificFirst(t *testing.T) {
	p := NewPolicy(nil, nil)
	assert.Equal(t, Defaults()[GroupActivity], p.Order("step_count"))
	assert.Equal(t, Defaults()[GroupSleep], p.Order(MetricSleep))
	assert.Equal(t, Defaults()[All], p.Order("mindful_session"), "a metric of no group takes the '*' default")

	p = NewPolicy(Priorities{
		All:           {"whoop"},
		GroupActivity: {"oura", "apple_health"},
		"step_count":  {"withings"},
	}, nil)
	assert.Equal(t, []string{"withings"}, p.Order("step_count"), "the metric's own order")
	assert.Equal(t, []string{"oura", "apple_health"}, p.Order("active_energy_burned"), "then the group's")
	assert.Equal(t, []string{"whoop"}, p.Order("heart_rate"), "then the user's '*' before any default")
}

func TestScoresRankOnlyUnorderedMetrics(t *testing.T) {
	scores := map[string]map[string]float64{
		"heart_rate": {"oura": 0.9, "polar": 0.4, "garmin": 0.95},
		"step_count": {"oura": 0.99},
	}
	p := NewPolicy(Priorities{"step_count": {"apple_health"}}, scores)
	assert.Equal(t, []string{"garmin", "oura", "polar", "apple_health", "whoop", "withings"}, p.Order("heart_rate"),
		"scored sources first by score, then the default order")
	assert.Equal(t, []string{"apple_health"}, p.Order("step_count"), "the user's order wins over scores")
}

func TestKeys(t *testing.T) {
	p := NewPolicy(Priorities{"step_count": {"oura"}, All: {"whoop"}}, nil)
	assert.Equal(t, []string{All, "step_count"}, p.Keys())
}

func TestNormalize(t *testing.T) {
	got, err := Priorities{"Step_Count": nil}.Normalize()
	assert.Error(t, err, "a key must be snake_case")
	assert.Nil(t, got)

	got, err = Priorities{"sleep": {" Oura ", "WHOOP"}, All: {"apple_health"}}.Normalize()
	require.NoError(t, err)
	assert.Equal(t, Priorities{"sleep": {"oura", "whoop"}, All: {"apple_health"}}, got)

	for name, p := range map[string]Priorities{
		"empty order": {"sleep": {}},
		"duplicate":   {"sleep": {"oura", "OURA"}},
		"blank":       {"sleep": {" "}},
		"bad key":     {"heart-rate": {"oura"}},
	} {
		_, err := p.Normalize()
		assert.Error(t, err, name)
	}
	long := make([]string, MaxSources+1)
	for i := range long {
		long[i] = string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	_, err = Priorities{All: long}.Normalize()
	assert.Error(t, err, "too many sources")
}

func TestRules(t *testing.T) {
	assert.Equal(t, cumulativeRule, RuleFor("step_count"))
	assert.Equal(t, readingRule, RuleFor("heart_rate"))
	assert.Equal(t, dailyFigureRule, RuleFor("hrv_rmssd"))
	assert.Equal(t, bodyRule, RuleFor("body_mass"))
	assert.Equal(t, sessionRule, RuleFor(MetricSleep))
	assert.Equal(t, sessionRule, RuleFor(MetricWorkouts))
	assert.Equal(t, strictRule, RuleFor("something_new"))
	assert.Equal(t, 5*time.Minute, RuleFor("heart_rate").Tolerance)
	assert.Equal(t, 0.5, RuleFor(MetricSleep).MinShare)
}

func TestEveryGroupHasADefault(t *testing.T) {
	d := Defaults()
	for _, g := range Groups() {
		assert.NotEmpty(t, d[g], g)
	}
	assert.NotEmpty(t, d[All])
	for m := range groupOf {
		assert.True(t, ValidKey(m), m)
	}
}

func TestKnownMetrics(t *testing.T) {
	m := KnownMetrics()
	require.NotEmpty(t, m)
	assert.IsIncreasing(t, m, "sorted, each once")
	for _, metric := range m {
		assert.NotEmpty(t, Group(metric), metric)
	}
	assert.Contains(t, m, "step_count")
	assert.Contains(t, m, MetricSleep)
}
