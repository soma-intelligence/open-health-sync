// SPDX-License-Identifier: AGPL-3.0-only

package timetag

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// Page limits, as on every list.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// Querier runs the reads (a *pgxpool.Pool or a pgx.Tx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DB runs the writes, each in a transaction of its own (a *pgxpool.Pool).
type DB interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Query filters and pages a list of tags.
type Query struct {
	// Kind keeps the tags of one kind; empty keeps every kind.
	Kind string
	// Start and End bound a window [Start, End): a tag is listed when it
	// overlaps it, an ongoing tag included. Zero is open.
	Start, End time.Time
	Cursor     string
	// Limit is the page size: 0 means DefaultLimit, at most MaxLimit.
	Limit int
}

// Page is one page of tags. NextCursor is empty on the last page.
type Page struct {
	Records    []Tag
	NextCursor string
}

type normalized struct {
	Query
	afterT  time.Time
	afterID uuid.UUID
	paged   bool
}

func (q Query) normalize() (normalized, error) {
	n := normalized{Query: q}
	if q.Kind != "" && !KnownKind(q.Kind) {
		return n, invalid("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	if !q.Start.IsZero() && !q.End.IsZero() && q.Start.After(q.End) {
		return n, invalid("start must not be after end")
	}
	switch {
	case q.Limit == 0:
		n.Limit = DefaultLimit
	case q.Limit < 0 || q.Limit > MaxLimit:
		return n, invalid("limit must be between 1 and %d", MaxLimit)
	}
	if q.Cursor != "" {
		t, id, err := decodeCursor(q.Cursor)
		if err != nil {
			return n, err
		}
		n.afterT, n.afterID, n.paged = t, id, true
	}
	return n, nil
}

// encodeCursor returns the cursor after the tag starting at t with id.
func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return t, id, nil
}

const cols = `id, kind, label, start_at, end_at, timezone, note, source, created_at, updated_at`

func scan(row pgx.Row) (Tag, error) {
	var t Tag
	err := row.Scan(&t.ID, &t.Kind, &t.Label, &t.StartAt, &t.EndAt, &t.Timezone, &t.Note, &t.Source,
		&t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	if err != nil {
		return Tag{}, err
	}
	t.StartAt, t.CreatedAt, t.UpdatedAt = t.StartAt.UTC(), t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	if t.EndAt != nil {
		end := t.EndAt.UTC()
		t.EndAt = &end
	}
	return t, nil
}

// userExists reports whether user is an active user of tenant.
func userExists(ctx context.Context, db Querier, tenant, user uuid.UUID) error {
	var ok bool
	if err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 AND status = 'active')`,
		tenant, user).Scan(&ok); err != nil {
		return fmt.Errorf("timetag: user: %w", err)
	}
	if !ok {
		return ErrUserNotFound
	}
	return nil
}

// lockUser holds the user row FOR SHARE until tx ends, like every writer of
// a user's rows: a deletion in flight finishes first, and one that starts
// later waits for this write, then deletes the tag with the rest.
func lockUser(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		tenant, user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("timetag: lock user: %w", err)
	}
	return nil
}

// List returns the user's tags that overlap [q.Start, q.End), by start time
// then id. A tag [s, e) overlaps when s < q.End and e > q.Start; an ongoing
// tag (no end) has no upper bound. An empty window (Start equal to End) holds
// no tag.
func List(ctx context.Context, db Querier, tenant, user uuid.UUID, q Query) (Page, error) {
	n, err := q.normalize()
	if err != nil {
		return Page{}, err
	}
	if err := userExists(ctx, db, tenant, user); err != nil {
		return Page{}, err
	}
	if !n.Start.IsZero() && n.Start.Equal(n.End) {
		return Page{Records: []Tag{}}, nil // an empty window
	}
	args := []any{tenant, user}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"tenant_id = $1", "user_id = $2"}
	if n.Kind != "" {
		where = append(where, "kind = "+arg(n.Kind))
	}
	if !n.End.IsZero() {
		where = append(where, "start_at < "+arg(n.End))
	}
	if !n.Start.IsZero() {
		where = append(where, "(end_at IS NULL OR end_at > "+arg(n.Start)+")")
	}
	if n.paged {
		where = append(where, "(start_at, id) > ("+arg(n.afterT)+", "+arg(n.afterID)+")")
	}
	rows, err := db.Query(ctx, "SELECT "+cols+" FROM ohs_data.time_tags WHERE "+strings.Join(where, " AND ")+
		" ORDER BY start_at, id LIMIT "+arg(n.Limit+1), args...)
	if err != nil {
		return Page{}, fmt.Errorf("timetag: list: %w", err)
	}
	defer rows.Close()
	out := Page{Records: []Tag{}}
	for rows.Next() {
		if len(out.Records) == n.Limit {
			last := out.Records[len(out.Records)-1]
			out.NextCursor = encodeCursor(last.StartAt, last.ID)
			break
		}
		t, err := scan(rows)
		if err != nil {
			return Page{}, fmt.Errorf("timetag: list: scan: %w", err)
		}
		out.Records = append(out.Records, t)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("timetag: list: %w", err)
	}
	return out, nil
}

// Get returns one tag of the user.
func Get(ctx context.Context, db Querier, tenant, user, id uuid.UUID) (Tag, error) {
	if err := userExists(ctx, db, tenant, user); err != nil {
		return Tag{}, err
	}
	t, err := scan(db.QueryRow(ctx, `SELECT `+cols+` FROM ohs_data.time_tags
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, tenant, user, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Tag{}, fmt.Errorf("timetag: get: %w", err)
	}
	return t, err
}

// Create checks f and stores it as a new tag of the user. The tag.created
// webhook event is queued in the same transaction.
func Create(ctx context.Context, db DB, tenant, user uuid.UUID, f Fields) (Tag, error) {
	if err := f.Check(); err != nil {
		return Tag{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Tag{}, err
	}
	return inTx(ctx, db, tenant, user, func(tx pgx.Tx) (Tag, error) {
		t, err := scan(tx.QueryRow(ctx, `INSERT INTO ohs_data.time_tags
			(id, tenant_id, user_id, kind, label, start_at, end_at, timezone, note, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING `+cols,
			id, tenant, user, f.Kind, f.Label, f.StartAt, f.EndAt, f.Timezone, f.Note, f.Source))
		if err != nil {
			return Tag{}, fmt.Errorf("timetag: create: %w", err)
		}
		return t, event(ctx, tx, tenant, user, webhook.TagCreated, t.ID)
	})
}

// Update applies p to one tag of the user and returns it. The result is
// checked as a whole, so a new start after the current end is refused. A
// patch that changes nothing returns the tag as it is, with no event.
func Update(ctx context.Context, db DB, tenant, user, id uuid.UUID, p Patch) (Tag, error) {
	if err := p.check(); err != nil {
		return Tag{}, err
	}
	return inTx(ctx, db, tenant, user, func(tx pgx.Tx) (Tag, error) {
		cur, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM ohs_data.time_tags
			WHERE tenant_id = $1 AND user_id = $2 AND id = $3 FOR UPDATE`, tenant, user, id))
		if err != nil {
			return Tag{}, err
		}
		f, changed := p.apply(cur)
		if !changed {
			return cur, nil
		}
		if err := f.Check(); err != nil {
			return Tag{}, err
		}
		t, err := scan(tx.QueryRow(ctx, `UPDATE ohs_data.time_tags SET
				kind = $4, label = $5, start_at = $6, end_at = $7, timezone = $8, note = $9, updated_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND id = $3
			RETURNING `+cols,
			tenant, user, id, f.Kind, f.Label, f.StartAt, f.EndAt, f.Timezone, f.Note))
		if err != nil {
			return Tag{}, fmt.Errorf("timetag: update: %w", err)
		}
		return t, event(ctx, tx, tenant, user, webhook.TagUpdated, t.ID)
	})
}

// Delete removes one tag of the user and queues tag.deleted.
func Delete(ctx context.Context, db DB, tenant, user, id uuid.UUID) error {
	_, err := inTx(ctx, db, tenant, user, func(tx pgx.Tx) (Tag, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM ohs_data.time_tags WHERE tenant_id = $1 AND user_id = $2 AND id = $3`,
			tenant, user, id)
		if err != nil {
			return Tag{}, fmt.Errorf("timetag: delete: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return Tag{}, ErrNotFound
		}
		return Tag{}, event(ctx, tx, tenant, user, webhook.TagDeleted, id)
	})
	return err
}

// inTx runs fn in a transaction that holds the user row (lockUser) and
// commits when fn succeeds.
func inTx(ctx context.Context, db DB, tenant, user uuid.UUID, fn func(pgx.Tx) (Tag, error)) (Tag, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Tag{}, fmt.Errorf("timetag: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockUser(ctx, tx, tenant, user); err != nil {
		return Tag{}, err
	}
	t, err := fn(tx)
	if err != nil {
		return Tag{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tag{}, fmt.Errorf("timetag: commit: %w", err)
	}
	return t, nil
}

// event queues a webhook event about one tag. It names the tag, never its
// kind, times or text: the receiver reads the tag with the API.
func event(ctx context.Context, tx pgx.Tx, tenant, user uuid.UUID, typ string, id uuid.UUID) error {
	_, err := webhook.Enqueue(ctx, tx, webhook.Event{Tenant: tenant, User: &user, Type: typ,
		Data: map[string]any{"tag_id": id}})
	return err
}
