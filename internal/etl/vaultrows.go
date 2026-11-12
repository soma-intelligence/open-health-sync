// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// Sealed rows (docs/specs/vault.md).
//
// When the job holds the user's keys (scope.uv), every health row is written
// with its sensitive columns NULL and all of them sealed together into the
// row's sealed column (vault.Schema lists them). content_hash becomes the keyed
// HMAC of the plain hash the mapper computed, so dedup still works and the seal
// command can derive the same value from a stored clear row. The promoted clear
// columns (source_key, worn, tz, derived, synthesized, summary_date) are filled
// from the record, as migration 0050 filled them from stored rows.
//
// Without keys (no vault configured) the writer writes clear rows as before,
// with the promoted columns filled too, source_key aside.
//
// Whatever Go must compute on a sealed value (a daily sum, sleep totals, the
// latest wins merge of an upsert) reads the rows through openRows, which opens
// a sealed row and takes the clear columns of a row not converted yet.

// Row is one row read through openRows: every selected column by name, with
// the sealed columns of the table opened (or taken from the clear columns of
// a row with no sealed value). Sealed values have the types vault.Table.Decode
// returns: float64, int64, bool, string, json.RawMessage, time.Time.
type Row map[string]any

// openRows runs sql in tx and yields its rows opened under uv. The statement
// selects from table (a SELECT, or a DELETE ... RETURNING) and must return the
// columns of the table's identity and its sealed column whenever a row may be
// sealed, plus the clear sealed columns it needs from rows not converted yet.
// uv may be nil when no row is sealed (no vault configured); a sealed row then
// fails. The caller must not use tx before the sequence ends.
func openRows(ctx context.Context, tx TxLike, uv *vault.UserVault, table, sql string, args []any) iter.Seq2[Row, error] {
	return func(yield func(Row, error) bool) {
		t, ok := vault.Schema[table]
		if !ok {
			yield(nil, fmt.Errorf("etl: open rows: %s is not a sealed table", table))
			return
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			yield(nil, fmt.Errorf("read %s: %w", table, err))
			return
		}
		defer rows.Close()
		fields := rows.FieldDescriptions()
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				yield(nil, fmt.Errorf("read %s: %w", table, err))
				return
			}
			r := make(Row, len(fields)+len(t.Sealed))
			for i, f := range fields {
				r[f.Name] = vals[i]
			}
			if err := r.open(t, uv); err != nil {
				yield(nil, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(nil, fmt.Errorf("read %s: %w", table, err))
		}
	}
}

// collectRows reads every row of seq.
func collectRows(seq iter.Seq2[Row, error]) ([]Row, error) {
	var out []Row
	for r, err := range seq {
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// open replaces the sealed columns of r with what its sealed value holds, or
// normalizes the clear ones of a row not converted yet.
func (r Row) open(t vault.Table, uv *vault.UserVault) error {
	if sealed, _ := r["sealed"].([]byte); sealed != nil {
		if uv == nil {
			return fmt.Errorf("etl: a %s row is sealed and the job holds no keys", t.Name)
		}
		id, err := rowIdentity(t, func(c string) any { return r[c] })
		if err != nil {
			return err
		}
		vals, err := uv.OpenRow(t, id, sealed)
		if err != nil {
			return fmt.Errorf("open %s row: %w", t.Name, err)
		}
		for i, c := range t.Sealed {
			r[c.Name] = vals[i]
		}
		return nil
	}
	for _, c := range t.Sealed {
		v, ok := r[c.Name]
		if !ok {
			continue
		}
		n, err := vaultValue(c.Kind, v)
		if err != nil {
			return fmt.Errorf("etl: %s.%s: %w", t.Name, c.Name, err)
		}
		r[c.Name] = n
	}
	return nil
}

// float returns a float column of r, and false when it is NULL.
func (r Row) float(name string) (float64, bool) {
	v, ok := r[name].(float64)
	return v, ok
}

// int returns an integer column of r, and false when it is NULL.
func (r Row) int(name string) (int64, bool) {
	switch v := r[name].(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int16:
		return int64(v), true
	}
	return 0, false
}

// text returns a text column of r, "" when it is NULL.
func (r Row) text(name string) string {
	s, _ := r[name].(string)
	return s
}

// rowIdentity is the identity of a row of t, the tail of its AAD: the values
// of t.Identity in order, a string or byte column as its bytes, an instant as
// its UnixMicro (uint64 big endian, the grain Postgres keeps), an integer as
// 8 bytes big endian. For the samples it is content_hash || start_date.
func rowIdentity(t vault.Table, get func(column string) any) ([]byte, error) {
	var out []byte
	for _, c := range t.Identity {
		switch v := get(c).(type) {
		case string:
			out = append(out, v...)
		case []byte:
			out = append(out, v...)
		case time.Time:
			out = binary.BigEndian.AppendUint64(out, uint64(v.UnixMicro()))
		case int64:
			out = binary.BigEndian.AppendUint64(out, uint64(v))
		case int32:
			out = binary.BigEndian.AppendUint64(out, uint64(int64(v)))
		case int:
			out = binary.BigEndian.AppendUint64(out, uint64(int64(v)))
		case bool:
			if v {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		default:
			return nil, fmt.Errorf("etl: %s identity column %s is %T", t.Name, c, v)
		}
	}
	return out, nil
}

// deref returns the value a pointer points to, nil for a nil pointer, and any
// other value as it is.
func deref(v any) any {
	switch x := v.(type) {
	case *float64:
		if x == nil {
			return nil
		}
		return *x
	case *int:
		if x == nil {
			return nil
		}
		return *x
	case *int64:
		if x == nil {
			return nil
		}
		return *x
	case *int32:
		if x == nil {
			return nil
		}
		return *x
	case *bool:
		if x == nil {
			return nil
		}
		return *x
	case *string:
		if x == nil {
			return nil
		}
		return *x
	case *time.Time:
		if x == nil {
			return nil
		}
		return *x
	case []byte:
		if x == nil {
			return nil
		}
	case json.RawMessage:
		if x == nil {
			return nil
		}
	}
	return v
}

// vaultValue converts a write value or a value read from a clear column to
// the type the codec takes for kind k; nil is NULL. A JSON document that is not
// already bytes is marshalled, which sorts object keys, so the same document
// always gives the same bytes and the same content MAC.
func vaultValue(k vault.Kind, v any) (any, error) {
	v = deref(v)
	if v == nil {
		return nil, nil
	}
	switch k {
	case vault.KindFloat64:
		switch x := v.(type) {
		case float64:
			return x, nil
		case float32:
			return float64(x), nil
		}
	case vault.KindInt64:
		switch x := v.(type) {
		case int64:
			return x, nil
		case int:
			return int64(x), nil
		case int32:
			return int64(x), nil
		case int16:
			return int64(x), nil
		}
	case vault.KindBool:
		if x, ok := v.(bool); ok {
			return x, nil
		}
	case vault.KindText:
		if x, ok := v.(string); ok {
			return x, nil
		}
	case vault.KindJSON:
		switch x := v.(type) {
		case json.RawMessage:
			return x, nil
		case []byte:
			return json.RawMessage(x), nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	case vault.KindTime:
		if x, ok := v.(time.Time); ok {
			return x, nil
		}
	case vault.KindBytes:
		if x, ok := v.([]byte); ok {
			return x, nil
		}
	}
	return nil, fmt.Errorf("kind %d does not take %T", k, v)
}

// comparable maps a value to one == compares as SQL IS NOT DISTINCT FROM
// would: pointers followed, integers widened, instants to the microsecond,
// JSON documents to their marshalled text.
func comparable(v any) any {
	v = deref(v)
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case float32:
		return float64(x)
	case time.Time:
		return x.UnixMicro()
	case json.RawMessage:
		return string(x)
	case []byte:
		return string(x)
	case map[string]any, []any:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
	return v
}

func sameValue(a, b any) bool { return comparable(a) == comparable(b) }

// --- Write shapes ---

// shape is the write form of one table: the columns the writer fills (base),
// the promoted columns derived from them, and, with keys, the sealed and
// content_mac columns that replace the sensitive ones.
type shape struct {
	t        vault.Table
	base     []string
	index    map[string]int // base column -> position
	sealedAt []int          // base position of each t.Sealed column, -1 when not written
	sealed   map[string]bool
	promoted []string
	// derive computes the promoted columns, in promoted order, from a full
	// base row.
	derive func(sc scope, get func(column string) any) []any
}

// newShape describes the writes of table. It panics on a table the vault does
// not describe or an identity column the writer does not fill: both are
// programming errors found by the first test.
func newShape(table string, base, promoted []string, derive func(scope, func(string) any) []any) *shape {
	t, ok := vault.Schema[table]
	if !ok {
		panic("etl: no vault schema for " + table)
	}
	s := &shape{t: t, base: base, index: make(map[string]int, len(base)), sealed: map[string]bool{}, promoted: promoted, derive: derive}
	for i, c := range base {
		s.index[c] = i
	}
	for _, c := range t.Sealed {
		p, ok := s.index[c.Name]
		if !ok {
			p = -1
		}
		s.sealedAt = append(s.sealedAt, p)
		s.sealed[c.Name] = true
	}
	for _, c := range t.Identity {
		if _, ok := s.index[c]; !ok {
			panic("etl: " + table + " writes no identity column " + c)
		}
	}
	return s
}

// columns is the column list of a write row for sc.
func (s *shape) columns(sc scope) []string { return s.columnsFor(sc.uv != nil) }

// columnsFor is the column list of a clear (sealed false) or sealed row.
func (s *shape) columnsFor(sealed bool) []string {
	out := append(append([]string{}, s.base...), s.promoted...)
	if !sealed {
		return out
	}
	out = append(out, "sealed")
	if s.t.MAC {
		out = append(out, "content_mac")
	}
	return out
}

// finish turns a base row into the row written for sc: the promoted columns
// appended, and with keys the sensitive columns set to NULL and sealed.
func (s *shape) finish(sc scope, base []any) ([]any, error) {
	get := func(c string) any {
		if p, ok := s.index[c]; ok {
			return base[p]
		}
		return nil
	}
	row := make([]any, 0, len(base)+len(s.promoted)+2)
	row = append(row, base...)
	if s.derive != nil {
		row = append(row, s.derive(sc, get)...)
	}
	if sc.uv == nil {
		return row, nil
	}
	vals := make([]any, len(s.t.Sealed))
	for i, c := range s.t.Sealed {
		p := s.sealedAt[i]
		if p < 0 {
			continue
		}
		v, err := vaultValue(c.Kind, base[p])
		if err != nil {
			return nil, fmt.Errorf("etl: %s.%s: %w", s.t.Name, c.Name, err)
		}
		vals[i] = v
		row[p] = nil
	}
	id, err := rowIdentity(s.t, get)
	if err != nil {
		return nil, err
	}
	sealed, err := sc.uv.SealRow(s.t, id, vals)
	if err != nil {
		return nil, fmt.Errorf("seal %s row: %w", s.t.Name, err)
	}
	row = append(row, sealed)
	if s.t.MAC {
		row = append(row, sc.uv.ContentMAC(s.t, vals))
	}
	return row, nil
}

// finishAll is finish over rows.
func (s *shape) finishAll(sc scope, rows [][]any) ([][]any, error) {
	out := make([][]any, len(rows))
	for i, r := range rows {
		f, err := s.finish(sc, r)
		if err != nil {
			return nil, err
		}
		out[i] = f
	}
	return out, nil
}

// mergeStored applies latest wins in Go to a new base row and the stored row
// with its identity: a figure the new copy carries replaces the stored one, a
// figure it omits keeps the stored value, metadata follows the new copy, and
// every other sealed column keeps the stored value (the SQL merge never
// touched them). It reports whether a figure changed; a copy that changes no
// figure writes nothing, as the SQL comparison decided.
func (s *shape) mergeStored(base []any, stored Row, figures []string) ([]any, bool) {
	out := slices.Clone(base)
	changed := false
	for i, name := range s.base {
		switch {
		case slices.Contains(figures, name):
			if deref(base[i]) == nil {
				out[i] = stored[name]
			}
			if !sameValue(out[i], stored[name]) {
				changed = true
			}
		case name == "metadata":
		case s.sealed[name]:
			out[i] = stored[name]
		}
	}
	return out, changed
}

// sealedSet is the SET list a sealed upsert writes: the sealed value and its
// MAC, the capture time of the object the new copy came from (on the tables
// that keep one), the clear figures and the promoted columns from src
// ("EXCLUDED" or a staging alias), and NULL in every sensitive column, so a
// stored clear row is converted too.
func (s *shape) sealedSet(src string, figures []string) []string {
	set := []string{"sealed = " + src + ".sealed"}
	if s.t.MAC {
		set = append(set, "content_mac = "+src+".content_mac")
	}
	if s.ordered() {
		set = append(set, "captured_at = "+src+".captured_at")
	}
	for _, f := range s.clearFigures(figures) {
		set = append(set, f+" = "+src+"."+f)
	}
	for _, p := range s.promoted {
		set = append(set, p+" = "+src+"."+p)
	}
	for _, c := range s.t.Sealed {
		set = append(set, c.Name+" = NULL")
	}
	return set
}

// clearFigures are the figures that stay in clear columns.
func (s *shape) clearFigures(figures []string) []string {
	var out []string
	for _, f := range figures {
		if !s.sealed[f] {
			out = append(out, f)
		}
	}
	return out
}

// sealedDistinct is the WHERE of a sealed upsert: the content MAC or a clear
// figure differs between the stored row (alias) and src.
func (s *shape) sealedDistinct(alias, src string, figures []string) string {
	old, now := []string{alias + ".content_mac"}, []string{src + ".content_mac"}
	for _, f := range s.clearFigures(figures) {
		old, now = append(old, alias+"."+f), append(now, src+"."+f)
	}
	return "(" + strings.Join(old, ", ") + ") IS DISTINCT FROM (" + strings.Join(now, ", ") + ")"
}

// ordered reports whether the rows of the table carry captured_at, the
// capture time that orders the copies of a Replaceable row (latestWins).
func (s *shape) ordered() bool {
	_, ok := s.index["captured_at"]
	return ok
}

// notStale is the capture guard of a sealed upsert, as an " AND ..." suffix
// of its WHERE: the copy of src replaces the stored row (alias) only when it
// was not captured before it (see notStale in writer.go). Empty on a table
// without captured_at.
func (s *shape) notStale(alias, src string) string {
	if !s.ordered() {
		return ""
	}
	return " AND " + notStale(alias+".captured_at", src+".captured_at")
}

// latestWinsSealed is the conflict action of a Replaceable row written
// sealed: the merge already ran in Go (mergeStored), and a copy from an older
// object than the stored row replaces nothing.
func (s *shape) latestWinsSealed(conflictCols string, figures []string) string {
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s WHERE %s%s", conflictCols,
		strings.Join(s.sealedSet("EXCLUDED", figures), ", "), s.sealedDistinct(s.t.Name, "EXCLUDED", figures),
		s.notStale(s.t.Name, "EXCLUDED"))
}

// rowKey is a stored row's identity within one user: content hash and the
// time column, to the microsecond Postgres keeps.
type rowKey struct {
	hash  string
	micro int64
}

func keyOf(hash string, at time.Time) rowKey { return rowKey{hash, at.UnixMicro()} }

// storedRows returns the stored rows of table whose (content_hash, timeCol)
// is one of the pairs, opened, by key. A key may hold several copies
// (quantity_samples has no unique). timeType is the SQL type of timeCol; lo
// and hi bound timeCol so a hypertable reads only those chunks. With kindCol
// (the table's type column) each pair is looked up under its kind (kinds, one
// per pair), so the read goes through the (user, type, time) index and never
// touches the rows of another type in the window.
func storedRows(ctx context.Context, tx TxLike, sc scope, table, timeCol, timeType, kindCol string, hashes []string, times []time.Time, kinds []string, lo, hi time.Time) (map[rowKey][]Row, error) {
	out := map[rowKey][]Row{}
	if len(hashes) == 0 {
		return out, nil
	}
	args := []any{sc.tenant, sc.user, hashes, times, lo, hi}
	q := fmt.Sprintf(`SELECT t.* FROM %[1]s t
		WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[2]s BETWEEN $5 AND $6
		  AND (t.content_hash, t.%[2]s) IN (SELECT * FROM unnest($3::text[], $4::%[3]s[]))`, qualified(table), timeCol, timeType)
	if kindCol != "" {
		args = append(args, kinds)
		q = fmt.Sprintf(`SELECT t.* FROM %[1]s t
			JOIN (SELECT DISTINCT * FROM unnest($7::text[], $3::text[], $4::%[3]s[])) AS k(kind, hash, at)
			  ON t.%[4]s = k.kind AND t.%[2]s = k.at AND t.content_hash = k.hash
			WHERE t.tenant_id = $1 AND t.user_id = $2 AND t.%[2]s BETWEEN $5 AND $6`,
			qualified(table), timeCol, timeType, pgx.Identifier{kindCol}.Sanitize())
	}
	rows, err := collectRows(openRows(ctx, tx, sc.uv, table, q, args))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		h, _ := r["content_hash"].(string)
		at, _ := r[timeCol].(time.Time)
		out[keyOf(h, at)] = append(out[keyOf(h, at)], r)
	}
	return out, nil
}

// rewriteRow writes back the sensitive columns of a stored row Go changed
// (r holds every sealed column of the table, opened): sealed again with keys,
// as clear columns without. The row is found by its id. With keys, a row
// still clear is converted (convertRow): its content_hash is keyed too.
func rewriteRow(ctx context.Context, tx TxLike, sc scope, table string, r Row) error {
	if sc.uv != nil && r["sealed"] == nil {
		return convertRow(ctx, tx, sc, table, r, sc.hash(r.text("content_hash")))
	}
	t := vault.Schema[table]
	vals, err := sealedValues(t, r)
	if err != nil {
		return err
	}
	args := []any{sc.tenant, sc.user, r["id"]}
	var set []string
	if sc.uv == nil {
		for i, c := range t.Sealed {
			args = append(args, vals[i])
			set = append(set, fmt.Sprintf("%s = $%d", c.Name, len(args)))
		}
	} else {
		id, err := rowIdentity(t, func(c string) any { return r[c] })
		if err != nil {
			return err
		}
		sealed, err := sc.uv.SealRow(t, id, vals)
		if err != nil {
			return fmt.Errorf("seal %s row: %w", table, err)
		}
		args = append(args, sealed)
		set = append(set, fmt.Sprintf("sealed = $%d", len(args)))
		if t.MAC {
			args = append(args, sc.uv.ContentMAC(t, vals))
			set = append(set, fmt.Sprintf("content_mac = $%d", len(args)))
		}
		for _, c := range t.Sealed {
			set = append(set, c.Name+" = NULL")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE `+qualified(table)+` SET `+strings.Join(set, ", ")+`
		WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, args...); err != nil {
		return fmt.Errorf("rewrite %s row: %w", table, err)
	}
	return nil
}

// sealedValues are the sealed columns of r in t.Sealed order, as the codec
// takes them.
func sealedValues(t vault.Table, r Row) ([]any, error) {
	vals := make([]any, len(t.Sealed))
	for i, c := range t.Sealed {
		v, err := vaultValue(c.Kind, r[c.Name])
		if err != nil {
			return nil, fmt.Errorf("etl: %s.%s: %w", t.Name, c.Name, err)
		}
		vals[i] = v
	}
	return vals, nil
}

// --- Keyed hashes ---

// hash is the content_hash stored for a record whose plain hash is plain: the
// keyed HMAC of its hex text with keys, plain itself without. An empty hash
// stays empty.
func (s scope) hash(plain string) string {
	if s.uv == nil || plain == "" {
		return plain
	}
	return s.uv.ContentHash([]byte(plain))
}

// sourceKey is the keyed group key of a source, NULL without keys.
func (s scope) sourceKey(source string) any {
	if s.uv == nil {
		return nil
	}
	return s.uv.SourceKey(source)
}

// keyed returns acc with every content hash, and every parent hash a child
// names, replaced by its keyed form (scope.hash). The slices are copies: the
// caller's records keep their plain hashes, so a retried write keys them once.
// Child hashes the writer computes are keyed where it computes them, over the
// keyed parent hash, so a child hashes the same whether it lands with its
// parent or is attached to the stored parent later.
func (s scope) keyed(acc *model.MapResult) *model.MapResult {
	if s.uv == nil {
		return acc
	}
	out := *acc
	out.QuantitySamples = slices.Clone(acc.QuantitySamples)
	for i := range out.QuantitySamples {
		out.QuantitySamples[i].ContentHash = s.hash(out.QuantitySamples[i].ContentHash)
	}
	out.CategorySamples = slices.Clone(acc.CategorySamples)
	for i := range out.CategorySamples {
		out.CategorySamples[i].ContentHash = s.hash(out.CategorySamples[i].ContentHash)
	}
	out.SleepSessions = slices.Clone(acc.SleepSessions)
	for i := range out.SleepSessions {
		out.SleepSessions[i].ContentHash = s.hash(out.SleepSessions[i].ContentHash)
	}
	out.SleepPhases = slices.Clone(acc.SleepPhases)
	for i := range out.SleepPhases {
		out.SleepPhases[i].ParentContentHash = s.hash(out.SleepPhases[i].ParentContentHash)
	}
	out.Workouts = slices.Clone(acc.Workouts)
	for i := range out.Workouts {
		out.Workouts[i].ContentHash = s.hash(out.Workouts[i].ContentHash)
	}
	out.RoutePoints = slices.Clone(acc.RoutePoints)
	for i := range out.RoutePoints {
		out.RoutePoints[i].ParentContentHash = s.hash(out.RoutePoints[i].ParentContentHash)
	}
	out.NutritionLogs = slices.Clone(acc.NutritionLogs)
	for i := range out.NutritionLogs {
		out.NutritionLogs[i].ContentHash = s.hash(out.NutritionLogs[i].ContentHash)
	}
	out.ECGRecordings = slices.Clone(acc.ECGRecordings)
	for i := range out.ECGRecordings {
		out.ECGRecordings[i].ContentHash = s.hash(out.ECGRecordings[i].ContentHash)
	}
	out.ECGSamples = slices.Clone(acc.ECGSamples)
	for i := range out.ECGSamples {
		out.ECGSamples[i].ParentContentHash = s.hash(out.ECGSamples[i].ParentContentHash)
	}
	out.DailySummaries = slices.Clone(acc.DailySummaries)
	for i := range out.DailySummaries {
		out.DailySummaries[i].ContentHash = s.hash(out.DailySummaries[i].ContentHash)
	}
	out.Observations = slices.Clone(acc.Observations)
	for i := range out.Observations {
		out.Observations[i].ContentHash = s.hash(out.Observations[i].ContentHash)
	}
	return &out
}

// --- Promoted columns ---
//
// Each follows the backfill expression of migration 0050, so a row the ETL
// writes and a row the migration filled agree.

// metaValue reads key from a metadata value: the map the writer holds, or the
// JSON of a stored row.
func metaValue(md any, key string) any {
	switch m := md.(type) {
	case map[string]any:
		return m[key]
	case json.RawMessage:
		var v map[string]any
		if json.Unmarshal(m, &v) == nil {
			return v[key]
		}
	}
	return nil
}

// metaText is metadata->>key: a string as it is, another JSON value as its
// JSON text, NULL for a missing key or a JSON null.
func metaText(md any, key string) any {
	switch v := metaValue(md, key).(type) {
	case nil:
		return nil
	case string:
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return string(b)
	}
}

// metaTrue is coalesce(metadata->>key = 'true', false).
func metaTrue(md any, key string) bool {
	s, _ := metaText(md, key).(string)
	return s == "true"
}

var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// metaDate is the day metadata names under key ("YYYY-MM-DD"), NULL when it
// is not one.
func metaDate(md any, key string) any {
	s, _ := metaText(md, key).(string)
	if !isoDate.MatchString(s) {
		return nil
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}
	return d
}

// textOf is a text value, "" for NULL.
func textOf(v any) string {
	s, _ := deref(v).(string)
	return s
}

// firstNonEmpty is coalesce(nullif(a, ”), nullif(b, ”), ..., ”).
func firstNonEmpty(vals ...any) string {
	for _, v := range vals {
		if s := textOf(v); s != "" {
			return s
		}
	}
	return ""
}

// namesWatch is the ILIKE '%watch%' test of the merged view.
func namesWatch(vals ...any) bool {
	for _, v := range vals {
		if strings.Contains(strings.ToLower(textOf(v)), "watch") {
			return true
		}
	}
	return false
}

var (
	quantityPromoted = []string{"source_key", "worn", "tz", "derived", "synthesized", "summary_date"}
	parentPromoted   = []string{"source_key", "worn"}
	sleepPromoted    = []string{"source_key", "worn", "synthesized"}
	tzPromoted       = []string{"tz"}
	derivedPromoted  = []string{"derived"}
)

func deriveQuantity(sc scope, get func(string) any) []any {
	md := get("metadata")
	return []any{
		sc.sourceKey(firstNonEmpty(get("source_bundle_id"), get("source_name"), get("device_id"))),
		namesWatch(get("product_type"), get("device_model"), get("source_name")),
		metaText(md, MetadataTimezone), metaTrue(md, "derived"), metaTrue(md, "synthesized"),
		metaDate(md, model.MetadataSummaryDate),
	}
}

// parentSource is the stream key of a sleep session or a workout
// (api/read/merge.go parentStream).
func parentSource(get func(string) any) string {
	md := get("metadata")
	return firstNonEmpty(get("device_id"), metaText(md, "sourceBundleId"), metaText(md, "sourceName"))
}

func deriveWorkout(sc scope, get func(string) any) []any {
	return []any{sc.sourceKey(parentSource(get)), namesWatch(metaText(get("metadata"), "sourceName"))}
}

func deriveSleep(sc scope, get func(string) any) []any {
	return append(deriveWorkout(sc, get), metaTrue(get("metadata"), "synthesized"))
}

func deriveTZ(_ scope, get func(string) any) []any {
	return []any{metaText(get("metadata"), MetadataTimezone)}
}

func deriveDerived(_ scope, get func(string) any) []any {
	return []any{metaTrue(get("metadata"), "derived")}
}
