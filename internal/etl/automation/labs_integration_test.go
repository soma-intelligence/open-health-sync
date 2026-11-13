// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package automation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Retention prunes lab results by their collection time, like any other
// observation: an old one goes, a recent one stays, whatever their upload
// time.
func TestRetentionPrunesLabResults(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	insert := func(ts time.Time, ext string) {
		e.exec(`INSERT INTO ohs_data.observations (tenant_id, user_id, provider, ts, canonical_code, value_num,
				content_hash, external_id, report_id, captured_at)
			VALUES ($1, $2, 'labs', $3, 'lab:loinc:2093-3', 5.1, $4, $4, 'r-1', $5)`, e.tenant, e.user, ts, ext, now)
	}
	insert(now.AddDate(0, 0, -400), "old")
	insert(now.AddDate(0, 0, -3), "new")
	_, err := (&Retention{Pool: e.pool, Store: e.rs, Days: 90, KeepChunks: true, Now: func() time.Time { return now }}).Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, e.scalar(`SELECT count(*) FROM ohs_data.observations WHERE external_id = 'old'`))
	require.Equal(t, 1, e.scalar(`SELECT count(*) FROM ohs_data.observations WHERE external_id = 'new'`))
}
