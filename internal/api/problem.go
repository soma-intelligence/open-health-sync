// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/pgtext"
)

// maxJSONBody caps a JSON request body.
const maxJSONBody = 1 << 20

func problem(w http.ResponseWriter, status int, detail string) {
	auth.WriteProblem(w, status, http.StatusText(status), detail)
}

func badRequest(w http.ResponseWriter, detail string) { problem(w, http.StatusBadRequest, detail) }

func notFound(w http.ResponseWriter, what string) {
	problem(w, http.StatusNotFound, what+" not found")
}

// internalError logs the cause (never sent) and answers 500, or 503 with
// Retry-After when the database could not be reached: that request can be
// retried as is.
func internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.ErrorContext(r.Context(), msg, "error", err, "route", r.Pattern)
	if db.Unavailable(err) {
		// The cause may be the raw store's network as well as the database's.
		auth.WriteUnavailable(w, "storage is unavailable; retry in a few seconds")
		return
	}
	problem(w, http.StatusInternalServerError, "an internal error occurred")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON reads a JSON object body into v. An empty body leaves v as is
// when allowEmpty. It writes the problem and returns false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		problem(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return false
	} else if err != nil {
		badRequest(w, "the request body could not be read")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	err = dec.Decode(v)
	if errors.Is(err, io.EOF) && allowEmpty {
		return true
	}
	if err != nil {
		badRequest(w, "the request body is not valid JSON for this endpoint: "+jsonErrDetail(err))
		return false
	}
	if dec.More() {
		badRequest(w, "the request body holds more than one JSON value")
		return false
	}
	// Go's decoder already turns invalid UTF-8 into U+FFFD; a \u0000 escape
	// survives and Postgres refuses it in text.
	if name, ok := nulFree(body); !ok {
		where := "a string in the request body"
		if name != "" {
			where = "field " + strconv.Quote(name)
		}
		badRequest(w, where+" holds a NUL character (\\u0000), which is not accepted")
		return false
	}
	return true
}

// nulFree scans a JSON value Decode already accepted and reports false with
// the nearest member name when a string, member names included, holds a NUL.
func nulFree(body []byte) (name string, ok bool) {
	if !bytes.Contains(body, []byte(`u0000`)) {
		return "", true
	}
	type frame struct{ object, atName bool }
	var stack []frame
	dec := json.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", true
		}
		var top *frame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		if d, isDelim := tok.(json.Delim); isDelim {
			switch d {
			case '{', '[':
				stack = append(stack, frame{object: d == '{', atName: d == '{'})
			default:
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].object {
					stack[len(stack)-1].atName = true
				}
			}
			continue
		}
		str, isString := tok.(string)
		if top != nil && top.object {
			if top.atName {
				name = str
			}
			top.atName = !top.atName
		}
		if isString && strings.ContainsRune(str, 0) {
			return pgtext.Clean(name), false
		}
	}
}

// textOK refuses a request whose path or a query name or value holds a NUL
// character or invalid UTF-8 (raw or percent-encoded): Postgres refuses such
// text, so the request would otherwise fail with a 500 in the first query.
// It runs before routing and authentication.
func textOK(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !pgtext.Valid(r.URL.Path) {
			badRequest(w, "the request path holds a NUL character or invalid UTF-8")
			return
		}
		for name, values := range r.URL.Query() {
			ok := pgtext.Valid(name)
			for _, v := range values {
				ok = ok && pgtext.Valid(v)
			}
			if !ok {
				badRequest(w, "query parameter "+strconv.Quote(pgtext.Clean(name))+" holds a NUL character or invalid UTF-8")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func jsonErrDetail(err error) string {
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) && typ.Field != "" {
		return "field " + typ.Field + " has the wrong type"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected end of input"
	}
	// encoding/json names the unknown field; nothing from the body beyond it.
	return err.Error()
}
