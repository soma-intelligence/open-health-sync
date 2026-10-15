// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package fusion

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/db"
)

type scorer struct {
	scores map[string]map[string]float64
	err    error
}

func (s scorer) SourceScores(context.Context, uuid.UUID, uuid.UUID) (map[string]map[string]float64, error) {
	return s.scores, s.err
}

func TestSaveLoadPriorities(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	ten, user, other := uuid.New(), uuid.New(), uuid.New()

	got, err := LoadPriorities(ctx, pool, ten, user)
	require.NoError(t, err)
	assert.Empty(t, got, "no order set")

	set := Priorities{MetricSleep: {"oura", "apple_health"}, All: {"whoop"}}
	require.NoError(t, SavePriorities(ctx, pool, ten, user, set))
	require.NoError(t, SavePriorities(ctx, pool, ten, other, Priorities{All: {"garmin"}}))
	got, err = LoadPriorities(ctx, pool, ten, user)
	require.NoError(t, err)
	assert.Equal(t, set, got)

	// A save replaces every order; an empty one goes back to the defaults.
	require.NoError(t, SavePriorities(ctx, pool, ten, user, Priorities{"step_count": {"withings"}}))
	got, err = LoadPriorities(ctx, pool, ten, user)
	require.NoError(t, err)
	assert.Equal(t, Priorities{"step_count": {"withings"}}, got)
	require.NoError(t, SavePriorities(ctx, pool, ten, user, Priorities{}))
	got, err = LoadPriorities(ctx, pool, ten, user)
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = LoadPriorities(ctx, pool, ten, other)
	require.NoError(t, err)
	assert.Equal(t, Priorities{All: {"garmin"}}, got, "another user's orders untouched")
	got, err = LoadPriorities(ctx, pool, uuid.New(), other)
	require.NoError(t, err)
	assert.Empty(t, got, "scoped by tenant")

	// A refused row leaves the previous orders in place.
	err = SavePriorities(ctx, pool, ten, other, Priorities{All: {"oura"}, "heart_rate": {}})
	require.Error(t, err, "an empty order breaks the table's check")
	got, err = LoadPriorities(ctx, pool, ten, other)
	require.NoError(t, err)
	assert.Equal(t, Priorities{All: {"garmin"}}, got, "the failed save was rolled back")
}

func TestLoadPolicy(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	ten, user := uuid.New(), uuid.New()
	require.NoError(t, SavePriorities(ctx, pool, ten, user, Priorities{"step_count": {"withings"}}))

	p, err := Load(ctx, pool, ten, user, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"withings"}, p.Order("step_count"))
	assert.Equal(t, Defaults()[GroupSleep], p.Order(MetricSleep))

	p, err = Load(ctx, pool, ten, user, scorer{scores: map[string]map[string]float64{"heart_rate": {"polar": 0.99}}})
	require.NoError(t, err)
	assert.Equal(t, "polar", p.Order("heart_rate")[0], "scores rank a metric the user did not order")

	boom := errors.New("scores unavailable")
	p, err = Load(ctx, pool, ten, user, scorer{err: boom})
	assert.ErrorIs(t, err, ErrScores)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, []string{"withings"}, p.Order("step_count"), "the policy is still usable")
	assert.Equal(t, Defaults()[All], p.Order("mindful_session"))
}
