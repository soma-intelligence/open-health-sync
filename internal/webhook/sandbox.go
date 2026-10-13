// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Sandbox limits.
const (
	// ExcerptLen is how many bytes of a receiver's response are kept per
	// attempt.
	ExcerptLen = 1024
	// KeepAttempts is how many attempts are kept per delivery, newest.
	KeepAttempts = 20
	// MaxQueuedReplays is how many replays of one endpoint may wait to be
	// sent at once.
	MaxQueuedReplays = 10
)

// ErrTooManyReplays: the endpoint already has MaxQueuedReplays replays
// pending or running.
var ErrTooManyReplays = errors.New("webhook: too many replays queued")

// Attempt is one try of a delivery, as the API shows it.
type Attempt struct {
	ID int64 `json:"id"`
	// Attempt counts the delivery's tries from 1 and keeps counting across
	// retries.
	Attempt    int       `json:"attempt"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int       `json:"duration_ms"`
	StatusCode *int      `json:"status_code"`
	Error      *string   `json:"error"`
	// ResponseBodyExcerpt is the start of the receiver's answer (ExcerptLen
	// bytes at most, printable UTF-8 only).
	ResponseBodyExcerpt *string `json:"response_body_excerpt"`
}

// DeliveryDetail is one delivery with the exact body it sends and its
// attempt history, oldest attempt first.
type DeliveryDetail struct {
	Delivery
	Payload json.RawMessage `json:"payload"`
	History []Attempt       `json:"attempt_history"`
}

// Excerpt makes the start of a response body safe to store and show: at
// most ExcerptLen bytes, valid UTF-8, printable characters, newlines and
// tabs only (anything else becomes '?'; a carriage return is dropped). A
// character cut by the length limit is dropped. Empty means no body.
func Excerpt(b []byte) string {
	if len(b) > ExcerptLen {
		b = b[:ExcerptLen]
	}
	// A multi-byte character cut at the end is not an encoding error.
	if n := len(b); n == ExcerptLen {
		for i := n - 1; i >= 0 && i >= n-utf8.UTFMax; i-- {
			if utf8.RuneStart(b[i]) {
				if !utf8.FullRune(b[i:]) {
					b = b[:i]
				}
				break
			}
		}
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		switch {
		case r == '\r':
		case r == '\n' || r == '\t':
			sb.WriteRune(r)
		case r == utf8.RuneError && size == 1:
			sb.WriteByte('?')
		case unicode.IsGraphic(r):
			sb.WriteRune(r)
		default:
			sb.WriteByte('?')
		}
	}
	return sb.String()
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// recordAttempt writes one attempt of j in the transaction that settles it
// and drops the delivery's attempts beyond KeepAttempts. cause is nil on a
// success.
func recordAttempt(ctx context.Context, tx pgx.Tx, j job, o outcome, cause error) error {
	var status *int
	if o.code != 0 {
		status = &o.code
	}
	var errMsg *string
	if cause != nil {
		errMsg = nullIfEmpty(errText(cause))
	}
	// The delivery row is locked by the settle UPDATE, so max(attempt)
	// cannot race.
	if _, err := tx.Exec(ctx, `
		INSERT INTO ohs_core.webhook_attempts (tenant_id, delivery_id, endpoint_id, attempt, started_at,
			duration_ms, status_code, error, response_body_excerpt)
		SELECT $1, $2, $3, COALESCE(max(attempt), 0) + 1, $4, $5, $6, $7, $8
		FROM ohs_core.webhook_attempts WHERE delivery_id = $2`,
		j.tenant, j.id, j.endpoint, o.started.UTC(), int(o.duration.Milliseconds()), status, errMsg,
		nullIfEmpty(o.excerpt)); err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM ohs_core.webhook_attempts WHERE delivery_id = $1 AND id <= (
			SELECT id FROM ohs_core.webhook_attempts WHERE delivery_id = $1 ORDER BY id DESC OFFSET $2 LIMIT 1)`,
		j.id, KeepAttempts); err != nil {
		return fmt.Errorf("trim attempts: %w", err)
	}
	return nil
}

// GetDelivery returns one delivery of the endpoint with its payload and
// attempts.
func (s *Store) GetDelivery(ctx context.Context, tenant, endpoint uuid.UUID, id int64) (DeliveryDetail, error) {
	var payload string
	d, err := scanDelivery(s.pool.QueryRow(ctx, `SELECT `+deliveryCols+`, payload::text
		FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 AND endpoint_id = $2 AND id = $3`,
		tenant, endpoint, id), &payload)
	if err != nil {
		return DeliveryDetail{}, err
	}
	out := DeliveryDetail{Delivery: d, Payload: json.RawMessage(payload), History: []Attempt{}}
	rows, err := s.pool.Query(ctx, `SELECT id, attempt, started_at, duration_ms, status_code, error,
			response_body_excerpt
		FROM ohs_core.webhook_attempts WHERE tenant_id = $1 AND delivery_id = $2 ORDER BY id`, tenant, id)
	if err != nil {
		return DeliveryDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.ID, &a.Attempt, &a.StartedAt, &a.DurationMS, &a.StatusCode, &a.Error,
			&a.ResponseBodyExcerpt); err != nil {
			return DeliveryDetail{}, err
		}
		a.StartedAt = a.StartedAt.UTC()
		out.History = append(out.History, a)
	}
	return out, rows.Err()
}

// Replay queues a new delivery of a finished (done or parked) delivery's
// event: same event id, so the same webhook-id header, and same payload. The
// original keeps its history. A delivery still pending or running is
// ErrNotRetryable; an endpoint with MaxQueuedReplays replays waiting is
// ErrTooManyReplays.
func (s *Store) Replay(ctx context.Context, tenant, endpoint uuid.UUID, delivery int64) (Delivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	// Serialize the endpoint's replays so the cap holds. NO KEY UPDATE does
	// not block the deliveries enqueued meanwhile.
	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM ohs_core.webhook_endpoints WHERE tenant_id = $1 AND id = $2
		FOR NO KEY UPDATE`, tenant, endpoint).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("lock endpoint: %w", err)
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 AND endpoint_id = $2 AND id = $3`, tenant, endpoint, delivery).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	if status != "done" && status != "parked" {
		return Delivery{}, ErrNotRetryable
	}
	var queued int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ohs_core.webhook_deliveries
		WHERE tenant_id = $1 AND endpoint_id = $2 AND replay_of IS NOT NULL AND status IN ('pending', 'running')`,
		tenant, endpoint).Scan(&queued); err != nil {
		return Delivery{}, fmt.Errorf("count replays: %w", err)
	}
	if queued >= MaxQueuedReplays {
		return Delivery{}, ErrTooManyReplays
	}
	d, err := scanDelivery(tx.QueryRow(ctx, `
		INSERT INTO ohs_core.webhook_deliveries (tenant_id, endpoint_id, user_id, event_id, event_type, payload, replay_of)
		SELECT tenant_id, endpoint_id, user_id, event_id, event_type, payload, id
		FROM ohs_core.webhook_deliveries WHERE tenant_id = $1 AND endpoint_id = $2 AND id = $3
		RETURNING `+deliveryCols, tenant, endpoint, delivery))
	if err != nil {
		return Delivery{}, fmt.Errorf("insert replay: %w", err)
	}
	return d, tx.Commit(ctx)
}
