// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Reasons a connection is broken, and what fixes it (see read.HealthOf).
const (
	BrokenNeedsReauth  = read.BrokenNeedsReauth
	BrokenMissingScope = read.BrokenMissingScope
	BrokenFailing      = read.BrokenFailing
	BrokenRateLimited  = read.BrokenRateLimited
	ActionReconnect    = read.ActionReconnect
	ActionWait         = read.ActionWait
)

// RateLimitBrokenAfter is the shortest provider rate limit that breaks a
// connection. Shorter waits are routine and only delay a pull.
const RateLimitBrokenAfter = time.Hour

// reauthReason is the broken reason of a refused grant: missing_scope when
// the connector said a permission is missing, needs_reauth otherwise.
func reauthReason(err error) string {
	if errors.Is(err, connector.ErrMissingScope) {
		return BrokenMissingScope
	}
	return BrokenNeedsReauth
}

// severe reports whether reason needs the user (it replaces a reason that
// does not, and is never replaced by one).
func severe(reason string) bool { return read.ActionFor(reason) == ActionReconnect }

// markBroken records that a connection broke for reason and announces
// connection.broken, in q's transaction. A connection already broken for the
// same reason, or for a reason that needs the user while this one does not,
// is left alone and announces nothing. detail adds fields to the event.
func markBroken(ctx context.Context, q execer, tenantID, userID uuid.UUID, provider, reason string, now time.Time, detail map[string]any) error {
	tag, err := q.Exec(ctx, `
		UPDATE ohs_sync.connections SET broken_reason = $4,
			broken_since = CASE WHEN broken_reason IS NULL THEN $5 ELSE broken_since END,
			updated_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		  AND (broken_reason IS NULL OR (broken_reason <> $4 AND $6))`,
		tenantID, userID, provider, reason, now, severe(reason))
	if err != nil {
		return fmt.Errorf("mark connection broken: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	data := map[string]any{"provider": provider, "reason": reason, "action": read.ActionFor(reason)}
	for k, v := range detail {
		data[k] = v
	}
	if _, err := webhook.Enqueue(ctx, q, webhook.Event{
		Tenant: tenantID, User: &userID, Type: webhook.ConnectionBroken, Data: data,
	}); err != nil {
		return err
	}
	return nil
}

// markRestored clears a broken connection after it synced and announces
// connection.restored, in tx. A healthy connection announces nothing.
func markRestored(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, provider string, now time.Time) error {
	var prev string
	var since *time.Time
	err := tx.QueryRow(ctx, `
		UPDATE ohs_sync.connections c SET broken_reason = NULL, broken_since = NULL,
			rate_limited_until = NULL, updated_at = now()
		FROM (SELECT broken_reason, broken_since FROM ohs_sync.connections
		      WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 FOR UPDATE) old
		WHERE c.tenant_id = $1 AND c.user_id = $2 AND c.provider = $3 AND c.broken_reason IS NOT NULL
		RETURNING old.broken_reason, old.broken_since`,
		tenantID, userID, provider).Scan(&prev, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark connection restored: %w", err)
	}
	data := map[string]any{"provider": provider, "previous_reason": prev}
	if since != nil {
		data["broken_since"] = since.UTC()
		data["broken_seconds"] = int64(now.Sub(*since) / time.Second)
	}
	if _, err := webhook.Enqueue(ctx, tx, webhook.Event{
		Tenant: tenantID, User: &userID, Type: webhook.ConnectionRestored, Data: data,
	}); err != nil {
		return err
	}
	return nil
}
