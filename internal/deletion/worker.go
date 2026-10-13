// SPDX-License-Identifier: AGPL-3.0-only

package deletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// ConnectionRevoker revokes a user's provider connections (their OAuth grants)
// at the providers. It is best effort: an error is logged and deletion goes on.
type ConnectionRevoker interface {
	RevokeAll(ctx context.Context, tenantID, userID uuid.UUID) error
}

const (
	defaultInterval = 5 * time.Second
	// revokePerConnection is the provider budget of one connection
	// (refresh, unsubscribe, revoke); the total is capped at maxRevokeBudget.
	revokePerConnection = 35 * time.Second
	maxRevokeBudget     = 5 * time.Minute
	defaultBackoff      = 5 * time.Second
	maxBackoff          = 10 * time.Minute
)

// Worker finishes the deletion jobs the API queued.
//
// Slow steps (provider revocation, raw file deletion) run outside any
// transaction: they are idempotent, so two workers racing on one job only
// duplicate work. The row deletes and the job's done mark are one short
// transaction that takes the job row with FOR UPDATE SKIP LOCKED and
// rechecks it is still unfinished, so exactly one worker completes it.
// A crashed or failed attempt leaves the job pending for a later pass.
type Worker struct {
	pool    *pgxpool.Pool
	raw     rawstore.Store
	revoker ConnectionRevoker

	interval time.Duration
	backoff  time.Duration // first retry delay of a failing job; doubles, capped at maxBackoff
	grace    time.Duration // how long a job in flight may go on once Run's context ends

	mu    sync.Mutex
	fails map[uuid.UUID]failState
}

type failState struct {
	n     int
	until time.Time
}

// NewWorker returns a worker. revoker may be nil (no provider revocation).
func NewWorker(pool *pgxpool.Pool, raw rawstore.Store, revoker ConnectionRevoker) *Worker {
	return &Worker{pool: pool, raw: raw, revoker: revoker, interval: defaultInterval,
		backoff: defaultBackoff, fails: map[uuid.UUID]failState{}}
}

// WithBackoff sets the first retry delay of a failing job (default 5 s). The
// delay doubles with each failure of the same job and is capped at 10 minutes.
// The state is kept in memory, per job id.
func (w *Worker) WithBackoff(d time.Duration) *Worker {
	w.backoff = d
	return w
}

// WithShutdownGrace sets how long Run lets a job in flight go on once its
// context ends (default 0: the job is cancelled at once). The cancelled job
// stays pending and is picked again on the next start.
func (w *Worker) WithShutdownGrace(d time.Duration) *Worker {
	w.grace = d
	return w
}

// Run polls until ctx ends, then stops picking jobs and gives the job in
// flight the shutdown grace before cancelling it. Each failure is logged.
func (w *Worker) Run(ctx context.Context) error {
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	stop := context.AfterFunc(ctx, func() {
		t := time.AfterFunc(w.grace, cancelJobs)
		context.AfterFunc(jobCtx, func() { t.Stop() })
	})
	defer stop()

	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		n, err := w.pass(ctx, jobCtx)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "deletion: pass failed", "error", err)
		}
		if n > 0 && err == nil {
			continue // more may be waiting
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Pass runs every claimable job once and returns how many finished. A job
// that fails is logged, skipped for the rest of the pass and put on backoff;
// the other jobs go on. The error joins the failures of this pass.
func (w *Worker) Pass(ctx context.Context) (int, error) { return w.pass(ctx, ctx) }

// pass picks jobs while ctx lasts and runs each under jobCtx, which Run keeps
// alive for the shutdown grace after ctx ends.
func (w *Worker) pass(ctx, jobCtx context.Context) (int, error) {
	skip := w.backedOff()
	var errs []error
	done := 0
	for ctx.Err() == nil {
		j, ok, err := w.pick(ctx, skip)
		if err != nil {
			if ctx.Err() == nil {
				slog.ErrorContext(ctx, "deletion: pick job failed", "error", err)
			}
			errs = append(errs, err)
			break
		}
		if !ok {
			break
		}
		skip = append(skip, j.id.String())
		finished, err := w.run(jobCtx, j)
		if err != nil {
			if ctx.Err() != nil || jobCtx.Err() != nil {
				// Shutdown cut the job short: not a failure, it stays
				// pending for the next start.
				slog.InfoContext(ctx, "deletion: job interrupted by shutdown", "job_id", j.id)
				errs = append(errs, fmt.Errorf("job %s: %w", j.id, err))
				break
			}
			slog.ErrorContext(ctx, "deletion: job failed", "job_id", j.id, "tenant_id", j.tenant, "user_id", j.user, "error", err)
			w.noteFailure(j.id)
			errs = append(errs, fmt.Errorf("job %s: %w", j.id, err))
			continue
		}
		w.clearFailure(j.id)
		if finished {
			done++
		}
	}
	return done, errors.Join(errs...)
}

func (w *Worker) backedOff() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	out := []string{}
	for id, f := range w.fails {
		if f.until.After(now) {
			out = append(out, id.String())
		}
	}
	return out
}

func (w *Worker) noteFailure(id uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f := w.fails[id]
	f.n++
	d := w.backoff
	for i := 1; i < f.n && d < maxBackoff; i++ {
		d *= 2
	}
	d = min(d, maxBackoff)
	f.until = time.Now().Add(d)
	w.fails[id] = f
}

func (w *Worker) clearFailure(id uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.fails, id)
}

// job is one deletion job: the whole user, or with provider set only the
// user's data from that provider.
type job struct {
	id, tenant, user uuid.UUID
	provider         *string
}

// pick returns the oldest unfinished job not in skip, without locking it.
func (w *Worker) pick(ctx context.Context, skip []string) (job, bool, error) {
	var j job
	err := w.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, provider FROM ohs_core.deletion_jobs
		WHERE status IN ('pending', 'running') AND id::text <> ALL($1::text[])
		ORDER BY created_at
		LIMIT 1`, skip).Scan(&j.id, &j.tenant, &j.user, &j.provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return job{}, false, nil
	}
	if err != nil {
		return job{}, false, fmt.Errorf("pick job: %w", err)
	}
	return j, true, nil
}

// run does one job. It reports false when another worker finished it first,
// or when a provider job waits for a pull that is still running.
func (w *Worker) run(ctx context.Context, j job) (bool, error) {
	if j.provider != nil {
		return w.runProvider(ctx, j, *j.provider)
	}
	log := slog.With("job_id", j.id, "tenant_id", j.tenant, "user_id", j.user)

	if w.revoker != nil {
		var n int
		if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.connections
			WHERE tenant_id = $1 AND user_id = $2 AND status <> 'disconnected'`, j.tenant, j.user).Scan(&n); err != nil {
			return false, fmt.Errorf("count connections: %w", err)
		}
		rctx, cancel := context.WithTimeout(ctx, revokeBudget(n))
		if err := w.revoker.RevokeAll(rctx, j.tenant, j.user); err != nil {
			log.WarnContext(ctx, "deletion: revoking provider connections failed, continuing", "error", err)
		}
		cancel()
	}
	if err := w.raw.DeletePrefix(ctx, fmt.Sprintf("raw/%s/%s/", j.tenant, j.user)); err != nil {
		return false, fmt.Errorf("delete raw files: %w", err)
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM ohs_core.deletion_jobs
		WHERE id = $1 AND status IN ('pending', 'running')
		FOR UPDATE SKIP LOCKED`, j.id).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // done or being finished by another worker
	}
	if err != nil {
		return false, fmt.Errorf("claim: %w", err)
	}
	// Lock the user row: an ETL writer holding it FOR SHARE finishes before we
	// delete, and one arriving later finds a deleting user.
	if _, err := tx.Exec(ctx,
		`SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, j.tenant, j.user); err != nil {
		return false, fmt.Errorf("lock user: %w", err)
	}
	// A user's rows in a compressed chunk are whole batches (compression is
	// segmented by tenant and user), which TimescaleDB drops without
	// decompressing. Lift the per-transaction decompression limit anyway, so an
	// erasure can never stop half way on a server with a low limit.
	if _, err := tx.Exec(ctx, `SET LOCAL timescaledb.max_tuples_decompressed_per_dml_transaction = 0`); err != nil {
		return false, fmt.Errorf("allow decompression: %w", err)
	}
	tables, err := userTables(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, tbl := range tables {
		if _, err := tx.Exec(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE tenant_id = $1 AND user_id = $2`, tbl.Sanitize()), j.tenant, j.user); err != nil {
			return false, fmt.Errorf("delete from %s: %w", tbl.Sanitize(), err)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM ohs_core.user_tokens WHERE tenant_id = $1 AND user_id = $2`, j.tenant, j.user); err != nil {
		return false, fmt.Errorf("delete tokens: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM ohs_core.idempotency_keys WHERE tenant_id = $1 AND user_id = $2`, j.tenant, j.user); err != nil {
		return false, fmt.Errorf("delete idempotency keys: %w", err)
	}
	// Webhooks not yet sent about the user go with them; user.deleted is
	// queued while the user row still gives its external_ref.
	if _, err := tx.Exec(ctx,
		`DELETE FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 AND user_id = $2`, j.tenant, j.user); err != nil {
		return false, fmt.Errorf("delete webhook deliveries: %w", err)
	}
	user := j.user
	if _, err := webhook.Enqueue(ctx, tx, webhook.Event{Tenant: j.tenant, User: &user, Type: webhook.UserDeleted}); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2`, j.tenant, j.user); err != nil {
		return false, fmt.Errorf("delete user: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE ohs_core.deletion_jobs SET status = 'done', finished_at = now() WHERE id = $1`, j.id); err != nil {
		return false, fmt.Errorf("finish job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	log.InfoContext(ctx, "deletion: user deleted", "tables", len(tables))
	return true, nil
}

// revokeBudget is the time RevokeAll gets for n live connections, so a slow
// first provider does not leave the later ones unrevoked.
func revokeBudget(n int) time.Duration {
	return min(time.Duration(max(n, 1))*revokePerConnection, maxRevokeBudget)
}

// userTables lists the tables of ohs_sync and ohs_data that hold a user_id
// column, read from pg_catalog so a new table is covered without a code
// change. Reference tables (no user_id) are skipped. Finding none is an error:
// the schema is not what the worker expects, and "deleted" would be a lie.
func userTables(ctx context.Context, tx pgx.Tx) ([]pgx.Identifier, error) {
	rows, err := tx.Query(ctx, `
		SELECT n.nspname, c.relname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('ohs_sync', 'ohs_data')
		  AND a.attname = 'user_id' AND a.attnum > 0 AND NOT a.attisdropped
		  AND c.relkind IN ('r', 'p')
		ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list user tables: %w", err)
	}
	defer rows.Close()
	var out []pgx.Identifier
	for rows.Next() {
		var schema, name string
		if err := rows.Scan(&schema, &name); err != nil {
			return nil, err
		}
		out = append(out, pgx.Identifier{schema, name})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no user-scoped tables found in ohs_sync or ohs_data")
	}
	return out, nil
}
