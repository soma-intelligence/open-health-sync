// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres has no year 0 as a date, and +23:59 on year 1 midnight lands in
// year 0 in UTC: both must be a tool error naming the argument, never a
// database error.
func TestParseTimeRefusesYearsOutsideRange(t *testing.T) {
	for _, v := range []string{"0000-01-01", "0001-01-01T00:00:00+23:59", "0000-06-01T00:00:00Z"} {
		_, err := parseTime("start", v)
		require.Errorf(t, err, "%s", v)
		var te toolError
		require.ErrorAs(t, err, &te, v)
		assert.Contains(t, err.Error(), "start")
	}
	got, err := parseTime("start", "9999-12-31")
	require.NoError(t, err)
	assert.Equal(t, 9999, got.Year())
}
