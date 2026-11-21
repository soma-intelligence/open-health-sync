// SPDX-License-Identifier: AGPL-3.0-only

package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Backlog is what waits in one queue: the jobs due and not claimed yet, and
// how long the oldest of them has been due. Jobs waiting for a retry or a
// rate limit (not_before in the future) are not due and not counted.
type Backlog struct {
	Depth     int64
	OldestAge time.Duration
}

// ETLBacklog returns the backlog of each ETL tier, the tiers with nothing
// due included. It reads the index the claim reads (pending jobs by tier and
// not_before), never the table: cheap enough for a sample every few seconds,
// too dear to run on every scrape of a long queue.
func ETLBacklog(ctx context.Context, pool *pgxpool.Pool) (map[string]Backlog, error) {
	rows, err := pool.Query(ctx, `
		SELECT tier, count(*), coalesce(extract(epoch FROM now() - min(not_before)), 0)::double precision
		FROM ohs_sync.etl_jobs
		WHERE status = 'pending' AND not_before <= now()
		GROUP BY tier`)
	if err != nil {
		return nil, fmt.Errorf("etl backlog: %w", err)
	}
	defer rows.Close()
	out := map[string]Backlog{TierStandard: {}, TierHighFreq: {}}
	for rows.Next() {
		var (
			tier string
			b    Backlog
			age  float64
		)
		if err := rows.Scan(&tier, &b.Depth, &age); err != nil {
			return nil, fmt.Errorf("etl backlog: %w", err)
		}
		b.OldestAge = backlogAge(age)
		out[tier] = b
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("etl backlog: %w", err)
	}
	return out, nil
}

// PullBacklog returns the backlog of the pull queue, read from the index of
// its pending jobs like ETLBacklog.
func PullBacklog(ctx context.Context, pool *pgxpool.Pool) (Backlog, error) {
	var (
		b   Backlog
		age float64
	)
	err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(extract(epoch FROM now() - min(not_before)), 0)::double precision
		FROM ohs_sync.pull_jobs
		WHERE status = 'pending' AND not_before <= now()`).Scan(&b.Depth, &age)
	if err != nil {
		return Backlog{}, fmt.Errorf("pull backlog: %w", err)
	}
	b.OldestAge = backlogAge(age)
	return b, nil
}

func backlogAge(seconds float64) time.Duration {
	return time.Duration(max(seconds, 0) * float64(time.Second))
}
