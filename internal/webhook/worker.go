// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/ops"
	"github.com/soma-intelligence/open-health-sync/internal/tracing"
)

// Delivery tuning.
const (
	// MaxAttempts is how many times a delivery is tried before it is parked.
	MaxAttempts = 8
	// RequestTimeout bounds one attempt, response included.
	RequestTimeout = 15 * time.Second
	// DisableAfter: an endpoint that has failed without one success for this
	// long is disabled when one more delivery runs out of attempts.
	DisableAfter = 5 * 24 * time.Hour
	// Retention is how long finished deliveries are kept for the API.
	Retention = 30 * 24 * time.Hour
	// visibility is how long a claimed delivery may stay running before
	// another worker takes it again (a crashed worker).
	visibility = 2 * time.Minute
	// DefaultConcurrency is how many deliveries one worker posts at once
	// unless WorkerConfig.Concurrency says otherwise.
	DefaultConcurrency = 8
	// DefaultPerEndpoint is how many deliveries may be in flight to one
	// endpoint at once, over every worker, unless WorkerConfig.PerEndpoint
	// says otherwise. It keeps one slow receiver from taking every slot.
	DefaultPerEndpoint = 2
	// DefaultPerTenantDivisor sets how many deliveries may be in flight to
	// the endpoints of one tenant at once, over every worker, unless
	// WorkerConfig.PerTenant says otherwise: Concurrency divided by it, at
	// least 1. It keeps one tenant with many slow endpoints from taking
	// every slot.
	DefaultPerTenantDivisor = 2
	// DefaultPauseAfter is how many attempts in a row an endpoint may fail
	// before it is paused.
	DefaultPauseAfter = 5
	// DefaultPauseBase is the first pause of an endpoint. Each further
	// failure doubles it, up to DefaultPauseMax.
	DefaultPauseBase = 30 * time.Second
	// DefaultPauseMax is the longest pause of an endpoint.
	DefaultPauseMax = 10 * time.Minute
	maxErrorLen     = 500
	pruneEvery      = time.Hour
	// pruneLimit is how long one prune of old deliveries may hold the loop
	// before it counts as hung.
	pruneLimit = 15 * time.Minute
)

// retryDelays is the wait after the nth failed attempt: about 28 hours from
// the first attempt to the last, as Standard Webhooks senders commonly do.
var retryDelays = []time.Duration{
	5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour,
	5 * time.Hour, 10 * time.Hour, 10 * time.Hour,
}

// UserAgent is sent with every delivery.
const UserAgent = "open-health-sync-webhooks/1"

// WorkerConfig tunes a Worker. The zero value is the production setting.
type WorkerConfig struct {
	// AllowPrivate lets deliveries reach private and loopback addresses
	// (OHS_WEBHOOK_ALLOW_PRIVATE).
	AllowPrivate bool
	// Delays replaces retryDelays (tests). Its length must be MaxAttempts-1.
	Delays []time.Duration
	// Idle is the poll interval when nothing is due (default 1 s).
	Idle time.Duration
	// Client replaces the HTTP client (tests).
	Client *http.Client
	// Concurrency is how many deliveries this worker posts at once (default
	// DefaultConcurrency). A finished delivery frees its slot at once.
	Concurrency int
	// PerEndpoint caps the deliveries in flight to one endpoint (default
	// DefaultPerEndpoint, at most Concurrency). The claim enforces it in the
	// database, so the workers of several replicas share the cap; give them
	// the same value.
	PerEndpoint int
	// PerTenant caps the deliveries in flight to all the endpoints of one
	// tenant (default Concurrency / DefaultPerTenantDivisor, at least 1; at
	// most Concurrency). The claim enforces it in the database like
	// PerEndpoint; give every replica the same value.
	PerTenant int
	// PauseAfter is how many attempts in a row an endpoint may fail before
	// it is paused (default DefaultPauseAfter). A paused endpoint gets
	// nothing until its pause ends, then one delivery at a time until one
	// succeeds. A negative value turns the pause off.
	PauseAfter int
	// PauseBase is the first pause; each further failure doubles it, up to
	// PauseMax (defaults DefaultPauseBase and DefaultPauseMax).
	PauseBase time.Duration
	PauseMax  time.Duration
	// Loop, when set, gets the heartbeat of the delivery loop: one beat per
	// round, and the attempts in flight as work that excuses a silent loop
	// (every slot can be held by a slow receiver).
	Loop *ops.Loop
	// Observe, when set, is told how each attempt ended (OutcomeDelivered
	// or OutcomeFailed) and how long the receiver took.
	Observe func(outcome string, took time.Duration)
}

// The outcomes of a delivery attempt, as WorkerConfig.Observe gets them.
const (
	OutcomeDelivered = "delivered"
	OutcomeFailed    = "failed"
)

// Worker posts due deliveries.
type Worker struct {
	pool   *pgxpool.Pool
	box    *crypto.Box
	client *http.Client
	delays []float64
	idle   time.Duration

	concurrency int
	perEndpoint int
	perTenant   int
	pauseAfter  int       // 0: endpoints are never paused
	pauses      []float64 // seconds, see pauseSchedule

	loop    *ops.Loop
	observe func(outcome string, took time.Duration)

	// claimFn and deliverFn are claim and deliver, replaced in tests of the
	// slot pool.
	claimFn   func(ctx context.Context, n int) ([]job, error)
	deliverFn func(ctx context.Context, j job) error
}

// NewWorker returns a delivery worker.
func NewWorker(pool *pgxpool.Pool, box *crypto.Box, cfg WorkerConfig) *Worker {
	w := &Worker{pool: pool, box: box, client: cfg.Client, idle: cfg.Idle, loop: cfg.Loop, observe: cfg.Observe}
	if w.client == nil {
		w.client = NewClient(cfg.AllowPrivate)
	}
	if w.idle <= 0 {
		w.idle = time.Second
	}
	delays := cfg.Delays
	if len(delays) != MaxAttempts-1 {
		delays = retryDelays
	}
	for _, d := range delays {
		w.delays = append(w.delays, d.Seconds())
	}
	w.concurrency = cfg.Concurrency
	if w.concurrency <= 0 {
		w.concurrency = DefaultConcurrency
	}
	w.perEndpoint = cfg.PerEndpoint
	if w.perEndpoint <= 0 {
		w.perEndpoint = DefaultPerEndpoint
	}
	w.perEndpoint = min(w.perEndpoint, w.concurrency)
	w.perTenant = cfg.PerTenant
	if w.perTenant <= 0 {
		w.perTenant = max(w.concurrency/DefaultPerTenantDivisor, 1)
	}
	w.perTenant = min(w.perTenant, w.concurrency)
	switch {
	case cfg.PauseAfter == 0:
		w.pauseAfter = DefaultPauseAfter
	case cfg.PauseAfter > 0:
		w.pauseAfter = cfg.PauseAfter
	}
	base, longest := cfg.PauseBase, cfg.PauseMax
	if base <= 0 && longest <= 0 {
		base, longest = DefaultPauseBase, DefaultPauseMax
	}
	if longest <= 0 {
		longest = max(base, DefaultPauseMax)
	}
	w.pauses = pauseSchedule(base, longest)
	w.claimFn, w.deliverFn = w.claim, w.deliver
	return w
}

// pauseSchedule lists, in seconds, the pause after the first failure that
// trips an endpoint and after each further one: base doubled each time, the
// last entry being longest. Failures past the list keep its last entry.
func pauseSchedule(base, longest time.Duration) []float64 {
	base = max(min(base, longest), 0)
	var out []float64
	for d := base; d < longest && d > 0; d *= 2 {
		out = append(out, d.Seconds())
	}
	return append(out, max(longest, 0).Seconds())
}

// NewClient is the delivery HTTP client: no redirects followed (a 3xx is a
// failed attempt), no proxy from the environment, and unless allowPrivate
// no connection to a private or reserved address.
func NewClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: RequestTimeout,
	}
	if allowPrivate {
		tr.Proxy = http.ProxyFromEnvironment
	} else {
		dialer.Control = dialControl
	}
	return &http.Client{
		Transport: tr,
		Timeout:   RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Run posts deliveries until ctx ends, then waits for the attempts in flight
// (each bounded by RequestTimeout) and returns nil.
func (w *Worker) Run(ctx context.Context) error {
	lastPrune := time.Time{}
	return w.pump(ctx, func() {
		if time.Since(lastPrune) <= pruneEvery {
			return
		}
		done := w.loop.Busy(pruneLimit)
		if err := w.Prune(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "webhook: prune deliveries", "err", err)
		}
		done()
		lastPrune = time.Now()
	})
}

// pump keeps Concurrency slots busy: it claims as many deliveries as there
// are free slots, and a finished delivery frees its slot at once, so one
// receiver that answers slowly holds its own slots and no others. each runs
// before every claim.
func (w *Worker) pump(ctx context.Context, each func()) error {
	// Attempts finish after shutdown starts: each is short, and cutting one
	// would only make the receiver see it twice.
	jobCtx := context.WithoutCancel(ctx)
	slots := make(chan struct{}, w.concurrency)
	// freed wakes the idle wait when a delivery finishes: the endpoint it
	// went to may have more due that its in-flight cap held back.
	freed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		w.loop.Tick()
		each()
		// Wait for one free slot, then take every other free one.
		select {
		case <-ctx.Done():
			return nil
		case slots <- struct{}{}:
		}
		n := 1
	more:
		for n < w.concurrency {
			select {
			case slots <- struct{}{}:
				n++
			default:
				break more
			}
		}

		jobs, err := w.claimFn(ctx, n)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "webhook: claim deliveries", "err", err)
		}
		for range n - len(jobs) {
			<-slots
		}
		for _, j := range jobs {
			wg.Add(1)
			// An attempt and its settle end well inside the visibility
			// timeout: past it the loop is no longer excused.
			done := w.loop.Busy(visibility)
			go func() {
				defer wg.Done()
				defer done()
				if err := w.deliverFn(jobCtx, j); err != nil {
					slog.ErrorContext(jobCtx, "webhook: settle delivery", "delivery_id", j.id, "err", err)
				}
				<-slots
				select {
				case freed <- struct{}{}:
				default:
				}
			}()
		}
		if len(jobs) > 0 {
			continue
		}
		t := time.NewTimer(w.idle)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-freed:
			t.Stop()
		case <-t.C:
		}
	}
}

type job struct {
	id        int64
	tenant    uuid.UUID
	endpoint  uuid.UUID
	eventID   uuid.UUID
	eventType string
	attempts  int
	claimedAt time.Time
	payload   []byte
	url       string
	sealed    []byte
	// sealedPrev is the secret the last rotation replaced, nil once its
	// grace period is over.
	sealedPrev []byte
}

// lockEndpointsSQL locks the endpoints that may have something to claim:
// active, not paused, with a delivery due or a running one past the
// visibility timeout. The lock is what makes the in-flight cap hold between
// replicas: claimSQL counts an endpoint's running deliveries in a statement
// that starts after the lock is held, so it sees every claim made before.
// An endpoint another worker has locked is skipped until the next claim.
// FOR NO KEY UPDATE does not block the inserts of new deliveries.
const lockEndpointsSQL = `
	SELECT e.id::text, e.tenant_id::text FROM ohs_core.webhook_endpoints e
	WHERE e.status = 'active' AND (e.paused_until IS NULL OR e.paused_until <= now())
	  AND (EXISTS (SELECT 1 FROM ohs_core.webhook_deliveries d
			WHERE d.endpoint_id = e.id AND d.status = 'pending' AND d.not_before <= now())
		OR EXISTS (SELECT 1 FROM ohs_core.webhook_deliveries d
			WHERE d.endpoint_id = e.id AND d.status = 'running'
			  AND d.claimed_at < now() - make_interval(secs => $1::double precision)))
	FOR NO KEY UPDATE OF e SKIP LOCKED`

// lockTenantsSQL takes, for the length of the claim, the claim lock of each
// tenant in $1 that no other worker holds, and returns those tenants. Two
// workers may lock different endpoints of one tenant; this lock is what
// makes the cap per tenant hold between them: claimSQL counts the tenant's
// running deliveries once the lock is held. A tenant another worker is
// claiming for is skipped until the next claim, like a locked endpoint.
const lockTenantsSQL = `
	SELECT t FROM unnest($1::text[]) AS t
	WHERE pg_try_advisory_xact_lock(hashtextextended('ohs_core.webhook_deliveries:claim:' || t, 0))`

// claimSQL takes due deliveries of the locked endpoints ($5), oldest first,
// and marks them running; a running one past the visibility timeout (its
// worker died) is taken again without burning an attempt. No endpoint gets
// more than $3 deliveries in flight, and one that has failed $4 attempts in
// a row gets a single one, the probe that ends its pause if it succeeds; no
// tenant gets more than $6 in flight over all its endpoints.
// (A running delivery keeps the not_before it was claimed with, so it is in
// the past.)
const claimSQL = `
	WITH room AS (
		SELECT e.id, e.tenant_id,
			CASE WHEN $4::int > 0 AND e.consecutive_failures >= $4::int THEN 1 ELSE $3::int END
			- (SELECT count(*) FROM ohs_core.webhook_deliveries r
				WHERE r.endpoint_id = e.id AND r.status = 'running'
				  AND r.claimed_at >= now() - make_interval(secs => $2::double precision)) AS free
		FROM ohs_core.webhook_endpoints e
		WHERE e.id = ANY ($5::text[]::uuid[]) AND e.status = 'active'
		  AND (e.paused_until IS NULL OR e.paused_until <= now())
	), tenant_room AS (
		SELECT t.tenant_id,
			$6::int - (SELECT count(*) FROM ohs_core.webhook_endpoints te
				JOIN ohs_core.webhook_deliveries r ON r.endpoint_id = te.id
				WHERE te.tenant_id = t.tenant_id AND r.status = 'running'
				  AND r.claimed_at >= now() - make_interval(secs => $2::double precision)) AS free
		FROM (SELECT DISTINCT tenant_id FROM room) t
	), cand AS (
		SELECT c.id, c.not_before, r.tenant_id FROM room r CROSS JOIN LATERAL (
			SELECT d.id, d.not_before FROM ohs_core.webhook_deliveries d
			WHERE d.endpoint_id = r.id AND d.not_before <= now()
			  AND (d.status = 'pending'
				OR (d.status = 'running' AND d.claimed_at < now() - make_interval(secs => $2::double precision)))
			ORDER BY d.not_before, d.id
			LIMIT greatest(r.free, 0)
			FOR UPDATE OF d SKIP LOCKED
		) c
	), ranked AS (
		SELECT c.id, c.not_before, tr.free,
			row_number() OVER (PARTITION BY c.tenant_id ORDER BY c.not_before, c.id) AS nth
		FROM cand c JOIN tenant_room tr ON tr.tenant_id = c.tenant_id
	), picked AS (
		SELECT id FROM ranked WHERE nth <= free ORDER BY not_before, id LIMIT $1
	)
	UPDATE ohs_core.webhook_deliveries t SET status = 'running', claimed_at = now()
	FROM picked p, ohs_core.webhook_endpoints e
	WHERE t.id = p.id AND e.id = t.endpoint_id
	RETURNING t.id, t.tenant_id, t.endpoint_id, t.event_id, t.event_type, t.attempts, t.claimed_at, t.payload::text,
		e.url, e.secret_sealed,
		CASE WHEN e.secret_previous_expires_at > now() THEN e.secret_previous_sealed END`

// claim marks up to n due deliveries running and returns them.
func (w *Worker) claim(ctx context.Context, n int) ([]job, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	rows, err := tx.Query(ctx, lockEndpointsSQL, visibility.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	type lockedEndpoint struct{ id, tenant string }
	locked, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lockedEndpoint, error) {
		var e lockedEndpoint
		return e, row.Scan(&e.id, &e.tenant)
	})
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	if len(locked) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var tenants []string
	for _, e := range locked {
		if !seen[e.tenant] {
			seen[e.tenant] = true
			tenants = append(tenants, e.tenant)
		}
	}
	rows, err = tx.Query(ctx, lockTenantsSQL, tenants)
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	if tenants, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	clear(seen)
	for _, t := range tenants {
		seen[t] = true
	}
	var endpoints []string
	for _, e := range locked {
		if seen[e.tenant] {
			endpoints = append(endpoints, e.id)
		}
	}
	if len(endpoints) == 0 {
		return nil, nil
	}
	rows, err = tx.Query(ctx, claimSQL, n, visibility.Seconds(), w.perEndpoint, w.pauseAfter, endpoints, w.perTenant)
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	var jobs []job
	for rows.Next() {
		var j job
		var payload string
		if err := rows.Scan(&j.id, &j.tenant, &j.endpoint, &j.eventID, &j.eventType, &j.attempts, &j.claimedAt, &payload,
			&j.url, &j.sealed, &j.sealedPrev); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		if j.payload, err = compactPayload(payload); err != nil {
			rows.Close()
			return nil, fmt.Errorf("delivery %d: %w", j.id, err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	return jobs, nil
}

// Pass claims up to Concurrency due deliveries, posts them concurrently,
// settles each and returns how many it claimed. Run does not use it: it
// refills each slot as it frees. Pass is for callers that want one bounded
// round (tests).
func (w *Worker) Pass(ctx context.Context) (int, error) {
	jobs, err := w.claimFn(ctx, w.concurrency)
	if err != nil {
		return 0, err
	}
	// Attempts finish after shutdown starts: each is short, and cutting one
	// would only make the receiver see it twice.
	jobCtx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	errs := make([]error, len(jobs))
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = w.deliverFn(jobCtx, j)
		}()
	}
	wg.Wait()
	return len(jobs), errors.Join(errs...)
}

// outcome is what one attempt observed, recorded in webhook_attempts.
type outcome struct {
	started  time.Time
	duration time.Duration
	code     int    // HTTP status, 0 when there was none
	excerpt  string // the start of the response body, sanitized
}

// deliver makes one attempt and records its outcome. With tracing on, the
// attempt is a client span carrying the ids its log line carries, the event
// type and the receiver's status code: never the endpoint URL, a header or
// the payload. No trace context is sent to the receiver.
func (w *Worker) deliver(ctx context.Context, j job) (err error) {
	ctx, span := tracing.Tracer().Start(ctx, "webhook delivery", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.Int64(tracing.AttrDeliveryID, j.id),
			attribute.String(tracing.AttrEndpointID, j.endpoint.String()),
			attribute.String(tracing.AttrEventType, j.eventType),
			attribute.Int(tracing.AttrAttempt, j.attempts+1)))
	o := outcome{started: time.Now()}
	var sendErr error
	defer func() {
		if o.code != 0 {
			span.SetAttributes(semconv.HTTPResponseStatusCode(o.code))
		}
		if sendErr != nil || err != nil {
			span.SetStatus(codes.Error, "")
		}
		span.End()
	}()
	sendErr = w.send(ctx, j, &o)
	o.duration = time.Since(o.started)
	if w.observe != nil {
		result := OutcomeDelivered
		if sendErr != nil {
			result = OutcomeFailed
		}
		w.observe(result, o.duration)
	}
	if sendErr == nil {
		return w.succeed(ctx, j, o)
	}
	slog.InfoContext(ctx, "webhook: delivery attempt failed", "delivery_id", j.id, "endpoint_id", j.endpoint,
		"attempt", j.attempts+1, "status_code", o.code, "err", sendErr)
	return w.fail(ctx, j, o, sendErr)
}

// send posts the payload; a non-2xx answer is an error. It fills o.code
// and o.excerpt.
func (w *Worker) send(ctx context.Context, j job, o *outcome) error {
	secret, err := w.box.OpenFor(j.sealed, SecretAAD(j.endpoint))
	if err != nil {
		return errors.New("signing secret cannot be opened (was OHS_ENCRYPTION_KEY changed without keeping the old key in OHS_PREVIOUS_ENCRYPTION_KEYS?)")
	}
	secrets := []string{string(secret)}
	if j.sealedPrev != nil {
		// A rotation is in its grace period: sign with the replaced secret
		// too, so a receiver that has not switched yet still accepts.
		if prev, err := w.box.OpenFor(j.sealedPrev, SecretAAD(j.endpoint)); err == nil {
			secrets = append(secrets, string(prev))
		} else {
			slog.WarnContext(ctx, "webhook: previous signing secret cannot be opened, signing with the current one only",
				"endpoint_id", j.endpoint)
		}
	}
	now := time.Now()
	id := j.eventID.String()
	sig, err := SignAll(secrets, id, now, j.payload)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, j.url, bytes.NewReader(j.payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set(HeaderID, id)
	req.Header.Set(HeaderTimestamp, fmt.Sprint(now.Unix()))
	req.Header.Set(HeaderSignature, sig)
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	o.code = resp.StatusCode
	// Keep the start of the body for the attempt history, and drain a little
	// more so the connection can be reused.
	head, _ := io.ReadAll(io.LimitReader(resp.Body, ExcerptLen))
	o.excerpt = Excerpt(head)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("receiver answered HTTP %d", resp.StatusCode)
	}
	return nil
}

func (w *Worker) succeed(ctx context.Context, j job, o outcome) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	tag, err := tx.Exec(ctx, `UPDATE ohs_core.webhook_deliveries SET status = 'done', attempts = attempts + 1,
			claimed_at = NULL, last_status = $3, last_error = NULL, delivered_at = now()
		WHERE id = $1 AND status = 'running' AND claimed_at = $2`, j.id, j.claimedAt, o.code)
	if err != nil {
		return fmt.Errorf("settle delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil // reclaimed meanwhile: the other worker settles it
	}
	if err := recordAttempt(ctx, tx, j, o, nil); err != nil {
		return err
	}
	// One success ends a pause and its probing: the endpoint gets its full
	// share of slots again.
	if _, err := tx.Exec(ctx, `UPDATE ohs_core.webhook_endpoints SET last_success_at = now(), failing_since = NULL,
			consecutive_failures = 0, paused_until = NULL
		WHERE id = $1`, j.endpoint); err != nil {
		return fmt.Errorf("mark endpoint healthy: %w", err)
	}
	return tx.Commit(ctx)
}

func (w *Worker) fail(ctx context.Context, j job, o outcome, cause error) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var lastStatus *int
	if o.code != 0 {
		lastStatus = &o.code
	}
	var parked bool
	err = tx.QueryRow(ctx, `UPDATE ohs_core.webhook_deliveries SET
			attempts   = attempts + 1,
			status     = CASE WHEN attempts + 1 >= $4 THEN 'parked' ELSE 'pending' END,
			not_before = now() + make_interval(secs => COALESCE(($5::double precision[])[attempts + 1], 0)),
			claimed_at = NULL,
			last_status = $6,
			last_error = $3
		WHERE id = $1 AND status = 'running' AND claimed_at = $2
		RETURNING status = 'parked'`,
		j.id, j.claimedAt, errText(cause), MaxAttempts, w.delays, lastStatus).Scan(&parked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("settle delivery: %w", err)
	}
	if err := recordAttempt(ctx, tx, j, o, cause); err != nil {
		return err
	}
	// Count the failure; from the pauseAfter-th in a row on, pause the
	// endpoint, longer each time.
	var failures int
	var pausedUntil *time.Time
	if err := tx.QueryRow(ctx, `UPDATE ohs_core.webhook_endpoints SET
			failing_since = COALESCE(failing_since, now()),
			consecutive_failures = consecutive_failures + 1,
			paused_until = CASE WHEN $2::int > 0 AND consecutive_failures + 1 >= $2::int
				THEN now() + make_interval(secs => ($3::double precision[])[
					least(consecutive_failures + 2 - $2::int, cardinality($3::double precision[]))])
				ELSE paused_until END
		WHERE id = $1
		RETURNING consecutive_failures, paused_until`, j.endpoint, w.pauseAfter, w.pauses).Scan(&failures, &pausedUntil); err != nil {
		return fmt.Errorf("mark endpoint failing: %w", err)
	}
	tripped := w.pauseAfter > 0 && failures >= w.pauseAfter
	if tripped && failures == w.pauseAfter {
		slog.WarnContext(ctx, "webhook: endpoint paused after consecutive failures", "endpoint_id", j.endpoint,
			"failures", failures, "paused_until", pausedUntil)
	}
	if parked || tripped {
		// Disable an endpoint that has not accepted anything for DisableAfter.
		// A paused endpoint is checked on every failed probe: with a backlog
		// its probes go to the oldest delivery due, so no single delivery
		// may ever run out of attempts.
		var disabled bool
		err := tx.QueryRow(ctx, `UPDATE ohs_core.webhook_endpoints SET status = 'disabled', updated_at = now(),
				disabled_reason = 'every delivery failed for ' || $2::text
			WHERE id = $1 AND status = 'active'
			  AND failing_since < now() - make_interval(secs => $3::double precision)
			RETURNING true`, j.endpoint, humanDuration(DisableAfter), DisableAfter.Seconds()).Scan(&disabled)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("disable endpoint: %w", err)
		}
		if disabled {
			slog.WarnContext(ctx, "webhook: endpoint disabled after repeated failures", "endpoint_id", j.endpoint)
			if err := parkPending(ctx, tx, j.endpoint, "endpoint disabled after repeated failures"); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// Prune deletes finished deliveries older than Retention, and the replaced
// signing secrets whose grace period is over.
func (w *Worker) Prune(ctx context.Context) error {
	_, err := w.pool.Exec(ctx, `DELETE FROM ohs_core.webhook_deliveries
		WHERE status IN ('done', 'parked') AND created_at < now() - make_interval(secs => $1::double precision)`,
		Retention.Seconds())
	if err != nil {
		return err
	}
	_, err = w.pool.Exec(ctx, `UPDATE ohs_core.webhook_endpoints
		SET secret_previous_sealed = NULL, secret_previous_expires_at = NULL
		WHERE secret_previous_expires_at <= now()`)
	return err
}

func humanDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d days", d/(24*time.Hour))
	}
	return d.String()
}

// errText makes a cause safe to store: valid UTF-8, no NUL, bounded.
func errText(cause error) string {
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
