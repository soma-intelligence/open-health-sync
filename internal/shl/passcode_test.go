// SPDX-License-Identifier: AGPL-3.0-only

package shl

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPasscodeRules(t *testing.T) {
	for _, ok := range []string{"123456", strings.Repeat("x", MaxPasscode), "correct horse", "été-2026",
		strings.Repeat("é", MaxPasscode)} {
		assert.NoError(t, CheckPasscodeRules(ok), ok)
	}
	for name, bad := range map[string]string{
		"empty":           "",
		"five":            "12345",
		"five characters": "ééééé",
		"65":              strings.Repeat("x", MaxPasscode+1),
		"new line":        "abc\n123",
		"NUL":             "abcdef\x00",
		"C1 control":      "abc\u0085def",
		"not UTF-8":       "abcdef\xff",
	} {
		assert.Error(t, CheckPasscodeRules(bad), name)
	}
}

func TestPasscodeHash(t *testing.T) {
	const pass = "invented-passcode"
	h, err := HashPasscode(pass)
	require.NoError(t, err)
	assert.NotContains(t, h, pass)

	parts := strings.Split(h, "$")
	require.Len(t, parts, 4)
	assert.Equal(t, "pbkdf2-sha256", parts[0])
	assert.Equal(t, "600000", parts[1])
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	assert.Len(t, salt, 16)
	dk, err := base64.RawStdEncoding.DecodeString(parts[3])
	require.NoError(t, err)
	// PBKDF2-SHA256 with the stated parameters, nothing else.
	want, err := pbkdf2.Key(sha256.New, pass, salt, 600000, 32)
	require.NoError(t, err)
	assert.Equal(t, want, dk)

	assert.True(t, VerifyPasscode(h, pass))
	assert.False(t, VerifyPasscode(h, "invented-passcodE"))
	assert.False(t, VerifyPasscode(h, ""))
	assert.False(t, VerifyPasscode(h, pass+" "))

	// A fresh salt each time.
	h2, err := HashPasscode(pass)
	require.NoError(t, err)
	assert.NotEqual(t, h, h2)
	assert.True(t, VerifyPasscode(h2, pass))

	// A stored value that is not one of ours never verifies.
	for _, bad := range []string{"", "plain", "pbkdf2-sha256$100000$" + parts[2],
		"pbkdf2-sha1$100000$" + parts[2] + "$" + parts[3],
		"pbkdf2-sha256$1$" + parts[2] + "$" + parts[3],
		"pbkdf2-sha256$99999999$" + parts[2] + "$" + parts[3],
		"pbkdf2-sha256$100000$***$" + parts[3],
		"pbkdf2-sha256$100000$" + parts[2] + "$" + parts[3][:10],
	} {
		assert.False(t, VerifyPasscode(bad, pass), bad)
	}
}

func TestRemainingAttempts(t *testing.T) {
	assert.Equal(t, 10, MaxPasscodeFailures)
	assert.Equal(t, 10, RemainingAttempts(0))
	assert.Equal(t, 1, RemainingAttempts(9))
	assert.Equal(t, 0, RemainingAttempts(10))
	assert.Equal(t, 0, RemainingAttempts(11))
}

// A passcode hashed by a release that used 100 000 iterations still
// verifies: the count is read from the stored hash.
func TestPasscodeVerifiesOlderIterationCount(t *testing.T) {
	const pass = "invented-passcode"
	salt := []byte("0123456789abcdef")
	dk, err := pbkdf2.Key(sha256.New, pass, salt, 100000, 32)
	require.NoError(t, err)
	enc := base64.RawStdEncoding
	stored := "pbkdf2-sha256$100000$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(dk)
	assert.True(t, VerifyPasscode(stored, pass))
	assert.False(t, VerifyPasscode(stored, pass+"x"))
	// The same key filed under another count does not.
	assert.False(t, VerifyPasscode("pbkdf2-sha256$600000$"+enc.EncodeToString(salt)+"$"+enc.EncodeToString(dk), pass))
}
