// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/queue"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
	"github.com/soma-intelligence/open-health-sync/internal/vault"
)

// env is one migrated database, one raw store and one tenant with one active
// user, which is what nearly every ETL test needs. Every user it creates has
// keys and every job it runs takes the vault, as in production, so the rows
// are written sealed; e.read() reads them back opened.
type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	rs     rawstore.Store
	v      *vault.Vault
	tenant uuid.UUID
	user   uuid.UUID
	reg    *connector.Registry
	now    func() time.Time // validation clock; nil means time.Now
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool, err := db.Open(context.Background(), db.TestDB(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	rs, err := rawstore.Open("file://" + t.TempDir())
	require.NoError(t, err)
	p, err := vault.OpenProvider(context.Background(), config.Vault{Provider: "local", RootKey: bytes.Repeat([]byte{7}, 32)})
	require.NoError(t, err)
	e := &env{t: t, pool: pool, rs: rs, reg: connector.NewRegistry(), v: vault.New(pool, p, vault.Options{CanOpen: true})}
	e.tenant = e.newTenant()
	e.user = e.newUser(e.tenant)
	return e
}

// keys returns the keys of a user of the env.
func (e *env) keys(tenant, user uuid.UUID) *vault.UserVault {
	e.t.Helper()
	uv, err := e.v.ForUser(context.Background(), tenant, user)
	require.NoError(e.t, err)
	return uv
}

// hash is the stored content_hash of a record of the env's user whose plain
// hash is plain.
func (e *env) hash(plain string) string {
	return scope{uv: e.keys(e.tenant, e.user)}.hash(plain)
}

// read is the database as the tests read it: each query runs in a
// transaction, rolled back after it, on copies of the sealed tables it names
// in which every sealed row has its clear columns filled from its opened
// value (openRows). A query on content_hash compares the stored, keyed hash
// (e.hash).
func (e *env) read() openedDB { return openedDB{e} }

type openedDB struct{ e *env }

// begin opens the view's transaction and returns sql reading the copies.
func (o openedDB) begin(ctx context.Context, sql string) (pgx.Tx, string) {
	o.e.t.Helper()
	tx, err := o.e.pool.Begin(ctx)
	require.NoError(o.e.t, err)
	for name := range vault.Schema {
		if name == "quantity_hourly_sealed" || !namesTable(sql, name) {
			continue
		}
		if err := o.e.unseal(ctx, tx, name); err != nil {
			_ = tx.Rollback(ctx)
			require.NoError(o.e.t, err, "unseal %s", name)
		}
		sql = renameTable(sql, name)
	}
	return tx, sql
}

// tableAt returns the index of the next reference to ohs_data.<table> in sql
// at or after from, or -1.
func tableAt(sql, table string, from int) int {
	q := "ohs_data." + table
	for i := from; ; {
		j := strings.Index(sql[i:], q)
		if j < 0 {
			return -1
		}
		end := i + j + len(q)
		if end == len(sql) || !(sql[end] == '_' || sql[end] >= 'a' && sql[end] <= 'z') {
			return i + j
		}
		i = end
	}
}

// namesTable reports whether sql reads ohs_data.<table>.
func namesTable(sql, table string) bool { return tableAt(sql, table, 0) >= 0 }

// renameTable points every reference to ohs_data.<table> at its opened copy.
func renameTable(sql, table string) string {
	for i := tableAt(sql, table, 0); i >= 0; i = tableAt(sql, table, i) {
		sql = sql[:i] + "_open_" + table + sql[i+len("ohs_data."+table):]
	}
	return sql
}

// unseal copies table into the temporary table _open_<table> and fills, in
// the copy, the clear columns of every sealed row from its opened value.
func (e *env) unseal(ctx context.Context, tx pgx.Tx, table string) error {
	t := vault.Schema[table]
	if _, err := tx.Exec(ctx, `CREATE TEMPORARY TABLE _open_`+table+` ON COMMIT DROP AS SELECT * FROM `+qualified(table)); err != nil {
		return err
	}
	owners, err := pgx.CollectRows(must(tx.Query(ctx, `SELECT DISTINCT tenant_id, user_id FROM _open_`+table+` WHERE sealed IS NOT NULL`)),
		func(r pgx.CollectableRow) ([2]uuid.UUID, error) {
			var o [2]uuid.UUID
			return o, r.Scan(&o[0], &o[1])
		})
	if err != nil || len(owners) == 0 {
		return err
	}
	cols := append([]string{"tenant_id", "user_id"}, t.Identity...)
	for _, c := range t.Sealed {
		cols = append(cols, c.Name)
	}
	var rows [][]any
	for _, o := range owners {
		uv, err := e.v.ForUser(ctx, o[0], o[1])
		if err != nil {
			return err
		}
		opened, err := collectRows(openRows(ctx, tx, uv, table,
			`SELECT * FROM _open_`+table+` WHERE tenant_id = $1 AND user_id = $2 AND sealed IS NOT NULL`, []any{o[0], o[1]}))
		if err != nil {
			return err
		}
		for _, r := range opened {
			row := make([]any, len(cols))
			for i, c := range cols {
				row[i] = r[c]
			}
			rows = append(rows, row)
		}
	}
	temp, _, err := stage(ctx, tx, table, cols, rows)
	if err != nil {
		return err
	}
	set := make([]string, 0, len(t.Sealed))
	for _, c := range t.Sealed {
		set = append(set, c.Name+" = o."+c.Name)
	}
	match := []string{"r.tenant_id = o.tenant_id", "r.user_id = o.user_id", "r.sealed IS NOT NULL"}
	for _, c := range t.Identity {
		id := pgx.Identifier{c}.Sanitize()
		match = append(match, "r."+id+" = o."+id)
	}
	_, err = tx.Exec(ctx, `UPDATE _open_`+table+` r SET `+strings.Join(set, ", ")+` FROM `+temp+` o WHERE `+strings.Join(match, " AND "))
	return err
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// QueryRow runs sql on the opened view.
func (o openedDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx, sql := o.begin(ctx, sql)
	return openedRow{tx.QueryRow(ctx, sql, args...), tx, ctx}
}

// Query runs sql on the opened view; closing the rows ends the view.
func (o openedDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx, sql := o.begin(ctx, sql)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &openedRows{Rows: rows, tx: tx, ctx: ctx}, nil
}

type openedRow struct {
	row pgx.Row
	tx  pgx.Tx
	ctx context.Context
}

func (r openedRow) Scan(dest ...any) error {
	defer func() { _ = r.tx.Rollback(r.ctx) }()
	return r.row.Scan(dest...)
}

type openedRows struct {
	pgx.Rows
	tx  pgx.Tx
	ctx context.Context
}

func (r *openedRows) Close() {
	r.Rows.Close()
	_ = r.tx.Rollback(r.ctx)
}

func (r *openedRows) Next() bool {
	if r.Rows.Next() {
		return true
	}
	r.Close()
	return false
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(e.t, err)
}

func (e *env) newTenant() uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO ohs_core.tenants (name) VALUES ('t') RETURNING id`).Scan(&id))
	return id
}

// newUser creates an active user of tenant, with keys as the API gives them.
func (e *env) newUser(tenant uuid.UUID) uuid.UUID {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	require.NoError(e.t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var id uuid.UUID
	require.NoError(e.t, tx.QueryRow(ctx,
		`INSERT INTO ohs_core.end_users (tenant_id) VALUES ($1) RETURNING id`, tenant).Scan(&id))
	require.NoError(e.t, e.v.EnsureUser(ctx, tx, tenant, id))
	require.NoError(e.t, tx.Commit(ctx))
	return id
}

// count returns the number of rows of table for the env's tenant and user.
func (e *env) count(table string) int {
	e.t.Helper()
	return e.countFor(table, e.tenant, e.user)
}

func (e *env) countFor(table string, tenant, user uuid.UUID) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT count(*) FROM ohs_data.%s WHERE tenant_id = $1 AND user_id = $2`, pgx.Identifier{table}.Sanitize()),
		tenant, user).Scan(&n))
	return n
}

var batchSeq atomic.Int64

// upload stores lines as one gzipped raw object of dataType for (tenant, user,
// provider), registers it in raw_objects and returns the job that processes it.
func (e *env) upload(tenant, user uuid.UUID, provider, dataType string, lines ...string) queue.ETLJob {
	e.t.Helper()
	ctx := context.Background()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, l := range lines {
		_, _ = gz.Write([]byte(l + "\n"))
	}
	require.NoError(e.t, gz.Close())
	body := buf.Bytes()
	batch := fmt.Sprintf("b%d", batchSeq.Add(1))
	key := rawstore.Key(tenant, user, provider, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), batch, 0)
	require.NoError(e.t, e.rs.Put(ctx, key, bytes.NewReader(body)))
	sum := sha256.Sum256(append([]byte(batch), body...))
	_, err := e.pool.Exec(ctx, `
		INSERT INTO ohs_sync.raw_objects (tenant_id, user_id, provider, key, content_hash, size_bytes, batch, part, data_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8)`,
		tenant, user, provider, key, hex.EncodeToString(sum[:]), len(body), batch, dataType)
	require.NoError(e.t, err)
	return queue.ETLJob{TenantID: tenant, UserID: user, Provider: provider, Key: key, Tier: queue.TierStandard}
}

// register adds a fake connector to the env's private registry.
func (e *env) register(slug string, mapFn func(connector.RawRecord) (model.MapResult, error)) {
	e.reg.Register(&fakeConnector{slug: slug, mapFn: mapFn})
}

func (e *env) opts() Options { return Options{Lookup: e.reg.Get, Now: e.now, Vault: e.v} }

// clearOpts are the options of a process with no vault configured.
func (e *env) clearOpts() Options { return Options{Lookup: e.reg.Get, Now: e.now} }

func (e *env) process(job queue.ETLJob) Counts {
	e.t.Helper()
	c, err := ProcessWith(context.Background(), e.pool, e.rs, job, e.opts())
	require.NoError(e.t, err)
	return c
}

// fakeConnector maps with a test supplied function.
type fakeConnector struct {
	slug  string
	mapFn func(connector.RawRecord) (model.MapResult, error)
}

func (f *fakeConnector) Slug() string                  { return f.slug }
func (f *fakeConnector) Enabled(connector.Config) bool { return true }
func (f *fakeConnector) Map(r connector.RawRecord) (model.MapResult, error) {
	return f.mapFn(r)
}

// line marshals one raw NDJSON line.
func line(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// quantityMap is the mapper most tests use: one line with type, value, unit,
// start, end and an optional tz becomes one quantity sample whose content hash
// covers its fields. A line with "fail" set is a mapping error; a line with
// "empty" set maps to nothing.
func quantityMap(r connector.RawRecord) (model.MapResult, error) {
	if msg, ok := r.Fields["fail"].(string); ok {
		return model.MapResult{}, fmt.Errorf("%s", msg)
	}
	if _, ok := r.Fields["empty"]; ok {
		return model.MapResult{}, nil
	}
	start, err := time.Parse(time.RFC3339, r.Fields["start"].(string))
	if err != nil {
		return model.MapResult{}, err
	}
	end := start
	if s, ok := r.Fields["end"].(string); ok {
		if end, err = time.Parse(time.RFC3339, s); err != nil {
			return model.MapResult{}, err
		}
	}
	qt := r.Fields["type"].(string)
	v := r.Fields["value"].(float64)
	unit, _ := r.Fields["unit"].(string)
	md := map[string]any{}
	src, _ := r.Fields["source"].(string)
	if tz, ok := r.Fields["tz"].(string); ok {
		md["timezone"] = tz
	}
	s := model.QuantitySample{
		TenantID:     r.TenantID,
		UserID:       r.UserID,
		Provider:     r.Provider,
		QuantityType: qt,
		Value:        v,
		Unit:         unit,
		StartDate:    start.UTC(),
		EndDate:      end.UTC(),
		SourceName:   src,
		Metadata:     md,
		ContentHash: model.ComputeContentHash(map[string]string{
			"user_id": r.UserID, "type": qt, "start": start.UTC().Format(time.RFC3339Nano),
			"value": model.CanonicalFloat(v), "source": src,
		}),
	}
	if rep, ok := r.Fields["replaceable"].(bool); ok && rep {
		s.Replaceable = true
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id": r.UserID, "type": qt, "start": start.UTC().Format(time.RFC3339Nano),
		})
	}
	return model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

func q(qt string, v float64, unit string, start time.Time) string {
	return line(map[string]any{"type": qt, "value": v, "unit": unit, "start": start.UTC().Format(time.RFC3339)})
}
