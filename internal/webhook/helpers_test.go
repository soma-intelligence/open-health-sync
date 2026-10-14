// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckTypes(t *testing.T) {
	got, err := CheckTypes([]string{DataWritten, UserDeleted, DataWritten})
	require.NoError(t, err)
	assert.Equal(t, []string{DataWritten, UserDeleted}, got, "deduplicated, first order kept")

	got, err = CheckTypes(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{}, got, "an empty list is every type, never null")

	for _, bad := range []string{Test, "data.*", "", "DATA.WRITTEN"} {
		_, err := CheckTypes([]string{DataWritten, bad})
		assert.ErrorIs(t, err, ErrUnknownType, "%q", bad)
	}
	for _, typ := range Types {
		assert.True(t, KnownType(typ), typ)
	}
	assert.False(t, KnownType(Test), "the test event is sent on request only")
}

func TestHumanDuration(t *testing.T) {
	assert.Equal(t, "5 days", humanDuration(DisableAfter))
	assert.Equal(t, "1 days", humanDuration(24*time.Hour))
	assert.Equal(t, "36h0m0s", humanDuration(36*time.Hour))
	assert.Equal(t, "5s", humanDuration(5*time.Second))
}

func TestErrText(t *testing.T) {
	assert.Equal(t, "dial tcp: refused", errText(errors.New("dial tcp: refused")))
	assert.Equal(t, "ab", errText(errors.New("a\x00b")), "no NUL reaches Postgres text")
	assert.Equal(t, "a?b", errText(errors.New("a\xffb")), "invalid UTF-8 replaced")

	long := errText(errors.New(strings.Repeat("x", maxErrorLen+100)))
	assert.Len(t, long, maxErrorLen)

	// A multi-byte rune across the limit is dropped whole, never cut.
	cut := errText(errors.New(strings.Repeat("x", maxErrorLen-1) + "é and more"))
	assert.True(t, utf8.ValidString(cut))
	assert.Equal(t, strings.Repeat("x", maxErrorLen-1), cut)
}
