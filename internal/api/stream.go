// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/soma-intelligence/open-health-sync/internal/logging"
	"github.com/soma-intelligence/open-health-sync/internal/logutil"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// The event stream defaults (StreamOptions).
const (
	// DefaultStreamMax and DefaultStreamMaxPerTenant are the caps of
	// OHS_STREAM_MAX and OHS_STREAM_MAX_PER_TENANT.
	DefaultStreamMax          = 1000
	DefaultStreamMaxPerTenant = 10
	// StreamHeartbeat is how often an idle stream sends a comment, well
	// inside the 60 s read timeout of common proxies.
	StreamHeartbeat = 15 * time.Second
	// StreamRetry is the reconnection delay the stream asks clients for.
	StreamRetry = 3 * time.Second
	// streamBuffer is how many events wait for one slow stream before it is
	// dropped.
	streamBuffer = 256
	// streamWriteTimeout bounds one write to a client.
	streamWriteTimeout = 10 * time.Second
	// streamMaxAge ends a stream after an hour; the client reconnects with
	// Last-Event-ID and its credential is checked again (a revoked key or
	// token stops streaming within the hour).
	streamMaxAge = time.Hour
	// streamResumeMax caps the events one resume replays; past it the
	// client is told to read the window again (stream.reset).
	streamResumeMax = 10_000
	// streamResumePage is how many events one resume query reads.
	streamResumePage = 500
)

// StreamOptions tune a StreamHub. A zero field takes its default.
type StreamOptions struct {
	// Max caps the streams open at once in this process, MaxPerTenant those
	// of one tenant.
	Max, MaxPerTenant int
	// Heartbeat is the interval of the keep-alive comments, Retry the
	// reconnection delay sent to the client.
	Heartbeat, Retry time.Duration
	// Buffer is how many events wait for one stream before it is dropped.
	Buffer int
	// WriteTimeout bounds one write to a client, MaxAge one stream.
	WriteTimeout, MaxAge time.Duration
}

func (o StreamOptions) withDefaults() StreamOptions {
	if o.Max <= 0 {
		o.Max = DefaultStreamMax
	}
	if o.MaxPerTenant <= 0 {
		o.MaxPerTenant = min(DefaultStreamMaxPerTenant, o.Max)
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = StreamHeartbeat
	}
	if o.Retry <= 0 {
		o.Retry = StreamRetry
	}
	if o.Buffer <= 0 {
		o.Buffer = streamBuffer
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = streamWriteTimeout
	}
	if o.MaxAge <= 0 {
		o.MaxAge = streamMaxAge
	}
	return o
}

// Why a stream could not open.
var (
	errStreamFull       = errors.New("api: every event stream slot of this process is taken")
	errStreamTenantFull = errors.New("api: the tenant holds as many event streams as it may")
	errStreamNotReady   = errors.New("api: the event stream is not listening yet")
	errStreamClosed     = errors.New("api: the event stream is shutting down")
)

// Why a stream ended from the hub's side.
const (
	streamEndSlow     = "slow client"
	streamEndShutdown = "shutdown"
	streamEndListener = "listener lost"
)

// streamEvent is one event as the stream sends it.
type streamEvent struct {
	seq     int64
	id      uuid.UUID
	typ     string
	user    *uuid.UUID
	payload []byte // compact JSON, the webhook body
}

// streamSub is one open stream: its filters and the events waiting for it.
type streamSub struct {
	tenant uuid.UUID
	user   *uuid.UUID
	types  map[string]bool // nil: every type
	ch     chan streamEvent
	done   chan struct{}
	once   sync.Once
	reason string // set before done is closed
}

func (s *streamSub) wants(ev streamEvent) bool {
	if s.user != nil && (ev.user == nil || *ev.user != *s.user) {
		return false
	}
	return s.types == nil || s.types[ev.typ]
}

// end closes the stream from the hub's side; the first reason wins.
func (s *streamSub) end(reason string) {
	s.once.Do(func() {
		s.reason = reason
		close(s.done)
	})
}

// StreamHub fans the events of every tenant out to the event streams open
// in this process. Events are written by any process (webhook.Enqueue) with
// a NOTIFY carrying only their row; Run listens on one connection of its own
// and loads each row once, for the tenants that have a stream here.
type StreamHub struct {
	pool *pgxpool.Pool
	opts StreamOptions

	mu       sync.Mutex
	byTenant map[uuid.UUID]map[*streamSub]struct{}
	open     int
	ready    bool // LISTEN is in place
	closed   bool
}

// NewStreamHub returns a hub; Run must run for it to accept streams.
func NewStreamHub(pool *pgxpool.Pool, opts StreamOptions) *StreamHub {
	return &StreamHub{pool: pool, opts: opts.withDefaults(), byTenant: map[uuid.UUID]map[*streamSub]struct{}{}}
}

// Ready reports whether the hub listens and accepts streams.
func (h *StreamHub) Ready() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ready && !h.closed
}

// Open reports how many streams are open.
func (h *StreamHub) Open() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open
}

// Close ends every stream and refuses new ones. The API server calls it when
// it shuts down (http.Server.RegisterOnShutdown), so the streams, which
// never go idle, do not hold the shutdown for its whole grace.
func (h *StreamHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.endAllLocked(streamEndShutdown)
}

func (h *StreamHub) endAllLocked(reason string) {
	for _, subs := range h.byTenant {
		for s := range subs {
			s.end(reason)
		}
	}
}

func (h *StreamHub) subscribe(tenant uuid.UUID, user *uuid.UUID, types map[string]bool) (*streamSub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closed:
		return nil, errStreamClosed
	case !h.ready:
		return nil, errStreamNotReady
	case h.open >= h.opts.Max:
		return nil, errStreamFull
	case len(h.byTenant[tenant]) >= h.opts.MaxPerTenant:
		return nil, errStreamTenantFull
	}
	s := &streamSub{tenant: tenant, user: user, types: types,
		ch: make(chan streamEvent, h.opts.Buffer), done: make(chan struct{})}
	subs := h.byTenant[tenant]
	if subs == nil {
		subs = map[*streamSub]struct{}{}
		h.byTenant[tenant] = subs
	}
	subs[s] = struct{}{}
	h.open++
	return s, nil
}

func (h *StreamHub) unsubscribe(s *streamSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs := h.byTenant[s.tenant]; subs != nil {
		if _, ok := subs[s]; ok {
			delete(subs, s)
			h.open--
		}
		if len(subs) == 0 {
			delete(h.byTenant, s.tenant)
		}
	}
	s.end("")
}

func (h *StreamHub) hasTenant(tenant uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byTenant[tenant]) > 0
}

// publish queues ev for every stream of tenant that wants it. A stream whose
// queue is full is dropped rather than waited for: it reconnects and resumes
// with Last-Event-ID.
func (h *StreamHub) publish(tenant uuid.UUID, ev streamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.byTenant[tenant] {
		if !s.wants(ev) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			s.end(streamEndSlow)
		}
	}
}

func (h *StreamHub) endTenant(tenant uuid.UUID, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.byTenant[tenant] {
		s.end(reason)
	}
}

func (h *StreamHub) setReady(ready bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = ready
	if !ready {
		// Events announced while nobody listened are unknown: the streams
		// end, and their clients resume from their last event.
		h.endAllLocked(streamEndListener)
	}
}

// Run listens for events until ctx ends, then closes every stream. It holds
// a connection of its own (not one of the pool's) and reconnects after a
// failure; the streams open at the failure end, since events may have been
// missed, and their clients resume with Last-Event-ID.
func (h *StreamHub) Run(ctx context.Context) error {
	defer h.Close()
	backoff := time.Second
	for {
		listened, err := h.listenOnce(ctx)
		h.setReady(false)
		if ctx.Err() != nil {
			return nil
		}
		if listened {
			backoff = time.Second
		}
		logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.WarnLevel).Int64("retry_in_ms", (backoff).Milliseconds()).Msg("event_stream_listening_failed_retrying")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (h *StreamHub) listenOnce(ctx context.Context) (bool, error) {
	conn, err := pgx.ConnectConfig(ctx, h.pool.Config().ConnConfig.Copy())
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+webhook.StreamChannel); err != nil {
		return false, fmt.Errorf("listen: %w", err)
	}
	h.setReady(true)
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return true, err
		}
		tenant, seq, ok := webhook.ParseStreamNotification(n.Payload)
		if !ok || !h.hasTenant(tenant) {
			continue
		}
		ev, err := loadStreamEvent(ctx, h.pool, tenant, seq)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Erased with its user, or pruned, since: nothing to send.
		case err != nil:
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			// The tenant's streams would miss it: they end and resume.
			logutil.ErrCtx(ctx, *logging.Ctx(ctx), err, zerolog.WarnLevel).Msg("event_stream_loading_event_failed")
			h.endTenant(tenant, "event not loaded")
		default:
			h.publish(tenant, ev)
		}
	}
}

// streamQuerier is a pool or a connection.
type streamQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadStreamEvent(ctx context.Context, db streamQuerier, tenant uuid.UUID, seq int64) (streamEvent, error) {
	var ev streamEvent
	var payload string
	err := db.QueryRow(ctx, `SELECT seq, event_id, event_type, user_id, payload::text
		FROM ohs_core.stream_events WHERE tenant_id = $1 AND seq = $2`, tenant, seq).
		Scan(&ev.seq, &ev.id, &ev.typ, &ev.user, &payload)
	if err != nil {
		return streamEvent{}, err
	}
	ev.payload, err = compactJSON(payload)
	return ev, err
}

// streamSeqOf finds the row of the event a client saw last, within the
// window. pgx.ErrNoRows: unknown, or older than the window.
func streamSeqOf(ctx context.Context, db streamQuerier, tenant, event uuid.UUID) (int64, error) {
	var seq int64
	err := db.QueryRow(ctx, `SELECT seq FROM ohs_core.stream_events
		WHERE tenant_id = $1 AND event_id = $2 AND created_at > now() - make_interval(secs => $3::double precision)`,
		tenant, event, webhook.StreamRetention.Seconds()).Scan(&seq)
	return seq, err
}

// streamEventsAfter reads, in order, at most limit events of the tenant
// written after seq and within the window, that the filters keep.
func streamEventsAfter(ctx context.Context, db streamQuerier, tenant uuid.UUID, seq int64, user *uuid.UUID, types []string, limit int) ([]streamEvent, error) {
	if types == nil {
		types = []string{}
	}
	rows, err := db.Query(ctx, `SELECT seq, event_id, event_type, user_id, payload::text
		FROM ohs_core.stream_events
		WHERE tenant_id = $1 AND seq > $2
		  AND created_at > now() - make_interval(secs => $3::double precision)
		  AND ($4::uuid IS NULL OR user_id = $4)
		  AND (cardinality($5::text[]) = 0 OR event_type = ANY ($5))
		ORDER BY seq LIMIT $6`, tenant, seq, webhook.StreamRetention.Seconds(), user, types, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []streamEvent
	for rows.Next() {
		var ev streamEvent
		var payload string
		if err := rows.Scan(&ev.seq, &ev.id, &ev.typ, &ev.user, &payload); err != nil {
			return nil, err
		}
		if ev.payload, err = compactJSON(payload); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// compactJSON is the payload as a webhook delivery sends it: one line.
func compactJSON(text string) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(text)); err != nil {
		return nil, fmt.Errorf("compact stream payload: %w", err)
	}
	return b.Bytes(), nil
}
