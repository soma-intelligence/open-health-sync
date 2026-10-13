// SPDX-License-Identifier: AGPL-3.0-only

// Package rekey walks every sealed secret stored in the database (OAuth
// tokens, webhook signing secrets): Run re-seals each one under the active
// encryption key, Inspect counts which key each one is sealed with.
package rekey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/webhook"
)

// DefaultBatch is how many rows Run and Inspect read at once.
const DefaultBatch = 100

// DB is what Run and Inspect need of a pool or a connection.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// table is one table holding sealed columns. Every sealed column of a row is
// bound to the same aad, built from the row's primary key by the helper the
// writer of the column uses.
type table struct {
	name   string   // schema.table
	key    []string // primary key columns
	types  []string // their SQL types
	sealed []string // sealed bytea columns
	aad    func(key []string) ([]byte, error)
}

// tables lists every sealed column. A new SealFor call on a stored value
// belongs here, or rotation leaves it under the old key.
var tables = []table{
	{
		name:   "ohs_sync.connections",
		key:    []string{"tenant_id", "user_id", "provider"},
		types:  []string{"uuid", "uuid", "text"},
		sealed: []string{"access_token_enc", "refresh_token_enc"},
		aad: func(k []string) ([]byte, error) {
			t, err := uuid.Parse(k[0])
			if err != nil {
				return nil, err
			}
			u, err := uuid.Parse(k[1])
			if err != nil {
				return nil, err
			}
			return collect.SealAAD(t, u, k[2]), nil
		},
	},
	{
		name:   "ohs_core.webhook_endpoints",
		key:    []string{"id"},
		types:  []string{"uuid"},
		sealed: []string{"secret_sealed"},
		aad: func(k []string) ([]byte, error) {
			id, err := uuid.Parse(k[0])
			if err != nil {
				return nil, err
			}
			return webhook.SecretAAD(id), nil
		},
	},
}

// Columns lists the sealed columns as schema.table.column.
func Columns() []string {
	var out []string
	for _, t := range tables {
		for _, c := range t.sealed {
			out = append(out, t.name+"."+c)
		}
	}
	return out
}

// Counts is the outcome of Run for one sealed column. Null values are not
// counted.
type Counts struct {
	Column    string
	Rewritten int // re-sealed under the active key
	Current   int // already under the active key, left as they were
	Failed    int // no configured key opens them, left as they were
}

// Failure names a value no configured key opens. Row is its primary key
// (ids and a provider slug, never a secret).
type Failure struct {
	Column, Row string
}

// Options tunes Run.
type Options struct {
	// Batch is how many rows are read at once; 0 means DefaultBatch.
	Batch int
	// OnFailure, when set, is called for each value no key opens.
	OnFailure func(Failure)
}

// Run re-seals every stored secret that is not under the active key of box,
// with the same aad, so a secret stays bound to its row. It reads the rows in
// batches; each row to change is locked, read again and written in its own
// short transaction, so it runs safely next to serving processes that hold
// both the active and the previous keys. A value no configured key opens is
// reported and left untouched. Running it again changes nothing.
func Run(ctx context.Context, db DB, box *crypto.Box, o Options) ([]Counts, error) {
	var out []Counts
	for _, t := range tables {
		counts := make([]Counts, len(t.sealed))
		for i, c := range t.sealed {
			counts[i].Column = t.name + "." + c
		}
		err := walk(ctx, db, t, o.Batch, func(key []string, vals [][]byte) error {
			aad, err := t.aad(key)
			if err != nil {
				return err
			}
			todo := false
			for _, v := range vals {
				if v != nil && box.Status(v, aad) != crypto.StatusCurrent {
					todo = true
				}
			}
			if !todo {
				for i, v := range vals {
					if v != nil {
						counts[i].Current++
					}
				}
				return nil
			}
			return rekeyRow(ctx, db, box, t, key, aad, counts, o.OnFailure)
		})
		out = append(out, counts...)
		if err != nil {
			return out, fmt.Errorf("rekey %s: %w", t.name, err)
		}
	}
	return out, nil
}

// rekeyRow locks the row, reads its sealed values again (a serving process
// may have written new ones since the batch was read) and writes back those
// that changed.
func rekeyRow(ctx context.Context, db DB, box *crypto.Box, t table, key []string, aad []byte,
	counts []Counts, onFailure func(Failure)) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	vals := make([][]byte, len(t.sealed))
	dest := make([]any, len(vals))
	for i := range vals {
		dest[i] = &vals[i]
	}
	keyArgs := make([]any, len(key))
	for i, k := range key {
		keyArgs[i] = k
	}
	where := keyMatch(t, 1)
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s FOR UPDATE`,
		strings.Join(t.sealed, ", "), t.name, where), keyArgs...).Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted since the batch was read
	}
	if err != nil {
		return err
	}
	local := make([]Counts, len(vals))
	var sets []string
	args := keyArgs
	for i, v := range vals {
		if v == nil {
			continue
		}
		fresh, changed, err := box.Rekey(v, aad)
		switch {
		case err != nil:
			local[i].Failed++
			if onFailure != nil {
				onFailure(Failure{Column: t.name + "." + t.sealed[i], Row: describe(t, key)})
			}
		case changed:
			local[i].Rewritten++
			args = append(args, fresh)
			sets = append(sets, fmt.Sprintf("%s = $%d", t.sealed[i], len(args)))
		default:
			local[i].Current++
		}
	}
	if len(sets) > 0 {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s WHERE %s`,
			t.name, strings.Join(sets, ", "), where), args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for i := range local {
		counts[i].Rewritten += local[i].Rewritten
		counts[i].Current += local[i].Current
		counts[i].Failed += local[i].Failed
	}
	return nil
}

// Survey is the outcome of Inspect for one sealed column: how many values
// each kind of key opens. Null values are not counted.
type Survey struct {
	Column string
	// Current values are under the active key; Legacy values too, without
	// the key id header of values sealed since rotation support.
	Current, Legacy int
	// Previous values open with a previous key only.
	Previous int
	// Unknown values open with no configured key.
	Unknown int
}

// Inspect counts, for every sealed column, the values under the active key,
// under a previous key and under no configured key. It decrypts each value
// and changes nothing.
func Inspect(ctx context.Context, db DB, box *crypto.Box) ([]Survey, error) {
	var out []Survey
	for _, t := range tables {
		s := make([]Survey, len(t.sealed))
		for i, c := range t.sealed {
			s[i].Column = t.name + "." + c
		}
		err := walk(ctx, db, t, 0, func(key []string, vals [][]byte) error {
			aad, err := t.aad(key)
			if err != nil {
				return err
			}
			for i, v := range vals {
				if v == nil {
					continue
				}
				switch box.Status(v, aad) {
				case crypto.StatusCurrent:
					s[i].Current++
				case crypto.StatusLegacy:
					s[i].Legacy++
				case crypto.StatusPrevious:
					s[i].Previous++
				default:
					s[i].Unknown++
				}
			}
			return nil
		})
		out = append(out, s...)
		if err != nil {
			return out, fmt.Errorf("inspect %s: %w", t.name, err)
		}
	}
	return out, nil
}

// walk calls fn for every row of t in primary key order, batch rows per
// query, with the key as text and the sealed values. Keyset pagination keeps
// each query short whatever the table size.
func walk(ctx context.Context, db DB, t table, batch int, fn func(key []string, vals [][]byte) error) error {
	if batch <= 0 {
		batch = DefaultBatch
	}
	cols := make([]string, 0, len(t.key)+len(t.sealed))
	for _, k := range t.key {
		cols = append(cols, k+"::text")
	}
	cols = append(cols, t.sealed...)
	order := strings.Join(t.key, ", ")
	var last []string
	for {
		var args []any
		where := ""
		if last != nil {
			ph := make([]string, len(last))
			for i, k := range last {
				args = append(args, k)
				ph[i] = fmt.Sprintf("$%d::%s", i+1, t.types[i])
			}
			where = fmt.Sprintf("WHERE (%s) > (%s)", order, strings.Join(ph, ", "))
		}
		args = append(args, batch)
		q := fmt.Sprintf(`SELECT %s FROM %s %s ORDER BY %s LIMIT $%d`,
			strings.Join(cols, ", "), t.name, where, order, len(args))
		type row struct {
			key  []string
			vals [][]byte
		}
		// Read the whole batch before calling fn: fn may open a transaction
		// on the same connection.
		var page []row
		rows, err := db.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			r := row{key: make([]string, len(t.key)), vals: make([][]byte, len(t.sealed))}
			dest := make([]any, 0, len(cols))
			for i := range r.key {
				dest = append(dest, &r.key[i])
			}
			for i := range r.vals {
				dest = append(dest, &r.vals[i])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return err
			}
			page = append(page, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range page {
			if err := fn(r.key, r.vals); err != nil {
				return err
			}
		}
		if len(page) < batch {
			return nil
		}
		last = page[len(page)-1].key
	}
}

// keyMatch is the WHERE clause matching one row of t by its primary key,
// with placeholders from $first.
func keyMatch(t table, first int) string {
	parts := make([]string, len(t.key))
	for i, k := range t.key {
		parts[i] = fmt.Sprintf("%s = $%d::%s", k, first+i, t.types[i])
	}
	return strings.Join(parts, " AND ")
}

// describe names a row by its primary key, for a failure report.
func describe(t table, key []string) string {
	parts := make([]string, len(key))
	for i, k := range key {
		parts[i] = t.key[i] + "=" + k
	}
	return strings.Join(parts, " ")
}
