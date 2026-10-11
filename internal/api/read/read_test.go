// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 3, 1, 9, 30, 0, 123456000, time.FixedZone("x", 3600))
	c := EncodeCursor(at, 42)
	gotT, gotID, err := DecodeCursor(c)
	require.NoError(t, err)
	assert.True(t, at.Equal(gotT))
	assert.Equal(t, int64(42), gotID)

	for _, bad := range []string{"%%", base64.RawURLEncoding.EncodeToString([]byte("nope")),
		base64.RawURLEncoding.EncodeToString([]byte("2026-03-01T00:00:00Z|x")),
		base64.RawURLEncoding.EncodeToString([]byte("soon|1"))} {
		_, _, err := DecodeCursor(bad)
		assert.ErrorIs(t, err, ErrInvalidCursor, bad)
	}
}

func TestQueryValidate(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		q       Query
		needTyp bool
		bad     bool
	}{
		{"defaults", Query{}, false, false},
		{"type missing", Query{}, true, true},
		{"type given", Query{Type: "heart_rate"}, true, false},
		{"start after end", Query{Start: t0.Add(time.Hour), End: t0}, false, true},
		{"start equals end", Query{Start: t0, End: t0}, false, false},
		{"limit too big", Query{Limit: 1001}, false, true},
		{"limit negative", Query{Limit: -1}, false, true},
		{"limit max", Query{Limit: 1000}, false, false},
		{"bad cursor", Query{Cursor: "%%"}, false, true},
	}
	for _, c := range cases {
		_, err := c.q.normalize(c.needTyp)
		if c.bad {
			var qe *QueryError
			assert.True(t, errors.As(err, &qe) || errors.Is(err, ErrInvalidCursor), "%s: %v", c.name, err)
		} else {
			assert.NoError(t, err, c.name)
		}
	}
	assert.NoError(t, Query{Limit: 10}.Check())
	assert.ErrorIs(t, Query{Cursor: "%%"}.Check(), ErrInvalidCursor)
	var qe *QueryError
	assert.ErrorAs(t, Query{Start: t0.Add(time.Hour), End: t0}.Check(), &qe)

	n, err := Query{}.normalize(false)
	require.NoError(t, err)
	assert.Equal(t, DefaultLimit, n.Limit)
}
