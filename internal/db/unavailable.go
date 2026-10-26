// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// RetryAfterSeconds is the Retry-After a request refused by Unavailable
// answers with: a restarted Postgres takes a second or two to accept
// connections again.
const RetryAfterSeconds = 5

// Unavailable reports whether err means the database could not be reached
// (no connection, a connection lost mid-query, a server shutting down,
// starting up or out of connection slots), as opposed to a query that ran
// and failed. A request failing for that reason can be retried as is: the
// API answers 503 with Retry-After instead of 500. A canceled request, a
// refused login (SQLSTATE class 28) and a protocol violation (08P01) are not
// outages: retrying them does not help.
func Unavailable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	// A server error decides, also inside a ConnectError (a refused login
	// fails the connect).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "08P01": // protocol violation: a bug, not an outage
			return false
		case strings.HasPrefix(pgErr.Code, "08"): // connection exception
			return true
		case pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03": // shutdown, crash, starting up
			return true
		case pgErr.Code == "53300": // too many connections
			return true
		}
		return false
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	// pgx closed the connection under us: the server went away mid-session
	// (a crashed or restarted backend), seen at the next begin or commit.
	if errors.Is(err, pgconn.ErrConnClosed) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
