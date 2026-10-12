// SPDX-License-Identifier: AGPL-3.0-only

// Package queue holds the Postgres-backed job queues: etl_jobs (one job per
// raw object) and pull_jobs (one job per provider pull window).
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// VisibilityTimeout is how long a claimed job may stay running before
	// another worker can claim it again.
	VisibilityTimeout = 10 * time.Minute
	// MaxAttempts is the number of failed attempts after which a job is parked.
	MaxAttempts = 5
	// BackoffBase is the first retry delay; it doubles with every attempt.
	BackoffBase = 30 * time.Second

	maxErrorLen = 1000
)

// ErrNotRunning is returned by Complete, Fail and Defer when the caller no
// longer holds the claim: the job was reclaimed by another worker after the
// visibility timeout, or it is parked, done, or unknown.
var ErrNotRunning = errors.New("queue: job is not running")

// errText makes a cause safe to store: valid UTF-8, no NUL bytes, at most
// maxErrorLen bytes cut on a rune boundary.
func errText(cause error) string {
	if cause == nil {
		return ""
	}
	s := strings.ToValidUTF8(cause.Error(), "?")
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > maxErrorLen {
		n := maxErrorLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return s
}

// poolExec is the part of *pgxpool.Pool the shared helpers need.
type poolExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func fail(ctx context.Context, table string, pool poolExec, id int64, claimedAt time.Time, cause error) error {
	// attempts is read before the increment: the first failure waits 30s, the
	// second 60s, and so on. The fifth failure parks the job.
	tag, err := pool.Exec(ctx, fmt.Sprintf(`
		UPDATE ohs_sync.%s SET
			attempts   = attempts + 1,
			status     = CASE WHEN attempts + 1 >= $4 THEN 'parked' ELSE 'pending' END,
			not_before = now() + make_interval(secs => $5::double precision * power(2, attempts)),
			claimed_at = NULL,
			last_error = $3
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, table),
		id, claimedAt, errText(cause), MaxAttempts, BackoffBase.Seconds())
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}

func complete(ctx context.Context, table string, pool poolExec, id int64, claimedAt time.Time) error {
	tag, err := pool.Exec(ctx, fmt.Sprintf(`
		UPDATE ohs_sync.%s SET status = 'done', claimed_at = NULL
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, table), id, claimedAt)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}

// claimSQL builds the claim statement. One statement picks eligible rows with
// FOR UPDATE SKIP LOCKED and either marks them running or, when a reclaim
// would exhaust the attempts, parks them; parked rows are not returned.
// $1 = limit, $2 = visibility timeout seconds, $3.. = extra filter args.
// order is the ORDER BY of the pick.
func claimSQL(table, extraWhere, cols string) string {
	return claimSQLOrdered(table, extraWhere, cols, "not_before, id")
}

func claimSQLOrdered(table, extraWhere, cols, order string) string {
	return fmt.Sprintf(`
		WITH picked AS (
			SELECT id, (status = 'running' AND attempts + 1 >= %[1]d) AS exhausted
			FROM ohs_sync.%[2]s
			WHERE %[3]s((status = 'pending' AND not_before <= now())
				OR (status = 'running' AND claimed_at < now() - make_interval(secs => $2::double precision)))
			ORDER BY %[5]s
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		), upd AS (
			UPDATE ohs_sync.%[2]s t SET
				attempts   = t.attempts + (t.status = 'running')::int,
				status     = CASE WHEN p.exhausted THEN 'parked' ELSE 'running' END,
				claimed_at = CASE WHEN p.exhausted THEN NULL ELSE now() END,
				last_error = CASE WHEN p.exhausted THEN 'visibility timeout expired' ELSE t.last_error END
			FROM picked p WHERE t.id = p.id
			RETURNING t.*
		)
		SELECT %[4]s FROM upd WHERE status = 'running'`, MaxAttempts, table, extraWhere, cols, order)
}
