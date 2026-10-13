// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/deletion"
)

func acceptedJob(t *testing.T, code int, body []byte) uuid.UUID {
	t.Helper()
	require.Equal(t, http.StatusAccepted, code, "%s", body)
	var resp struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	id, err := uuid.Parse(resp.JobID)
	require.NoError(t, err)
	return id
}

// A disconnect with delete_data=true revokes like a plain one, queues the
// erasure of the provider's data and answers 202; the provider cannot be
// connected again until the job is done, which removes what was pulled.
func TestDisconnectWithDeleteDataErasesTheProvidersData(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	require.Positive(t, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA))
	e.start(t, e.keyA, e.userA, "") // a consent link handed out and never used
	path := "/v1/users/" + e.userA.String() + "/connections/" + fakeSlug

	assert.Equal(t, http.StatusBadRequest, e.do(t, http.MethodDelete, path+"?delete_data=yes", e.keyA, nil).Code)
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA), "a refused request changes nothing")

	rec := e.do(t, http.MethodDelete, path+"?delete_data=true", e.keyA, nil)
	job := acceptedJob(t, rec.Code, rec.Body.Bytes())
	require.Len(t, e.fake.revokedTokens(), 1, "the grant is revoked as on a plain disconnect")
	assert.Equal(t, collect.StatusDisconnected, e.connStatus(t, e.userA))
	assert.Zero(t, e.scalar(t, `SELECT count(*) FROM ohs_sync.oauth_states WHERE user_id = $1`, e.userA))
	assert.Equal(t, 1, e.scalar(t, `SELECT count(*) FROM ohs_core.deletion_jobs
		WHERE id = $1 AND provider = $2 AND status = 'pending'`, job, fakeSlug))

	// Asking again returns the same job; reconnecting waits for it.
	rec = e.do(t, http.MethodDelete, path+"?delete_data=true", e.keyA, nil)
	assert.Equal(t, job, acceptedJob(t, rec.Code, rec.Body.Bytes()))
	rec = e.do(t, http.MethodPost, path, e.keyA, []byte(`{}`))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	n, err := deletion.NewWorker(e.pool, e.raw, nil).Pass(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	for _, tbl := range []string{"ohs_sync.connections", "ohs_sync.raw_objects", "ohs_sync.etl_jobs",
		"ohs_sync.pull_jobs", "ohs_sync.sync_log", "ohs_sync.inbound_subscriptions"} {
		assert.Zero(t, e.scalar(t, `SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, e.userA), tbl)
	}
	keys, err := e.raw.List(ctx, "raw/"+e.tenantA.String()+"/"+e.userA.String()+"/")
	require.NoError(t, err)
	assert.Empty(t, keys)

	// Done: the user may connect again, which backfills afresh.
	e.connect(t, e.keyA, e.userA, "pu-1")
	assert.Equal(t, collect.StatusActive, e.connStatus(t, e.userA))
}

// Without delete_data the behaviour is unchanged: 204, and the data stays.
func TestDisconnectWithoutDeleteDataKeepsTheData(t *testing.T) {
	e := newConnEnv(t)
	e.connect(t, e.keyA, e.userA, "pu-1")
	e.drain(t)
	before := e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA)
	require.Positive(t, before)
	path := "/v1/users/" + e.userA.String() + "/connections/" + fakeSlug
	assert.Equal(t, http.StatusNoContent, e.do(t, http.MethodDelete, path+"?delete_data=false", e.keyA, nil).Code)
	assert.Equal(t, before, e.scalar(t, `SELECT count(*) FROM ohs_sync.raw_objects WHERE user_id = $1`, e.userA))
	assert.Zero(t, e.scalar(t, `SELECT count(*) FROM ohs_core.deletion_jobs`))
	assert.Equal(t, http.StatusNotFound,
		e.do(t, http.MethodDelete, "/v1/users/"+e.userA.String()+"/connections/other?delete_data=true", e.keyA, nil).Code,
		"no connection is still 404")
}

// A consent that completes while the provider's data is being erased is
// refused, and the grant just issued is revoked.
func TestCallbackRefusedWhileProviderDataIsErased(t *testing.T) {
	e := newConnEnv(t)
	ctx := context.Background()
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	tx, err := e.pool.Begin(ctx)
	require.NoError(t, err)
	_, err = deletion.QueueProvider(ctx, tx, e.tenantA, e.userA, fakeSlug)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	e.fake.grant("code-x", "pu-x")
	rec := e.callback(t, state, "code-x")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Location"), "status=error")
	assert.Zero(t, e.scalar(t, `SELECT count(*) FROM ohs_sync.connections`))
	revoked := e.fake.revokedTokens()
	require.Len(t, revoked, 1)
	assert.True(t, strings.HasPrefix(revoked[0], "access-secret-"))
}
