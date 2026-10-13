// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// RefreshMargin: a token expiring within this margin is refreshed before use.
const RefreshMargin = 5 * time.Minute

// errInactive means the connection is not active (needs_reauth, disconnected
// or gone), or the end user is gone or being deleted: its pull jobs stop and
// uploads get 410.
var errInactive = errors.New("collect: connection is not active")

func needsRefresh(t connector.Tokens, now time.Time) bool {
	return !t.Expiry.IsZero() && t.Expiry.Sub(now) < RefreshMargin
}

// connection loads an active connection with usable tokens, refreshing them
// first when they are about to expire. ErrNeedsReauth from the provider marks
// the connection needs_reauth and parks its pending pull jobs.
func (d Deps) connection(ctx context.Context, oc connector.OAuthConnector, tenantID, userID uuid.UUID, provider string) (connector.Connection, error) {
	c, err := scanConn(d.Pool.QueryRow(ctx, `SELECT `+connCols+`
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		tenantID, userID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return connector.Connection{}, errInactive
	}
	if err != nil {
		return connector.Connection{}, fmt.Errorf("load connection: %w", err)
	}
	if c.status != StatusActive {
		return connector.Connection{}, errInactive
	}
	conn, err := d.open(c)
	if err != nil {
		return connector.Connection{}, err
	}
	if !needsRefresh(conn.Tokens, d.now()) {
		return conn, nil
	}
	return d.refresh(ctx, oc, tenantID, userID, provider, "")
}

// refresh renews the tokens under a row lock, so two workers never spend the
// same refresh token (providers rotate it: the loser would be told the grant
// is revoked). After the lock the row is read again: a token another worker
// just refreshed is used as is.
//
// rejected is empty for a refresh before expiry. Otherwise it is the access
// token the provider just refused: the refresh is forced, unless the stored
// token already differs from it (another worker refreshed meanwhile).
func (d Deps) refresh(ctx context.Context, oc connector.OAuthConnector, tenantID, userID uuid.UUID, provider, rejected string) (connector.Connection, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return connector.Connection{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	c, err := scanConn(tx.QueryRow(ctx, `SELECT `+connCols+`
		FROM ohs_sync.connections WHERE tenant_id = $1 AND user_id = $2 AND provider = $3
		FOR UPDATE`, tenantID, userID, provider))
	if errors.Is(err, pgx.ErrNoRows) {
		return connector.Connection{}, errInactive
	}
	if err != nil {
		return connector.Connection{}, fmt.Errorf("lock connection: %w", err)
	}
	if c.status != StatusActive {
		return connector.Connection{}, errInactive
	}
	conn, err := d.renewLocked(ctx, ctx, tx, oc, c, rejected)
	if errors.Is(err, connector.ErrNeedsReauth) {
		if cerr := tx.Commit(ctx); cerr != nil {
			return connector.Connection{}, fmt.Errorf("commit: %w", cerr)
		}
		return connector.Connection{}, err
	}
	if err != nil {
		return connector.Connection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return connector.Connection{}, fmt.Errorf("commit: %w", err)
	}
	return conn, nil
}

// renewLocked returns the connection of c, a row the caller locked FOR UPDATE
// in tx, with usable tokens: refreshed (and stored in tx) when they expire
// within RefreshMargin, or when rejected names the stored access token. A
// grant the provider refuses marks the connection needs_reauth in tx and
// gives connector.ErrNeedsReauth; the caller commits or rolls back. The
// provider call runs under pctx, the database work under ctx, so a refresh the
// provider granted is stored even when pctx runs out right after.
func (d Deps) renewLocked(ctx, pctx context.Context, tx pgx.Tx, oc connector.OAuthConnector, c connRow, rejected string) (connector.Connection, error) {
	tenantID, userID, provider := c.tenant, c.user, c.provider
	conn, err := d.open(c)
	if err != nil {
		return connector.Connection{}, err
	}
	if rejected == "" && !needsRefresh(conn.Tokens, d.now()) {
		return conn, nil
	}
	if rejected != "" && conn.Tokens.Access != rejected {
		return conn, nil
	}

	fresh, err := oc.Refresh(pctx, conn.Tokens)
	if errors.Is(err, connector.ErrNeedsReauth) {
		if merr := markNeedsReauth(ctx, tx, tenantID, userID, provider, reauthReason(err), d.now()); merr != nil {
			return connector.Connection{}, merr
		}
		return connector.Connection{}, connector.ErrNeedsReauth
	}
	if err != nil {
		return connector.Connection{}, fmt.Errorf("refresh: %w", err)
	}
	// Providers that do not rotate omit the refresh token or the user id.
	if fresh.Refresh == "" {
		fresh.Refresh = conn.Tokens.Refresh
	}
	if fresh.ProviderUserID == "" {
		fresh.ProviderUserID = conn.Tokens.ProviderUserID
	}
	if fresh.Scopes == nil {
		fresh.Scopes = conn.Tokens.Scopes
	}
	s, err := d.seal(tenantID, userID, provider, fresh)
	if err != nil {
		return connector.Connection{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ohs_sync.connections SET access_token_enc = $4, refresh_token_enc = $5,
			token_expires_at = $6, scopes = $7,
			provider_user_id = coalesce($8, provider_user_id), updated_at = now()
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		tenantID, userID, provider, s.access, s.refresh, s.expiresAt, s.scopes, s.providerUserID); err != nil {
		return connector.Connection{}, fmt.Errorf("store refreshed tokens: %w", err)
	}
	conn.Tokens = fresh
	return conn, nil
}
