// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package consent_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/consent"
	"github.com/soma-intelligence/open-health-sync/internal/db"
)

// Revoking labs purges the user's lab results of that source, whatever the
// analyte (a lab glucose is a lab result, not glucose data); revoking
// glucose leaves them.
func TestPurgeLabs(t *testing.T) {
	ctx := context.Background()
	pool, err := db.Open(ctx, db.TestDB(t))
	require.NoError(t, err)
	defer pool.Close()
	ten, user := uuid.New(), uuid.New()
	at := time.Date(2026, 3, 4, 8, 30, 0, 0, time.UTC)
	insert := func(provider, code, hash string) {
		_, err := pool.Exec(ctx, `INSERT INTO ohs_data.observations (tenant_id, user_id, provider, ts, canonical_code,
				value_num, content_hash, external_id, report_id, captured_at)
			VALUES ($1, $2, $3, $4, $5, 1, $6, $6, 'r-1', now())`, ten, user, provider, at, code, hash)
		require.NoError(t, err)
	}
	insert("labs", "lab:loinc:2345-7", "glucose")
	insert("labs", "lab:local:K", "potassium")
	insert("other_ehr", "lab:loinc:2345-7", "other-glucose")
	labs := func(provider string) int {
		return count(t, pool, `SELECT count(*) FROM ohs_data.observations
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND canonical_code LIKE 'lab:%'`, ten, user, provider)
	}
	drain := func() {
		purger := consent.NewPurger(pool)
		for {
			did, err := purger.Once(ctx)
			require.NoError(t, err)
			if !did {
				return
			}
		}
	}

	set(t, pool, ten, user, "labs", consent.Labs, consent.Glucose)
	drain()
	set(t, pool, ten, user, "labs", consent.Labs)
	drain()
	assert.Equal(t, 2, labs("labs"), "revoking glucose leaves the lab results")

	set(t, pool, ten, user, "labs", consent.Glucose)
	drain()
	assert.Zero(t, labs("labs"), "labs revoked")
	assert.Equal(t, 1, labs("other_ehr"), "another source is untouched")
	var deleted int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT rows_deleted FROM ohs_sync.consent_purges
		WHERE user_id = $1 AND provider = 'labs' AND category = 'labs'`, user).Scan(&deleted))
	assert.Equal(t, int64(2), deleted)
}
