// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package deletion_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Lab results (provider labs, rows of ohs_data.observations) go with a
// deletion of the labs source, and with the erasure of the user; another
// source's observations stay through the first.
func TestLabResultsGoWithTheirSourceAndTheUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "a")
	at := time.Date(2026, 3, 4, 8, 30, 0, 0, time.UTC)
	lab := func(user uuid.UUID, provider, code, ext string) {
		e.exec(t, `INSERT INTO ohs_data.observations (tenant_id, user_id, provider, source_slug, ts, canonical_code,
				value_num, unit_ucum, type_concept, content_hash, external_id, report_id, captured_at)
			VALUES ($1, $2, $3, $3, $4, $5, 6.3, 'mmol/L', 'ehr', $6, $6, 'report-1', now())`,
			e.tenant.ID, user, provider, at, code, ext)
	}
	lab(a.id, "labs", "lab:loinc:2093-3", "chol-1")
	lab(a.id, "labs", "lab:local:K", "k-1")
	lab(a.id, other, "invented_code", "x-1")
	count := func(provider string) (n int) {
		require.NoError(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_data.observations
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, e.tenant.ID, a.id, provider).Scan(&n))
		return n
	}
	require.Equal(t, 2, count("labs"))

	jobOf(t, e.requestProviderDelete(t, a.id, "labs"))
	n, err := e.worker(nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Zero(t, count("labs"), "the lab results of the source are erased")
	assert.Equal(t, 1, count(other), "another source's observation stays")

	lab(a.id, "labs", "lab:loinc:2093-3", "chol-2")
	rec := e.requestDelete(t, a.id)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	_, err = e.worker(&fakeRevoker{}).Pass(ctx)
	require.NoError(t, err)
	assert.Empty(t, e.rowsFor(t, a.id), "the user's lab results go with the user")
}
