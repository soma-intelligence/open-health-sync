// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRetryableWrite(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "40P01"}, true},
		{fmt.Errorf("write records: %w", &pgconn.PgError{Code: "40001"}), true},
		{&pgconn.PgError{Code: "53400"}, false},
		{&pgconn.PgError{Code: "23505"}, false},
		{errors.New("connection reset"), false},
	}
	for _, c := range cases {
		if got := retryableWrite(c.err); got != c.want {
			t.Errorf("retryableWrite(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
