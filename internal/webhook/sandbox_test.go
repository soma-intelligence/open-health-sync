// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExcerpt(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"json", `{"ok":false,"error":"bad signature"}`, `{"ok":false,"error":"bad signature"}`},
		{"newlines and tabs kept, CR dropped", "line 1\r\nline\t2\n", "line 1\nline\t2\n"},
		{"control characters", "a\x00b\x1bc\x7fd", "a?b?c?d"},
		{"invalid utf-8", "caf\xe9 ok", "caf? ok"},
		{"unicode kept", "réponse refusée – 403 ✓", "réponse refusée – 403 ✓"},
		{"non-breaking space kept", "a b", "a b"},
		{"bidi override replaced", "a\u202eb", "a?b"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, Excerpt([]byte(c.in)), c.name)
	}
}

func TestExcerptBounded(t *testing.T) {
	long := strings.Repeat("x", 5000)
	assert.Len(t, Excerpt([]byte(long)), ExcerptLen)

	// A two-byte character cut by the limit is dropped, not turned into '?'.
	in := strings.Repeat("a", ExcerptLen-1) + "é" + "tail"
	got := Excerpt([]byte(in))
	assert.True(t, utf8.ValidString(got))
	assert.Equal(t, strings.Repeat("a", ExcerptLen-1), got)

	// A three-byte character that fits whole is kept.
	in = strings.Repeat("a", ExcerptLen-3) + "✓" + "tail"
	assert.Equal(t, strings.Repeat("a", ExcerptLen-3)+"✓", Excerpt([]byte(in)))

	// Binary noise stays printable and bounded.
	noise := make([]byte, 3000)
	for i := range noise {
		noise[i] = byte(i * 7)
	}
	got = Excerpt(noise)
	assert.True(t, utf8.ValidString(got))
	assert.LessOrEqual(t, len(got), ExcerptLen, "a replacement is never longer than what it replaces")
	for _, r := range got {
		assert.False(t, r < 0x20 && r != '\n' && r != '\t', "control character %q", r)
	}
}

func TestSampleData(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)
	for _, typ := range Types {
		data := SampleData(typ, now)
		assert.Equal(t, uuid.Nil.String(), data["user_id"], typ)
		assert.Equal(t, SampleUserRef, data["external_ref"], typ)
		_, err := json.Marshal(data)
		require.NoError(t, err, typ)
	}
	w := SampleData(DataWritten, now)
	assert.Equal(t, "oura", w["provider"])
	assert.Equal(t, "2026-10-11", w["first_day"])
	assert.Equal(t, "2026-10-12", w["last_day"])
	assert.NotEmpty(t, w["records"])
	assert.NotEmpty(t, w["types"])
	assert.Equal(t, 3, SampleData(DataDeleted, now)["records_deleted"])
	assert.Equal(t, "oura", SampleData(ConnectionNeedsReauth, now)["provider"])
	assert.NotContains(t, SampleData(UserDeleted, now), "provider")
}

func TestPayloadTestFlag(t *testing.T) {
	p := Payload{ID: uuid.Nil, Type: DataWritten, Data: map[string]any{}}
	assert.NotContains(t, string(mustJSON(p)), `"test"`, "a real event has no test field")
	p.Test = true
	assert.Contains(t, string(mustJSON(p)), `"test":true`)
}

func TestCompactPayload(t *testing.T) {
	// The text form of a jsonb value, as Postgres writes it.
	got, err := compactPayload(`{"id": "e1", "data": {"n": 1, "s": "a, b: c"}, "test": true}`)
	require.NoError(t, err)
	assert.Equal(t, `{"id":"e1","data":{"n":1,"s":"a, b: c"},"test":true}`, string(got))
	_, err = compactPayload(`{"id":`)
	assert.Error(t, err)
}
