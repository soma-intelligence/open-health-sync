// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

func TestGetActiveUser(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, err := s.CreateUser(ctx, ten.ID, "active-1")
	require.NoError(t, err)

	got, err := s.GetActiveUser(ctx, ten.ID, u.ID)
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)

	_, err = s.GetActiveUser(ctx, ten.ID, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)

	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u.ID)
	require.NoError(t, err)
	_, err = s.GetActiveUser(ctx, ten.ID, u.ID)
	require.ErrorIs(t, err, tenant.ErrNotFound, "a user being deleted is gone for the API")
	got, err = s.GetUser(ctx, ten.ID, u.ID)
	require.NoError(t, err, "GetUser still sees it")
	require.Equal(t, "deleting", got.Status)
}

// A stored response that names a user is kept with that user, so it can be
// deleted with them, and is not stored at all once they are being deleted.
func TestIdempotentResponseNamingAUser(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	u, err := s.CreateUser(ctx, ten.ID, "subject-1")
	require.NoError(t, err)
	userOf := func(key string) (found bool, user *uuid.UUID) {
		t.Helper()
		err := s.Pool().QueryRow(ctx, `SELECT user_id FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND key = $2`,
			ten.ID, key).Scan(&user)
		if err != nil {
			return false, nil
		}
		return true, user
	}

	// Outside WithIdempotencySubject, SetIdempotencyUser does nothing.
	tenant.SetIdempotencyUser(ctx, u.ID)
	_, _, err = s.Idempotent(ctx, ten.ID, "plain", "h", func() (int, []byte, error) { return 200, []byte("{}"), nil })
	require.NoError(t, err)
	found, user := userOf("plain")
	require.True(t, found)
	require.Nil(t, user)

	sub := tenant.WithIdempotencySubject(ctx)
	_, _, err = s.Idempotent(sub, ten.ID, "named", "h", func() (int, []byte, error) {
		tenant.SetIdempotencyUser(sub, u.ID)
		return 201, []byte(`{"id":"x"}`), nil
	})
	require.NoError(t, err)
	found, user = userOf("named")
	require.True(t, found)
	require.NotNil(t, user)
	require.Equal(t, u.ID, *user)

	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, u.ID)
	require.NoError(t, err)
	sub = tenant.WithIdempotencySubject(ctx)
	st, body, err := s.Idempotent(sub, ten.ID, "late", "h", func() (int, []byte, error) {
		tenant.SetIdempotencyUser(sub, u.ID)
		return 200, []byte(`{"late":true}`), nil
	})
	require.NoError(t, err)
	require.Equal(t, 200, st, "the response is still sent this once")
	require.Equal(t, `{"late":true}`, string(body))
	found, _ = userOf("late")
	require.False(t, found, "never stored for a user being deleted")
}
