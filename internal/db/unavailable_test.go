// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnavailable(t *testing.T) {
	// A pool on a port nobody listens on: the error a stopped Postgres gives.
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	defer pool.Close()
	_, err = pool.Exec(context.Background(), "SELECT 1")
	require.Error(t, err)
	assert.True(t, Unavailable(fmt.Errorf("tenant: lookup: %w", err)), "%v", err)

	for _, e := range []error{
		&pgconn.PgError{Code: "08006"},
		&pgconn.PgError{Code: "57P01"},
		&pgconn.PgError{Code: "57P03"},
		&pgconn.PgError{Code: "53300"},
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		fmt.Errorf("read: %w", io.ErrUnexpectedEOF),
		syscall.ECONNRESET,
	} {
		assert.True(t, Unavailable(e), "%v", e)
	}
	for _, e := range []error{
		nil,
		errors.New("boom"),
		&pgconn.PgError{Code: "22021"}, // a query that ran and failed
		&pgconn.PgError{Code: "23505"},
		&pgconn.PgError{Code: "57014"}, // canceled statement
		context.Canceled,
		context.DeadlineExceeded,
	} {
		assert.False(t, Unavailable(e), "%v", e)
	}
}
