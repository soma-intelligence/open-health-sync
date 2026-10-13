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
	// Concurrency is how many deliveries one worker posts at once.
	Concurrency = 8
	maxErrorLen = 500
	pruneEvery  = time.Hour
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
}

// Worker posts due deliveries.
type Worker struct {
	pool   *pgxpool.Pool
	box    *crypto.Box
	client *http.Client
	delays []float64
	idle   time.Duration
}

// NewWorker returns a delivery worker.
func NewWorker(pool *pgxpool.Pool, box *crypto.Box, cfg WorkerConfig) *Worker {
	w := &Worker{pool: pool, box: box, client: cfg.Client, idle: cfg.Idle}
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
	return w
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
	for {
		if time.Since(lastPrune) > pruneEvery {
			if err := w.Prune(ctx); err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "webhook: prune deliveries", "err", err)
			}
			lastPrune = time.Now()
		}
		n, err := w.Pass(ctx)
		if err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "webhook: delivery pass", "err", err)
		}
		if n > 0 && err == nil {
			continue
		}
		t := time.NewTimer(w.idle)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
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
}

// claimSQL takes due deliveries of active endpoints, oldest first, and marks
// them running; a running one past the visibility timeout (its worker died)
// is taken again without burning an attempt.
const claimSQL = `
	WITH picked AS (
		SELECT d.id FROM ohs_core.webhook_deliveries d
		JOIN ohs_core.webhook_endpoints e ON e.id = d.endpoint_id AND e.status = 'active'
		WHERE (d.status = 'pending' AND d.not_before <= now())
		   OR (d.status = 'running' AND d.claimed_at < now() - make_interval(secs => $2::double precision))
		ORDER BY d.not_before, d.id
		LIMIT $1
		FOR UPDATE OF d SKIP LOCKED
	)
	UPDATE ohs_core.webhook_deliveries t SET status = 'running', claimed_at = now()
	FROM picked p, ohs_core.webhook_endpoints e
	WHERE t.id = p.id AND e.id = t.endpoint_id
	RETURNING t.id, t.tenant_id, t.endpoint_id, t.event_id, t.event_type, t.attempts, t.claimed_at, t.payload::text, e.url, e.secret_sealed`

// Pass claims up to Concurrency due deliveries, posts them concurrently and
// settles each. It returns how many it claimed.
func (w *Worker) Pass(ctx context.Context) (int, error) {
	rows, err := w.pool.Query(ctx, claimSQL, Concurrency, visibility.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim deliveries: %w", err)
	}
	var jobs []job
	for rows.Next() {
		var j job
		var payload string
		if err := rows.Scan(&j.id, &j.tenant, &j.endpoint, &j.eventID, &j.eventType, &j.attempts, &j.claimedAt, &payload, &j.url, &j.sealed); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan delivery: %w", err)
		}
		if j.payload, err = compactPayload(payload); err != nil {
			rows.Close()
			return 0, fmt.Errorf("delivery %d: %w", j.id, err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("claim deliveries: %w", err)
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
			errs[i] = w.deliver(jobCtx, j)
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
	now := time.Now()
	id := j.eventID.String()
	sig, err := Sign(string(secret), id, now, j.payload)
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
	if _, err := tx.Exec(ctx, `UPDATE ohs_core.webhook_endpoints SET last_success_at = now(), failing_since = NULL
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
	if _, err := tx.Exec(ctx, `UPDATE ohs_core.webhook_endpoints SET failing_since = COALESCE(failing_since, now())
		WHERE id = $1`, j.endpoint); err != nil {
		return fmt.Errorf("mark endpoint failing: %w", err)
	}
	if parked {
		// Disable an endpoint that has not accepted anything for DisableAfter.
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

// Prune deletes finished deliveries older than Retention.
func (w *Worker) Prune(ctx context.Context) error {
	_, err := w.pool.Exec(ctx, `DELETE FROM ohs_core.webhook_deliveries
		WHERE status IN ('done', 'parked') AND created_at < now() - make_interval(secs => $1::double precision)`,
		Retention.Seconds())
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
