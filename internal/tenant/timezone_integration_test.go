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

// A user is created with or without a time zone, the zone is set and cleared
// as the developer's explicit choice, and a name that is no IANA zone is
// refused. Every read of the user carries it.
func TestUserTimezone(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, _ := s.CreateTenant(ctx, "Acme")
	other, _, _ := s.CreateTenant(ctx, "Other")

	bare, err := s.CreateUser(ctx, ten.ID, "bare")
	require.NoError(t, err)
	require.Empty(t, bare.Timezone)
	require.Empty(t, bare.TimezoneSource)

	u, err := s.CreateUserIn(ctx, ten.ID, "paris", "Europe/Paris")
	require.NoError(t, err)
	require.Equal(t, "Europe/Paris", u.Timezone)
	require.Equal(t, tenant.TimezoneExplicit, u.TimezoneSource)

	for _, bad := range []string{"Mars/Olympus", "Local", "+02:00"} {
		_, err = s.CreateUserIn(ctx, ten.ID, "bad-"+bad, bad)
		require.ErrorIs(t, err, tenant.ErrInvalidTimezone, bad)
		_, err = s.SetUserTimezone(ctx, ten.ID, bare.ID, bad)
		require.ErrorIs(t, err, tenant.ErrInvalidTimezone, bad)
	}
	require.False(t, tenant.ValidTimezone(""))
	require.True(t, tenant.ValidTimezone("UTC"))

	set, err := s.SetUserTimezone(ctx, ten.ID, bare.ID, "Asia/Tokyo")
	require.NoError(t, err)
	require.Equal(t, "Asia/Tokyo", set.Timezone)
	require.Equal(t, tenant.TimezoneExplicit, set.TimezoneSource)
	got, err := s.GetUser(ctx, ten.ID, bare.ID)
	require.NoError(t, err)
	require.Equal(t, "Asia/Tokyo", got.Timezone)
	ensured, created, err := s.EnsureUser(ctx, ten.ID, "bare")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "Asia/Tokyo", ensured.Timezone)
	users, _, err := s.ListUsers(ctx, ten.ID, "", 10)
	require.NoError(t, err)
	zones := map[string]string{}
	for _, l := range users {
		zones[l.ExternalRef] = l.Timezone + " " + l.TimezoneSource
	}
	require.Equal(t, map[string]string{"bare": "Asia/Tokyo explicit", "paris": "Europe/Paris explicit"}, zones)

	// A zone the ETL learnt reads as learnt, and setting one replaces it.
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users
		SET timezone = 'America/Chicago', timezone_source = 'learnt', timezone_seen_at = now() WHERE id = $1`, bare.ID)
	require.NoError(t, err)
	got, err = s.GetUser(ctx, ten.ID, bare.ID)
	require.NoError(t, err)
	require.Equal(t, tenant.TimezoneLearnt, got.TimezoneSource)

	cleared, err := s.SetUserTimezone(ctx, ten.ID, bare.ID, "")
	require.NoError(t, err)
	require.Empty(t, cleared.Timezone)
	require.Empty(t, cleared.TimezoneSource)
	var seen *string
	require.NoError(t, s.Pool().QueryRow(ctx, `SELECT timezone_seen_at::text FROM ohs_core.end_users WHERE id = $1`, bare.ID).Scan(&seen))
	require.Nil(t, seen, "clearing forgets when the zone was learnt, so the next upload teaches it again")

	// Another tenant's user, an unknown user and a user being deleted are not found.
	_, err = s.SetUserTimezone(ctx, other.ID, bare.ID, "UTC")
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, err = s.SetUserTimezone(ctx, ten.ID, uuid.New(), "UTC")
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, bare.ID)
	require.NoError(t, err)
	_, err = s.SetUserTimezone(ctx, ten.ID, bare.ID, "UTC")
	require.ErrorIs(t, err, tenant.ErrNotFound)

	// The database refuses a zone without a source and a source it does not know.
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET timezone = 'UTC', timezone_source = NULL WHERE id = $1`, u.ID)
	require.Error(t, err)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET timezone_source = 'guessed' WHERE id = $1`, u.ID)
	require.Error(t, err)
}
