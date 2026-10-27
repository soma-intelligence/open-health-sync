// SPDX-License-Identifier: AGPL-3.0-only

package pgtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"":              true,
		"heart_rate":    true,
		"clé événement": true,
		"a\x00b":        false,
		"\xff\xfe":      false,
		"ok\xc3":        false,
	} {
		assert.Equal(t, want, Valid(s), "%q", s)
	}
}

func TestClean(t *testing.T) {
	assert.Equal(t, "heart_rate", Clean("heart_rate"))
	assert.Equal(t, "ab", Clean("a\x00b"))
	assert.Equal(t, "a�b", Clean("a\xff\xfeb"))
	assert.True(t, Valid(Clean("\x00\xff\x00")))
}

func TestFirstInvalid(t *testing.T) {
	type inner struct {
		Kind string `json:"kind"`
	}
	type Embedded struct {
		UserID string `json:"user_id,omitempty"`
	}
	type window struct {
		Start string `json:"start"`
	}
	type args struct {
		Embedded
		window
		Type   string            `json:"type"`
		Note   *string           `json:"note"`
		Tags   []string          `json:"tags"`
		Meta   map[string]string `json:"meta"`
		Inner  inner             `json:"inner"`
		Raw    []byte            `json:"raw"`
		NoTag  string
		hidden string
	}
	_, ok := FirstInvalid(args{Type: "heart_rate", Raw: []byte{0, 0xff}, hidden: "\x00"})
	assert.True(t, ok, "bytes and unexported fields are not checked")

	bad := "a\x00"
	for want, v := range map[string]args{
		"user_id": {Embedded: Embedded{UserID: "\xff"}},
		"type":    {Type: bad},
		"start":   {window: window{Start: bad}},
		"note":    {Note: &bad},
		"tags":    {Tags: []string{"ok", bad}},
		"meta":    {Meta: map[string]string{bad: "v"}},
		"kind":    {Inner: inner{Kind: bad}},
		"NoTag":   {NoTag: bad},
	} {
		name, ok := FirstInvalid(v)
		assert.False(t, ok, want)
		assert.Equal(t, want, name)
	}
	_, ok = FirstInvalid(map[string]any{"a": []any{"x", map[string]any{"b": "\x00"}}})
	assert.False(t, ok, "decoded JSON")
	_, ok = FirstInvalid(nil)
	assert.True(t, ok)
}
