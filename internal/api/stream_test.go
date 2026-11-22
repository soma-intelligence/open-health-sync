// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// streamRig serves the stream over a real HTTP server, with a hub that
// listens to nothing: the tests publish to it directly.
type streamRig struct {
	g                     *guardServer
	hub                   *StreamHub
	srv                   *httptest.Server
	tenantA, tenantB      uuid.UUID
	keyA, keyB, keyC, key string
}

func newStreamRig(t *testing.T, opts StreamOptions, server ...func(*http.Server)) *streamRig {
	t.Helper()
	r := &streamRig{tenantA: uuid.New(), tenantB: uuid.New(), hub: NewStreamHub(nil, opts)}
	r.hub.setReady(true)
	keyA, hashA := newKey(t, auth.PrefixSecretKey)
	keyB, hashB := newKey(t, auth.PrefixSecretKey)
	keyC, hashC := newKey(t, auth.PrefixSecretKey)
	key, hash := newKey(t, auth.PrefixSecretKey)
	r.keyA, r.keyB, r.keyC, r.key = keyA, keyB, keyC, key
	r.g = newGuardServer(t, map[string]auth.Principal{
		hashA: {TenantID: r.tenantA, KeyID: uuid.NewString(), KeyScopes: []string{auth.ScopeWebhooksRead}},
		hashB: {TenantID: r.tenantB, KeyID: uuid.NewString()},
		hashC: {TenantID: uuid.New(), KeyID: uuid.NewString()},
		// Reads data but not webhooks: refused the tenant stream.
		hash: {TenantID: r.tenantA, KeyID: uuid.NewString(), KeyScopes: []string{auth.ScopeDataRead}},
	}, func(d *Deps) { d.Stream = r.hub; d.RateLimitAPIOff = true })
	r.srv = httptest.NewUnstartedServer(r.g.h)
	for _, f := range server {
		f(r.srv.Config)
	}
	r.srv.Start()
	t.Cleanup(r.srv.Close)
	return r
}

// open connects a stream; the connection ends with the test.
func (r *streamRig) open(t *testing.T, path, secret string) (*http.Response, *bufio.Reader) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := r.srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { res.Body.Close() })
	return res, bufio.NewReader(res.Body)
}

// connect opens a stream that must start, and reads its first two blocks.
func (r *streamRig) connect(t *testing.T, path, secret string) *bufio.Reader {
	t.Helper()
	res, br := r.open(t, path, secret)
	require.Equal(t, http.StatusOK, res.StatusCode)
	f := next(t, br)
	require.Equal(t, StreamRetry, f.retry)
	require.Equal(t, "connected", next(t, br).comment)
	return br
}

// next reads one block within 2 s.
func next(t *testing.T, br *bufio.Reader) sseFrame {
	t.Helper()
	type result struct {
		f   sseFrame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := readSSEFrame(br)
		ch <- result{f, err}
	}()
	select {
	case got := <-ch:
		require.NoError(t, got.err)
		return got.f
	case <-time.After(2 * time.Second):
		t.Fatal("no block within 2 s")
		return sseFrame{}
	}
}

// ends waits for the stream to end.
func ends(t *testing.T, br *bufio.Reader) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := readSSEFrame(br); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream did not end")
	}
}

func event(typ string, user *uuid.UUID, seq int64) streamEvent {
	id := uuid.New()
	return streamEvent{seq: seq, id: id, typ: typ, user: user,
		payload: []byte(`{"id":"` + id.String() + `","type":"` + typ + `","data":{}}`)}
}

func TestStreamDeliversWithFiltersAndTenantIsolation(t *testing.T) {
	r := newStreamRig(t, StreamOptions{})
	alice, bob := uuid.New(), uuid.New()

	res, all := r.open(t, "/v1/events/stream", r.keyA)
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	assert.Equal(t, "no", res.Header.Get("X-Accel-Buffering"))
	assert.Empty(t, res.Header.Get("Content-Encoding"), "an event stream is never compressed")
	assert.Equal(t, StreamRetry, next(t, all).retry)
	assert.Equal(t, "connected", next(t, all).comment)

	tags := r.connect(t, "/v1/events/stream?types=tag.created,tag.deleted&types=user.deleted", r.keyA)
	ofAlice := r.connect(t, "/v1/events/stream?user_id="+alice.String(), r.keyA)
	other := r.connect(t, "/v1/events/stream", r.keyB)

	written := event(webhook.DataWritten, &alice, 1)
	r.hub.publish(r.tenantA, written)
	f := next(t, all)
	assert.Equal(t, sseFrame{id: written.id.String(), event: webhook.DataWritten, data: string(written.payload)}, f)
	assert.Equal(t, written.id.String(), next(t, ofAlice).id)

	tag := event(webhook.TagCreated, &bob, 2)
	r.hub.publish(r.tenantA, tag)
	assert.Equal(t, tag.id.String(), next(t, all).id)
	assert.Equal(t, tag.id.String(), next(t, tags).id, "data.written was filtered out")

	gone := event(webhook.UserDeleted, &alice, 3)
	r.hub.publish(r.tenantA, gone)
	assert.Equal(t, gone.id.String(), next(t, tags).id)
	assert.Equal(t, gone.id.String(), next(t, ofAlice).id, "bob's tag was filtered out")

	// Tenant B saw none of A's events: its first event is its own.
	mine := event(webhook.DataDeleted, nil, 4)
	r.hub.publish(r.tenantB, mine)
	assert.Equal(t, mine.id.String(), next(t, other).id)
	r.hub.publish(r.tenantB, event(webhook.DataDeleted, nil, 5))
	assert.Equal(t, gone.id.String(), next(t, all).id, "A never gets B's events")
}

func TestStreamRefusals(t *testing.T) {
	r := newStreamRig(t, StreamOptions{})
	for path, want := range map[string]int{
		"/v1/events/stream?types=data.written,nope": http.StatusBadRequest,
		"/v1/events/stream?types=webhook.test":      http.StatusBadRequest,
		"/v1/events/stream?user_id=42":              http.StatusBadRequest,
	} {
		rec := r.g.get(path, r.keyA, "")
		assert.Equal(t, want, rec.Code, path)
	}
	rec := r.g.get("/v1/events/stream", r.key, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, "data:read is not webhooks:read")
	assert.Equal(t, http.StatusUnauthorized, r.g.get("/v1/events/stream", "", "").Code)
	assert.Zero(t, r.hub.Open())

	k, h := newKey(t, auth.PrefixSecretKey)
	off := newGuardServer(t, map[string]auth.Principal{h: {TenantID: uuid.New(), KeyID: uuid.NewString()}})
	rec = off.get("/v1/events/stream", k, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "OHS_STREAM_MAX=0")
	_, detail := problemOf(t, rec)
	assert.Contains(t, detail, "OHS_STREAM_MAX=0")
}

func TestStreamHeartbeat(t *testing.T) {
	r := newStreamRig(t, StreamOptions{Heartbeat: 30 * time.Millisecond})
	br := r.connect(t, "/v1/events/stream", r.keyA)
	assert.Equal(t, "ping", next(t, br).comment)
	assert.Equal(t, "ping", next(t, br).comment)
}

func TestStreamCaps(t *testing.T) {
	r := newStreamRig(t, StreamOptions{Max: 2, MaxPerTenant: 1})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+"/v1/events/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+r.keyA)
	first, err := r.srv.Client().Do(req)
	require.NoError(t, err)
	defer first.Body.Close()
	require.Equal(t, http.StatusOK, first.StatusCode)

	res, _ := r.open(t, "/v1/events/stream", r.keyA)
	assert.Equal(t, http.StatusTooManyRequests, res.StatusCode, "one stream per tenant")
	assert.Equal(t, "5", res.Header.Get("Retry-After"))
	r.connect(t, "/v1/events/stream", r.keyB)
	res, _ = r.open(t, "/v1/events/stream", r.keyC)
	assert.Equal(t, http.StatusTooManyRequests, res.StatusCode, "two streams in the process")
	assert.Equal(t, 2, r.hub.Open())

	// A closed stream frees its slot.
	cancel()
	require.Eventually(t, func() bool { return r.hub.Open() == 1 }, 2*time.Second, 10*time.Millisecond)
	r.connect(t, "/v1/events/stream", r.keyA)
}

func TestStreamHubDropsAFullQueue(t *testing.T) {
	h := NewStreamHub(nil, StreamOptions{Buffer: 2})
	h.setReady(true)
	tenant := uuid.New()
	slow, err := h.subscribe(tenant, nil, nil)
	require.NoError(t, err)
	picky, err := h.subscribe(tenant, nil, map[string]bool{webhook.TagCreated: true})
	require.NoError(t, err)
	for i := range 3 {
		h.publish(tenant, event(webhook.DataWritten, nil, int64(i+1)))
	}
	select {
	case <-slow.done:
		assert.Equal(t, streamEndSlow, slow.reason)
	default:
		t.Fatal("a stream that does not drain its queue is dropped")
	}
	select {
	case <-picky.done:
		t.Fatal("a stream the events did not match keeps going")
	default:
	}
	h.unsubscribe(slow)
	h.unsubscribe(picky)
	assert.Zero(t, h.Open())
}

// stuckWriter is a client that read the first write and then stopped: every
// later write blocks until the write deadline, as on a full TCP window.
type stuckWriter struct {
	header http.Header
	mu     sync.Mutex
	writes int
	until  time.Time
}

func (w *stuckWriter) Header() http.Header { return w.header }
func (w *stuckWriter) WriteHeader(int)     {}
func (w *stuckWriter) Flush()              {}
func (w *stuckWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	n, until := w.writes, w.until
	w.mu.Unlock()
	if n == 1 {
		return len(b), nil
	}
	time.Sleep(time.Until(until))
	return 0, os.ErrDeadlineExceeded
}
func (w *stuckWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.until = t
	return nil
}
func (w *stuckWriter) SetReadDeadline(time.Time) error { return nil }

func TestStreamSlowClientIsDropped(t *testing.T) {
	r := newStreamRig(t, StreamOptions{Buffer: 4, WriteTimeout: 100 * time.Millisecond})
	p := auth.Principal{TenantID: r.tenantA}
	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := &stuckWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.g.s.serveStream(w, req, p, nil)
	}()
	require.Eventually(t, func() bool { return r.hub.Open() == 1 }, time.Second, 5*time.Millisecond)
	// The client never reads again: the handler blocks on the first event,
	// then the queue fills.
	for i := range 10 {
		r.hub.publish(r.tenantA, event(webhook.DataWritten, nil, int64(i+1)))
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a client that stopped reading holds its stream")
	}
	assert.Zero(t, r.hub.Open(), "its slot is free again")
}

func TestStreamShutdownClosesStreams(t *testing.T) {
	r := newStreamRig(t, StreamOptions{})
	br := r.connect(t, "/v1/events/stream", r.keyA)
	r.hub.Close()
	ends(t, br)
	res, _ := r.open(t, "/v1/events/stream", r.keyA)
	assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode, "no new stream once shutting down")

	// The same through http.Server.Shutdown, as internal/app wires it.
	r = newStreamRig(t, StreamOptions{})
	r.srv.Config.RegisterOnShutdown(r.hub.Close)
	br = r.connect(t, "/v1/events/stream", r.keyA)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, r.srv.Config.Shutdown(ctx))
	assert.Less(t, time.Since(start), 2*time.Second, "the stream does not hold the shutdown")
	ends(t, br)
}

func TestStreamListenerLostEndsStreams(t *testing.T) {
	r := newStreamRig(t, StreamOptions{})
	br := r.connect(t, "/v1/events/stream", r.keyA)
	r.hub.setReady(false)
	ends(t, br)
	res, _ := r.open(t, "/v1/events/stream", r.keyA)
	assert.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	assert.NotEmpty(t, res.Header.Get("Retry-After"))
}

// The server's read timeout (OHS's 2 minutes, 150 ms here) does not end a
// stream, nor does a write timeout: each write sets its own deadline.
func TestStreamOutlivesServerTimeouts(t *testing.T) {
	r := newStreamRig(t, StreamOptions{Heartbeat: 40 * time.Millisecond}, func(s *http.Server) {
		s.ReadTimeout, s.WriteTimeout = 150*time.Millisecond, 150*time.Millisecond
	})
	br := r.connect(t, "/v1/events/stream", r.keyA)
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		assert.Equal(t, "ping", next(t, br).comment)
	}
	ev := event(webhook.DataWritten, nil, 1)
	r.hub.publish(r.tenantA, ev)
	for {
		f := next(t, br)
		if f.comment == "" {
			assert.Equal(t, ev.id.String(), f.id)
			break
		}
	}
}

func TestStreamMaxAge(t *testing.T) {
	r := newStreamRig(t, StreamOptions{MaxAge: 100 * time.Millisecond})
	br := r.connect(t, "/v1/events/stream", r.keyA)
	ends(t, br)
	require.Eventually(t, func() bool { return r.hub.Open() == 0 }, time.Second, 5*time.Millisecond)
}

func TestParseStreamTypes(t *testing.T) {
	set, list, err := parseStreamTypes(nil)
	require.NoError(t, err)
	assert.Nil(t, set)
	assert.Nil(t, list)
	set, list, err = parseStreamTypes([]string{" data.written , tag.created", "data.written", ""})
	require.NoError(t, err)
	assert.Equal(t, []string{"data.written", "tag.created"}, list)
	assert.Equal(t, map[string]bool{"data.written": true, "tag.created": true}, set)
	_, _, err = parseStreamTypes([]string{"data.*"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"data.*"`)
}
