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

// InboundProbe serves HEAD and GET /inbound/{provider}: some providers check
// that a notification URL answers before they subscribe it. It answers 200
// with no body for a provider that sends notifications, 404 otherwise. A
// connector that implements connector.ChallengeResponder answers the request
// itself (a subscription challenge); its error is a 401. Mount it without
// auth.
func (h *Handlers) InboundProbe() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := h.d.lookup(r.PathValue("provider"))
		if !ok {
			writeNotFound(w, "unknown provider")
			return
		}
		if _, ok := connector.AsPush(c); !ok {
			writeNotFound(w, "this provider does not send notifications")
			return
		}
		if cr, ok := c.(connector.ChallengeResponder); ok {
			ctype, body, err := cr.AnswerChallenge(r)
			if err != nil {
				// Anyone can probe here: DEBUG, like a refused notification.
				slog.DebugContext(r.Context(), "collect: inbound challenge refused", "provider", r.PathValue("provider"), "error", safeErr(err))
				auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "the challenge could not be verified")
				return
			}
			if body != nil {
				w.Header().Set("Content-Type", ctype)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Inbound serves POST /inbound/{provider}. It is provider facing: mount it
// without auth; the connector verifies the request (signature, secret or
// whatever the provider uses). A body over 1 MiB is 413; a request the
// connector rejects gets 401. Neither enqueues anything. Each event is
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
		c, ok := h.d.lookup(provider)
		if !ok {
			writeNotFound(w, "unknown provider")
			return
		}
		pc, ok := connector.AsPush(c)
		if !ok {
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
		r.Body = io.NopCloser(bytes.NewReader(body))
		events, err := pc.HandleInbound(ctx, r)
		if err != nil {
			// Anyone can post here: DEBUG, so junk traffic cannot flood the log.
			slog.DebugContext(ctx, "collect: inbound refused", "provider", provider, "error", safeErr(err))
			auth.WriteProblem(w, http.StatusUnauthorized, "Unauthorized", "the notification could not be verified")
			return
		}
		var pulls, stores []connector.InboundEvent
		for _, ev := range events {
			if len(ev.Lines) > 0 {
				stores = append(stores, ev)
			} else {
				pulls = append(pulls, ev)
			}
		}
		if err := h.storeInbound(ctx, provider, stores); err != nil {
			slog.ErrorContext(ctx, "collect: inbound store", "provider", provider, "error", err)
			writeInternal(w)
			return
		}
		if err := h.enqueueInbound(ctx, provider, pulls); err != nil {
			slog.ErrorContext(ctx, "collect: inbound enqueue", "provider", provider, "error", err)
			writeInternal(w)
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
// under providerUserID, across tenants.
func inboundOwners(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, provider, providerUserID, lock string) ([]inboundOwner, error) {
	rows, err := q.Query(ctx, `
		SELECT c.tenant_id, c.user_id FROM ohs_sync.connections c
		JOIN ohs_core.end_users u ON u.tenant_id = c.tenant_id AND u.id = c.user_id AND u.status = 'active'
		WHERE c.provider = $1 AND c.provider_user_id = $2 AND c.status = 'active'`+lock,
		provider, providerUserID)
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
// connection, filed under the day the notification arrived. The batch name
// carries the arrival time, so two notifications never share a key; the same
// bytes delivered twice are stored once (storeRaw skips registered bytes). A
// user being deleted is skipped.
func (h *Handlers) storeInbound(ctx context.Context, provider string, events []connector.InboundEvent) error {
	now := h.d.now().UTC()
	for i, ev := range events {
		if ev.ProviderUserID == "" || ev.DataType == "" {
			continue
		}
		owners, err := inboundOwners(ctx, h.d.Pool, provider, ev.ProviderUserID, "")
		if err != nil {
			return err
		}
		for _, o := range owners {
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
		}
	}
	return nil
}

// enqueueInbound queues a pull per notified connection of an active user. The
// user rows are held FOR SHARE until the transaction ends, so a deletion
// cannot start between the check and the inserts.
func (h *Handlers) enqueueInbound(ctx context.Context, provider string, events []connector.InboundEvent) error {
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
		owners, err := inboundOwners(ctx, tx, provider, ev.ProviderUserID, " FOR SHARE OF u")
		if err != nil {
			return err
		}
		from, to := inboundWindow(ev, now)
		for _, o := range owners {
			if err := coalescePull(ctx, tx, queue.PullJob{
				TenantID: o.tenant, UserID: o.user, Provider: provider, From: from, To: to,
			}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE ohs_sync.inbound_subscriptions SET last_received_at = $4
				WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'subscribed'`,
				o.tenant, o.user, provider, now); err != nil {
				return fmt.Errorf("touch subscription: %w", err)
			}
		}
	}
	return tx.Commit(ctx)
}

// maxCoalesced bounds the window a pending job may grow to by absorbing
// notifications.
const maxCoalesced = BackfillChunk

// coalescePull enqueues an inbound pull unless a pending job of the same
// connection already covers its window; a pending job that overlaps it is
// widened to cover both, while the union stays within maxCoalesced. A pull
// fetches every data type, so the data type of the event does not matter.
// Providers notify in bursts; this keeps one job per burst.
func coalescePull(ctx context.Context, tx pgx.Tx, j queue.PullJob) error {
	var covered bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM ohs_sync.pull_jobs
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'pending'
		  AND from_time <= $4 AND to_time >= $5)`,
		j.TenantID, j.UserID, j.Provider, j.From, j.To).Scan(&covered); err != nil {
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
		UPDATE ohs_sync.pull_jobs SET from_time = least(from_time, $4), to_time = greatest(to_time, $5)
		WHERE id = (
			SELECT id FROM ohs_sync.pull_jobs
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status = 'pending'
			  AND from_time <= $5 AND to_time >= $4
			  AND greatest(to_time, $5) - least(from_time, $4) <= make_interval(secs => $6::double precision)
			ORDER BY not_before, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		  AND status = 'pending'`,
		j.TenantID, j.UserID, j.Provider, j.From, j.To, maxCoalesced.Seconds())
	if err != nil {
		_ = sp.Rollback(ctx)
	} else if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("release savepoint: %w", err)
	} else if tag.RowsAffected() == 1 {
		return nil
	}
	return queue.EnqueuePull(ctx, tx, j)
}
