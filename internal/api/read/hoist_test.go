// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingDB answers every QueryRow with one true and counts them.
type countingDB struct{ rows int }

type trueRow struct{}

func (trueRow) Scan(dest ...any) error {
	*dest[0].(*bool) = true
	return nil
}

func (c *countingDB) QueryRow(context.Context, string, ...any) pgx.Row {
	c.rows++
	return trueRow{}
}

func (c *countingDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("not used")
}

// Under HoistLookups the user and the type are looked up once however many
// pages are read; without it, once per page, as before.
func TestHoistLookupsAsksOnce(t *testing.T) {
	tenantID, user, other := uuid.New(), uuid.New(), uuid.New()
	db := &countingDB{}
	ctx := context.Background()
	for range 3 {
		require.NoError(t, userExists(ctx, db, tenantID, user))
		require.NoError(t, knownQuantityType(ctx, db, tenantID, user, "heart_rate"))
	}
	assert.Equal(t, 6, db.rows, "no hoisting: every page asks")

	db = &countingDB{}
	hctx := HoistLookups(ctx)
	for range 3 {
		require.NoError(t, userExists(hctx, db, tenantID, user))
		require.NoError(t, knownQuantityType(hctx, db, tenantID, user, "heart_rate"))
	}
	assert.Equal(t, 2, db.rows, "hoisted: once for the user, once for the type")
	require.NoError(t, knownQuantityType(hctx, db, tenantID, user, "step_count"))
	assert.Equal(t, 3, db.rows, "another type is asked")

	// The answers are for the first user read under the context only.
	require.NoError(t, userExists(hctx, db, tenantID, other))
	require.NoError(t, userExists(hctx, db, tenantID, other))
	assert.Equal(t, 5, db.rows)

	// CheckUser on the caller's own context always asks.
	require.NoError(t, CheckUser(ctx, db, tenantID, user))
	assert.Equal(t, 6, db.rows)
}
