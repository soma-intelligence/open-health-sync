// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Push subscriptions (ohs_sync.inbound_subscriptions) are durable rows: a
// connect writes one pending, the scheduler subscribes it at the provider
// with fresh tokens (retrying with backoff), and a disconnect or a user
// deletion unsubscribes it and marks it removed.
//
// Lock order, everywhere: the connections row first, then its subscription
// rows. Disconnect and RevokeAll lock both before talking to the provider, so
// a subscribe in flight finishes (or has not started) before the unsubscribe.

// Subscription statuses.
const (
	subPending    = "pending"
	subSubscribed = "subscribed"
	subRemoved    = "removed"
)

// subscriptionTopic is the topic of the single subscription a connection
// holds (the connector subscribes every category it pulls at once).
const subscriptionTopic = "all"

// Subscribe retry backoff.
const (
	subscribeBackoff    = time.Minute
	subscribeMaxBackoff = 6 * time.Hour
	// subscribeTimeout bounds one subscribe call at the provider.
	subscribeTimeout = 30 * time.Second
	// defaultSubscribeBatch bounds the rows one pass handles.
	defaultSubscribeBatch = 20
	// subscribePassBudget bounds one pass, so a slow provider delays the
	// next tick at most this long.
	subscribePassBudget = 2 * time.Minute
	// ResubscribeAfter: a subscription whose connection heard nothing for
	// PushSilence is renewed at the provider once it is this old, in case
	// the provider dropped it (some do after failed deliveries, without
	// telling anyone). At most once per ResubscribeAfter per connection.
	ResubscribeAfter = 7 * 24 * time.Hour
)

// RenewSilent puts back to pending up to n subscribed rows of enabled
// providers whose connection had no notification for PushSilence and that
// were subscribed more than ResubscribeAfter ago; the subscribe pass then
// renews them (subscribing again is harmless). attempts is set to 1 so a
// disconnect meanwhile still unsubscribes the live subscription. It returns
// how many rows it reset.
func (s *Scheduler) RenewSilent(ctx context.Context, n int) (int, error) {
	slugs, err := s.subscribable(ctx, subSubscribed)
	if err != nil || len(slugs) == 0 {
		return 0, err
	}
	now := s.d.now()
	tag, err := s.d.Pool.Exec(ctx, `
		UPDATE ohs_sync.inbound_subscriptions SET status = 'pending', attempts = 1,
			next_attempt_at = $1, last_error = NULL, updated_at = now()
		WHERE id IN (
			SELECT sub.id FROM ohs_sync.inbound_subscriptions sub
			JOIN ohs_sync.connections c ON c.tenant_id = sub.tenant_id AND c.user_id = sub.user_id
				AND c.provider = sub.provider AND c.status = 'active'
			WHERE sub.status = 'subscribed' AND sub.provider = ANY($2)
			  AND sub.subscribed_at < $3
			  AND (c.last_event_at IS NULL OR c.last_event_at < $4)
			ORDER BY sub.subscribed_at
			LIMIT $5
			FOR UPDATE OF sub SKIP LOCKED)
		  AND status = 'subscribed'`,
		now, slugs, now.Add(-ResubscribeAfter), now.Add(-PushSilence), n)
	if err != nil {
		return 0, fmt.Errorf("renew silent subscriptions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// subscriber reports whether provider takes subscriptions on this instance.
func (d Deps) subscriber(provider string) (connector.Subscriber, bool) {
	oc, ok := d.capableOAuth(provider)
	if !ok {
		return nil, false
	}
	s, ok := oc.(connector.Subscriber)
	return s, ok
}

// enqueueSubscription writes (or resets) the pending subscription row of a
// connection, in the connect transaction. The row is due at the application
// clock's now, the clock the subscribe pass compares with: the database's
// own now() can run ahead of it and keep a new row from being due.
func (d Deps) enqueueSubscription(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, provider string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO ohs_sync.inbound_subscriptions (tenant_id, user_id, provider, topic, callback_url, status, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6)
		ON CONFLICT (tenant_id, user_id, provider, topic) DO UPDATE SET
			callback_url = EXCLUDED.callback_url, status = 'pending', attempts = 0,
			next_attempt_at = EXCLUDED.next_attempt_at, last_error = NULL, updated_at = now()`,
		tenantID, userID, provider, subscriptionTopic, d.InboundURL(provider), d.now()); err != nil {
		return fmt.Errorf("enqueue subscription: %w", err)
	}
	return nil
}

func subscribeDelay(attempts int) time.Duration {
	d := subscribeBackoff
	for i := 1; i < attempts && d < subscribeMaxBackoff; i++ {
		d *= 2
	}
	return min(d, subscribeMaxBackoff)
}

type subRef struct {
	id                 uuid.UUID
	tenant, user       uuid.UUID
	provider, callback string
}

// SubscribePending subscribes up to n due pending rows at their providers and
// returns how many it handled (subscribed, failed or dropped). Only rows of
// providers enabled here that take subscriptions are looked at, so rows of a
// disabled provider never fill the batch. The collect role runs it in its own
// loop; tools and tests call it directly.
func (s *Scheduler) SubscribePending(ctx context.Context, n int) (int, error) {
	return s.subscribePass(ctx, ctx, n)
}

// subscribePass lists under ctx (it stops picking rows once ctx ends) and
// works under jobCtx, bounded by subscribePassBudget.
func (s *Scheduler) subscribePass(ctx, jobCtx context.Context, n int) (int, error) {
	slugs, err := s.subscribable(ctx, subPending)
	if err != nil || len(slugs) == 0 {
		return 0, err
	}
	rows, err := s.d.Pool.Query(ctx, `
		SELECT id, tenant_id, user_id, provider, callback_url
		FROM ohs_sync.inbound_subscriptions
		WHERE status = 'pending' AND next_attempt_at <= $1 AND provider = ANY($3)
		ORDER BY next_attempt_at LIMIT $2`, s.d.now(), n, slugs)
	if err != nil {
		return 0, fmt.Errorf("list pending subscriptions: %w", err)
	}
	subs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (subRef, error) {
		var x subRef
		err := r.Scan(&x.id, &x.tenant, &x.user, &x.provider, &x.callback)
		return x, err
	})
	if err != nil {
		return 0, fmt.Errorf("list pending subscriptions: %w", err)
	}
	pctx, cancel := context.WithTimeout(jobCtx, subscribePassBudget)
	defer cancel()
	done := 0
	for _, sub := range subs {
		if ctx.Err() != nil || pctx.Err() != nil {
			break
		}
		handled, err := s.subscribeOne(pctx, sub)
		if err != nil {
			slog.ErrorContext(ctx, "collect: subscribe", "tenant_id", sub.tenant, "user_id", sub.user,
				"provider", sub.provider, "error", err)
			continue
		}
		if handled {
			done++
		}
	}
	return done, nil
}

// subscribable lists the providers enabled here that take subscriptions:
// Deps.Slugs when set, else the providers of rows in status.
func (s *Scheduler) subscribable(ctx context.Context, status string) ([]string, error) {
	candidates := s.d.Slugs
	if candidates == nil {
		rows, err := s.d.Pool.Query(ctx, `SELECT DISTINCT provider FROM ohs_sync.inbound_subscriptions WHERE status = $1`, status)
		if err != nil {
			return nil, fmt.Errorf("list subscription providers: %w", err)
		}
		candidates, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("list subscription providers: %w", err)
		}
	}
	out := make([]string, 0, len(candidates))
	for _, slug := range candidates {
		if _, ok := s.d.subscriber(slug); ok {
			out = append(out, slug)
		}
	}
	return out, nil
}

// subscribeOne subscribes one row in three steps, so a call that worked at
// the provider is never lost:
//
//  1. Under the connection lock, then the row lock: recheck the row is
//     pending and due, renew the tokens, count the attempt and lease the row
//     (next_attempt_at moves past the call's deadline), commit.
//  2. Call the provider, holding no lock.
//  3. Under the same locks, record the outcome if the row is still pending;
//     if it was removed meanwhile (disconnect), undo the subscription.
//
// A success whose recording fails leaves a tried pending row: it is retried
// (subscribing twice is harmless) and a disconnect unsubscribes it. It
// reports false when another instance got the row first.
func (s *Scheduler) subscribeOne(ctx context.Context, sub subRef) (bool, error) {
	d := s.d
	log := slog.With("tenant_id", sub.tenant, "user_id", sub.user, "provider", sub.provider)
	if _, ok := d.capableOAuth(sub.provider); !ok {
		// Provider disabled on this instance: leave the row for one that has it.
		log.WarnContext(ctx, "collect: subscription left pending, provider not enabled in this process")
		return false, nil
	}

	// Step 1.
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	c, connGone, status, due, err := lockSub(ctx, tx, sub, d.now())
	if err != nil {
		return false, err
	}
	if status != subPending || !due {
		// Gone, or another instance handled it since it was listed.
		return false, nil
	}
	drop := func(reason string) (bool, error) {
		if _, err := tx.Exec(ctx, `UPDATE ohs_sync.inbound_subscriptions
			SET status = 'removed', last_error = $2, updated_at = now() WHERE id = $1`, sub.id, reason); err != nil {
			return false, fmt.Errorf("drop subscription: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit: %w", err)
		}
		log.InfoContext(ctx, "collect: subscription dropped", "reason", reason)
		return true, nil
	}
	if connGone || c.status != StatusActive {
		return drop("connection not active")
	}
	// The keys the connection was made with subscribe it.
	oc, ok := d.oauthOf(c)
	if !ok {
		log.WarnContext(ctx, "collect: subscription left pending, the connection's provider keys are not available in this process")
		return false, nil
	}
	sc, isSub := oc.(connector.Subscriber)
	if !isSub {
		return drop("provider does not take subscriptions")
	}
	conn, err := d.renewLocked(ctx, ctx, tx, c, "")
	if errors.Is(err, connector.ErrNeedsReauth) {
		// renewLocked marked the connection needs_reauth in tx.
		return drop("provider rejected the grant")
	}
	renewErr := err
	// attempts and the stored lease identify this attempt: a disconnect and
	// reconnect during the call reset the same row (attempts 0, due now),
	// and step 3 must not take that new incarnation for this one.
	var attempts int
	var lease time.Time
	if err := tx.QueryRow(ctx, `UPDATE ohs_sync.inbound_subscriptions
		SET attempts = attempts + 1, next_attempt_at = $2, updated_at = now() WHERE id = $1
		RETURNING attempts, next_attempt_at`, sub.id, d.now().Add(subscribeTimeout+subscribeBackoff)).Scan(&attempts, &lease); err != nil {
		return false, fmt.Errorf("start attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}

	// Step 2.
	callErr := renewErr
	if callErr == nil {
		sctx, cancel := context.WithTimeout(ctx, subscribeTimeout)
		callErr = sc.Subscribe(sctx, conn, sub.callback)
		cancel()
	}

	// Step 3, recorded even when ctx ran out during the call.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	same, err := s.recordOutcome(rctx, sub, attempts, lease, callErr)
	if err != nil {
		return false, err
	}
	if same {
		if callErr != nil {
			log.WarnContext(ctx, "collect: subscribe failed, will retry", "attempts", attempts, "error", safeErr(callErr))
		} else {
			log.InfoContext(ctx, "collect: subscribed to provider notifications")
		}
		return true, nil
	}
	// The row was removed or reset (disconnect, maybe reconnect) while the
	// call ran: the disconnect may have unsubscribed before this subscribe
	// landed. Undo it with the tokens it used, holding no lock; a new
	// incarnation is left pending for its own attempt.
	if callErr == nil {
		uctx, ucancel := context.WithTimeout(rctx, subscribeTimeout)
		if err := sc.Unsubscribe(uctx, conn, sub.callback); err != nil {
			log.WarnContext(ctx, "collect: undo of a subscribe raced by a disconnect failed", "error", safeErr(err))
		}
		ucancel()
	}
	return true, nil
}

// recordOutcome records the result of attempt (attempts, lease) on the row,
// under the connection lock then the row lock. same is false, with nothing
// written, when the row is no longer that attempt's (removed, or reset by a
// reconnect).
func (s *Scheduler) recordOutcome(ctx context.Context, sub subRef, attempts int, lease time.Time, callErr error) (same bool, err error) {
	d := s.d
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM ohs_sync.connections
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 FOR UPDATE`,
		sub.tenant, sub.user, sub.provider); err != nil {
		return false, fmt.Errorf("lock connection: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT status = 'pending' AND attempts = $2 AND next_attempt_at = $3
		FROM ohs_sync.inbound_subscriptions WHERE id = $1 FOR UPDATE`, sub.id, attempts, lease).Scan(&same)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock subscription: %w", err)
	}
	if !same {
		return false, nil
	}
	if callErr != nil {
		next := d.now().Add(subscribeDelay(attempts))
		if _, err := tx.Exec(ctx, `UPDATE ohs_sync.inbound_subscriptions
			SET last_error = $2, next_attempt_at = $3, updated_at = now() WHERE id = $1`,
			sub.id, safeErr(callErr), next); err != nil {
			return false, fmt.Errorf("record subscribe failure: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit: %w", err)
		}
		return true, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE ohs_sync.inbound_subscriptions
		SET status = 'subscribed', subscribed_at = $2, last_error = NULL, updated_at = now()
		WHERE id = $1`, sub.id, d.now()); err != nil {
		return false, fmt.Errorf("record subscription: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// lockSub locks the connection row, then the subscription row (the lock
// order everywhere), and reads both. status is "" when the row is gone.
func lockSub(ctx context.Context, tx pgx.Tx, sub subRef, now time.Time) (c connRow, connGone bool, status string, due bool, err error) {
	c, err = scanConn(tx.QueryRow(ctx, `SELECT `+connCols+`
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		FOR UPDATE`, sub.tenant, sub.user, sub.provider))
	connGone = errors.Is(err, pgx.ErrNoRows)
	if err != nil && !connGone {
		return c, false, "", false, fmt.Errorf("lock connection: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT status, next_attempt_at <= $2 FROM ohs_sync.inbound_subscriptions
		WHERE id = $1 FOR UPDATE`, sub.id, now).Scan(&status, &due)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, connGone, "", false, nil
	}
	if err != nil {
		return c, connGone, "", false, fmt.Errorf("lock subscription: %w", err)
	}
	return c, connGone, status, due, nil
}

// revokeLocked ends a connection at the provider, best effort: c is the
// connections row the caller locked FOR UPDATE in tx. Tokens are refreshed
// first when expired; every subscribed (or tried pending) row is
// unsubscribed; the grant is revoked when the connector can; every live row
// is marked removed. Provider calls run under pctx; the database work under
// ctx, which the caller keeps alive past pctx, so what the provider already
// did is always recorded. Provider failures are logged, never returned; a
// database failure is returned.
func (d Deps) revokeLocked(ctx, pctx context.Context, tx pgx.Tx, c connRow) error {
	log := slog.With("tenant_id", c.tenant, "user_id", c.user, "provider", c.provider)
	rows, err := tx.Query(ctx, `SELECT id, callback_url, status, attempts FROM ohs_sync.inbound_subscriptions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status <> 'removed'
		ORDER BY id FOR UPDATE`, c.tenant, c.user, c.provider)
	if err != nil {
		return fmt.Errorf("lock subscriptions: %w", err)
	}
	type live struct {
		id               uuid.UUID
		callback, status string
		attempts         int
	}
	subs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (live, error) {
		var x live
		err := r.Scan(&x.id, &x.callback, &x.status, &x.attempts)
		return x, err
	})
	if err != nil {
		return fmt.Errorf("lock subscriptions: %w", err)
	}

	oc, ok := d.oauthOf(c)
	if !ok {
		// Its keys are not set in this process (or the tenant keys it was
		// made with are gone): nothing can be called at the provider. The
		// rows are still marked removed below.
		log.WarnContext(ctx, "collect: provider not enabled in this process, grant not revoked and subscriptions not unsubscribed at the provider")
	}
	if ok && c.accessEnc != nil {
		conn, err := d.renewLocked(ctx, pctx, tx, c, "")
		if err != nil {
			log.WarnContext(ctx, "collect: token refresh before revoke failed, using stored tokens", "error", safeErr(err))
			conn, err = d.open(c)
		}
		if err != nil {
			log.WarnContext(ctx, "collect: revoke skipped, tokens unreadable")
		} else {
			if sc, ok := oc.(connector.Subscriber); ok {
				for _, s := range subs {
					// A pending row that was tried may be live at the
					// provider (the call worked, recording it did not).
					if s.status != subSubscribed && s.attempts == 0 {
						continue
					}
					if err := sc.Unsubscribe(pctx, conn, s.callback); err != nil {
						log.WarnContext(ctx, "collect: provider unsubscribe failed", "error", safeErr(err))
					}
				}
			}
			if rv, ok := oc.(connector.Revoker); ok {
				if err := rv.Revoke(pctx, conn.Tokens); err != nil {
					log.WarnContext(ctx, "collect: provider revoke failed", "error", safeErr(err))
				}
			}
		}
	}
	if len(subs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE ohs_sync.inbound_subscriptions
			SET status = 'removed', updated_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 AND status <> 'removed'`,
			c.tenant, c.user, c.provider); err != nil {
			return fmt.Errorf("remove subscriptions: %w", err)
		}
	}
	return nil
}
