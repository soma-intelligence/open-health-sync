// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/selfreport"
	"github.com/soma-intelligence/open-health-sync/internal/api"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// streamEnv serves the API over a real HTTP server with a listening event
// stream hub, and the raw store the ETL reads uploads back from.
type streamEnv struct {
	*srEnv
	hub *api.StreamHub
	srv *httptest.Server
}

func newStreamEnv(t *testing.T, opts api.StreamOptions) *streamEnv {
	t.Helper()
	var raw rawstore.Store
	var hub *api.StreamHub
	e := newEnv(t, func(d *api.Deps) {
		d.SelfReport = selfreport.New()
		d.PublicURL = publicURL + "/"
		raw = d.Raw
		hub = api.NewStreamHub(d.Pool, opts)
		d.Stream = hub
	})
	srv := httptest.NewServer(e.handler)
	t.Cleanup(srv.Close) // after the hub stopped, which ends the streams
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = hub.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	require.Eventually(t, hub.Ready, 5*time.Second, 10*time.Millisecond, "the hub listens")
	return &streamEnv{srEnv: &srEnv{env: e, raw: raw}, hub: hub, srv: srv}
}

type frame struct {
	id, event, data, comment string
}

// stream is one open event stream.
type stream struct {
	t      *testing.T
	res    *http.Response
	br     *bufio.Reader
	cancel context.CancelFunc
	frames chan frame
}

// open connects path with secret and extra headers (name, value...).
func (e *streamEnv) open(path, secret string, hdr ...string) *stream {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+path, nil)
	require.NoError(e.t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := e.srv.Client().Do(req)
	require.NoError(e.t, err)
	s := &stream{t: e.t, res: res, br: bufio.NewReader(res.Body), cancel: cancel, frames: make(chan frame, 64)}
	e.t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode == http.StatusOK {
		go s.read()
	}
	return s
}

// connect opens a stream that must start.
func (e *streamEnv) connect(path, secret string, hdr ...string) *stream {
	e.t.Helper()
	s := e.open(path, secret, hdr...)
	require.Equal(e.t, http.StatusOK, s.res.StatusCode)
	assert.Equal(e.t, "text/event-stream", s.res.Header.Get("Content-Type"))
	return s
}

// read parses blocks into frames, the comments and retry left out.
func (s *stream) read() {
	defer close(s.frames)
	var f frame
	var data []string
	for {
		line, err := s.br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if f.id != "" || f.event != "" || len(data) > 0 {
				f.data = strings.Join(data, "\n")
				s.frames <- f
			}
			f, data = frame{}, nil
		case strings.HasPrefix(line, ":"), strings.HasPrefix(line, "retry:"):
		default:
			name, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch name {
			case "id":
				f.id = value
			case "event":
				f.event = value
			case "data":
				data = append(data, value)
			}
		}
	}
}

// next is the next event, within 2 s.
func (s *stream) next() frame {
	s.t.Helper()
	select {
	case f, ok := <-s.frames:
		require.True(s.t, ok, "the stream ended")
		return f
	case <-time.After(2 * time.Second):
		s.t.Fatal("no event within 2 s")
		return frame{}
	}
}

type streamPayload struct {
	ID        uuid.UUID      `json:"id"`
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

func (f frame) payload(t *testing.T) streamPayload {
	t.Helper()
	var p streamPayload
	require.NoError(t, json.Unmarshal([]byte(f.data), &p), f.data)
	assert.Equal(t, f.id, p.ID.String(), "the SSE id is the event id, as webhook-id")
	assert.Equal(t, f.event, p.Type, "the SSE event name is the event type")
	return p
}

func (e *streamEnv) tagFor(user uuid.UUID, key string) {
	e.t.Helper()
	r := e.do("POST", "/v1/users/"+user.String()+"/tags", key, `{"kind":"travel","start_at":"2026-04-10T07:00:00Z"}`)
	require.Equal(e.t, http.StatusCreated, r.code, string(r.body))
}

func TestStreamETLEventArrivesFilteredAndIsolated(t *testing.T) {
	e := newStreamEnv(t, api.StreamOptions{})
	u, other := e.user(e.tenantA, "streamer"), e.user(e.tenantA, "other")
	ub := e.user(e.tenantB, "b-user")

	all := e.connect("/v1/events/stream", e.keyA)
	ofOther := e.connect("/v1/events/stream?user_id="+other.String(), e.keyA)
	tagsOnly := e.connect("/v1/events/stream?types=tag.created", e.keyA)
	tenantB := e.connect("/v1/events/stream", e.keyB)

	r, _ := e.report(u, e.keyA, `{"reports":[{"id":"`+srFeeling+`","kind":"feeling","value":4,"start_at":"2026-03-01T08:30:00Z"}]}`)
	require.Equal(t, http.StatusAccepted, r.code, string(r.body))
	require.Equal(t, 1, e.processAll())
	start := time.Now()
	f := all.next()
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, "data.written", f.event)
	p := f.payload(t)
	assert.Equal(t, u.String(), p.Data["user_id"])
	assert.Equal(t, "streamer", p.Data["external_ref"])
	for _, k := range []string{"provider", "records", "types", "first_day", "last_day"} {
		assert.Contains(t, p.Data, k)
	}
	assert.NotContains(t, f.data, `"value"`, "an event carries no health value")

	// The webhook delivery payload and the stream's are the same event.
	var delivered int
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ohs_core.stream_events WHERE event_id = $1 AND tenant_id = $2`, p.ID, e.tenantA).Scan(&delivered))
	assert.Equal(t, 1, delivered)

	e.tagFor(other, e.keyA)
	tagged := all.next()
	assert.Equal(t, "tag.created", tagged.event)
	assert.Equal(t, tagged.id, ofOther.next().id, "data.written was about another user")
	assert.Equal(t, tagged.id, tagsOnly.next().id, "data.written is not a tag.created")

	e.tagFor(ub, e.keyB)
	fb := tenantB.next()
	assert.Equal(t, ub.String(), fb.payload(t).Data["user_id"], "tenant B's first event is its own: none of A's")
	e.tagFor(u, e.keyA)
	assert.Equal(t, u.String(), all.next().payload(t).Data["user_id"], "A never gets B's event")
}

func TestStreamResumesWithLastEventID(t *testing.T) {
	e := newStreamEnv(t, api.StreamOptions{})
	u, ub := e.user(e.tenantA, "resumer"), e.user(e.tenantB, "b")

	s := e.connect("/v1/events/stream", e.keyA)
	e.tagFor(u, e.keyA)
	last := s.next().id
	s.cancel()
	require.Eventually(t, func() bool { return e.hub.Open() == 0 }, 2*time.Second, 10*time.Millisecond)

	// Missed while disconnected: two of A, one of B.
	e.tagFor(u, e.keyA)
	e.tagFor(ub, e.keyB)
	e.tagFor(u, e.keyA)
	var missed []string
	rows, err := e.pool.Query(context.Background(), `SELECT event_id::text FROM ohs_core.stream_events
		WHERE tenant_id = $1 AND seq > (SELECT seq FROM ohs_core.stream_events WHERE event_id = $2) ORDER BY seq`, e.tenantA, last)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		missed = append(missed, id)
	}
	require.NoError(t, rows.Err())
	require.Len(t, missed, 2)

	s = e.connect("/v1/events/stream", e.keyA, "Last-Event-ID", last)
	assert.Equal(t, missed[0], s.next().id)
	assert.Equal(t, missed[1], s.next().id)
	// Then live, with nothing sent twice.
	e.tagFor(u, e.keyA)
	live := s.next()
	assert.NotContains(t, missed, live.id)
	assert.NotEqual(t, last, live.id)

	// The query parameter, for a client that cannot set the header.
	q := e.connect("/v1/events/stream?types=tag.created&last_event_id="+missed[0], e.keyA)
	assert.Equal(t, missed[1], q.next().id)
	assert.Equal(t, live.id, q.next().id)

	// An event the window does not hold (unknown here): reset, then live.
	unknown := e.connect("/v1/events/stream", e.keyA, "Last-Event-ID", uuid.NewString())
	reset := unknown.next()
	assert.Equal(t, api.StreamResetEvent, reset.event)
	assert.Empty(t, reset.id, "a reset never moves the client's last event id")
	assert.JSONEq(t, `{"reason":"last_event_id_unknown","window_hours":24}`, reset.data)
	e.tagFor(u, e.keyA)
	assert.Equal(t, "tag.created", unknown.next().event)

	// Another tenant's event id is unknown to A.
	sb := e.connect("/v1/events/stream", e.keyB)
	e.tagFor(ub, e.keyB)
	bid := sb.next().id
	cross := e.connect("/v1/events/stream", e.keyA, "Last-Event-ID", bid)
	assert.Equal(t, api.StreamResetEvent, cross.next().event)
}

func TestStreamOfOneUser(t *testing.T) {
	e := newStreamEnv(t, api.StreamOptions{})
	u, other := e.user(e.tenantA, "me"), e.user(e.tenantA, "not-me")
	tok := e.mint(u, e.keyA)

	mine := e.connect("/v1/users/"+u.String()+"/events/stream", tok.Token)
	byKey := e.connect("/v1/users/"+u.String()+"/events/stream?types=tag.created", e.keyA)
	e.tagFor(other, e.keyA)
	e.tagFor(u, e.keyA)
	f := mine.next()
	assert.Equal(t, u.String(), f.payload(t).Data["user_id"], "the other user's event is not in this stream")
	assert.Equal(t, f.id, byKey.next().id)

	assert.Equal(t, http.StatusNotFound, e.open("/v1/users/"+other.String()+"/events/stream", tok.Token).res.StatusCode,
		"a token reads only its own user")
	assert.Equal(t, http.StatusForbidden, e.open("/v1/events/stream", tok.Token).res.StatusCode,
		"the tenant stream takes a tenant key")
	assert.Equal(t, http.StatusNotFound, e.open("/v1/users/"+uuid.NewString()+"/events/stream", e.keyA).res.StatusCode)
	ub := e.user(e.tenantB, "b")
	assert.Equal(t, http.StatusNotFound, e.open("/v1/users/"+ub.String()+"/events/stream", e.keyA).res.StatusCode,
		"another tenant's user")
}

func TestStreamTenantCap(t *testing.T) {
	e := newStreamEnv(t, api.StreamOptions{MaxPerTenant: 2})
	e.connect("/v1/events/stream", e.keyA)
	e.connect("/v1/events/stream", e.keyA)
	s := e.open("/v1/events/stream", e.keyA)
	assert.Equal(t, http.StatusTooManyRequests, s.res.StatusCode)
	assert.NotEmpty(t, s.res.Header.Get("Retry-After"))
	e.connect("/v1/events/stream", e.keyB)
}
