// SPDX-License-Identifier: AGPL-3.0-only

// Package reqid carries the request id of an API call: the X-Request-Id
// header of the response, the request_id member of a problem body and the
// request_id field of every log line written with the request context (the API puts it on the
// logger of the context, see logging.WithRequestID).
// An operator finds the logs of a failed call from the id its client saw.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Header is the request and response header.
const Header = "X-Request-Id"

const maxLen = 128

type ctxKey struct{}

// With returns ctx carrying id.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// From returns the id ctx carries, or "".
func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Sanitize keeps an incoming id of 1 to 128 characters among letters,
// digits, '-', '_' and '.', so a proxy's id flows through; anything else
// (absent, too long, a character that could forge a log line) is replaced by a
// fresh random id.
func Sanitize(in string) string {
	if len(in) > 0 && len(in) <= maxLen {
		ok := true
		for i := 0; i < len(in); i++ {
			if !idByte(in[i]) {
				ok = false
				break
			}
		}
		if ok {
			return in
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// idByte reports whether c may appear in a kept id.
func idByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}
