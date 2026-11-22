// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// StreamResetEvent is the one event of the stream that is not a webhook
// event: the stream could not resume where the client left off (the event
// it names is unknown or older than the window, or too many events were
// missed), so the client re-reads what it needs and goes on from here.
const StreamResetEvent = "stream.reset"

// mountStream serves the event stream: every event of the tenant to a tenant
// key (webhooks:read, the scope that reads delivery payloads), and the
// events of one user to a tenant key or that user's token with scope read
// (data:read, the key scope a read token needs). The routes are mounted
// even with the stream off, so the scopes still answer; then they are 404.
func (s *server) mountStream(mux *http.ServeMux) {
	s.tenantRoute(mux, "GET /v1/events/stream", s.streamEvents)
	mux.Handle("GET /v1/users/{id}/events/stream",
		auth.MiddlewareAny(s.lookup, auth.TenantKey, auth.UserRead)(http.HandlerFunc(s.streamUserEvents)))
}

func (s *server) streamEvents(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	var user *uuid.UUID
	if v := r.URL.Query().Get("user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			badRequest(w, "user_id is not a UUID")
			return
		}
		user = &id
	}
	s.serveStream(w, r, p, user)
}

// streamUserEvents streams the events of one user. A user token reads only
// its own user (another user is a 404, never a 403).
func (s *server) streamUserEvents(w http.ResponseWriter, r *http.Request) {
	p, id, ok := pathUser(w, r)
	if !ok {
		return
	}
	if p.UserID != nil && *p.UserID != id {
		notFound(w, "user")
		return
	}
	if s.d.Stream == nil {
		streamOff(w)
		return
	}
	if _, err := s.d.Store.GetActiveUser(r.Context(), p.TenantID, id); err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			notFound(w, "user")
			return
		}
		internalError(w, r, "api: stream user", err)
		return
	}
	s.serveStream(w, r, p, &id)
}

func streamOff(w http.ResponseWriter) {
	problem(w, http.StatusNotFound, "the event stream is off on this instance (OHS_STREAM_MAX=0)")
}

// parseStreamTypes reads types: event types separated by commas, the
// parameter repeated or not. Nil means every type.
func parseStreamTypes(values []string) (map[string]bool, []string, error) {
	var list []string
	set := map[string]bool{}
	for _, v := range values {
		for t := range strings.SplitSeq(v, ",") {
			t = strings.TrimSpace(t)
			if t == "" || set[t] {
				continue
			}
			if !webhook.KnownType(t) {
				return nil, nil, errors.New("types: unknown event type " + strconv.Quote(t) +
					"; the types are " + strings.Join(webhook.Types, ", "))
			}
			set[t] = true
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		return nil, nil, nil
	}
	return set, list, nil
}

// serveStream streams the events of p's tenant that the filters keep, from
// now on, or from the event named by Last-Event-ID (or last_event_id) when
// it is within the window.
func (s *server) serveStream(w http.ResponseWriter, r *http.Request, p auth.Principal, user *uuid.UUID) {
	hub := s.d.Stream
	if hub == nil {
		streamOff(w)
		return
	}
	q := r.URL.Query()
	types, typeList, err := parseStreamTypes(q["types"])
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = q.Get("last_event_id")
	}

	// Subscribed before the resume reads the table, so an event committed
	// meanwhile is queued, and sent once (seen below).
	sub, err := hub.subscribe(p.TenantID, user, types)
	switch {
	case errors.Is(err, errStreamFull):
		w.Header().Set("Retry-After", "5")
		problem(w, http.StatusTooManyRequests, "this server holds as many event streams as it may (OHS_STREAM_MAX); retry later")
		return
	case errors.Is(err, errStreamTenantFull):
		w.Header().Set("Retry-After", "5")
		problem(w, http.StatusTooManyRequests, "the tenant holds as many event streams as it may on this server (OHS_STREAM_MAX_PER_TENANT); close one or retry later")
		return
	case err != nil:
		auth.WriteUnavailable(w, "the event stream is not available right now; retry shortly")
		return
	}
	defer hub.unsubscribe(sub)

	var after int64
	reset := ""
	if last != "" {
		id, perr := uuid.Parse(last)
		if perr == nil {
			after, err = streamSeqOf(r.Context(), s.d.Pool, p.TenantID, id)
		}
		switch {
		case perr != nil || errors.Is(err, pgx.ErrNoRows):
			after, reset = 0, "last_event_id_unknown"
		case err != nil:
			internalError(w, r, "api: stream resume", err)
			return
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// nginx (and a proxy that honours it) passes the stream unbuffered.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// A stream has no body to read: the request read deadline (the server's
	// ReadTimeout) is cleared, so nothing tied to it can end the stream. The
	// write deadline is set again before every write (sseConn), which
	// replaces the server's WriteTimeout, if any, for this response.
	c := newSSEConn(w, hub.opts.WriteTimeout)
	_ = c.rc.SetReadDeadline(time.Time{})

	if err := c.send(func(w io.Writer) error {
		if err := writeSSERetry(w, hub.opts.Retry); err != nil {
			return err
		}
		return writeSSEComment(w, "connected")
	}); err != nil {
		return
	}
	sendReset := func(reason string) error {
		data := []byte(`{"reason":"` + reason + `","window_hours":` +
			strconv.Itoa(int(webhook.StreamRetention/time.Hour)) + `}`)
		return c.send(func(w io.Writer) error { return writeSSEEvent(w, "", StreamResetEvent, data) })
	}
	sendEvent := func(ev streamEvent) error {
		return c.send(func(w io.Writer) error { return writeSSEEvent(w, ev.id.String(), ev.typ, ev.payload) })
	}
	if reset != "" {
		if err := sendReset(reset); err != nil {
			return
		}
	}
	seen := map[int64]bool{}
	if after > 0 {
		replayed := 0
		for {
			page, err := streamEventsAfter(r.Context(), s.d.Pool, p.TenantID, after, user, typeList, streamResumePage)
			if err != nil {
				// Headers are sent: the client reconnects and resumes.
				return
			}
			if replayed+len(page) > streamResumeMax {
				if sendReset("too_many_missed") != nil {
					return
				}
				break
			}
			for _, ev := range page {
				if sendEvent(ev) != nil {
					return
				}
				seen[ev.seq] = true
				after = ev.seq
			}
			replayed += len(page)
			if len(page) < streamResumePage {
				break
			}
		}
	}

	heartbeat := time.NewTicker(hub.opts.Heartbeat)
	defer heartbeat.Stop()
	maxAge := time.NewTimer(hub.opts.MaxAge)
	defer maxAge.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.done:
			return
		case <-maxAge.C:
			return
		case <-heartbeat.C:
			if c.send(func(w io.Writer) error { return writeSSEComment(w, "ping") }) != nil {
				return
			}
		case ev := <-sub.ch:
			if seen[ev.seq] {
				delete(seen, ev.seq)
				continue
			}
			if sendEvent(ev) != nil {
				return
			}
		}
	}
}
