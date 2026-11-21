// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Backlog returns how many deliveries are due and not claimed yet, and how
// long the oldest of them has been due. Deliveries waiting for a retry are
// not due and not counted. It reads the index of the deliveries not finished
// yet, never the table: meant for a sample every few seconds, not for every
// scrape.
func Backlog(ctx context.Context, pool *pgxpool.Pool) (depth int64, oldest time.Duration, err error) {
	var age float64
	err = pool.QueryRow(ctx, `
		SELECT count(*), coalesce(extract(epoch FROM now() - min(not_before)), 0)::double precision
		FROM ohs_core.webhook_deliveries
		WHERE status = 'pending' AND not_before <= now()`).Scan(&depth, &age)
	if err != nil {
		return 0, 0, fmt.Errorf("webhook backlog: %w", err)
	}
	return depth, time.Duration(max(age, 0) * float64(time.Second)), nil
}
