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

func TestObservationCursor(t *testing.T) {
	at := time.Date(2026, 3, 1, 9, 30, 0, 123456000, time.FixedZone("x", 3600))
	gotT, gotK, gotID, err := decodeObsCursor(encodeObsCursor(at, KindWorkout, 42))
	require.NoError(t, err)
	assert.True(t, at.Equal(gotT))
	assert.Equal(t, KindWorkout, gotK)
	assert.Equal(t, int64(42), gotID)

	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, bad := range []string{"%%", enc("2026-03-01T00:00:00Z|1"), enc("2026-03-01T00:00:00Z|0|1"),
		enc("2026-03-01T00:00:00Z|7|1"), enc("2026-03-01T00:00:00Z|x|1"), enc("soon|1|1"),
		enc("2026-03-01T00:00:00Z|1|x"), EncodeCursor(at, 42)} {
		_, _, _, err := decodeObsCursor(bad)
		assert.ErrorIs(t, err, ErrInvalidCursor, bad)
	}
	assert.NoError(t, CheckObservationQuery(Query{Cursor: encodeObsCursor(at, KindSleep, 1)}))
	assert.ErrorIs(t, CheckObservationQuery(Query{Cursor: EncodeCursor(at, 1)}), ErrInvalidCursor,
		"a REST list cursor is not a merged list cursor")
	var qe *QueryError
	assert.ErrorAs(t, CheckObservationQuery(Query{Limit: 5000}), &qe)
}

func TestObservationFilterAnd(t *testing.T) {
	all := AllObservations()
	types := func(ts ...string) ObservationFilter { return ObservationFilter{Quantities: true, Types: ts} }
	panels := ObservationFilter{Quantities: true, PanelsOnly: true}
	sleep := ObservationFilter{Sleep: true}
	activity := ObservationFilter{Quantities: true, Types: []string{"step_count"}, Workouts: true, Daily: true}

	assert.Equal(t, all, all.And(all))
	assert.False(t, all.Empty())
	assert.Equal(t, types("heart_rate"), all.And(types("heart_rate")))
	assert.Equal(t, types("heart_rate"), types("heart_rate", "step_count").And(types("heart_rate")))
	assert.True(t, types("heart_rate").And(types("step_count")).Empty())
	assert.True(t, ObservationFilter{Quantities: true, Types: []string{}}.Empty(), "an empty type list keeps nothing")
	assert.False(t, types().Empty(), "a nil type list keeps every type")
	assert.True(t, ObservationFilter{}.Empty())

	assert.Equal(t, panels, all.And(panels))
	assert.Equal(t, panels, panels.And(types(Diastolic)))
	assert.Equal(t, panels, types(Systolic, "heart_rate").And(panels))
	assert.True(t, panels.And(types("heart_rate")).Empty())
	assert.True(t, panels.And(sleep).Empty())

	assert.Equal(t, sleep, all.And(sleep))
	assert.True(t, sleep.And(activity).Empty())
	assert.Equal(t, ObservationFilter{Workouts: true}, activity.And(ObservationFilter{Workouts: true}))
	assert.Equal(t, types("step_count"), activity.And(types("step_count", "heart_rate")))

	nutrition := ObservationFilter{Nutrition: true}
	assert.Equal(t, nutrition, all.And(nutrition))
	assert.False(t, nutrition.Empty())
	assert.True(t, nutrition.And(activity).Empty(), "no category holds nutrition logs")
}
