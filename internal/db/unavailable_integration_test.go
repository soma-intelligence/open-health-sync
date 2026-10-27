// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refused login fails the connect, but it is not an outage: retrying the
// same credentials does not help, so it stays a 500.
func TestUnavailableNotForARefusedLogin(t *testing.T) {
	u, err := url.Parse(TestDB(t))
	require.NoError(t, err)
	u.User = url.UserPassword(u.User.Username(), "not-the-password")
	_, err = pgx.Connect(context.Background(), u.String())
	require.Error(t, err)
	assert.False(t, Unavailable(err), "%v", err)
}
