package httplog

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader is read on the way in and echoed on the way out, so a client
// that supplies one can quote it in a bug report and land on a server line.
const RequestIDHeader = "X-Request-Id"

// maxRequestIDLen bounds what a client can push into every log line of its
// request.
const maxRequestIDLen = 64

// requestIDFromRequest reuses a valid incoming id or mints one.
//
// hlog.RequestIDHandler is deliberately not used here. It builds its id from
// IDFromRequest, which reads only the context and never the headers, so it
// always mints a fresh one; and its id type is xid.ID, which cannot hold a
// client-supplied id. The minted id is 16 random bytes in hex, from the
// standard library, so this package needs no uuid dependency.
func requestIDFromRequest(r *http.Request) string {
	if id := sanitizeRequestID(r.Header.Get(RequestIDHeader)); id != "" {
		return id
	}
	return newRequestID()
}

// sanitizeRequestID accepts a client value only when it is short and made of
// unreserved characters. The value goes verbatim into a JSON log line and into
// a response header, so a quote, a brace or a newline would let a caller forge
// fields in our own logs or split the header.
func sanitizeRequestID(s string) string {
	if s == "" || len(s) > maxRequestIDLen {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return ""
		}
	}
	return s
}

// newRequestID returns 16 random bytes as 32 hex characters.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms. A fixed id keeps
		// the request served rather than panicking in the middleware.
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
