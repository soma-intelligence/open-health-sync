// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"compress/gzip"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// compressMinBytes is the smallest body worth compressing: below it the
// gzip framing and the CPU cost more than they save.
const compressMinBytes = 1024

// compressible are the media types gzip is applied to: the text formats of
// the API. Parquet files and zip archives are compressed already, and an
// event stream must reach the client line by line.
var compressible = map[string]bool{
	"application/json":         true,
	"application/problem+json": true,
	"application/fhir+json":    true,
	"application/x-ndjson":     true,
	"application/fhir+ndjson":  true,
	"application/yaml":         true,
	"text/csv":                 true,
}

var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	return zw
}}

// compress gzips the responses of next for a client that sends
// Accept-Encoding: gzip, when the body is one of the compressible types, at
// least compressMinBytes long and not encoded already. Every response says
// Vary: Accept-Encoding, compressed or not. A handler that flushes keeps
// streaming: a flush sends what was written so far, compressed.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				// A handler that gave up (an export cut short): no gzip
				// trailer, so the client never sees a body that looks whole.
				cw.release()
				panic(v)
			}
			cw.finish()
		}()
		next.ServeHTTP(cw, r)
	})
}

// acceptsGzip reports whether an Accept-Encoding value takes gzip: named, or
// covered by "*", with a quality above zero.
func acceptsGzip(header string) bool {
	star := false
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding != "gzip" && coding != "x-gzip" && coding != "*" {
			continue
		}
		ok := true
		if q, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(params)), "q="); found {
			v, err := strconv.ParseFloat(strings.TrimSpace(q), 64)
			ok = err == nil && v > 0
		}
		if coding != "*" {
			return ok
		}
		star = ok
	}
	return star
}

// compressWriter holds the start of a response until it knows whether to
// compress it: compressMinBytes written, a flush, or the end of the handler
// (a short body, sent as it is).
type compressWriter struct {
	http.ResponseWriter
	code    int
	buf     []byte
	decided bool
	zw      *gzip.Writer // set when the response is compressed
}

func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) WriteHeader(code int) {
	if c.decided || c.code != 0 {
		return
	}
	if code < 200 {
		// An informational response goes out at once; the final one follows.
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.code = code
}

func (c *compressWriter) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if !c.decided {
		if len(c.buf)+len(b) < compressMinBytes {
			c.buf = append(c.buf, b...)
			return len(b), nil
		}
		if err := c.decide(true); err != nil {
			return 0, err
		}
	}
	if c.zw != nil {
		return c.zw.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// decide sends the header and what was held back, compressed when the
// response qualifies and enough is true (the body is long enough, or it is a
// stream being flushed).
func (c *compressWriter) decide(enough bool) error {
	c.decided = true
	if c.code == 0 {
		c.code = http.StatusOK
	}
	h := c.Header()
	if enough && c.eligible() {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		// The compressed bytes are not the ones a validator of the plain
		// body names.
		if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			h.Set("ETag", "W/"+etag)
		}
		c.zw = gzipWriters.Get().(*gzip.Writer)
		c.zw.Reset(c.ResponseWriter)
	}
	c.ResponseWriter.WriteHeader(c.code)
	buf := c.buf
	c.buf = nil
	if len(buf) == 0 {
		return nil
	}
	var err error
	if c.zw != nil {
		_, err = c.zw.Write(buf)
	} else {
		_, err = c.ResponseWriter.Write(buf)
	}
	return err
}

func (c *compressWriter) eligible() bool {
	if c.code == http.StatusNoContent || c.code == http.StatusNotModified || c.code == http.StatusPartialContent {
		return false
	}
	h := c.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && compressible[mt]
}

// FlushError sends what was written so far (http.ResponseController and
// http.Flusher reach it). A response flushed before compressMinBytes is a
// stream: it is compressed whatever its length so far.
func (c *compressWriter) FlushError() error {
	if !c.decided {
		if err := c.decide(true); err != nil {
			return err
		}
	}
	if c.zw != nil {
		if err := c.zw.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Flush is FlushError for a handler that asks for an http.Flusher.
func (c *compressWriter) Flush() { _ = c.FlushError() }

// finish ends the response: a body shorter than compressMinBytes goes out
// as it is, a compressed one gets its trailer. A handler that wrote nothing
// and set no status is left to net/http.
func (c *compressWriter) finish() {
	if !c.decided {
		if c.code == 0 && len(c.buf) == 0 {
			return
		}
		_ = c.decide(false)
	}
	if c.zw != nil {
		_ = c.zw.Close()
		c.release()
	}
}

// release gives the gzip writer back without ending its stream.
func (c *compressWriter) release() {
	if c.zw != nil {
		c.zw.Reset(nil)
		gzipWriters.Put(c.zw)
		c.zw = nil
	}
}
