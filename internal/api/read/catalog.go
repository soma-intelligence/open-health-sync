// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// QuantityType is one canonical quantity type the server knows: the value of
// the type parameter of Samples, with the unit its samples are stored in.
type QuantityType struct {
	Type      string  `json:"type"`
	Unit      *string `json:"unit"`
	UCUMUnit  *string `json:"ucum_unit"`
	LOINCCode *string `json:"loinc_code"`
}

// QuantityTypes lists the quantity type catalog (ohs_data.quantity_codes, a
// reference table shared by every tenant), ordered by type.
func QuantityTypes(ctx context.Context, db Querier) ([]QuantityType, error) {
	rows, err := db.Query(ctx, `SELECT canonical_code, storage_unit, ucum_unit, loinc_code
		FROM ohs_data.quantity_codes ORDER BY canonical_code`)
	if err != nil {
		return nil, fmt.Errorf("read: quantity types: %w", err)
	}
	defer rows.Close()
	out := []QuantityType{}
	for rows.Next() {
		var q QuantityType
		if err := rows.Scan(&q.Type, &q.Unit, &q.UCUMUnit, &q.LOINCCode); err != nil {
			return nil, fmt.Errorf("read: quantity types: scan: %w", err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: quantity types: %w", err)
	}
	return out, nil
}

// Connection is a user's link to one provider. Tokens never leave the store.
type Connection struct {
	Provider   string     `json:"provider"`
	Status     string     `json:"status"` // active, needs_reauth or disconnected
	Scopes     []string   `json:"scopes"`
	LastSyncAt *time.Time `json:"last_sync_at"`
	// InitialBackfillAt is when the pull of the user's history was queued,
	// null until then.
	InitialBackfillAt *time.Time `json:"initial_backfill_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	// ConnectedAt is CreatedAt under the name earlier versions served, kept
	// for the clients that read it.
	ConnectedAt time.Time `json:"connected_at"`
	// Health says whether the connection syncs, and if not why and what
	// fixes it.
	Health ConnectionHealth `json:"health"`
}

// Reasons a connection is broken (ohs_sync.connections.broken_reason).
const (
	// BrokenNeedsReauth: the provider refused the grant (token expired or
	// revoked). The user must connect again.
	BrokenNeedsReauth = "needs_reauth"
	// BrokenMissingScope: the grant lacks a permission the data needs. The
	// user must connect again and allow it.
	BrokenMissingScope = "missing_scope"
	// BrokenFailing: several pulls in a row failed (provider outage,
	// unexpected answers). Pulls go on with backoff.
	BrokenFailing = "failing"
	// BrokenRateLimited: the provider asked to wait an hour or more (a spent
	// daily quota). Pulls resume by themselves.
	BrokenRateLimited = "rate_limited"
)

// Actions that fix a broken connection.
const (
	ActionReconnect = "reconnect" // send the user through the connect flow again
	ActionWait      = "wait"      // nothing to do: syncing resumes by itself
)

// ConnectionHealth is the health of one connection.
type ConnectionHealth struct {
	// Healthy is false while the connection is broken.
	Healthy bool `json:"healthy"`
	// Reason is why it is broken (needs_reauth, missing_scope, failing,
	// rate_limited); null when healthy.
	Reason *string `json:"reason"`
	// Action is what fixes it: reconnect or wait; null when healthy.
	Action *string `json:"action"`
	// Since is when it broke.
	Since *time.Time `json:"since"`
	// RetryAt is when a rate limited connection is pulled again.
	RetryAt *time.Time `json:"retry_at"`
}

// ActionFor is what fixes a connection broken for reason.
func ActionFor(reason string) string {
	if reason == BrokenNeedsReauth || reason == BrokenMissingScope {
		return ActionReconnect
	}
	return ActionWait
}

// HealthOf builds the health of a connection from its broken_reason,
// broken_since and rate_limited_until columns.
func HealthOf(reason *string, since, rateLimitedUntil *time.Time, now time.Time) ConnectionHealth {
	if reason == nil {
		return ConnectionHealth{Healthy: true}
	}
	h := ConnectionHealth{Reason: reason, Since: since}
	utc(h.Since)
	a := ActionFor(*reason)
	h.Action = &a
	if rateLimitedUntil != nil && rateLimitedUntil.After(now) {
		t := rateLimitedUntil.UTC()
		h.RetryAt = &t
	}
	return h
}

// Connections lists every provider connection of the user, ordered by
// provider. ErrNotFound when the user is not the tenant's.
func Connections(ctx context.Context, db Querier, tenantID, userID uuid.UUID) ([]Connection, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT provider, status, scopes, last_sync_at, initial_backfill_at,
			created_at, updated_at, broken_reason, broken_since, rate_limited_until
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 ORDER BY provider`, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("read: connections: %w", err)
	}
	defer rows.Close()
	out := []Connection{}
	now := time.Now()
	for rows.Next() {
		var c Connection
		var reason *string
		var since, until *time.Time
		if err := rows.Scan(&c.Provider, &c.Status, &c.Scopes, &c.LastSyncAt, &c.InitialBackfillAt,
			&c.CreatedAt, &c.UpdatedAt, &reason, &since, &until); err != nil {
			return nil, fmt.Errorf("read: connections: scan: %w", err)
		}
		utc(c.LastSyncAt)
		utc(c.InitialBackfillAt)
		utc(&c.CreatedAt)
		utc(&c.UpdatedAt)
		c.ConnectedAt = c.CreatedAt
		c.Health = HealthOf(reason, since, until, now)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: connections: %w", err)
	}
	return out, nil
}

// ConnectionDetail is a user's link to one provider as the user archive
// describes it: a Connection plus the provider's own id of the user, the
// granted scopes and the last change. Tokens and every sealed value never
// leave the store.
type ConnectionDetail struct {
	Provider       string     `json:"provider"`
	Status         string     `json:"status"`
	ProviderUserID *string    `json:"provider_user_id"`
	Scopes         []string   `json:"scopes"`
	LastSyncAt     *time.Time `json:"last_sync_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ConnectionDetails lists every provider connection of the user, ordered by
// provider. ErrNotFound when the user is not the tenant's.
func ConnectionDetails(ctx context.Context, db Querier, tenantID, userID uuid.UUID) ([]ConnectionDetail, error) {
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT provider, status, provider_user_id, scopes, last_sync_at, created_at, updated_at
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 ORDER BY provider`, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("read: connection details: %w", err)
	}
	defer rows.Close()
	out := []ConnectionDetail{}
	for rows.Next() {
		var c ConnectionDetail
		if err := rows.Scan(&c.Provider, &c.Status, &c.ProviderUserID, &c.Scopes, &c.LastSyncAt,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("read: connection details: scan: %w", err)
		}
		if c.Scopes == nil {
			c.Scopes = []string{}
		}
		utc(c.LastSyncAt)
		utc(&c.CreatedAt)
		utc(&c.UpdatedAt)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read: connection details: %w", err)
	}
	return out, nil
}
