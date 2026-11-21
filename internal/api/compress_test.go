// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(out)
}

func serveCompressed(h http.HandlerFunc, method, acceptEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	compress(h).ServeHTTP(rec, req)
	return rec
}

func bodyOf(contentType string, n int) (string, http.HandlerFunc) {
	body := strings.Repeat(`{"value":72,"unit":"count/min"},`, n/32+1)[:n]
	return body, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", "12345")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
}

func TestCompressTextFormats(t *testing.T) {
	for _, ct := range []string{"application/json", "application/problem+json", "application/fhir+json; charset=utf-8",
		"application/x-ndjson", "text/csv", "application/yaml"} {
		body, h := bodyOf(ct, 8000)
		rec := serveCompressed(h, "GET", "br, gzip")
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), ct)
		assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"), ct)
		assert.Empty(t, rec.Header().Get("Content-Length"), ct)
		assert.Less(t, rec.Body.Len(), len(body)/4, ct)
		assert.Equal(t, body, gunzip(t, rec.Body.Bytes()), ct)
	}
}

func TestCompressLeavesTheRestAlone(t *testing.T) {
	body, jsonBody := bodyOf("application/json", 8000)
	plain := func(name string, rec *httptest.ResponseRecorder, want string) {
		t.Helper()
		assert.Empty(t, rec.Header().Get("Content-Encoding"), name)
		assert.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"), name)
		assert.Equal(t, want, rec.Body.String(), name)
	}
	plain("no Accept-Encoding", serveCompressed(jsonBody, "GET", ""), body)
	plain("gzip refused", serveCompressed(jsonBody, "GET", "gzip;q=0, identity"), body)
	plain("another coding", serveCompressed(jsonBody, "GET", "br"), body)

	small, smallBody := bodyOf("application/json", compressMinBytes-1)
	rec := serveCompressed(smallBody, "GET", "gzip")
	plain("a small body", rec, small)
	assert.Equal(t, "12345", rec.Header().Get("Content-Length"), "the handler's header is kept")

	for _, ct := range []string{"application/vnd.apache.parquet", "application/zip", "application/octet-stream", "text/event-stream", ""} {
		b, h := bodyOf(ct, 8000)
		plain("type "+ct, serveCompressed(h, "GET", "gzip"), b)
	}

	rec = serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, body)
	}, "GET", "gzip")
	assert.Equal(t, "br", rec.Header().Get("Content-Encoding"), "already encoded")
	assert.Equal(t, body, rec.Body.String(), "already encoded")

	rec = serveCompressed(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, "DELETE", "gzip")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Zero(t, rec.Body.Len())

	rec = serveCompressed(func(http.ResponseWriter, *http.Request) {}, "GET", "gzip")
	assert.Equal(t, http.StatusOK, rec.Code, "a handler that writes nothing")
	assert.Zero(t, rec.Body.Len())

	rec = serveCompressed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"status":404}`)
	}, "GET", "gzip")
	assert.Equal(t, http.StatusNotFound, rec.Code, "the status of a short body is kept")
	assert.Equal(t, `{"status":404}`, rec.Body.String())
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip": true, "GZIP": true, "deflate, gzip;q=0.5": true, "*": true, "br, *;q=0.1": true, "x-gzip": true,
		"": false, "identity": false, "gzip;q=0": false, "gzip; q=0.0": false, "*;q=0": false, "gzip;q=0, *": false,
	} {
		assert.Equal(t, want, acceptsGzip(header), header)
	}
}

// A handler that flushes keeps streaming: each flush reaches the client,
// compressed, before the handler ends.
func TestCompressKeepsStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	var afterFirst int
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		rc := http.NewResponseController(w)
		_, _ = io.WriteString(w, "id,value\n")
		require.NoError(t, rc.Flush())
		afterFirst = rec.Body.Len()
		_, _ = io.WriteString(w, "1,72\n")
		require.NoError(t, rc.Flush())
	}))
	// The statusWriter of the router sits outside: the controller must
	// still find the flush through it.
	h.ServeHTTP(&statusWriter{ResponseWriter: rec}, req)
	assert.Positive(t, afterFirst, "the first rows left before the handler ended")
	assert.True(t, rec.Flushed)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Equal(t, "id,value\n1,72\n", gunzip(t, rec.Body.Bytes()))
}

// A handler that aborts leaves a truncated stream, never a valid gzip file.
func TestCompressAbortLeavesNoTrailer(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	body, _ := bodyOf("text/csv", 8000)
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, body)
		panic(http.ErrAbortHandler)
	}))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() { h.ServeHTTP(rec, req) })
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err == nil {
		_, err = io.ReadAll(zr)
	}
	assert.Error(t, err, "the stream must not read as complete")
}

func TestRouterCompressesTheOpenAPIDocument(t *testing.T) {
	h := offlineRouter(t)
	req := httptest.NewRequest("GET", "/openapi.yaml", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Contains(t, gunzip(t, rec.Body.Bytes()), "openapi: 3.")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.yaml", nil))
	assert.Empty(t, rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Body.String(), "openapi: 3.")
}
