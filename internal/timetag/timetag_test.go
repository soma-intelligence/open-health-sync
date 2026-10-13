// SPDX-License-Identifier: AGPL-3.0-only

package timetag

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

var start = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

func valid() Fields {
	return Fields{Kind: "illness", StartAt: start}
}

func TestCheckAcceptsAndNormalizes(t *testing.T) {
	f := valid()
	f.StartAt = time.Date(2026, 3, 2, 9, 0, 0, 1234567, time.FixedZone("CET", 3600))
	f.EndAt = ptr(f.StartAt.Add(48 * time.Hour))
	f.Label, f.Note, f.Timezone = ptr(""), ptr("fever"), ptr("Asia/Tokyo")
	require.NoError(t, f.Check())
	assert.Equal(t, DefaultSource, f.Source, "source defaults to user")
	assert.Nil(t, f.Label, "an empty string is no label")
	assert.Equal(t, time.UTC, f.StartAt.Location())
	assert.Equal(t, 1234000, f.StartAt.Nanosecond(), "truncated to the microsecond the database keeps")
	assert.Equal(t, time.UTC, f.EndAt.Location())

	f = valid()
	f.Source = "app-1.2:sync_x"
	require.NoError(t, f.Check())
	assert.Equal(t, "app-1.2:sync_x", f.Source)

	for _, k := range Kinds {
		f := valid()
		f.Kind = k
		assert.NoError(t, f.Check(), k)
	}
}

func TestCheckRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*Fields)
		msg  string
	}{
		"missing kind":       {func(f *Fields) { f.Kind = "" }, "kind is required"},
		"unknown kind":       {func(f *Fields) { f.Kind = "flu" }, "kind must be one of illness, injury"},
		"missing start":      {func(f *Fields) { f.StartAt = time.Time{} }, "start_at is required"},
		"start out of range": {func(f *Fields) { f.StartAt = time.Date(1850, 1, 1, 0, 0, 0, 0, time.UTC) }, "start_at must be between"},
		"end before start":   {func(f *Fields) { f.EndAt = ptr(start.Add(-time.Hour)) }, "end_at must be after start_at"},
		"end equal to start": {func(f *Fields) { f.EndAt = ptr(start) }, "end_at must be after start_at"},
		"end within a microsecond": {func(f *Fields) {
			f.StartAt = start.Add(100)
			f.EndAt = ptr(start.Add(200))
		}, "end_at must be after start_at"},
		"end out of range": {func(f *Fields) { f.EndAt = ptr(time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)) }, "end_at must be between"},
		"long label":       {func(f *Fields) { f.Label = ptr(strings.Repeat("é", MaxLabel+1)) }, "label is at most 100 characters"},
		"long note":        {func(f *Fields) { f.Note = ptr(strings.Repeat("a", MaxNote+1)) }, "note is at most 2000 characters"},
		"unknown zone":     {func(f *Fields) { f.Timezone = ptr("Mars/Olympus") }, "timezone must be an IANA time zone"},
		"local zone":       {func(f *Fields) { f.Timezone = ptr("Local") }, "timezone must be an IANA time zone"},
		"path zone":        {func(f *Fields) { f.Timezone = ptr("../etc/passwd") }, "timezone must be an IANA time zone"},
		"bad source":       {func(f *Fields) { f.Source = "my app" }, "source must be 1 to 64"},
		"long source":      {func(f *Fields) { f.Source = strings.Repeat("a", MaxSource+1) }, "source must be 1 to 64"},
	} {
		t.Run(name, func(t *testing.T) {
			f := valid()
			tc.edit(&f)
			err := f.Check()
			var ie *InvalidError
			require.True(t, errors.As(err, &ie), "%v", err)
			assert.Contains(t, ie.Msg, tc.msg)
		})
	}
}

func TestLabelLengthCountsCharacters(t *testing.T) {
	f := valid()
	f.Label = ptr(strings.Repeat("é", MaxLabel))
	assert.NoError(t, f.Check(), "100 two-byte characters fit")
}

func TestPatchApply(t *testing.T) {
	end := start.Add(72 * time.Hour)
	cur := Tag{ID: uuid.New(), Kind: "travel", Label: ptr("Lisbon"), StartAt: start, Timezone: ptr("Europe/Lisbon"), Source: "app"}

	f, changed := Patch{}.apply(cur)
	assert.False(t, changed)
	assert.Equal(t, "travel", f.Kind)

	f, changed = Patch{EndAt: Change[time.Time]{Set: true, Value: &end}, Label: Change[string]{Set: true}}.apply(cur)
	assert.True(t, changed)
	assert.Equal(t, &end, f.EndAt)
	assert.Nil(t, f.Label, "null clears")
	assert.Equal(t, ptr("Europe/Lisbon"), f.Timezone, "absent stays")
	assert.Equal(t, "app", f.Source, "source is kept")

	err := Patch{Kind: Change[string]{Set: true}}.check()
	assert.ErrorContains(t, err, "kind cannot be null")
	err = Patch{StartAt: Change[time.Time]{Set: true}}.check()
	assert.ErrorContains(t, err, "start_at cannot be null")
}

func TestCursorRoundTrip(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 3, 2, 9, 0, 0, 123000, time.UTC)
	gotT, gotID, err := decodeCursor(encodeCursor(at, id))
	require.NoError(t, err)
	assert.True(t, at.Equal(gotT))
	assert.Equal(t, id, gotID)
	for _, bad := range []string{"%%%", "bm9waXBl", encodeCursor(at, id)[:5]} {
		_, _, err := decodeCursor(bad)
		assert.ErrorIs(t, err, ErrInvalidCursor, bad)
	}
}

func TestQueryNormalize(t *testing.T) {
	q, err := Query{}.normalize()
	require.NoError(t, err)
	assert.Equal(t, DefaultLimit, q.Limit)
	_, err = Query{Kind: "flu"}.normalize()
	assert.ErrorContains(t, err, "kind must be one of")
	_, err = Query{Limit: MaxLimit + 1}.normalize()
	assert.ErrorContains(t, err, "limit must be between 1 and 1000")
	_, err = Query{Start: start, End: start.Add(-time.Second)}.normalize()
	assert.ErrorContains(t, err, "start must not be after end")
	_, err = Query{Cursor: "x"}.normalize()
	assert.ErrorIs(t, err, ErrInvalidCursor)
}
