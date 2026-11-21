// SPDX-License-Identifier: AGPL-3.0-only

package shl

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Passcode rules.
const (
	// MinPasscode and MaxPasscode bound a passcode, in characters.
	MinPasscode = 6
	MaxPasscode = 64
	// MaxPasscodeFailures is the lifetime count of wrong passcodes a share
	// takes; the last one disables it.
	MaxPasscodeFailures = 10
)

// The passcode hash: PBKDF2 with HMAC-SHA256. The iteration count is part of
// each stored hash, so raising passcodeIterations only changes new passcodes:
// a hash stored with an older count (100 000 before 0.3.0) still verifies.
const (
	passcodeScheme     = "pbkdf2-sha256"
	passcodeIterations = 600000
	passcodeSalt       = 16
	passcodeKey        = 32
	// A stored hash outside these iteration counts is not one of ours.
	minIterations = 10000
	maxIterations = 1000000
)

// CheckPasscodeRules refuses a passcode that is not 6 to 64 characters of
// UTF-8 without control characters.
func CheckPasscodeRules(p string) error {
	if !utf8.ValidString(p) {
		return errors.New("passcode must be UTF-8 text")
	}
	if n := utf8.RuneCountInString(p); n < MinPasscode || n > MaxPasscode {
		return fmt.Errorf("passcode must be %d to %d characters", MinPasscode, MaxPasscode)
	}
	if strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return errors.New("passcode must not hold control characters")
	}
	return nil
}

// HashPasscode returns what the database keeps of a passcode:
// "pbkdf2-sha256$<iterations>$<salt>$<key>", salt and key in base64.
func HashPasscode(p string) (string, error) {
	salt := make([]byte, passcodeSalt)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("shl: salt: %w", err)
	}
	dk, err := pbkdf2.Key(sha256.New, p, salt, passcodeIterations, passcodeKey)
	if err != nil {
		return "", fmt.Errorf("shl: passcode hash: %w", err)
	}
	enc := base64.RawStdEncoding
	return passcodeScheme + "$" + strconv.Itoa(passcodeIterations) + "$" + enc.EncodeToString(salt) + "$" +
		enc.EncodeToString(dk), nil
}

// VerifyPasscode reports whether p is the passcode stored hashes, comparing
// in constant time. A stored value of another shape never verifies.
func VerifyPasscode(stored, p string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != passcodeScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < minIterations || iter > maxIterations {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(salt) != passcodeSalt || len(want) != passcodeKey {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, p, salt, iter, passcodeKey)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// RemainingAttempts is how many wrong passcodes a share still takes after
// failures of them.
func RemainingAttempts(failures int) int { return max(MaxPasscodeFailures-failures, 0) }
