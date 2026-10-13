// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
)

// Limits on what a tenant registers.
const (
	// MaxEndpoints is how many endpoints one tenant may have.
	MaxEndpoints = 20
	// MaxDescription bounds an endpoint's description.
	MaxDescription = 255
)

// Errors of the endpoint store.
var (
	ErrNotFound     = errors.New("webhook: not found")
	ErrTooMany      = errors.New("webhook: too many endpoints")
	ErrUnknownType  = errors.New("webhook: unknown event type")
	ErrNotRetryable = errors.New("webhook: delivery is still pending")
	ErrCursor       = errors.New("webhook: invalid cursor")
)

// Endpoint is a registered receiver. The secret is never part of it: it is
// returned once, by Create and RotateSecret.
type Endpoint struct {
	ID             uuid.UUID  `json:"id"`
	URL            string     `json:"url"`
	Description    string     `json:"description"`
	EventTypes     []string   `json:"event_types"`
	Status         string     `json:"status"`
	DisabledReason *string    `json:"disabled_reason"`
	FailingSince   *time.Time `json:"failing_since"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Delivery is one event for one endpoint, as the API lists it.
type Delivery struct {
	ID          int64      `json:"id"`
	EventID     uuid.UUID  `json:"event_id"`
	EventType   string     `json:"event_type"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	LastStatus  *int       `json:"last_status_code"`
	LastError   *string    `json:"last_error"`
	NextAttempt *time.Time `json:"next_attempt_at"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
	// ReplayOf is the delivery this one replays (same event id and
	// payload), nil for an original delivery.
	ReplayOf *int64 `json:"replay_of"`
}

// Store manages a tenant's endpoints and deliveries.
type Store struct {
	pool *pgxpool.Pool
	box  *crypto.Box
}

// NewStore returns a Store. box seals the signing secrets.
func NewStore(pool *pgxpool.Pool, box *crypto.Box) *Store { return &Store{pool: pool, box: box} }

// SecretAAD binds a sealed secret to its endpoint row.
func SecretAAD(id uuid.UUID) []byte { return []byte("webhook-secret:" + id.String()) }

const endpointCols = `id, url, description, event_types, status, disabled_reason, failing_since,
	last_success_at, created_at, updated_at`

func scanEndpoint(row pgx.Row) (Endpoint, error) {
	var e Endpoint
	err := row.Scan(&e.ID, &e.URL, &e.Description, &e.EventTypes, &e.Status, &e.DisabledReason,
		&e.FailingSince, &e.LastSuccessAt, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	if err != nil {
		return Endpoint{}, err
	}
	if e.EventTypes == nil {
		e.EventTypes = []string{}
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	utcPtr(e.FailingSince)
	utcPtr(e.LastSuccessAt)
	return e, nil
}

func utcPtr(t *time.Time) {
	if t != nil {
		*t = t.UTC()
	}
}

// CheckTypes refuses an unknown event type and returns the list deduplicated.
func CheckTypes(types []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range types {
		if !KnownType(t) {
			return nil, fmt.Errorf("%w %q", ErrUnknownType, t)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// Create registers an endpoint and returns it with its signing secret. The
// caller has checked the URL (CheckURL) and the types (CheckTypes).
func (s *Store) Create(ctx context.Context, tenant uuid.UUID, url, description string, types []string) (Endpoint, string, error) {
	secret, err := NewSecret()
	if err != nil {
		return Endpoint{}, "", err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Endpoint{}, "", err
	}
	sealed, err := s.box.SealFor([]byte(secret), SecretAAD(id))
	if err != nil {
		return Endpoint{}, "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Endpoint{}, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	// Serialize the tenant's creates so the count check holds.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM ohs_core.tenants WHERE id = $1 FOR UPDATE`, tenant); err != nil {
		return Endpoint{}, "", fmt.Errorf("lock tenant: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ohs_core.webhook_endpoints WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
		return Endpoint{}, "", fmt.Errorf("count endpoints: %w", err)
	}
	if n >= MaxEndpoints {
		return Endpoint{}, "", ErrTooMany
	}
	e, err := scanEndpoint(tx.QueryRow(ctx, `
		INSERT INTO ohs_core.webhook_endpoints (id, tenant_id, url, description, event_types, secret_sealed)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+endpointCols, id, tenant, url, description, types, sealed))
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("insert endpoint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Endpoint{}, "", err
	}
	return e, secret, nil
}

// List returns the tenant's endpoints, oldest first (at most MaxEndpoints).
func (s *Store) List(ctx context.Context, tenant uuid.UUID) ([]Endpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+endpointCols+` FROM ohs_core.webhook_endpoints
		WHERE tenant_id = $1 ORDER BY created_at, id`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one endpoint of the tenant.
func (s *Store) Get(ctx context.Context, tenant, id uuid.UUID) (Endpoint, error) {
	return scanEndpoint(s.pool.QueryRow(ctx, `SELECT `+endpointCols+` FROM ohs_core.webhook_endpoints
		WHERE tenant_id = $1 AND id = $2`, tenant, id))
}

// Patch is a partial update; nil fields are left as they are.
type Patch struct {
	URL         *string
	Description *string
	EventTypes  *[]string
	// Enabled true re-activates a disabled endpoint and clears its failure
	// state; false disables it and parks its pending deliveries.
	Enabled *bool
}

// Update applies p and returns the endpoint.
func (s *Store) Update(ctx context.Context, tenant, id uuid.UUID, p Patch) (Endpoint, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Endpoint{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var types []string
	if p.EventTypes != nil {
		types = *p.EventTypes
	}
	var enable, disable bool
	if p.Enabled != nil {
		enable, disable = *p.Enabled, !*p.Enabled
	}
	e, err := scanEndpoint(tx.QueryRow(ctx, `
		UPDATE ohs_core.webhook_endpoints SET
			url             = COALESCE($3, url),
			description     = COALESCE($4, description),
			event_types     = CASE WHEN $5 THEN $6::text[] ELSE event_types END,
			status          = CASE WHEN $7 THEN 'active' WHEN $8 THEN 'disabled' ELSE status END,
			disabled_reason = CASE WHEN $7 THEN NULL WHEN $8 AND status = 'active' THEN 'disabled by the API'
				ELSE disabled_reason END,
			failing_since   = CASE WHEN $7 THEN NULL ELSE failing_since END,
			updated_at      = now()
		WHERE tenant_id = $1 AND id = $2
		RETURNING `+endpointCols,
		tenant, id, p.URL, p.Description, p.EventTypes != nil, types, enable, disable))
	if err != nil {
		return Endpoint{}, err
	}
	if disable {
		if err := parkPending(ctx, tx, id, "endpoint disabled"); err != nil {
			return Endpoint{}, err
		}
	}
	return e, tx.Commit(ctx)
}

// parkPending parks the endpoint's deliveries that are not sent yet. A
// running one finishes its attempt.
func parkPending(ctx context.Context, db Execer, endpoint uuid.UUID, reason string) error {
	_, err := db.Exec(ctx, `UPDATE ohs_core.webhook_deliveries SET status = 'parked', last_error = $2
		WHERE endpoint_id = $1 AND status = 'pending'`, endpoint, reason)
	if err != nil {
		return fmt.Errorf("park deliveries: %w", err)
	}
	return nil
}

// Delete removes the endpoint and its deliveries.
func (s *Store) Delete(ctx context.Context, tenant, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ohs_core.webhook_endpoints WHERE tenant_id = $1 AND id = $2`, tenant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateSecret replaces the signing secret and returns the new one. The old
// secret stops signing at once, including retries of earlier events.
func (s *Store) RotateSecret(ctx context.Context, tenant, id uuid.UUID) (string, error) {
	secret, err := NewSecret()
	if err != nil {
		return "", err
	}
	sealed, err := s.box.SealFor([]byte(secret), SecretAAD(id))
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE ohs_core.webhook_endpoints SET secret_sealed = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tenant, id, sealed)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return secret, nil
}

// SendTest queues a webhook.test event for the endpoint alone, whatever its
// event types and status say, and returns the delivery.
func (s *Store) SendTest(ctx context.Context, tenant, id uuid.UUID) (Delivery, error) {
	return s.SendSample(ctx, tenant, id, Test)
}

// SendSample queues a test event for the endpoint alone, whatever its event
// types and status say, and returns the delivery. typ Test (or "") is the
// generic webhook.test event; a subscribable type sends a sample of that
// event (SampleData) about no real user. Either payload has "test": true.
// An unknown type is ErrUnknownType.
func (s *Store) SendSample(ctx context.Context, tenant, id uuid.UUID, typ string) (Delivery, error) {
	ev := Event{Tenant: tenant, Type: typ}
	switch {
	case typ == "" || typ == Test:
		ev.Type, ev.Data = Test, map[string]any{"endpoint_id": id}
	case KnownType(typ):
		ev.Data = SampleData(typ, time.Now())
	default:
		return Delivery{}, fmt.Errorf("%w %q", ErrUnknownType, typ)
	}
	body, err := newPayload(ev)
	if err != nil {
		return Delivery{}, err
	}
	body.Test = true
	return scanDelivery(s.pool.QueryRow(ctx, `
		INSERT INTO ohs_core.webhook_deliveries (tenant_id, endpoint_id, event_id, event_type, payload)
		SELECT tenant_id, id, $3, $4, $5 FROM ohs_core.webhook_endpoints WHERE tenant_id = $1 AND id = $2
		RETURNING `+deliveryCols, tenant, id, body.ID, ev.Type, mustJSON(body)))
}

const deliveryCols = `id, event_id, event_type, status, attempts, last_status, last_error,
	CASE WHEN status IN ('pending', 'running') THEN not_before END, created_at, delivered_at, replay_of`

// scanDelivery reads deliveryCols, then extra.
func scanDelivery(row pgx.Row, extra ...any) (Delivery, error) {
	var d Delivery
	err := row.Scan(append([]any{&d.ID, &d.EventID, &d.EventType, &d.Status, &d.Attempts, &d.LastStatus, &d.LastError,
		&d.NextAttempt, &d.CreatedAt, &d.DeliveredAt, &d.ReplayOf}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	d.CreatedAt = d.CreatedAt.UTC()
	utcPtr(d.NextAttempt)
	utcPtr(d.DeliveredAt)
	return d, nil
}

// DeliveryQuery filters ListDeliveries.
type DeliveryQuery struct {
	Status string // empty: every status
	Cursor string
	Limit  int
}

// Delivery page sizes.
const (
	DefaultDeliveryLimit = 50
	MaxDeliveryLimit     = 200
)

// ListDeliveries returns the endpoint's deliveries, newest first, and the
// cursor of the next page ("" on the last one).
func (s *Store) ListDeliveries(ctx context.Context, tenant, endpoint uuid.UUID, q DeliveryQuery) ([]Delivery, string, error) {
	if _, err := s.Get(ctx, tenant, endpoint); err != nil {
		return nil, "", err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultDeliveryLimit
	}
	limit = min(limit, MaxDeliveryLimit)
	var before int64
	if q.Cursor != "" {
		n, err := strconv.ParseInt(q.Cursor, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", ErrCursor
		}
		before = n
	}
	rows, err := s.pool.Query(ctx, `SELECT `+deliveryCols+` FROM ohs_core.webhook_deliveries
		WHERE endpoint_id = $1 AND tenant_id = $2
		  AND ($3 = '' OR status = $3) AND ($4 = 0 OR id < $4)
		ORDER BY id DESC LIMIT $5`, endpoint, tenant, q.Status, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[limit-1].ID, 10)
	}
	return out, next, nil
}

// Retry sends a finished delivery (done or parked) again from the first
// attempt, with its original event id and payload. A delivery still pending
// or running is ErrNotRetryable.
func (s *Store) Retry(ctx context.Context, tenant, endpoint uuid.UUID, delivery int64) (Delivery, error) {
	d, err := scanDelivery(s.pool.QueryRow(ctx, `
		UPDATE ohs_core.webhook_deliveries SET status = 'pending', attempts = 0, not_before = now(),
			claimed_at = NULL, last_error = NULL, last_status = NULL, delivered_at = NULL
		WHERE id = $3 AND endpoint_id = $2 AND tenant_id = $1 AND status IN ('done', 'parked')
		RETURNING `+deliveryCols, tenant, endpoint, delivery))
	if !errors.Is(err, ErrNotFound) {
		return d, err
	}
	var status string
	err = s.pool.QueryRow(ctx, `SELECT status FROM ohs_core.webhook_deliveries
		WHERE id = $3 AND endpoint_id = $2 AND tenant_id = $1`, tenant, endpoint, delivery).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{}, ErrNotRetryable
}
