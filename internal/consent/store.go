// SPDX-License-Identifier: AGPL-3.0-only

package consent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is a pool or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Policy is what one user allows from one source.
type Policy struct {
	// Recorded is true when the user has any consent row for the source.
	Recorded bool
	granted  map[string]bool
	// Required is OHS_CONSENT_REQUIRED: without a recorded consent nothing
	// is allowed.
	Required bool
}

// Allows reports whether records of category c may be stored. A source the
// user has no consent row for allows everything, unless consent is required.
func (p Policy) Allows(c string) bool {
	if !p.Recorded {
		return !p.Required
	}
	return p.granted[c]
}

// Load reads the policy of a user for a source.
func Load(ctx context.Context, db DB, tenant, user uuid.UUID, provider string, required bool) (Policy, error) {
	rows, err := db.Query(ctx, `
		SELECT category, granted FROM ohs_sync.consents
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`, tenant, user, provider)
	if err != nil {
		return Policy{}, fmt.Errorf("consent: load: %w", err)
	}
	defer rows.Close()
	p := Policy{granted: map[string]bool{}, Required: required}
	for rows.Next() {
		var (
			c string
			g bool
		)
		if err := rows.Scan(&c, &g); err != nil {
			return Policy{}, fmt.Errorf("consent: load: %w", err)
		}
		p.Recorded = true
		p.granted[c] = g
	}
	return p, rows.Err()
}

// Category is the state of one category of one source.
type Category struct {
	Category  string     `json:"category"`
	Granted   bool       `json:"granted"`
	GrantedAt *time.Time `json:"granted_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// Source is a user's consent for one source.
type Source struct {
	Provider   string     `json:"provider"`
	Categories []Category `json:"categories"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// ErrUnknownCategory is returned by Set for a category Known refuses.
var ErrUnknownCategory = errors.New("consent: unknown category")

// Change is one category whose state Set changed.
type Change struct {
	Category string
	Granted  bool
}

// Set makes granted the full list of categories the user allows from the
// source: every other category is revoked. It records an event per change
// with actor and evidence, queues a purge per newly revoked category, and
// returns the changes. Run it in a transaction (with the webhook events it
// announces).
func Set(ctx context.Context, tx DB, tenant, user uuid.UUID, provider string, granted []string, actor, evidence string) ([]Change, error) {
	want := map[string]bool{}
	for _, c := range granted {
		if !Known(c) {
			return nil, fmt.Errorf("%w %q", ErrUnknownCategory, c)
		}
		want[c] = true
	}
	current := map[string]bool{}
	rows, err := tx.Query(ctx, `
		SELECT category, granted FROM ohs_sync.consents
		WHERE tenant_id = $1 AND user_id = $2 AND provider = $3 FOR UPDATE`, tenant, user, provider)
	if err != nil {
		return nil, fmt.Errorf("consent: set: %w", err)
	}
	recorded := false
	for rows.Next() {
		var (
			c string
			g bool
		)
		if err := rows.Scan(&c, &g); err != nil {
			rows.Close()
			return nil, fmt.Errorf("consent: set: %w", err)
		}
		recorded = true
		current[c] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("consent: set: %w", err)
	}

	var changes []Change
	for _, c := range Categories {
		g := want[c]
		was := current[c]
		first := !recorded
		if !first && was == g {
			continue
		}
		// A first consent writes every category, so the policy is explicit.
		if _, err := tx.Exec(ctx, `
			INSERT INTO ohs_sync.consents (tenant_id, user_id, provider, category, granted, granted_at, revoked_at)
			VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 THEN now() END, CASE WHEN NOT $5 THEN now() END)
			ON CONFLICT (tenant_id, user_id, provider, category) DO UPDATE SET
			    granted = EXCLUDED.granted,
			    granted_at = CASE WHEN EXCLUDED.granted THEN now() ELSE consents.granted_at END,
			    revoked_at = CASE WHEN EXCLUDED.granted THEN NULL ELSE now() END,
			    updated_at = now()`,
			tenant, user, provider, c, g); err != nil {
			return nil, fmt.Errorf("consent: set %s: %w", c, err)
		}
		if !g {
			// Records of the category may be stored already: a revocation,
			// or a category left out of the first consent recorded after
			// data arrived. They are purged by the etl role.
			if _, err := tx.Exec(ctx, `
				INSERT INTO ohs_sync.consent_purges (tenant_id, user_id, provider, category)
				VALUES ($1, $2, $3, $4)`, tenant, user, provider, c); err != nil {
				return nil, fmt.Errorf("consent: queue purge: %w", err)
			}
			if first {
				continue // never granted: nothing to revoke, no event
			}
		}
		action := "granted"
		if !g {
			action = "revoked"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ohs_sync.consent_events (tenant_id, user_id, provider, category, action, actor_id, evidence)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, tenant, user, provider, c, action, actor, evidence); err != nil {
			return nil, fmt.Errorf("consent: event: %w", err)
		}
		changes = append(changes, Change{Category: c, Granted: g})
	}
	return changes, nil
}

// List returns the user's consent per source, sources in name order.
func List(ctx context.Context, db DB, tenant, user uuid.UUID) ([]Source, error) {
	rows, err := db.Query(ctx, `
		SELECT provider, category, granted, granted_at, revoked_at, updated_at FROM ohs_sync.consents
		WHERE tenant_id = $1 AND user_id = $2 ORDER BY provider`, tenant, user)
	if err != nil {
		return nil, fmt.Errorf("consent: list: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var (
			p string
			c Category
			u time.Time
		)
		if err := rows.Scan(&p, &c.Category, &c.Granted, &c.GrantedAt, &c.RevokedAt, &u); err != nil {
			return nil, fmt.Errorf("consent: list: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].Provider != p {
			out = append(out, Source{Provider: p})
		}
		s := &out[len(out)-1]
		s.Categories = append(s.Categories, c)
		if u.After(s.UpdatedAt) {
			s.UpdatedAt = u.UTC()
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("consent: list: %w", err)
	}
	for i := range out {
		slices.SortFunc(out[i].Categories, func(a, b Category) int {
			return slices.Index(Categories, a.Category) - slices.Index(Categories, b.Category)
		})
	}
	return out, nil
}

// Event is one change in the consent history.
type Event struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Provider string    `json:"provider"`
	Category string    `json:"category"`
	Action   string    `json:"action"` // granted or revoked
	ActorID  string    `json:"actor_id"`
	Evidence string    `json:"evidence"`
}

// History returns up to limit events of the user, newest first, with ids
// below before when it is positive, and the cursor of the next page (0 on
// the last).
func History(ctx context.Context, db DB, tenant, user uuid.UUID, before int64, limit int) ([]Event, int64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Query(ctx, `
		SELECT id, at, provider, category, action, actor_id, evidence FROM ohs_sync.consent_events
		WHERE tenant_id = $1 AND user_id = $2 AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, tenant, user, before, limit+1)
	if err != nil {
		return nil, 0, fmt.Errorf("consent: history: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.At, &e.Provider, &e.Category, &e.Action, &e.ActorID, &e.Evidence); err != nil {
			return nil, 0, fmt.Errorf("consent: history: %w", err)
		}
		e.At = e.At.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("consent: history: %w", err)
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
