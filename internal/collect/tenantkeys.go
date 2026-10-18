// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/tenantkeys"
)

// RetireTenantKeys is the tenantkeys.Retire of the collect role. It runs in
// the transaction that removes a tenant's keys for provider, or replaces
// them with another application's: every active connection of the tenant
// made with keysID is set needs_reauth, broken for needs_reauth, with a
// connection.needs_reauth webhook event and its pending pulls parked, as
// when the provider refuses a grant; the consents started with those keys
// and not completed yet are dropped. Connecting the user again makes the
// connection with the keys the tenant has then. It returns how many
// connections it set needs_reauth.
func RetireTenantKeys(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, provider string, keysID uuid.UUID) (int, error) {
	rows, err := tx.Query(ctx, `SELECT user_id FROM ohs_sync.connections
		WHERE tenant_id = $1 AND provider = $2 AND tenant_keys_id = $3 AND status = 'active'
		ORDER BY user_id FOR UPDATE`, tenant, provider, keysID)
	if err != nil {
		return 0, fmt.Errorf("list connections of the tenant keys: %w", err)
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, fmt.Errorf("list connections of the tenant keys: %w", err)
	}
	now := time.Now()
	for _, u := range users {
		if err := markNeedsReauth(ctx, tx, tenant, u, provider, BrokenNeedsReauth, now); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ohs_sync.oauth_states
		WHERE tenant_id = $1 AND provider = $2 AND tenant_keys_id = $3`, tenant, provider, keysID); err != nil {
		return 0, fmt.Errorf("drop consents of the tenant keys: %w", err)
	}
	return len(users), nil
}

var _ tenantkeys.Retire = RetireTenantKeys
