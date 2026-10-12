// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

const testToken = "ohs_ut_test_token"

// fakeIngest mimics POST /v1/ingest/apple-health: it checks the headers and
// the body as the real handler does, and keeps one object per
// (day, batch, part).
type fakeIngest struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
	calls   atomic.Int64
	// fail, when set, answers instead of storing.
	fail func(r *http.Request, call int64) (status int, header http.Header, done bool)
	t    *testing.T
}

var (
	batchRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	typeRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func newFakeIngest(t *testing.T) (*fakeIngest, *httptest.Server) {
	f := &fakeIngest{objects: map[string][]byte{}, types: map[string]string{}, t: t}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeIngest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := f.calls.Add(1)
	if r.Method != http.MethodPost || r.URL.Path != "/v1/ingest/apple-health" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.fail != nil {
		if status, h, done := f.fail(r, call); done {
			for k, v := range h {
				w.Header()[k] = v
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"title":"test answer","detail":"status %d"}`, status)
			return
		}
	}
	typ, day, batch, part := r.Header.Get("X-OHS-Type"), r.Header.Get("X-OHS-Day"), r.Header.Get("X-OHS-Batch"), r.Header.Get("X-OHS-Part")
	_, dayErr := time.Parse(time.DateOnly, day)
	n, partErr := strconv.Atoi(part)
	if !typeRe.MatchString(typ) || dayErr != nil || !batchRe.MatchString(batch) || partErr != nil || n < 0 || n > maxPartNumber {
		http.Error(w, "bad header", http.StatusBadRequest)
		return
	}
	if r.Header.Get("Content-Type") != "application/gzip" {
		http.Error(w, "content type", http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(r.Body)
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		http.Error(w, "not gzip", http.StatusBadRequest)
		return
	}
	if err := applehealth.New().ParseUpload(connector.UploadMeta{}, zr, nil); err != nil {
		http.Error(w, "bad line", http.StatusBadRequest)
		return
	}
	key := day + "/" + batch + "_" + part
	f.mu.Lock()
	defer f.mu.Unlock()
	dup := false
	if old, ok := f.objects[key]; ok {
		if !bytes.Equal(old, body) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		dup = true
	}
	f.objects[key] = body
	f.types[key] = typ
	_ = json.NewEncoder(w).Encode(map[string]any{"key": "raw/t/u/apple_health/" + key + ".ndjson.gz", "duplicate": dup})
}

func newUploader(srv *httptest.Server, batch string) *Uploader {
	return &Uploader{
		Endpoint: srv.URL + "/v1/ingest/apple-health", Token: testToken, Batch: batch,
		Backoff: func(int) time.Duration { return time.Millisecond },
	}
}

func runImport(t *testing.T, up *Uploader, opts Options) (Summary, error) {
	t.Helper()
	e, err := Open(fixtureZip(t))
	require.NoError(t, err)
	defer e.Close()
	return Run(context.Background(), e, opts, up, 3, nil)
}

// TestUploadRoundTrip: every part reaches the server with valid headers and
// gzip; the stored bytes decompress to the converted lines; a second run of
// the same export is all duplicates.
func TestUploadRoundTrip(t *testing.T) {
	f, srv := newFakeIngest(t)
	parts, _ := convertFile(t, fixtureZip(t), Options{})

	sum, err := runImport(t, newUploader(srv, "ahx-test"), Options{})
	require.NoError(t, err)
	assert.Equal(t, len(parts), sum.Uploaded)
	assert.Equal(t, 0, sum.Duplicates)
	assert.Equal(t, len(parts), sum.Parts)
	assert.Equal(t, 19, sum.Records)
	assert.Equal(t, "ahx-test", sum.Batch)
	require.Len(t, f.objects, len(parts))
	for _, p := range parts {
		key := fmt.Sprintf("%s/ahx-test_%d", p.Day, p.Number)
		require.Contains(t, f.objects, key)
		assert.Equal(t, p.Type, f.types[key])
		zr, err := gzip.NewReader(bytes.NewReader(f.objects[key]))
		require.NoError(t, err)
		got, err := io.ReadAll(zr)
		require.NoError(t, err)
		assert.Equal(t, p.NDJSON, got)
	}

	again, err := runImport(t, newUploader(srv, "ahx-test"), Options{})
	require.NoError(t, err)
	assert.Equal(t, len(parts), again.Duplicates)
	assert.Len(t, f.objects, len(parts))
}

func TestUploadRetries(t *testing.T) {
	f, srv := newFakeIngest(t)
	var failures atomic.Int64
	f.fail = func(r *http.Request, _ int64) (int, http.Header, bool) {
		// The first three requests fail with 429, 503 and 500.
		switch failures.Add(1) {
		case 1:
			return http.StatusTooManyRequests, http.Header{"Retry-After": {"0"}}, true
		case 2:
			return http.StatusServiceUnavailable, nil, true
		case 3:
			return http.StatusInternalServerError, nil, true
		}
		return 0, nil, false
	}
	parts, _ := convertFile(t, fixtureZip(t), Options{})
	sum, err := runImport(t, newUploader(srv, "ahx-retry"), Options{})
	require.NoError(t, err)
	assert.Equal(t, len(parts), sum.Uploaded)
	assert.Len(t, f.objects, len(parts))
	assert.EqualValues(t, len(parts)+3, f.calls.Load())
}

func TestUploadGivesUpAfterMaxAttempts(t *testing.T) {
	f, srv := newFakeIngest(t)
	f.fail = func(*http.Request, int64) (int, http.Header, bool) { return http.StatusBadGateway, nil, true }
	up := newUploader(srv, "ahx-down")
	up.MaxAttempts = 3
	_, err := up.Upload(context.Background(), Part{Number: 7, Type: "HKQuantityTypeIdentifierHeartRate", Day: "2026-01-10", Lines: 1, NDJSON: []byte("{}\n")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gave up after 3 attempts")
	assert.Contains(t, err.Error(), "HTTP 502")
	assert.EqualValues(t, 3, f.calls.Load())
}

// TestUploadStopsOnAuthError: a refused token stops the whole import at once
// and the error never shows the token.
func TestUploadStopsOnAuthError(t *testing.T) {
	f, srv := newFakeIngest(t)
	up := newUploader(srv, "ahx-auth")
	up.Token = "ohs_ut_wrong_secret"
	sum, err := runImport(t, up, Options{})
	require.ErrorIs(t, err, ErrAuth)
	assert.Contains(t, err.Error(), "HTTP 401")
	assert.NotContains(t, err.Error(), "wrong_secret")
	assert.Equal(t, 0, sum.Uploaded)
	assert.Empty(t, f.objects)
	// Parts already in flight may have been tried, not the whole export.
	assert.LessOrEqual(t, f.calls.Load(), int64(3+3))

	f.fail = func(*http.Request, int64) (int, http.Header, bool) { return http.StatusForbidden, nil, true }
	_, err = runImport(t, newUploader(srv, "ahx-auth"), Options{})
	require.ErrorIs(t, err, ErrAuth)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestUploadConflictNamesTheFix(t *testing.T) {
	_, srv := newFakeIngest(t)
	up := newUploader(srv, "ahx-conflict")
	ctx := context.Background()
	p := Part{Number: 0, Type: "HKQuantityTypeIdentifierHeartRate", Day: "2026-01-10", Lines: 1, NDJSON: []byte(`{"a":1}` + "\n")}
	dup, err := up.Upload(ctx, p)
	require.NoError(t, err)
	assert.False(t, dup)
	dup, err = up.Upload(ctx, p)
	require.NoError(t, err)
	assert.True(t, dup)
	p.NDJSON = []byte(`{"a":2}` + "\n")
	_, err = up.Upload(ctx, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
	assert.Contains(t, err.Error(), "--batch")
}

func TestUploadBadRequestIsNotRetried(t *testing.T) {
	f, srv := newFakeIngest(t)
	f.fail = func(*http.Request, int64) (int, http.Header, bool) { return http.StatusBadRequest, nil, true }
	_, err := newUploader(srv, "ahx-bad").Upload(context.Background(), Part{Type: "X", Day: "2026-01-10", NDJSON: []byte("{}\n")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 400: test answer: status 400")
	assert.EqualValues(t, 1, f.calls.Load())
}

func TestPartNumbersPastTheServerLimitMoveToANewBatch(t *testing.T) {
	up := &Uploader{Batch: "ahx-b"}
	b, n := up.address(999_999)
	assert.Equal(t, "ahx-b", b)
	assert.Equal(t, 999_999, n)
	b, n = up.address(1_000_000)
	assert.Equal(t, "ahx-b-1", b)
	assert.Equal(t, 0, n)
	b, n = up.address(2_000_005)
	assert.Equal(t, "ahx-b-2", b)
	assert.Equal(t, 5, n)
}

func TestDryRunUploadsNothing(t *testing.T) {
	e, err := Open(fixtureZip(t))
	require.NoError(t, err)
	defer e.Close()
	var calls int
	sum, err := Run(context.Background(), e, Options{}, nil, 4, func(Summary) { calls++ })
	require.NoError(t, err)
	assert.Equal(t, 0, sum.Uploaded)
	assert.Equal(t, sum.Parts, calls)
	assert.Equal(t, 19, sum.Records)
}

func TestRunStopsWhenCancelled(t *testing.T) {
	_, srv := newFakeIngest(t)
	e, err := Open(fixtureZip(t))
	require.NoError(t, err)
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Run(ctx, e, Options{}, newUploader(srv, "ahx-cancel"), 2, nil)
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
}
