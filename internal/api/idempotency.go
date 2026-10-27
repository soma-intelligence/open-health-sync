// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/reqid"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

const (
	// HeaderIdempotencyKey is the request header honoured on POST.
	HeaderIdempotencyKey = "Idempotency-Key"
	// HeaderIdempotentReplayed is "true" on a response replayed from storage.
	HeaderIdempotentReplayed = "Idempotent-Replayed"
	// maxStoredResponse caps a stored response. A larger one is sent but not
	// stored, so a retry runs the request again.
	maxStoredResponse = 64 << 10
)

// requestHash fingerprints a request: SHA-256 of method, path and body.
func requestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte("\n"))
	h.Write([]byte(path))
	h.Write([]byte("\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// capture buffers a handler's response so it can be stored, then sent.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }
func (c *capture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(b)
}
func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

func (c *capture) sendTo(w http.ResponseWriter) {
	for k, v := range c.header {
		w.Header()[k] = v
	}
	w.WriteHeader(c.status)
	_, _ = w.Write(c.body.Bytes())
}

// withRequestID sets request_id in a JSON object body, other members kept
// as they are. A body that is not a JSON object, or an empty id, is returned
// unchanged.
func withRequestID(body []byte, id string) []byte {
	if id == "" {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	v, err := json.Marshal(id)
	if err != nil {
		return body
	}
	m["request_id"] = v
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return append(out, '\n')
}

type panicValue struct{ v any }

// errPanicked marks a do closure that panicked.
var errPanicked = errors.New("api: handler panicked")

// errNotStored carries a response too large to store out of Store.Idempotent.
var errNotStored = errors.New("api: response not stored")

// idempotent honours Idempotency-Key on a tenant route: the first call runs
// next and stores its status and body for 24 hours, a retry with the same key
// and the same request gets the stored response back. Without the header next
// runs as is. It must sit behind the auth middleware (the key is per tenant).
//
// Never wrap a route whose response holds a secret: the stored body is plain.
func (s *server) idempotent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(HeaderIdempotencyKey)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, ok := auth.From(r.Context())
		if !ok {
			problem(w, http.StatusUnauthorized, "a valid API key is required")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			problem(w, http.StatusRequestEntityTooLarge, "the request body is too large")
			return
		case err != nil:
			badRequest(w, "the request body could not be read")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		// The handler may name the user its response is about, so the stored
		// response goes when that user is deleted.
		ctx := tenant.WithIdempotencySubject(r.Context())
		r = r.WithContext(ctx)

		var ran, big *capture
		var pv *panicValue
		status, stored, err := s.d.Store.Idempotent(ctx, p.TenantID, key, requestHash(r.Method, r.URL.Path, body),
			func() (_ int, _ []byte, err error) {
				// A panic is turned into an error so Idempotent releases the
				// claim, then re-raised below: a retry must not see 409.
				defer func() {
					if v := recover(); v != nil {
						pv = &panicValue{v}
						err = errPanicked
					}
				}()
				// The captured header carries the request id, so a problem
				// written by the handler names it like any other.
				c := &capture{header: http.Header{reqid.Header: {reqid.From(r.Context())}}}
				next.ServeHTTP(c, r)
				if c.status == 0 {
					c.status = http.StatusOK
				}
				if c.body.Len() > maxStoredResponse {
					big = c
					return 0, nil, errNotStored
				}
				ran = c
				return c.status, c.body.Bytes(), nil
			})
		if pv != nil {
			panic(pv.v)
		}
		switch {
		case errors.Is(err, errNotStored):
			big.sendTo(w)
		case errors.Is(err, tenant.ErrIdempotencyKeyInvalid):
			badRequest(w, "the Idempotency-Key header must be 1 to 255 characters of UTF-8 text")
		case errors.Is(err, tenant.ErrIdempotencyMismatch):
			problem(w, http.StatusUnprocessableEntity, "this Idempotency-Key was used with a different request")
		case errors.Is(err, tenant.ErrIdempotencyInProgress):
			problem(w, http.StatusConflict, "a request with this Idempotency-Key is still in progress")
		case err != nil:
			internalError(w, r, "api: idempotency", err)
		case ran != nil:
			ran.sendTo(w)
		default:
			// Replayed from storage: only the status and body were kept.
			ct := "application/json"
			if status >= 400 {
				ct = "application/problem+json"
			}
			if status >= 400 {
				// A stored problem names the first call's id: answer with
				// this request's, the one in X-Request-Id.
				stored = withRequestID(stored, reqid.From(r.Context()))
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set(HeaderIdempotentReplayed, "true")
			w.WriteHeader(status)
			_, _ = w.Write(stored)
		}
	})
}
