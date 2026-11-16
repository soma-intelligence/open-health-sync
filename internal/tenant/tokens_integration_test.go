// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tenant_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

// A key with an expiry works until then and is refused after; listings show
// the expiry and the tenant's count of active keys leaves an expired key out.
func TestKeyExpiry(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)

	past := time.Now().Add(-time.Minute)
	_, _, err = s.CreateKeyWith(ctx, ten.ID, tenant.KeyOptions{ExpiresAt: &past})
	require.ErrorIs(t, err, tenant.ErrExpiryPast)

	exp := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	full, k, err := s.CreateKeyWith(ctx, ten.ID, tenant.KeyOptions{ExpiresAt: &exp})
	require.NoError(t, err)
	require.NotNil(t, k.ExpiresAt)
	require.True(t, exp.Equal(*k.ExpiresAt))
	_, err = s.LookupKey(ctx, auth.Hash(full))
	require.NoError(t, err)

	keys, err := s.ListKeys(ctx, ten.ID)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Nil(t, keys[0].ExpiresAt, "the tenant's first key never expires")
	require.True(t, exp.Equal(*keys[1].ExpiresAt))
	sum, err := s.GetTenantSummary(ctx, ten.ID)
	require.NoError(t, err)
	require.Equal(t, 2, sum.ActiveKeys)

	// The expiry passes.
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.api_keys SET expires_at = now() - interval '1 second' WHERE id = $1`, k.ID)
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(full))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	sum, err = s.GetTenantSummary(ctx, ten.ID)
	require.NoError(t, err)
	require.Equal(t, 1, sum.ActiveKeys)
	why, err := s.WhyRefused(ctx, auth.Hash(full))
	require.NoError(t, err)
	require.Equal(t, tenant.Refused{Tenant: ten.ID, Reason: "expired", KeyID: k.ID.String()}, why)

	// An audited key takes an expiry too.
	_, k2, err := s.CreateKeyAuditedWith(ctx, ten.ID, tenant.KeyOptions{ExpiresAt: &exp},
		audit.Event{ActorType: audit.Admin, ActorID: "test", Action: audit.KeyCreate})
	require.NoError(t, err)
	require.True(t, exp.Equal(*k2.ExpiresAt))
}

// One token of a user, or all of them, can be revoked; a token id of
// another user or tenant is not found.
func TestRevokeUserTokens(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	other, _, err := s.CreateTenant(ctx, "Other")
	require.NoError(t, err)
	u, err := s.CreateUser(ctx, ten.ID, "u1")
	require.NoError(t, err)
	u2, err := s.CreateUser(ctx, ten.ID, "u2")
	require.NoError(t, err)

	mint := func(user uuid.UUID) (token, id string) {
		token, _, err := s.MintUserToken(ctx, ten.ID, user, []string{"read"}, 0)
		require.NoError(t, err)
		return token, audit.TokenID(auth.Hash(token))
	}
	valid := func(token string) bool {
		_, err := s.LookupKey(ctx, auth.Hash(token))
		if err != nil {
			require.ErrorIs(t, err, auth.ErrUnauthenticated)
		}
		return err == nil
	}
	a, aID := mint(u.ID)
	b, _ := mint(u.ID)
	c, cID := mint(u2.ID)
	require.True(t, tenant.ValidTokenID(aID))

	require.ErrorIs(t, s.RevokeUserToken(ctx, ten.ID, u.ID, cID), tenant.ErrNotFound, "another user's token")
	require.ErrorIs(t, s.RevokeUserToken(ctx, other.ID, u.ID, aID), tenant.ErrNotFound, "another tenant")
	require.ErrorIs(t, s.RevokeUserToken(ctx, ten.ID, u.ID, "not-an-id"), tenant.ErrNotFound)
	require.ErrorIs(t, s.RevokeUserToken(ctx, ten.ID, u.ID, "0123456789abcdef"), tenant.ErrNotFound)
	require.True(t, valid(a) && valid(b) && valid(c))

	require.NoError(t, s.RevokeUserToken(ctx, ten.ID, u.ID, aID))
	require.NoError(t, s.RevokeUserToken(ctx, ten.ID, u.ID, aID), "twice is fine")
	require.False(t, valid(a))
	require.True(t, valid(b) && valid(c))
	why, err := s.WhyRefused(ctx, auth.Hash(a))
	require.NoError(t, err)
	require.Equal(t, tenant.Refused{Tenant: ten.ID, Reason: "revoked"}, why)
	why, err = s.WhyRefused(ctx, auth.Hash("ohs_sk_nothing"))
	require.NoError(t, err)
	require.Equal(t, tenant.Refused{Reason: "unknown"}, why)

	mint(u.ID)
	n, err := s.RevokeUserTokens(ctx, ten.ID, u.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, n, "the two tokens still valid")
	require.False(t, valid(b))
	require.True(t, valid(c), "another user's token is untouched")
	n, err = s.RevokeUserTokens(ctx, ten.ID, u.ID)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = s.RevokeUserTokens(ctx, other.ID, u.ID)
	require.ErrorIs(t, err, tenant.ErrNotFound)
	_, err = s.RevokeUserTokens(ctx, ten.ID, uuid.New())
	require.ErrorIs(t, err, tenant.ErrNotFound)
}

// Revoking an API key revokes the user tokens it minted, whichever way the
// key is revoked; tokens minted by another key, or tied to none, live on.
func TestRevokingAKeyRevokesItsTokens(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, first, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	u, err := s.CreateUser(ctx, ten.ID, "u1")
	require.NoError(t, err)
	valid := func(token string) bool {
		_, err := s.LookupKey(ctx, auth.Hash(token))
		return err == nil
	}
	mintBy := func(key *uuid.UUID) string {
		token, _, err := s.MintUserTokenBy(ctx, ten.ID, u.ID, []string{"read"}, 0, key)
		require.NoError(t, err)
		return token
	}
	untied := mintBy(nil)

	revokers := map[string]func(full string, k tenant.APIKey) error{
		"by id":     func(_ string, k tenant.APIKey) error { return s.RevokeKeyByID(ctx, ten.ID, k.ID) },
		"by prefix": func(_ string, k tenant.APIKey) error { return s.RevokeKeyByPrefix(ctx, ten.ID, k.Prefix) },
		"by hash":   func(full string, _ tenant.APIKey) error { return s.RevokeKey(ctx, ten.ID, auth.Hash(full)) },
		"audited": func(_ string, k tenant.APIKey) error {
			_, err := s.RevokeKeyAudited(ctx, ten.ID, k.ID, audit.Event{ActorType: audit.Admin, ActorID: "test", Action: audit.KeyRevoke})
			return err
		},
	}
	for name, revoke := range revokers {
		full, k, err := s.CreateKeyInfo(ctx, ten.ID)
		require.NoError(t, err, name)
		_, keeper, err := s.CreateKeyInfo(ctx, ten.ID)
		require.NoError(t, err, name)
		mine, theirs := mintBy(&k.ID), mintBy(&keeper.ID)
		require.True(t, valid(mine) && valid(theirs), name)

		require.NoError(t, revoke(full, k), name)
		require.False(t, valid(mine), name+": the token of the revoked key")
		require.True(t, valid(theirs), name+": the token of another key")
		require.True(t, valid(untied), name+": a token tied to no key")
		var revokedAt *time.Time
		require.NoError(t, s.Pool().QueryRow(ctx, `SELECT revoked_at FROM ohs_core.user_tokens WHERE hash = $1`,
			auth.Hash(mine)).Scan(&revokedAt), name)
		require.NotNil(t, revokedAt, name+": the revocation is stated on the token")
	}

	// The lookup alone refuses a token whose key is revoked, even one the
	// revocation did not reach (minted while it ran).
	_, k, err := s.CreateKeyInfo(ctx, ten.ID)
	require.NoError(t, err)
	late := mintBy(&k.ID)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.api_keys SET revoked_at = now() WHERE id = $1`, k.ID)
	require.NoError(t, err)
	require.False(t, valid(late))
	why, err := s.WhyRefused(ctx, auth.Hash(late))
	require.NoError(t, err)
	require.Equal(t, "revoked", why.Reason)
	require.True(t, valid(first), "the tenant's first key still works")
}

// A user token stops working when the key that minted it expires, and the
// refusal says why: revoked, expired, or a user who is no longer active.
func TestTokenFollowsItsKeyExpiryAndRefusalReasons(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	ten, _, err := s.CreateTenant(ctx, "Acme")
	require.NoError(t, err)
	u, err := s.CreateUser(ctx, ten.ID, "u1")
	require.NoError(t, err)
	reason := func(token string) string {
		why, err := s.WhyRefused(ctx, auth.Hash(token))
		require.NoError(t, err)
		require.Equal(t, ten.ID, why.Tenant)
		return why.Reason
	}

	exp := time.Now().Add(time.Hour)
	_, k, err := s.CreateKeyWith(ctx, ten.ID, tenant.KeyOptions{ExpiresAt: &exp})
	require.NoError(t, err)
	token, _, err := s.MintUserTokenBy(ctx, ten.ID, u.ID, []string{"read"}, 0, &k.ID)
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(token))
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.api_keys SET expires_at = now() - interval '1 second' WHERE id = $1`, k.ID)
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(token))
	require.ErrorIs(t, err, auth.ErrUnauthenticated, "the token of an expired key")
	require.Equal(t, "expired", reason(token))

	// A token that is itself valid, of a user being deleted.
	other, err := s.CreateUser(ctx, ten.ID, "u2")
	require.NoError(t, err)
	live, _, err := s.MintUserToken(ctx, ten.ID, other.ID, []string{"read"}, time.Hour)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.end_users SET status = 'deleting' WHERE id = $1`, other.ID)
	require.NoError(t, err)
	_, err = s.LookupKey(ctx, auth.Hash(live))
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
	require.Equal(t, "user_inactive", reason(live))

	// Revoked wins over the rest, expired over the user's status.
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.user_tokens SET expires_at = now() - interval '1 second' WHERE hash = $1`, auth.Hash(live))
	require.NoError(t, err)
	require.Equal(t, "expired", reason(live))
	_, err = s.Pool().Exec(ctx, `UPDATE ohs_core.user_tokens SET revoked_at = now() WHERE hash = $1`, auth.Hash(live))
	require.NoError(t, err)
	require.Equal(t, "revoked", reason(live))
}
