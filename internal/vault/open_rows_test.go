// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sealed JSON document holds the bytes the writer gave; a clear jsonb
// column prints the same document in Postgres' own form. JSONBText must give
// that form, or a CSV export would differ between a sealed and a clear row.
func TestJSONBTextIsWhatPostgresPrints(t *testing.T) {
	for in, want := range map[string]string{
		// Keys by length, then bytes; ": " and ", ".
		`{"synthetic":true,"timezone":"Europe/Paris"}`: `{"timezone": "Europe/Paris", "synthetic": true}`,
		`{"derived":true,"timezone":"UTC"}`:            `{"derived": true, "timezone": "UTC"}`,
		`{"b":1,"a":2,"aa":3}`:                         `{"a": 2, "b": 1, "aa": 3}`,
		// The last of two equal keys wins.
		`{"a":1,"a":2}`:                   `{"a": 2}`,
		`{}`:                              `{}`,
		`[]`:                              `[]`,
		`[1,"x",null,{"k":[true,false]}]`: `[1, "x", null, {"k": [true, false]}]`,
		`null`:                            `null`,
		`"a<b"`:                           `"a<b"`,
		`"tab\tquote\"back\\nl\n\u0001é"`: `"tab\tquote\"back\\nl\n\u0001é"`,
		// Numbers keep their digits, lose their exponent and a negative zero.
		`1.50`:          `1.50`,
		`1e2`:           `100`,
		`1.5e1`:         `15`,
		`1.50e1`:        `15.0`,
		`1e-2`:          `0.01`,
		`12e-1`:         `1.2`,
		`-1.5E+3`:       `-1500`,
		`1e-7`:          `0.0000001`,
		`1e+21`:         `1000000000000000000000`,
		`-0`:            `0`,
		`-0.0`:          `0.0`,
		`0`:             `0`,
		`  {"a" : 1}  `: `{"a": 1}`,
	} {
		assert.Equal(t, want, string(JSONBText([]byte(in))), in)
	}
	// What does not parse is left alone.
	assert.Equal(t, `{"a":`, string(JSONBText([]byte(`{"a":`))))
	assert.Equal(t, `1 2`, string(JSONBText([]byte(`1 2`))))
}

func TestRowIdentityEncodesEveryType(t *testing.T) {
	at := time.Date(2026, 9, 24, 8, 0, 0, 123456000, time.UTC)
	id := uuid.MustParse("0192f3a4-5b6c-7d8e-9f00-112233445566")
	tab := Table{Name: "t", Identity: []string{"h", "at", "n", "b", "u", "raw"}}
	got, err := RowIdentity(tab, func(c string) any {
		return map[string]any{"h": "abc", "at": at, "n": int64(7), "b": true, "u": [16]byte(id), "raw": []byte{9}}[c]
	})
	require.NoError(t, err)
	// The text as its bytes, the instant as its microseconds, the integer on
	// 8 bytes, the boolean on one, the uuid on 16, the bytes as they are.
	want := binary.BigEndian.AppendUint64([]byte("abc"), uint64(at.UnixMicro()))
	want = append(want, 0, 0, 0, 0, 0, 0, 0, 7, 1)
	want = append(append(want, id[:]...), 9)
	assert.Equal(t, want, got)

	// A uuid.UUID and the [16]byte a driver returns are the same identity.
	a, err := RowIdentity(Table{Identity: []string{"id"}}, func(string) any { return id })
	require.NoError(t, err)
	assert.Equal(t, id[:], a)

	_, err = RowIdentity(tab, func(string) any { return 1.5 })
	assert.Error(t, err, "a type with no encoding is refused, never skipped")
}

// fakeRow is one result row: Scan copies its values into the destinations
// the way pgx does for the types the reader passes.
type fakeRow []any

func (r fakeRow) Scan(dest ...any) error {
	if len(dest) != len(r) {
		return errors.New("fake row: wrong number of destinations")
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *any:
			*p = r[i]
		case *[]byte:
			if r[i] != nil {
				*p = r[i].([]byte)
			}
		case *int64:
			*p = r[i].(int64)
		case *string:
			*p = r[i].(string)
		case **string:
			if r[i] != nil {
				s := r[i].(string)
				*p = &s
			}
		case **float64:
			if r[i] != nil {
				f := r[i].(float64)
				*p = &f
			}
		case **bool:
			if r[i] != nil {
				b := r[i].(bool)
				*p = &b
			}
		default:
			return errors.New("fake row: unsupported destination")
		}
	}
	return nil
}

func testUserVault(t *testing.T) *UserVault {
	t.Helper()
	k, err := GenerateUserKeys(1)
	require.NoError(t, err)
	return &UserVault{tenant: uuid.New(), user: uuid.New(), keys: map[uint32]*UserKeys{1: k}, newest: k, metrics: NewMetrics()}
}

// Columns fills the same destinations from a clear row and from a sealed
// one, and fails, never fills zero values, when the sealed value does not
// open.
func TestColumnsScanClearAndSealedRows(t *testing.T) {
	ctx := context.Background()
	uv := testUserVault(t)
	rd := ReaderOf(uv)
	sel := Select("quantity_samples", `id, value, unit, source_name, was_user_entered, metadata, NULL::text`)
	assert.Equal(t, `q.id, q.value, q.unit, q.source_name, q.was_user_entered, q.metadata, NULL::text, q."content_hash", q."start_date", q.sealed`,
		sel.List("q"))

	type rec struct {
		id     int64
		value  float64
		unit   *string
		source *string
		user   *bool
		meta   json.RawMessage
		device *string
	}
	scan := func(row fakeRow) (rec, int64, error) {
		var r rec
		var extra int64
		err := sel.Scan(ctx, rd, row, []any{&r.id, &r.value, &r.unit, &r.source, &r.user, &r.meta, &r.device}, &extra)
		return r, extra, err
	}
	at := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

	// A clear row: the columns hold the values, sealed is NULL.
	clear, extra, err := scan(fakeRow{int64(1), 72.5, "bpm", "Watch", true, []byte(`{"timezone": "UTC", "synthetic": true}`), nil,
		"hash", at, nil, int64(42)})
	require.NoError(t, err)
	assert.Equal(t, int64(42), extra)
	assert.Equal(t, 72.5, clear.value)
	assert.Equal(t, "Watch", *clear.source)
	assert.True(t, *clear.user)
	assert.JSONEq(t, `{"timezone":"UTC","synthetic":true}`, string(clear.meta))

	// The same row sealed: the columns are NULL, the values come out of sealed.
	tab := Schema["quantity_samples"]
	id, err := RowIdentity(tab, func(c string) any { return map[string]any{"content_hash": "hash", "start_date": at}[c] })
	require.NoError(t, err)
	blob, err := uv.SealRow(tab, id, []any{72.5, "Watch", nil, nil, nil, nil, true, json.RawMessage(`{"synthetic":true,"timezone":"UTC"}`)})
	require.NoError(t, err)
	sealed, _, err := scan(fakeRow{int64(1), nil, "bpm", nil, nil, nil, nil, "hash", at, blob, int64(42)})
	require.NoError(t, err)
	assert.Equal(t, clear.value, sealed.value)
	assert.Equal(t, *clear.source, *sealed.source)
	assert.Equal(t, *clear.user, *sealed.user)
	assert.Equal(t, string(clear.meta), string(sealed.meta), "the document reads as the jsonb column prints it")

	// A nil document was sealed as the JSON null: it reads as NULL.
	blob, err = uv.SealRow(tab, id, []any{1.0, nil, nil, nil, nil, nil, nil, json.RawMessage(`null`)})
	require.NoError(t, err)
	null, _, err := scan(fakeRow{int64(1), nil, nil, nil, nil, nil, nil, "hash", at, blob, int64(0)})
	require.NoError(t, err)
	assert.Nil(t, null.meta)
	assert.Nil(t, null.source)

	// A value that cannot be NULL refuses one, as a scan of the column does.
	blob, err = uv.SealRow(tab, id, []any{nil, nil, nil, nil, nil, nil, nil, nil})
	require.NoError(t, err)
	_, _, err = scan(fakeRow{int64(1), nil, nil, nil, nil, nil, nil, "hash", at, blob, int64(0)})
	assert.ErrorContains(t, err, "NULL")

	// A sealed value moved to another row, or with one bit flipped, fails.
	blob, err = uv.SealRow(tab, id, []any{72.5, nil, nil, nil, nil, nil, nil, nil})
	require.NoError(t, err)
	_, _, err = scan(fakeRow{int64(1), nil, nil, nil, nil, nil, nil, "other", at, blob, int64(0)})
	var open *OpenError
	require.ErrorAs(t, err, &open)
	assert.Equal(t, "quantity_samples", open.Table)
	assert.ErrorIs(t, err, ErrOpen)
	blob[len(blob)-1] ^= 1
	_, _, err = scan(fakeRow{int64(1), nil, nil, nil, nil, nil, nil, "hash", at, blob, int64(0)})
	assert.ErrorIs(t, err, ErrOpen)

	// Without a vault a sealed row is an error, never an empty row; a clear
	// row still reads.
	none := NewReader(nil, uv.tenant, uv.user)
	var r rec
	err = sel.Scan(ctx, none, fakeRow{int64(1), nil, nil, nil, nil, nil, nil, "hash", at, blob}, []any{&r.id, &r.value, &r.unit, &r.source, &r.user, &r.meta, &r.device})
	assert.ErrorIs(t, err, ErrNoVault)
	assert.False(t, none.HasVault())
	require.NoError(t, sel.Scan(ctx, none, fakeRow{int64(1), 60.0, nil, nil, nil, nil, nil, "hash", at, nil},
		[]any{&r.id, &r.value, &r.unit, &r.source, &r.user, &r.meta, &r.device}))
	assert.Equal(t, 60.0, r.value)
}

// A table born sealed has no clear value columns: they are selected as NULL.
func TestSelectSealedOnly(t *testing.T) {
	sel := SelectSealedOnly("quantity_hourly_sealed", `bucket, provider, n, sum`)
	assert.Equal(t, `bucket, provider, NULL::bigint, NULL::float8, "quantity_type", "provider", "source_key", "slug", "worn", "unit", "bucket", sealed`,
		sel.List(""))
	assert.Panics(t, func() { Select("no_such_table", "id") })
}
