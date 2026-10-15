// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package queue

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func enqueuePullID(t *testing.T, q *Pull, j PullJob) (int64, bool) {
	t.Helper()
	var id int64
	var created bool
	inTx(t, q.pool, func(tx pgx.Tx) error {
		var err error
		id, created, err = EnqueuePullID(context.Background(), tx, j)
		return err
	})
	return id, created
}

func TestEnqueuePullIDReturnsTheCoveringJob(t *testing.T) {
	pool := testPool(t)
	q := NewPull(pool)
	j := pullJob(1)

	id, created := enqueuePullID(t, q, j)
	assert.True(t, created)
	assert.Positive(t, id)

	again, created := enqueuePullID(t, q, j)
	assert.False(t, created, "the same window is already pending")
	assert.Equal(t, id, again)

	// A data type makes it another job.
	typed := j
	typed.DataType = "sleep"
	other, created := enqueuePullID(t, q, typed)
	assert.True(t, created)
	assert.NotEqual(t, id, other)

	// Running still covers the window; done no longer does.
	got, err := q.Claim(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	running, created := enqueuePullID(t, q, j)
	assert.False(t, created)
	assert.Equal(t, id, running)
	for _, c := range got {
		require.NoError(t, q.Complete(context.Background(), c))
	}
	fresh, created := enqueuePullID(t, q, j)
	assert.True(t, created, "a finished job does not hold the window")
	assert.NotEqual(t, id, fresh)
}
