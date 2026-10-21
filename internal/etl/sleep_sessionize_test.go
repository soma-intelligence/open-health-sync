// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func mkPhase(phase string, start time.Time, durMin int) model.SleepPhase {
	return model.SleepPhase{
		UserID:    "u1",
		Phase:     phase,
		StartDate: start,
		EndDate:   start.Add(time.Duration(durMin) * time.Minute),
		DurationS: durMin * 60,
	}
}

// Stage segments sent back to back are one night; a gap over an hour starts
// another one.
func TestGroupSleepNights_GapStartsANight(t *testing.T) {
	base := time.Date(2026, 6, 25, 22, 0, 0, 0, time.UTC)
	stages := []model.SleepPhase{
		mkPhase(model.SleepStageDeep, base.Add(30*time.Minute), 20), // out of order on purpose
		mkPhase(model.SleepStageLight, base, 30),
		mkPhase(model.SleepStageREM, base.Add(110*time.Minute), 10), // a 60 minute gap: same night
		mkPhase(model.SleepStageLight, base.Add(181*time.Minute), 10),
	}
	nights := groupSleepNights(stages, nil)
	require.Len(t, nights, 2)
	assert.Len(t, nights[0].stages, 3)
	assert.True(t, nights[0].start.Equal(base))
	assert.True(t, nights[0].end.Equal(base.Add(120*time.Minute)))
	assert.Len(t, nights[1].stages, 1)
	assert.True(t, nights[0].fresh && nights[1].fresh)
}

// Sessions join the night of the stages they reach, a session built from
// stages apart from those with a time in bed. A night of stored sessions alone
// is left as it is, and a session listed twice counts once.
func TestGroupSleepNights_Sessions(t *testing.T) {
	base := time.Date(2026, 6, 25, 22, 0, 0, 0, time.UTC)
	inBed := nightSession{id: 1, start: base, end: base.Add(8 * time.Hour), inBatch: true}
	built := nightSession{id: 2, start: base.Add(9 * time.Hour), end: base.Add(9*time.Hour + 30*time.Minute), synthesized: true}
	other := nightSession{id: 3, start: base.Add(-24 * time.Hour), end: base.Add(-16 * time.Hour)}
	stages := []model.SleepPhase{mkPhase(model.SleepStageLight, base.Add(time.Hour), 30)}

	nights := groupSleepNights(stages, []nightSession{inBed, other, {id: 1, start: base, end: base.Add(time.Hour)}, built})
	require.Len(t, nights, 2)
	assert.False(t, nights[0].fresh, "stored only")
	assert.Equal(t, []nightSession{other}, nights[0].anchors)

	n := nights[1]
	assert.True(t, n.fresh)
	assert.Equal(t, []nightSession{inBed}, n.anchors, "the batch's listing of session 1")
	assert.Equal(t, []nightSession{built}, n.built, "an hour after the time in bed: same night")
	assert.Len(t, n.stages, 1)
	assert.True(t, n.end.Equal(built.end))
}

// A stage goes to the session that contains it, within the tolerance, or
// else to the closest one.
func TestNearestSession(t *testing.T) {
	base := time.Date(2026, 6, 25, 22, 0, 0, 0, time.UTC)
	sessions := []nightSession{
		{id: 1, start: base, end: base.Add(3 * time.Hour)},
		{id: 2, start: base.Add(3*time.Hour + 20*time.Minute), end: base.Add(8 * time.Hour)},
	}
	for _, tc := range []struct {
		name  string
		phase model.SleepPhase
		want  int
	}{
		{"inside the first", mkPhase(model.SleepStageDeep, base.Add(time.Hour), 30), 0},
		{"inside the second", mkPhase(model.SleepStageREM, base.Add(4*time.Hour), 30), 1},
		{"starts just before, within the tolerance", mkPhase(model.SleepStageLight, base.Add(-4*time.Minute), 30), 0},
		{"overlaps both, contained by none", mkPhase(model.SleepStageLight, base.Add(170*time.Minute), 40), 0},
		{"in the gap, closer to the second", mkPhase(model.SleepStageAwake, base.Add(3*time.Hour+6*time.Minute), 10), 1},
		{"after both", mkPhase(model.SleepStageLight, base.Add(9*time.Hour), 10), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nearestSession(sessions, tc.phase))
		})
	}
}
