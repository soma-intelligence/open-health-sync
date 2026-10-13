// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func syntheticBatch(day time.Time) *model.MapResult {
	md := map[string]any{"timezone": "Europe/Paris", "synthetic": true}
	sess := model.SleepSession{
		ExternalID: "demo-sleep", BedtimeStart: day.Add(-2 * time.Hour), BedtimeEnd: day.Add(6 * time.Hour),
		ContentHash: "sleep-hash", Metadata: md,
	}
	mr := &model.MapResult{
		SleepSessions: []model.SleepSession{sess},
		SleepPhases: []model.SleepPhase{
			{Phase: "light", StartDate: sess.BedtimeStart, EndDate: day, DurationS: 7200, ParentContentHash: "sleep-hash"},
			{Phase: "deep", StartDate: day, EndDate: sess.BedtimeEnd, DurationS: 21600, ParentContentHash: "sleep-hash"},
		},
	}
	for h := 8; h < 12; h++ {
		at := day.Add(time.Duration(h) * time.Hour)
		mr.QuantitySamples = append(mr.QuantitySamples, model.QuantitySample{
			QuantityType: model.QTStepCount, Value: 1000, Unit: "count", StartDate: at, EndDate: at.Add(time.Hour),
			ContentHash: "steps-" + at.Format(time.RFC3339), Metadata: md,
		})
	}
	return mr
}

func TestWriteSyntheticIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	day := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	c, err := WriteSynthetic(ctx, e.pool, e.tenant, e.user, "fitbit", syntheticBatch(day))
	require.NoError(t, err)
	require.Equal(t, 4, c[tQuantity].Written)
	require.Equal(t, 1, c[tSleep].Written)
	require.Equal(t, 2, c[tPhases].Written)
	require.Equal(t, 1, c[tDaily].Written, "the derived daily row")
	require.Equal(t, 4, e.count(tQuantity))
	require.Equal(t, 1, e.count(tDaily))

	var steps int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT step_count FROM ohs_data.daily_summaries
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'fitbit'`, e.tenant, e.user).Scan(&steps))
	require.Equal(t, 4000, steps)
	var coverage int
	require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.pull_coverage
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'fitbit'`, e.tenant, e.user).Scan(&coverage))
	require.Positive(t, coverage)

	// The same records again: nothing new.
	c, err = WriteSynthetic(ctx, e.pool, e.tenant, e.user, "fitbit", syntheticBatch(day))
	require.NoError(t, err)
	require.Zero(t, c.TotalWritten(), "second write: %v", c)
	require.Equal(t, 4, e.count(tQuantity))
	require.Equal(t, 1, e.count(tSleep))
	require.Equal(t, 2, e.count(tPhases))
	require.Equal(t, 1, e.count(tDaily))

	// Another user of the tenant is untouched.
	other := e.newUser(e.tenant)
	require.Zero(t, e.countFor(tQuantity, e.tenant, other))
}

func TestWriteSyntheticRefusesInactiveUser(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, e.user)
	_, err := WriteSynthetic(context.Background(), e.pool, e.tenant, e.user, "fitbit",
		syntheticBatch(time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)))
	require.True(t, errors.Is(err, ErrUserNotActive), "got %v", err)
	require.Zero(t, e.count(tQuantity))
}
