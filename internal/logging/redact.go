package logging

import (
	"strconv"
	"unicode/utf8"
)

// Redact returns a form of a secret that is safe to log: enough to tell two
// secrets apart, never enough to use one. Empty gives "", a secret of 8
// characters or fewer gives only its length, a longer one keeps its first 4
// characters.
func Redact(secret string) string {
	n := utf8.RuneCountInString(secret)
	if n == 0 {
		return ""
	}
	tail := "…(" + strconv.Itoa(n) + ")"
	if n <= 8 {
		return tail
	}
	return string([]rune(secret)[:4]) + tail
}
