// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

func TestNewPageJSON(t *testing.T) {
	b, err := json.Marshal(newPage[int](nil, ""))
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":[],"next_cursor":null}`, string(b), "an empty last page")

	b, err = json.Marshal(newPage([]int{1, 2}, "abc"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"data":[1,2],"next_cursor":"abc"}`, string(b))
}

func TestParseTimeIn(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	v, err := parseTimeIn("2026-07-01", paris)
	require.NoError(t, err)
	assert.True(t, v.Equal(time.Date(2026, 6, 30, 22, 0, 0, 0, time.UTC)), "a date is midnight in the zone: %v", v)

	v, err = parseTimeIn("2026-07-01T08:00:00+02:00", paris)
	require.NoError(t, err)
	assert.True(t, v.Equal(time.Date(2026, 7, 1, 6, 0, 0, 0, time.UTC)), "an instant keeps its own offset")
	assert.Equal(t, time.UTC, v.Location())

	_, err = parseTimeIn("July 1st", paris)
	assert.Error(t, err)
}

func TestParseLimit(t *testing.T) {
	for v, want := range map[string]int{"": 0, "1": 1, "50": 50} {
		n, err := parseLimit(httptest.NewRequest("GET", "/x?limit="+v, nil))
		require.NoError(t, err, v)
		assert.Equal(t, want, n, v)
	}
	for _, bad := range []string{"0", "-1", "ten", "1.5", "99999999"} {
		_, err := parseLimit(httptest.NewRequest("GET", "/x?limit="+bad, nil))
		assert.Error(t, err, bad)
	}
	n, err := parseLimit(httptest.NewRequest("GET", "/x?limit=9", nil))
	require.NoError(t, err)
	assert.LessOrEqual(t, n, read.MaxLimit)
}

func TestParseReadQuery(t *testing.T) {
	q, err := parseReadQuery(httptest.NewRequest("GET",
		"/x?type=heart_rate&start=2026-01-10&end=2026-01-11T12:00:00Z&cursor=c1&limit=5", nil))
	require.NoError(t, err)
	assert.Equal(t, "heart_rate", q.Type)
	assert.Equal(t, "c1", q.Cursor)
	assert.Equal(t, 5, q.Limit)
	assert.True(t, q.Start.Equal(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)))
	assert.True(t, q.End.Equal(time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC)))

	for _, bad := range []string{"?start=soon", "?end=2026-13-01", "?limit=0", "?view=raw"} {
		_, err := parseReadQuery(httptest.NewRequest("GET", "/x"+bad, nil))
		assert.Error(t, err, bad)
	}
}
