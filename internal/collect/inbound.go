// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/auth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
)

// maxInboundBody caps one provider notification.
const maxInboundBody = 1 << 20

// inboundReadTimeout bounds reading one notification body, so a peer that
// trickles bytes cannot hold a connection (the routes take no auth). A var
// for tests.
var inboundReadTimeout = 30 * time.Second

// inboundDefaultSpan is the window pulled for an event that names no times.
const inboundDefaultSpan = 24 * time.Hour

// pushTarget is one set of keys a notification may come for: the
// instance's (tenant and keys nil), or one tenant's own. Its connector
// verifies the notification with those keys, and only the connections made
// with them get its events.
type pushTarget struct {
	tenant, keys *uuid.UUID
	c            connector.PushConnector
}

// pushTargets lists the keys a notification for provider may be meant for:
// the instance's first, then every tenant's own, by tenant. The route names
// only the provider (one callback URL per provider), so each is tried.
func (d Deps) pushTargets(provider string) []pushTarget {
	var out []pushTarget
	if c, ok := d.lookup(provider); ok {
		if pc, ok := connector.AsPush(c); ok {
			out = append(out, pushTarget{c: pc})
		}
	}
	for _, cand := range d.TenantKeys.Candidates(provider) {
		if pc, ok := connector.AsPush(cand.Connector); ok {
			out = append(out, pushTarget{tenant: &cand.Tenant, keys: &cand.ID, c: pc})
		}
	}
	return out
}

// InboundProbe serves HEAD and GET /inbound/{provider}: some providers check
// that a notification URL answers before they subscribe it. It answers 200
// with no body for a provider that sends notifications, 404 otherwise. A
// connector that implements connector.ChallengeResponder answers the request
// itself (a subscription challenge); its error is a 401. Mount it without
// auth.
func (h *Handlers) InboundProbe() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.d.capable(r.PathValue("provider"))
		if !ok {
			writeNotFound(w, "unknown provider")
			return
		}
		if _, ok := connector.AsPush(c); !ok {
			writeNotFound(w, "this provider does not send notifications")
			return
		}
		// Each set of keys may have registered this URL: the first whose
		// connector verifies the challenge answers it.
		var refused error
		for _, t := range h.d.pushTargets(r.PathValue("provider")) {
			cr, ok := t.c.(connector.ChallengeResponder)
			if !ok {
				continue
			}
			ctype, body, err := cr.AnswerChallenge(r)
			if err != nil {
				refused = err
				continue
			}
			if body != nil {
				w.Header().Set("Content-Type", ctype)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
			refused = nil
			break
		}
		if refused != nil {
			// Anyone can probe here: DEBUG, like a refused notification.
			slog.DebugContext(r.Context(), "collect: inbound challenge refused", "provider", r.PathValue("provider"), "error", safeErr(refused))
			auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "the challenge could not be verified")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Inbound serves POST /inbound/{provider}. It is provider facing: mount it
// without auth; the connector verifies the request (signature, secret or
// whatever the provider uses). A body over 1 MiB is 413; a request the
// connector rejects gets 401. Neither enqueues anything. The connector of
// each set of keys of the provider verifies it (the instance's, then each
// tenant's own, see pushTargets); the events of one that accepts it go only
// to the connections made with its keys. Each event is
// matched to every active connection holding its provider user id, across
// tenants (one provider account linked in two tenants feeds both), and a pull
// job for [From, To] is enqueued for each; events for unknown users are
// ignored. An event that carries raw lines (connector.InboundEvent.Lines) is
// stored as one raw object per such connection and queued for the ETL instead
// of a pull; a failure to store it is a 500, so the provider retries. The
// answer is 200 with no body.
func (h *Handlers) Inbound() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		provider := r.PathValue("provider")
		c, ok := h.d.capable(provider)
		if !ok {
			writeNotFound(w, "unknown provider")
			return
		}
		if _, ok := connector.AsPush(c); !ok {
			writeNotFound(w, "this provider does not send notifications")
			return
		}
		// Read the body here so an oversized one is a 413 whatever the
		// connector does with read errors; it gets the bytes back. A writer
		// without deadline support (tests) just reads without one.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(inboundReadTimeout))
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				auth.WriteProblem(w, http.StatusRequestEntityTooLarge, "Content Too Large",
					fmt.Sprintf("a notification may hold at most %d bytes", maxInboundBody))
				return
			}
			auth.WriteProblem(w, http.StatusBadRequest, "Bad Request", "the body could not be read")
			return
		}
		verified := false
		var lastErr error
		for _, t := range h.d.pushTargets(provider) {
			// Each connector reads the request afresh, its parsed form
			// included.
			rc := r.Clone(ctx)
			rc.Form, rc.PostForm, rc.MultipartForm = nil, nil, nil
			rc.Body = io.NopCloser(bytes.NewReader(body))
			events, err := t.c.HandleInbound(ctx, rc)
			if err != nil {
				lastErr = err
				continue
			}
			verified = true
			var pulls, stores []connector.InboundEvent
			for _, ev := range events {
				if len(ev.Lines) > 0 {
					stores = append(stores, ev)
				} else {
					pulls = append(pulls, ev)
				}
			}
			if err := h.storeInbound(ctx, provider, t, stores); err != nil {
				slog.ErrorContext(ctx, "collect: inbound store", "provider", provider, "error", err)
				writeInternal(w)
				return
			}
			if err := h.enqueueInbound(ctx, provider, t, pulls); err != nil {
				slog.ErrorContext(ctx, "collect: inbound enqueue", "provider", provider, "error", err)
				writeInternal(w)
				return
			}
		}
		if !verified {
			// Anyone can post here: DEBUG, so junk traffic cannot flood the log.
			slog.DebugContext(ctx, "collect: inbound refused", "provider", provider, "error", safeErr(lastErr))
			auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "the notification could not be verified")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// inboundWindow normalizes an event's window: whole seconds, To after From,
// at most BackfillWindow long, and the last day when the event names none.
func inboundWindow(ev connector.InboundEvent, now time.Time) (time.Time, time.Time) {
	from, to := ev.From.UTC().Truncate(time.Second), ev.To.UTC().Truncate(time.Second)
	if to.IsZero() {
		to = now.UTC().Truncate(time.Second)
	}
	if from.IsZero() {
		from = to.Add(-inboundDefaultSpan)
	}
	if !to.After(from) {
		to = from.Add(time.Second)
	}
	if to.Sub(from) > BackfillWindow {
		from = to.Add(-BackfillWindow)
	}
	return from, to
}

// inboundOwner is one end user holding an active connection.
type inboundOwner struct{ tenant, user uuid.UUID }

// inboundOwners lists the active users with an active connection to provider
// under providerUserID made with the keys of t: across tenants for the
// instance's keys, in t's tenant for a tenant's own.
func inboundOwners(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, provider string, t pushTarget, providerUserID, lock string) ([]inboundOwner, error) {
	rows, err := q.Query(ctx, `
		SELECT c.tenant_id, c.user_id FROM ohs_sync.connections c
		JOIN ohs_core.end_users u ON u.tenant_id = c.tenant_id AND u.id = c.user_id AND u.status = 'active'
		WHERE c.provider = $1 AND c.provider_user_id = $2 AND c.status = 'active'
		  AND c.tenant_keys_id IS NOT DISTINCT FROM $3 AND ($4::uuid IS NULL OR c.tenant_id = $4)`+lock,
		provider, providerUserID, t.keys, t.tenant)
	if err != nil {
		return nil, fmt.Errorf("find connection: %w", err)
	}
	owners, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (inboundOwner, error) {
		var o inboundOwner
		err := r.Scan(&o.tenant, &o.user)
		return o, err
	})
	if err != nil {
		return nil, fmt.Errorf("find connection: %w", err)
	}
	return owners, nil
}

// storeInbound stores the raw lines of each event for every active
// connection of its provider user: one raw object and one ETL job per
// connection, filed under the day the notification arrived, and marks the
// connection as having heard from its provider (touchInbound). The batch name
// carries the arrival time, so two notifications never share a key; the same
// bytes delivered twice are stored once (storeRaw skips registered bytes). A
// user being deleted, or a tenant that turned the provider off, is skipped.
func (h *Handlers) storeInbound(ctx context.Context, provider string, t pushTarget, events []connector.InboundEvent) error {
	now := h.d.now().UTC()
	for i, ev := range events {
		if ev.ProviderUserID == "" || ev.DataType == "" {
			continue
		}
		owners, err := inboundOwners(ctx, h.d.Pool, provider, t, ev.ProviderUserID, "")
		if err != nil {
			return err
		}
		for _, o := range owners {
			if !h.d.allowed(o.tenant, provider) {
				continue
			}
			_, err := storeRaw(ctx, h.d, rawTarget{
				tenant: o.tenant, user: o.user, provider: provider, dataType: ev.DataType,
				day: now, batch: fmt.Sprintf("inbound-%d", now.UnixMicro()), part: i,
			}, ev.Lines)
			if errors.Is(err, errInactive) {
				continue
			}
			if err != nil {
				return err
			}
			// A deletion is a notification like any other: it shows the
			// push channel works.
			if err := touchInbound(ctx, h.d.Pool, o, provider, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// enqueueInbound queues a pull per notified connection of an active user,
// unless the provider is turned off for the user's tenant. The user rows are
// held FOR SHARE until the transaction ends, so a deletion cannot start
// between the check and the inserts.
func (h *Handlers) enqueueInbound(ctx context.Context, provider string, t pushTarget, events []connector.InboundEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := h.d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := h.d.now()
	for _, ev := range events {
		if ev.ProviderUserID == "" {
			continue
		}
		owners, err := inboundOwners(ctx, tx, provider, t, ev.ProviderUserID, " FOR SHARE OF u")
		if err != nil {
			return err
		}
		from, to := inboundWindow(ev, now)
		dataType := ""
		if h.d.typed(provider) {
			dataType = ev.DataType
		}
		for _, o := range owners {
			if !h.d.allowed(o.tenant, provider) {
				continue
			}
			if err := coalescePull(ctx, tx, queue.PullJob{
				TenantID: o.tenant, UserID: o.user, Provider: provider, From: from, To: to,
				DataType: dataType, Inbound: true,
			}); err != nil {
				return err
			}
			if err := touchInbound(ctx, tx, o, provider, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// touchInbound records that a verified notification for the connection
// arrived: the push health signal freshness reports and the scheduler slows
// its backstop pulls on. A health signal, not data: it skips a row another
// transaction holds (a token refresh waits on the provider under that lock)
// rather than keep the provider's request waiting.
func touchInbound(ctx context.Context, q execer, o inboundOwner, provider string, now time.Time) error {
	if _, err := q.Exec(ctx, `
		UPDATE ohs_sync.connections c SET last_event_at = $4
		WHERE c.ctid = (SELECT ctid FROM ohs_sync.connections
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
			FOR UPDATE SKIP LOCKED)`,
		o.tenant, o.user, provider, now); err != nil {
		return fmt.Errorf("touch connection: %w", err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE ohs_sync.inbound_subscriptions SET last_received_at = $4
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'subscribed'`,
		o.tenant, o.user, provider, now); err != nil {
		return fmt.Errorf("touch subscription: %w", err)
	}
	return nil
}

// maxCoalesced bounds the window a pending job may grow to by absorbing
// notifications.
const maxCoalesced = BackfillChunk

// coalescePull enqueues an inbound pull unless a pending job of the same
// connection already covers its window and data type (a job of every type
// covers any type); a pending job of the same data type that overlaps it is
// widened to cover both, while the union stays within maxCoalesced.
// Providers notify in bursts; this keeps one job per burst and type.
func coalescePull(ctx context.Context, tx pgx.Tx, j queue.PullJob) error {
	var covered bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'pending'
		  AND from_time <= $4 AND to_time >= $5
		  AND (data_type IS NULL OR data_type = $6))`,
		j.TenantID, j.UserID, j.Provider, j.From, j.To, j.DataType).Scan(&covered); err != nil {
		return fmt.Errorf("find covering pull job: %w", err)
	}
	if covered {
		return nil
	}
	// Widen an overlapping pending job. SKIP LOCKED leaves a job a worker
	// is claiming alone; the status filter in the UPDATE does the rest. The
	// savepoint absorbs a unique clash with another pending window.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	tag, err := sp.Exec(ctx, `
		UPDATE ohs_sync.pull_jobs SET from_time = least(from_time, $4), to_time = greatest(to_time, $5),
			priority = least(priority, 0)
		WHERE id = (
			SELECT id FROM ohs_sync.pull_jobs
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'pending'
			  AND from_time <= $5 AND to_time >= $4
			  AND coalesce(data_type, '') = $7
			  AND greatest(to_time, $5) - least(from_time, $4) <= make_interval(secs => $6::double precision)
			ORDER BY not_before, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		  AND status = 'pending'`,
		j.TenantID, j.UserID, j.Provider, j.From, j.To, maxCoalesced.Seconds(), j.DataType)
	if err != nil {
		_ = sp.Rollback(ctx)
	} else if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	} else if tag.RowsAffected() == 1 {
		return nil
	}
	return queue.EnqueuePull(ctx, tx, j)
}
