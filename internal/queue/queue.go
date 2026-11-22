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

// settleBudget bounds how long a settle (Complete, Fail, Defer) is retried
// while the database cannot be reached. Half the visibility timeout: a job
// whose result could not be recorded by then is reclaimed as before.
var settleBudget = VisibilityTimeout / 2

// settleNow is the clock of the settle budget; tests replace it.
var settleNow = time.Now

// settleSleep waits d or until ctx ends; tests replace it.
var settleSleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryableSettle says whether a settle error may pass once the database is
// back: no answer from the server at all (a closed connection, a refused
// dial, a pool that cannot connect), or the server's own connection and
// shutdown classes (08, 57P01 to 57P03). An SQL error the server returned
// would fail again, and a cancelled context is the caller's.
func retryableSettle(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return strings.HasPrefix(pg.Code, "08") || pg.Code == "57P01" || pg.Code == "57P02" || pg.Code == "57P03"
	}
	return true
}

// settleExec runs a settle statement, retrying it with a backoff (250 ms
// doubling to 10 s, within settleBudget) while the database cannot be
// reached. Without it a worker whose job ended during a database restart
// dropped the outcome, and the job stayed running until the visibility
// timeout reclaimed it ten minutes later. The statements are fenced on
// claimed_at, so a retry of one that did commit matches no row and reports
// ErrNotRunning, which the workers log as a lost claim.
func settleExec(ctx context.Context, pool poolExec, sql string, args ...any) (pgconn.CommandTag, error) {
	deadline := settleNow().Add(settleBudget)
	wait := 250 * time.Millisecond
	for {
		tag, err := pool.Exec(ctx, sql, args...)
		if !retryableSettle(ctx, err) || settleNow().Add(wait).After(deadline) {
			return tag, err
		}
		if serr := settleSleep(ctx, wait); serr != nil {
			return tag, err
		}
		wait = min(2*wait, 10*time.Second)
	}
}

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

// backoff is a retry schedule: the first delay, doubled with every failed
// attempt up to max, and the number of failed attempts that parks the job.
type backoff struct {
	base, max time.Duration
	attempts  int
}

// standardBackoff is the schedule of Fail: 30s doubling, parked at the fifth
// failure. Its cap is never reached.
var standardBackoff = backoff{base: BackoffBase, max: BackoffBase << MaxAttempts, attempts: MaxAttempts}

func fail(ctx context.Context, table string, pool poolExec, id int64, claimedAt time.Time, cause error, b backoff) error {
	// attempts is read before the increment: the first failure waits the
	// base delay, the second twice that, and so on up to the cap. The
	// failure that reaches b.attempts parks the job.
	tag, err := settleExec(ctx, pool, fmt.Sprintf(`
		UPDATE ohs_sync.%s SET
			attempts   = attempts + 1,
			status     = CASE WHEN attempts + 1 >= $4 THEN 'parked' ELSE 'pending' END,
			not_before = now() + make_interval(secs => least($5::double precision * power(2, least(attempts, 40)), $6::double precision)),
			claimed_at = NULL,
			last_error = $3
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, table),
		id, claimedAt, errText(cause), b.attempts, b.base.Seconds(), b.max.Seconds())
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}

func complete(ctx context.Context, table string, pool poolExec, id int64, claimedAt time.Time) error {
	tag, err := settleExec(ctx, pool, fmt.Sprintf(`
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

// reclaimBatch bounds the expired claims one claim puts back.
const reclaimBatch = 1000

// reclaimSQL builds the statement that puts back the jobs whose claim
// expired: running past the visibility timeout ($1, in seconds). Each burns
// an attempt and waits again, or is parked when that was its last one. A
// reclaimed job keeps its not_before, so it is claimed before the jobs queued
// after it. The pick reads the reaper index (claimed_at of running jobs), so
// claims never scan the queue for expired claims. maxAttempts is the number
// of attempts that parks a job of this queue. extraWhere filters the pick
// ($2..), extraSet adds columns; in both, exhausted is the SQL condition that
// the job is out of attempts.
func reclaimSQL(table string, maxAttempts int, extraWhere, extraSet string) string {
	exhausted := fmt.Sprintf("t.attempts + 1 >= %d", maxAttempts)
	return fmt.Sprintf(`
		UPDATE ohs_sync.%[1]s t SET
			attempts   = t.attempts + 1,
			status     = CASE WHEN %[2]s THEN 'parked' ELSE 'pending' END,
			claimed_at = NULL,
			last_error = CASE WHEN %[2]s THEN 'visibility timeout expired' ELSE t.last_error END%[4]s
		WHERE t.id IN (
			SELECT id FROM ohs_sync.%[1]s
			WHERE %[3]sstatus = 'running' AND claimed_at < now() - make_interval(secs => $1::double precision)
			ORDER BY claimed_at
			LIMIT %[5]d
			FOR UPDATE SKIP LOCKED)`, table, exhausted, extraWhere, strings.ReplaceAll(extraSet, "exhausted", exhausted), reclaimBatch)
}
