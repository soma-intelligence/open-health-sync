// SPDX-License-Identifier: AGPL-3.0-only

// Package reqid carries the request id of an API call: the X-Request-Id
// header of the response, the request_id member of a problem body and the
// request_id attribute of every log line written with the request context.
// An operator finds the logs of a failed call from the id its client saw.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
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
			c := in[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
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

// Handler wraps h so that a record logged with a context carrying an id gets
// a request_id attribute.
func Handler(h slog.Handler) slog.Handler { return handler{h} }

type handler struct{ slog.Handler }

func (h handler) Handle(ctx context.Context, r slog.Record) error {
	if id := From(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h handler) WithAttrs(as []slog.Attr) slog.Handler { return handler{h.Handler.WithAttrs(as)} }
func (h handler) WithGroup(name string) slog.Handler    { return handler{h.Handler.WithGroup(name)} }
