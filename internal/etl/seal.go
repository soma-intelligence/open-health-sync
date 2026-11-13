// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Sealer converts stored rows for `ohs vault seal` and `ohs vault reseal`
// (vault.RowSealer). It seals a clear row exactly as a job with keys converts
// the clear rows its batch meets (legacy.go), so a row comes out the same
// whichever of the two got to it first.
type Sealer struct{}

var _ vault.RowSealer = Sealer{}

// The tables without a write shape of their own: a failed record and a time
// tag are sealed under their id, and a track summary is derived from the
// route of its workout, so a clear one is dropped and built again on demand.
const (
	tFailed   = "failed_records"
	tTimeTags = "time_tags"
	tTracks   = "workout_tracks"
)

// sealOrder is the order tables are converted in: the samples, then the
// parents (each takes its clear children along), then what is left of the
// child tables, then the tables sealed under their id.
var sealOrder = []string{
	tQuantity, tCategory, tHeartbeat, tNutrition, tObservation, tDaily,
	tSleep, tWorkouts, tECG,
	tPhases, tRoutes, tECGSamples,
	tFailed, tTimeTags, tTracks,
}

// sealParent names, for a child table, its parent and the column that links
// a child to it.
var sealParent = map[string]struct{ table, link string }{
	tPhases:     {tSleep, "session_id"},
	tRoutes:     {tWorkouts, "workout_id"},
	tECGSamples: {tECG, "ecg_id"},
}

// sealTime is the clear time column a batch walks a table by.
var sealTime = map[string]string{
	tQuantity: "start_date", tCategory: "start_date", tHeartbeat: "timestamp", tNutrition: "start_date",
	tObservation: "ts", tDaily: "summary_date", tSleep: "bedtime_start", tWorkouts: "start_date", tECG: "start_date",
	tPhases: "start_date", tRoutes: "timestamp", tECGSamples: "ts",
	tFailed: "created_at", tTimeTags: "created_at", tTracks: "started_at",
	vault.HourlyTable: "bucket",
}

// timeType is the SQL type of the time column of table.
func timeType(table string) string {
	if table == tDaily {
		return "date"
	}
	return "timestamptz"
}

// Tables implements vault.RowSealer.
func (Sealer) Tables() []string { return slices.Clone(sealOrder) }

// fromClause is the condition that starts a batch at from, and its argument
// appended to args.
func fromClause(table, alias string, from vault.Cursor, args *[]any) string {
	if from.At.IsZero() {
		return ""
	}
	*args = append(*args, from.At)
	return fmt.Sprintf(` AND %s.%s >= $%d::%s`, alias, quoteCols([]string{sealTime[table]}), len(*args), timeType(table))
}

// nextCursor is the cursor after rows read in time order.
func nextCursor(table string, rows []Row, from vault.Cursor) vault.Cursor {
	if len(rows) == 0 {
		return from
	}
	if at, ok := rows[len(rows)-1][sealTime[table]].(time.Time); ok {
		return vault.Cursor{At: at}
	}
	return from
}

// queuedTx sends the statements of a batch to the server together. Exec
// queues and answers with an empty tag; a read, or flush, sends what is
// queued first, so every statement still runs in the order it was issued. It
// is only handed to code that ignores the tag of its Exec calls (convertRow
// and the helpers below).
type queuedTx struct {
	pgx.Tx
	batch *pgx.Batch
}

// queuedMax bounds the statements sent in one round trip.
const queuedMax = 500

func (q *queuedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.batch.Queue(sql, args...)
	if q.batch.Len() >= queuedMax {
		return pgconn.CommandTag{}, q.flush(ctx)
	}
	return pgconn.CommandTag{}, nil
}

func (q *queuedTx) flush(ctx context.Context) error {
	if q.batch.Len() == 0 {
		return nil
	}
	b := q.batch
	q.batch = &pgx.Batch{}
	return q.Tx.SendBatch(ctx, b).Close()
}

func (q *queuedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := q.flush(ctx); err != nil {
		return nil, err
	}
	return q.Tx.Query(ctx, sql, args...)
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func (q *queuedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := q.flush(ctx); err != nil {
		return errRow{err}
	}
	return q.Tx.QueryRow(ctx, sql, args...)
}

// SealClear implements vault.RowSealer.
func (Sealer) SealClear(ctx context.Context, tx pgx.Tx, uv *vault.UserVault, tenant, user uuid.UUID, table string, from vault.Cursor, limit int) (vault.SealCount, error) {
	sc := scope{tenant: tenant, user: user, uv: uv}
	q := &queuedTx{Tx: tx, batch: &pgx.Batch{}}
	var (
		c   vault.SealCount
		err error
	)
	switch {
	case table == tTracks:
		c, err = dropClearTracks(ctx, tx, sc, limit)
	case table == tFailed || table == tTimeTags:
		c, err = sealByID(ctx, q, sc, table, from, limit)
	case sealParent[table].table != "":
		c, err = sealChildren(ctx, q, sc, table, limit)
	case shapeFor(table) != nil:
		c, err = sealHashed(ctx, q, sc, table, from, limit)
	default:
		return c, fmt.Errorf("etl: seal: %s is not a table the seal command converts", table)
	}
	if err != nil {
		return c, err
	}
	if err := q.flush(ctx); err != nil {
		return c, fmt.Errorf("seal %s rows: %w", table, err)
	}
	return c, nil
}

// storedCopy is a sealed row that already carries the keyed hash a clear row
// would take.
type storedCopy struct {
	id       any
	ingested time.Time
}

type copyKey struct {
	hash string
	at   int64
}

// sealHashed converts clear rows of a table whose identity is its content
// hash and a time: the samples and the parents. The writers of the user are
// held off the table (and off the aliases) for the batch, as a job holds them.
func sealHashed(ctx context.Context, tx *queuedTx, sc scope, table string, from vault.Cursor, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	t := vault.Schema[table]
	timeCol := sealTime[table]
	col := quoteCols([]string{timeCol})
	if aliased(table) {
		if err := lockAliases(ctx, tx, sc); err != nil {
			return c, err
		}
	}
	if err := lockUserTable(ctx, tx, table, sc); err != nil {
		return c, err
	}
	args := []any{sc.tenant, sc.user}
	where := fromClause(table, "r", from, &args)
	args = append(args, limit)
	rows, err := collectRows(openRows(ctx, tx, nil, table,
		`SELECT * FROM `+qualified(table)+` r WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.sealed IS NULL`+where+
			fmt.Sprintf(` ORDER BY r.%s LIMIT $%d`, col, len(args)), args))
	if err != nil {
		return c, fmt.Errorf("seal %s rows: %w", table, err)
	}
	if len(rows) == 0 {
		return c, nil
	}
	c.Next = nextCursor(table, rows, from)

	// The sealed rows that already carry the keyed hash of one of these: the
	// same record stored twice. A table with a dedup unique would refuse the
	// second; one without (a compressed hypertable) would keep both.
	hasID := slices.Contains(t.Clear, "id")
	keyOf := func(hash string, r Row) copyKey {
		at, _ := r[timeCol].(time.Time)
		return copyKey{hash, at.UnixMicro()}
	}
	hashes := make([]string, 0, len(rows))
	times := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		if h := sc.hash(r.text("content_hash")); h != "" {
			at, _ := r[timeCol].(time.Time)
			hashes, times = append(hashes, h), append(times, at)
		}
	}
	stored := map[copyKey]storedCopy{}
	if len(hashes) > 0 {
		idCol := "NULL::bigint"
		if hasID {
			idCol = "r.id"
		}
		found, err := tx.Query(ctx, fmt.Sprintf(`SELECT r.content_hash, r.%[1]s, r.ingested_at, %[2]s FROM %[3]s r
			WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.sealed IS NOT NULL AND r.%[1]s BETWEEN $3 AND $4
			  AND (r.content_hash, r.%[1]s) IN (SELECT * FROM unnest($5::text[], $6::%[4]s[]))`,
			col, idCol, qualified(table), timeType(table)),
			sc.tenant, sc.user, slices.MinFunc(times, time.Time.Compare), slices.MaxFunc(times, time.Time.Compare), hashes, times)
		if err != nil {
			return c, fmt.Errorf("seal %s rows: stored copies: %w", table, err)
		}
		for found.Next() {
			var (
				hash string
				at   time.Time
				cp   storedCopy
			)
			if err := found.Scan(&hash, &at, &cp.ingested, &cp.id); err != nil {
				found.Close()
				return c, fmt.Errorf("seal %s rows: stored copies: %w", table, err)
			}
			stored[copyKey{hash, at.UnixMicro()}] = cp
		}
		found.Close()
		if err := found.Err(); err != nil {
			return c, fmt.Errorf("seal %s rows: stored copies: %w", table, err)
		}
	}

	// Children counted before their parents take them along.
	child := ""
	switch table {
	case tSleep:
		child = tPhases
	case tWorkouts:
		child = tRoutes
	case tECG:
		child = tECGSamples
	}
	if child != "" {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			if id, ok := r.int("id"); ok {
				ids = append(ids, id)
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+qualified(child)+`
			WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL AND `+sealParent[child].link+` = ANY($3)`,
			sc.tenant, sc.user, ids).Scan(&n); err != nil {
			return c, fmt.Errorf("seal %s rows: count %s: %w", table, child, err)
		}
		c.Children, c.ChildTable = n, child
	}

	// Oldest first, so of two clear copies of one record the older stays.
	slices.SortStableFunc(rows, func(a, b Row) int {
		at, _ := a["ingested_at"].(time.Time)
		bt, _ := b["ingested_at"].(time.Time)
		return at.Compare(bt)
	})
	for _, r := range rows {
		old := r.text("content_hash")
		hash := sc.hash(old)
		k := keyOf(hash, r)
		if cp, twice := stored[k]; twice && hash != "" {
			c.Duplicates++
			ingested, _ := r["ingested_at"].(time.Time)
			if !cp.ingested.After(ingested) {
				// The stored copy is the older one: the clear row goes, and
				// its aliases now name the copy that stays.
				if err := deleteStored(ctx, tx, sc, table, child, r, false); err != nil {
					return c, err
				}
				if aliased(table) {
					at, _ := r["start_date"].(time.Time)
					if err := rekeyAliases(ctx, tx, sc, table, old, hash, at); err != nil {
						return c, err
					}
				}
				continue
			}
			// The clear row is the older one: the sealed copy goes.
			sealedRow := Row{"id": cp.id, "content_hash": hash, timeCol: r[timeCol]}
			if err := deleteStored(ctx, tx, sc, table, child, sealedRow, true); err != nil {
				return c, err
			}
		}
		if err := convertRow(ctx, tx, sc, table, r, hash); err != nil {
			return c, err
		}
		if hash != "" {
			stored[k] = storedCopy{id: r["id"], ingested: time.Time{}}
		}
		c.Rows++
	}
	return c, nil
}

// deleteStored deletes one row of table, clear or sealed as said, and the
// rows of its child table.
func deleteStored(ctx context.Context, tx TxLike, sc scope, table, child string, r Row, sealed bool) error {
	t := vault.Schema[table]
	state := "sealed IS NULL"
	if sealed {
		state = "sealed IS NOT NULL"
	}
	args := []any{sc.tenant, sc.user}
	var locate []string
	if id := r["id"]; id != nil {
		args = append(args, id)
		locate = append(locate, "id = $3")
		if child != "" {
			if _, err := tx.Exec(ctx, `DELETE FROM `+qualified(child)+` WHERE tenant_id = $1 AND user_id = $2 AND `+
				sealParent[child].link+` = $3`, args...); err != nil {
				return fmt.Errorf("seal %s rows: delete the %s of a duplicate: %w", table, child, err)
			}
		}
	}
	// The identity also bounds the delete to one chunk of a hypertable.
	for _, c := range t.Identity {
		args = append(args, r[c])
		locate = append(locate, fmt.Sprintf("%s = $%d", quoteCols([]string{c}), len(args)))
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+qualified(table)+` WHERE tenant_id = $1 AND user_id = $2 AND `+state+` AND `+
		strings.Join(locate, " AND "), args...); err != nil {
		return fmt.Errorf("seal %s rows: delete a duplicate: %w", table, err)
	}
	return nil
}

// sealChildren converts the clear rows of a child table their parent did not
// take along: those of a parent already sealed, hashed over its keyed hash as
// convertChildren does, and those whose parent is gone, which keep the keyed
// form of the hash they have.
func sealChildren(ctx context.Context, tx *queuedTx, sc scope, child string, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	p := sealParent[child]
	type parent struct {
		id   int64
		hash string
		n    int
	}
	found, err := tx.Query(ctx, `SELECT p.id, p.content_hash, count(*) FROM `+qualified(child)+` c
		JOIN `+qualified(p.table)+` p ON p.tenant_id = c.tenant_id AND p.user_id = c.user_id AND p.id = c.`+p.link+`
		WHERE c.tenant_id = $1 AND c.user_id = $2 AND c.sealed IS NULL AND p.sealed IS NOT NULL
		GROUP BY p.id, p.content_hash LIMIT $3`, sc.tenant, sc.user, max(1, limit/100))
	if err != nil {
		return c, fmt.Errorf("seal %s rows: %w", child, err)
	}
	parents, err := pgx.CollectRows(found, func(r pgx.CollectableRow) (parent, error) {
		var x parent
		return x, r.Scan(&x.id, &x.hash, &x.n)
	})
	if err != nil {
		return c, fmt.Errorf("seal %s rows: %w", child, err)
	}
	for _, x := range parents {
		if err := convertChildren(ctx, tx, sc, p.table, x.id, x.hash); err != nil {
			return c, err
		}
		c.Rows += x.n
	}
	if len(parents) > 0 {
		return c, nil
	}
	rows, err := collectRows(openRows(ctx, tx, nil, child, `SELECT c.* FROM `+qualified(child)+` c
		WHERE c.tenant_id = $1 AND c.user_id = $2 AND c.sealed IS NULL
		  AND NOT EXISTS (SELECT 1 FROM `+qualified(p.table)+` p
		      WHERE p.tenant_id = c.tenant_id AND p.user_id = c.user_id AND p.id = c.`+p.link+`)
		LIMIT $3`, []any{sc.tenant, sc.user, limit}))
	if err != nil {
		return c, fmt.Errorf("seal %s rows: %w", child, err)
	}
	for _, r := range rows {
		if err := convertRow(ctx, tx, sc, child, r, sc.hash(r.text("content_hash"))); err != nil {
			return c, err
		}
		c.Rows++
	}
	return c, nil
}

// identityOf is rowIdentity over a stored row, with a uuid column (as pgx
// reads it) taken as its 16 bytes.
func identityOf(t vault.Table, r Row) ([]byte, error) {
	return rowIdentity(t, func(c string) any {
		if id, ok := r[c].([16]byte); ok {
			return id[:]
		}
		return r[c]
	})
}

// sealByID converts clear rows of a table whose identity is its id: every
// sensitive column sealed together and set to NULL, nothing else changed.
func sealByID(ctx context.Context, tx *queuedTx, sc scope, table string, from vault.Cursor, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	t := vault.Schema[table]
	args := []any{sc.tenant, sc.user}
	where := fromClause(table, "r", from, &args)
	args = append(args, limit)
	rows, err := collectRows(openRows(ctx, tx, nil, table,
		`SELECT * FROM `+qualified(table)+` r WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.sealed IS NULL`+where+
			fmt.Sprintf(` ORDER BY r.%s LIMIT $%d`, quoteCols([]string{sealTime[table]}), len(args)), args))
	if err != nil {
		return c, fmt.Errorf("seal %s rows: %w", table, err)
	}
	c.Next = nextCursor(table, rows, from)
	set := []string{"sealed = $4"}
	for _, col := range t.Sealed {
		set = append(set, col.Name+" = NULL")
	}
	update := `UPDATE ` + qualified(table) + ` SET ` + strings.Join(set, ", ") + `
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3 AND sealed IS NULL`
	for _, r := range rows {
		vals, err := sealedValues(t, r)
		if err != nil {
			return c, err
		}
		id, err := identityOf(t, r)
		if err != nil {
			return c, err
		}
		sealed, err := sc.uv.SealRow(t, id, vals)
		if err != nil {
			return c, fmt.Errorf("seal %s row: %w", table, err)
		}
		if _, err := tx.Exec(ctx, update, sc.tenant, sc.user, r["id"], sealed); err != nil {
			return c, fmt.Errorf("seal %s row: %w", table, err)
		}
		c.Rows++
	}
	return c, nil
}

// dropClearTracks deletes clear track summaries: derived rows, built again
// from the sealed route when they are next asked for.
func dropClearTracks(ctx context.Context, tx pgx.Tx, sc scope, limit int) (vault.SealCount, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM ohs_data.workout_tracks WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL
		AND workout_id IN (SELECT workout_id FROM ohs_data.workout_tracks
		    WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NULL LIMIT $3)`, sc.tenant, sc.user, limit)
	if err != nil {
		return vault.SealCount{}, fmt.Errorf("seal %s rows: %w", tTracks, err)
	}
	return vault.SealCount{Rows: int(tag.RowsAffected())}, nil
}

// Reseal implements vault.RowSealer: rows sealed under an older key version
// are opened and sealed again under the newest. Nothing else of the row
// changes: the keyed hashes and the content MAC of a user are the same under
// every version of their keys.
func (Sealer) Reseal(ctx context.Context, tx pgx.Tx, uv *vault.UserVault, tenant, user uuid.UUID, table string, from vault.Cursor, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	t, ok := vault.Schema[table]
	if !ok || sealTime[table] == "" {
		return c, fmt.Errorf("etl: reseal: %s is not a sealed table", table)
	}
	q := &queuedTx{Tx: tx, batch: &pgx.Batch{}}
	// A sealed row is format (1) || key version (4, big endian) || ...
	newest := binary.BigEndian.AppendUint32(nil, uv.Version())
	args := []any{tenant, user, newest}
	where := fromClause(table, "r", from, &args)
	args = append(args, limit)
	cols := []string{"sealed"}
	if slices.Contains(t.Clear, "id") {
		cols = append(cols, "id")
	}
	for _, col := range append(slices.Clone(t.Identity), sealTime[table]) {
		if !slices.Contains(cols, col) {
			cols = append(cols, col)
		}
	}
	found, err := tx.Query(ctx, `SELECT `+quoteCols(cols)+` FROM `+qualified(table)+` r
		WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.sealed IS NOT NULL AND substring(r.sealed from 2 for 4) <> $3`+where+
		fmt.Sprintf(` ORDER BY r.%s LIMIT $%d`, quoteCols([]string{sealTime[table]}), len(args)), args...)
	if err != nil {
		return c, fmt.Errorf("reseal %s rows: %w", table, err)
	}
	rows, err := pgx.CollectRows(found, func(r pgx.CollectableRow) (Row, error) {
		vals, err := r.Values()
		if err != nil {
			return nil, err
		}
		row := make(Row, len(cols))
		for i, col := range cols {
			row[col] = vals[i]
		}
		return row, nil
	})
	if err != nil {
		return c, fmt.Errorf("reseal %s rows: %w", table, err)
	}
	c.Next = nextCursor(table, rows, from)
	for _, r := range rows {
		id, err := identityOf(t, r)
		if err != nil {
			return c, err
		}
		old, _ := r["sealed"].([]byte)
		vals, err := uv.OpenRow(t, id, old)
		if err != nil {
			return c, fmt.Errorf("reseal %s row: %w", table, err)
		}
		sealed, err := uv.SealRow(t, id, vals)
		if err != nil {
			return c, fmt.Errorf("reseal %s row: %w", table, err)
		}
		// Located by its id, or its identity on a table without one, and by
		// the value read: a row a job rewrote meanwhile is left to the next
		// batch.
		uargs := []any{tenant, user, sealed, old}
		var locate []string
		if rid, ok := r["id"]; ok {
			uargs = append(uargs, rid)
			locate = append(locate, fmt.Sprintf("id = $%d", len(uargs)))
		}
		for _, col := range t.Identity {
			if col == "id" {
				continue
			}
			uargs = append(uargs, r[col])
			locate = append(locate, fmt.Sprintf("%s = $%d", quoteCols([]string{col}), len(uargs)))
		}
		if _, err := q.Exec(ctx, `UPDATE `+qualified(table)+` SET sealed = $3
			WHERE tenant_id = $1 AND user_id = $2 AND sealed = $4 AND `+strings.Join(locate, " AND "), uargs...); err != nil {
			return c, fmt.Errorf("reseal %s row: %w", table, err)
		}
		c.Rows++
	}
	if err := q.flush(ctx); err != nil {
		return c, fmt.Errorf("reseal %s rows: %w", table, err)
	}
	return c, nil
}

// RebuildHours implements vault.RowSealer: the hours of the user that hold
// samples and have no bucket get theirs. A bucket is computed from the opened
// samples whether they are sealed or still clear, so an hour that has one is
// already right. from.At and from.Key are the bucket and the quantity type
// the last batch ended on.
func (Sealer) RebuildHours(ctx context.Context, tx pgx.Tx, uv *vault.UserVault, tenant, user uuid.UUID, from vault.Cursor, limit int) (vault.SealCount, error) {
	var c vault.SealCount
	args := []any{tenant, user, limit}
	after := ""
	if !from.At.IsZero() {
		args = append(args, from.At, from.Key)
		after = ` AND (date_trunc('hour', q.start_date, 'UTC'), q.quantity_type) > ($4::timestamptz, $5::text)
			AND q.start_date >= $4::timestamptz`
	}
	found, err := tx.Query(ctx, `SELECT DISTINCT date_trunc('hour', q.start_date, 'UTC') AS bucket, q.quantity_type
		FROM ohs_data.quantity_samples q
		WHERE q.tenant_id = $1 AND q.user_id = $2`+after+`
		  AND NOT EXISTS (SELECT 1 FROM ohs_data.quantity_hourly_sealed b
		      WHERE b.tenant_id = q.tenant_id AND b.user_id = q.user_id AND b.quantity_type = q.quantity_type
		        AND b.bucket = date_trunc('hour', q.start_date, 'UTC'))
		ORDER BY 1, 2 LIMIT $3`, args...)
	if err != nil {
		return c, fmt.Errorf("rebuild hourly buckets: %w", err)
	}
	hours, err := pgx.CollectRows(found, func(r pgx.CollectableRow) (HourKey, error) {
		var h HourKey
		return h, r.Scan(&h.Bucket, &h.QuantityType)
	})
	if err != nil {
		return c, fmt.Errorf("rebuild hourly buckets: %w", err)
	}
	if len(hours) == 0 {
		return c, nil
	}
	// RecomputeHours takes the user's sample lock; the aliases come first in
	// the writer's order and are not touched here.
	if err := RecomputeHours(ctx, tx, uv, tenant, user, hours); err != nil {
		return c, err
	}
	last := hours[len(hours)-1]
	c.Rows = len(hours)
	c.Next = vault.Cursor{At: last.Bucket, Key: last.QuantityType}
	return c, nil
}
