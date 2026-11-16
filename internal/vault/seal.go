// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soma-intelligence/open-health-sync/internal/audit"
	"github.com/soma-intelligence/open-health-sync/internal/rawstore"
)

// Sealing what an instance stored before the vault (docs/specs/vault.md,
// "Migration of existing instances").
//
// Seal walks the users one by one. For each it converts the rows still stored
// clear, table by table and batch by batch, each batch in a transaction of its
// own under the user's share lock; then the raw objects stored before the
// user had keys; then the hourly buckets of the hours that have none. Every
// step leaves the database valid and looks only at what is still to do, so a
// run stopped anywhere is resumed by running it again.
//
// The rows themselves are converted by a RowSealer, which the ETL implements
// (etl.Sealer): a row is sealed by the code that writes sealed rows, so the
// two cannot drift, and this package does not import the ETL.

// Audit actions of this file and of rotate.go.
const (
	// AuditSealProgress is rows, objects or buckets converted by Seal or
	// Reseal for one user; detail has the counts, never a value.
	AuditSealProgress = "vault.seal_progress"
	// AuditKeyRotated is a root, tenant or user key replaced by a new
	// version; detail has which and the versions, never key material.
	AuditKeyRotated = "vault.key_rotated"
)

// HourlyTable is the table of the sealed hourly buckets: Reseal rewrites its
// rows too, Seal fills it.
const HourlyTable = "quantity_hourly_sealed"

// DefaultSealBatch is how many rows one transaction converts.
const DefaultSealBatch = 1000

// sealProgressEvery is how many converted rows of one user separate two
// AuditSealProgress events.
const sealProgressEvery = 10000

// Cursor is where a batch of a RowSealer stopped, handed back for the next
// one, so each batch starts where the last ended and does not read again what
// is already converted. The zero Cursor starts from the beginning. It only
// saves work: a batch never returns a row that needs nothing.
type Cursor struct {
	At  time.Time
	Key string
}

// SealCount is what one batch of a RowSealer did.
type SealCount struct {
	// Rows is the rows of the table converted (or buckets written), Children
	// the rows of its child table (ChildTable) converted with them,
	// Duplicates the rows removed because the same record was stored twice.
	Rows, Children, Duplicates int
	ChildTable                 string
	// Next starts the batch after this one.
	Next Cursor
}

func (c SealCount) total() int { return c.Rows + c.Children + c.Duplicates }

// RowSealer converts stored rows of one user inside a transaction. Every
// method works on at most limit rows starting at from, and returns a zero
// count when nothing is left from there.
type RowSealer interface {
	// Tables lists the tables SealClear converts, in the order to convert
	// them: parents before their children.
	Tables() []string
	// SealClear seals rows of table stored clear: the content_hash becomes
	// keyed, the promoted columns and content_mac are filled, the sensitive
	// columns set to NULL. A row whose keyed hash an older stored row already
	// carries is the same record stored twice: the newer one is deleted.
	SealClear(ctx context.Context, tx pgx.Tx, uv *UserVault, tenant, user uuid.UUID, table string, from Cursor, limit int) (SealCount, error)
	// Reseal rewrites rows of table (one of Tables, or HourlyTable) sealed
	// under a key version older than uv's newest.
	Reseal(ctx context.Context, tx pgx.Tx, uv *UserVault, tenant, user uuid.UUID, table string, from Cursor, limit int) (SealCount, error)
	// RebuildHours writes the hourly buckets of hours that hold samples and
	// have no bucket.
	RebuildHours(ctx context.Context, tx pgx.Tx, uv *UserVault, tenant, user uuid.UUID, from Cursor, limit int) (SealCount, error)
}

// SealOptions configure Seal and Reseal.
type SealOptions struct {
	// Rows converts the rows: etl.Sealer{}. Required unless DryRun.
	Rows RowSealer
	// Batch is the rows of one transaction (0: DefaultSealBatch).
	Batch int
	// Users limits the run to these users; empty means every active user.
	Users []uuid.UUID
	// DryRun changes nothing and reports what a run would have to convert.
	DryRun bool
	// Progress, when set, is called after every batch with the running
	// report (the command prints it).
	Progress func(SealReport)
}

// SealReport is what a Seal or Reseal run did (or, for a dry run, would have
// to do).
type SealReport struct {
	// Users is the users something was converted for.
	Users int
	// Rows counts the converted rows by table.
	Rows map[string]int
	// Duplicates is the rows removed because the record was stored twice.
	Duplicates int
	// RawObjects is the raw objects rewritten sealed; RawMissing the
	// registered ones the raw store no longer holds, left as they are.
	RawObjects, RawMissing int
	// Hours is the hourly buckets hours rebuilt.
	Hours int
	// KeysRetired is the old user key versions Reseal retired.
	KeysRetired int
}

func (r *SealReport) add(table string, c SealCount) {
	if r.Rows == nil {
		r.Rows = map[string]int{}
	}
	if c.Rows > 0 {
		r.Rows[table] += c.Rows
	}
	if c.Children > 0 {
		r.Rows[c.ChildTable] += c.Children
	}
	r.Duplicates += c.Duplicates
}

// TotalRows is the converted rows of every table.
func (r SealReport) TotalRows() int {
	n := 0
	for _, c := range r.Rows {
		n += c
	}
	return n
}

// StatusReport is what is still stored clear.
type StatusReport struct {
	// Tables counts the clear rows by table, every sealed table listed.
	Tables map[string]int64
	// RawObjects is the raw objects registered before their user had keys
	// and not rewritten sealed yet.
	RawObjects int64
}

// ClearRows is the clear rows of every table.
func (s StatusReport) ClearRows() int64 {
	var n int64
	for _, c := range s.Tables {
		n += c
	}
	return n
}

// Sealed reports whether nothing is left in clear.
func (s StatusReport) Sealed() bool { return s.ClearRows() == 0 && s.RawObjects == 0 }

// statusTables are the tables Status counts: every sealed table that can
// hold a clear row. The hourly table cannot, its sealed column is NOT NULL.
func statusTables() []string {
	out := make([]string, 0, len(Schema))
	for name := range Schema {
		if name != HourlyTable {
			out = append(out, name)
		}
	}
	return out
}

// unsealedRaw selects the raw objects that are not sealed: registered before
// their user's first key (a user with keys gets nothing written in clear) and
// not rewritten by Seal, which marks the name of what it writes (sealedKey).
// It is the body of a query over ohs_sync.raw_objects r.
const unsealedRaw = `r.key !~ '/v[0-9]+\.[^/]*$'
	AND r.created_at < coalesce((SELECT min(k.created_at) FROM ohs_core.vault_keys k
	    WHERE k.tenant_id = r.tenant_id AND k.user_id = r.user_id), 'infinity')`

// Status counts what is still stored clear: the rows of each sealed table
// with no sealed value, every row of the per-point tables (PerPoint), which
// the seal command moves into series blobs, and the raw objects not sealed. It reads only; any
// process may run it.
func Status(ctx context.Context, pool *pgxpool.Pool) (StatusReport, error) {
	s := StatusReport{Tables: map[string]int64{}}
	for _, table := range statusTables() {
		var n int64
		left := ` WHERE sealed IS NULL`
		if slices.Contains(PerPoint, table) {
			// A per-point row is left to convert whether or not it is sealed:
			// the seal command moves it into its series blob.
			left = ``
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{"ohs_data", table}.Sanitize()+left).Scan(&n); err != nil {
			return StatusReport{}, fmt.Errorf("vault: status of %s: %w", table, err)
		}
		s.Tables[table] = n
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ohs_sync.raw_objects r WHERE `+unsealedRaw).Scan(&s.RawObjects); err != nil {
		return StatusReport{}, fmt.Errorf("vault: status of the raw objects: %w", err)
	}
	return s, nil
}

// sealUser is one user of a run.
type sealUser struct{ tenant, user uuid.UUID }

// sealUsers lists the active users of a run, in a fixed order.
func sealUsers(ctx context.Context, pool *pgxpool.Pool, only []uuid.UUID, where string) ([]sealUser, error) {
	q := `SELECT u.tenant_id, u.id FROM ohs_core.end_users u WHERE u.status = 'active'`
	var args []any
	if len(only) > 0 {
		q += ` AND u.id = ANY($1)`
		args = append(args, only)
	}
	if where != "" {
		q += ` AND ` + where
	}
	rows, err := pool.Query(ctx, q+` ORDER BY u.tenant_id, u.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("vault: list users: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (sealUser, error) {
		var u sealUser
		return u, r.Scan(&u.tenant, &u.user)
	})
}

// errUserGone ends the work on a user who was deleted, or whose deletion
// started, while the run was on them: what is left of theirs goes with them.
var errUserGone = errors.New("vault: the user is gone")

// inUserTx runs fn in a transaction that holds the user's share lock, as an
// ETL job does: a deletion waits for it, and it never writes for a user whose
// deletion started.
func inUserTx(ctx context.Context, pool *pgxpool.Pool, u sealUser, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("vault: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM ohs_core.end_users WHERE tenant_id = $1 AND id = $2 FOR SHARE`,
		u.tenant, u.user).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return errUserGone
	}
	if err != nil {
		return fmt.Errorf("vault: lock user: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("vault: commit: %w", err)
	}
	return nil
}

// sealRun is the state of one Seal or Reseal run.
type sealRun struct {
	v      *Vault
	pool   *pgxpool.Pool
	raw    rawstore.Store
	o      SealOptions
	report SealReport
	action string // "seal" or "reseal", in the audit detail
}

func newSealRun(v *Vault, pool *pgxpool.Pool, raw rawstore.Store, o SealOptions, action string) (*sealRun, error) {
	if o.Batch <= 0 {
		o.Batch = DefaultSealBatch
	}
	if !o.DryRun {
		if o.Rows == nil {
			return nil, errors.New("vault: SealOptions.Rows is required")
		}
		if !v.canOpen {
			return nil, ErrCannotOpen
		}
	}
	return &sealRun{v: v, pool: pool, raw: raw, o: o, report: SealReport{Rows: map[string]int{}}, action: action}, nil
}

func (s *sealRun) progress() {
	if s.o.Progress != nil {
		s.o.Progress(s.report)
	}
}

// audit records what the run converted for u since the last event.
func (s *sealRun) audit(ctx context.Context, u sealUser, detail map[string]any) error {
	detail["run"] = s.action
	err := audit.Record(ctx, s.pool, audit.Event{Tenant: u.tenant, User: &u.user, ActorType: audit.System, ActorID: "vault",
		Action: AuditSealProgress, Detail: detail})
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	return nil
}

// userProgress counts what a run converts for one user and records it every
// sealProgressEvery rows and at the end.
type userProgress struct {
	rows, objects, hours, duplicates int
	reported                         int
}

func (p *userProgress) detail(done bool) map[string]any {
	return map[string]any{"rows": p.rows, "raw_objects": p.objects, "hours": p.hours, "duplicates": p.duplicates, "done": done}
}

func (p *userProgress) any() bool { return p.rows+p.objects+p.hours+p.duplicates > 0 }

// tables runs step over every table for u, batch after batch, until a batch
// converts nothing.
func (s *sealRun) tables(ctx context.Context, u sealUser, uv *UserVault, p *userProgress, tables []string,
	step func(tx pgx.Tx, table string, from Cursor) (SealCount, error)) error {
	for _, table := range tables {
		var from Cursor
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var c SealCount
			err := inUserTx(ctx, s.pool, u, func(tx pgx.Tx) (err error) {
				c, err = step(tx, table, from)
				return err
			})
			if err != nil {
				return err
			}
			if c.total() == 0 {
				break
			}
			from = c.Next
			s.report.add(table, c)
			p.rows += c.Rows + c.Children
			p.duplicates += c.Duplicates
			if p.rows-p.reported >= sealProgressEvery {
				p.reported = p.rows
				if err := s.audit(ctx, u, p.detail(false)); err != nil {
					return err
				}
			}
			s.progress()
		}
	}
	return nil
}

// userVault returns the keys of u, giving them keys first when they have
// none (the instance was upgraded and no api or etl process started since).
func (s *sealRun) userVault(ctx context.Context, u sealUser) (*UserVault, error) {
	uv, err := s.v.ForUser(ctx, u.tenant, u.user)
	if !errors.Is(err, ErrNoKeys) {
		return uv, err
	}
	if err := inUserTx(ctx, s.pool, u, func(tx pgx.Tx) error { return s.v.EnsureUser(ctx, tx, u.tenant, u.user) }); err != nil {
		return nil, err
	}
	s.v.evictUser(userRef{u.tenant, u.user})
	return s.v.ForUser(ctx, u.tenant, u.user)
}

// Seal converts what is stored clear: rows, raw objects, and the hourly
// buckets of the converted samples. It stops at the first error or when ctx
// ends, and returns what it did until then; running it again goes on from
// there. raw may be nil, and the raw objects are then left for another run.
func Seal(ctx context.Context, v *Vault, pool *pgxpool.Pool, raw rawstore.Store, o SealOptions) (SealReport, error) {
	s, err := newSealRun(v, pool, raw, o, "seal")
	if err != nil {
		return SealReport{}, err
	}
	if o.DryRun {
		st, err := Status(ctx, pool)
		if err != nil {
			return SealReport{}, err
		}
		for table, n := range st.Tables {
			if n > 0 {
				s.report.Rows[table] = int(n)
			}
		}
		s.report.RawObjects = int(st.RawObjects)
		return s.report, nil
	}
	users, err := sealUsers(ctx, pool, o.Users, "")
	if err != nil {
		return SealReport{}, err
	}
	for _, u := range users {
		err := s.sealUser(ctx, u)
		if errors.Is(err, errUserGone) {
			continue
		}
		if err != nil {
			return s.report, err
		}
	}
	// The gauge of this process; a serving process refreshes its own.
	if st, err := Status(ctx, pool); err == nil {
		v.metrics.SetClearRows(st)
	}
	return s.report, nil
}

func (s *sealRun) sealUser(ctx context.Context, u sealUser) error {
	uv, err := s.userVault(ctx, u)
	if err != nil {
		return err
	}
	p := &userProgress{}
	// The progress made is recorded even when the run stops on an error.
	defer func() {
		if p.any() {
			s.report.Users++
			_ = s.audit(context.WithoutCancel(ctx), u, p.detail(true))
		}
	}()
	err = s.tables(ctx, u, uv, p, s.o.Rows.Tables(), func(tx pgx.Tx, table string, from Cursor) (SealCount, error) {
		return s.o.Rows.SealClear(ctx, tx, uv, u.tenant, u.user, table, from, s.o.Batch)
	})
	if err != nil {
		return err
	}
	if s.raw != nil {
		if err := s.sealRawObjects(ctx, u, p); err != nil {
			return err
		}
	}
	return s.hours(ctx, u, uv, p)
}

// hours rebuilds the buckets of the hours of u that have none.
func (s *sealRun) hours(ctx context.Context, u sealUser, uv *UserVault, p *userProgress) error {
	var from Cursor
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var c SealCount
		err := inUserTx(ctx, s.pool, u, func(tx pgx.Tx) (err error) {
			c, err = s.o.Rows.RebuildHours(ctx, tx, uv, u.tenant, u.user, from, s.o.Batch)
			return err
		})
		if err != nil {
			return err
		}
		if c.Rows == 0 {
			return nil
		}
		from = c.Next
		s.report.Hours += c.Rows
		p.hours += c.Rows
		s.progress()
	}
}

// --- Raw objects ---

// sealedName matches the base name of an object Seal or Reseal wrote: "v",
// the user key version it is sealed to, a dot, then the name it had.
var sealedName = regexp.MustCompile(`^v([0-9]+)\.`)

// sealedKey is the key of the object that replaces key, sealed to version: in
// the same folder, its base name marked. The mark goes in front because the
// raw store deletes by prefix: removing the old object must not take the new
// one, which a suffix would allow.
func sealedKey(key string, version uint32) string {
	dir, base := path.Split(key)
	base = sealedName.ReplaceAllString(base, "")
	return dir + "v" + strconv.FormatUint(uint64(version), 10) + "." + base
}

// sealedKeyVersion is the key version in the name of an object Seal or
// Reseal wrote, and false for any other object.
func sealedKeyVersion(key string) (uint32, bool) {
	m := sealedName.FindStringSubmatch(path.Base(key))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseUint(m[1], 10, 32)
	return uint32(n), err == nil
}

// rawRow is one registered raw object.
type rawRow struct {
	id  int64
	key string
}

// rawRows lists registered raw objects of u after id, in id order.
func (s *sealRun) rawRows(ctx context.Context, u sealUser, after int64, where string) ([]rawRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.id, r.key FROM ohs_sync.raw_objects r
		WHERE r.tenant_id = $1 AND r.user_id = $2 AND r.id > $3 AND `+where+` ORDER BY r.id LIMIT $4`,
		u.tenant, u.user, after, s.o.Batch)
	if err != nil {
		return nil, fmt.Errorf("vault: list raw objects: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (rawRow, error) {
		var x rawRow
		return x, r.Scan(&x.id, &x.key)
	})
}

// sealRawObjects rewrites sealed the raw objects of u stored before they had
// keys.
func (s *sealRun) sealRawObjects(ctx context.Context, u sealUser, p *userProgress) error {
	pub, version, err := s.v.PublicKey(ctx, s.pool, u.tenant, u.user)
	if err != nil {
		return err
	}
	var after int64
	for {
		rows, err := s.rawRows(ctx, u, after, unsealedRaw)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			after = r.id
			done, err := s.rewriteRaw(ctx, u, r, pub, version, func(stored []byte) ([]byte, error) {
				// What is stored is sealed as it is: the reader finds the
				// gzip or plain bytes under the seal as it found them bare.
				if _, sealed, _ := ReadObjectHeader(bytes.NewReader(stored)); sealed {
					return nil, nil
				}
				return stored, nil
			})
			if err != nil {
				return fmt.Errorf("vault: seal raw object %s: %w", r.key, err)
			}
			if done {
				s.report.RawObjects++
				p.objects++
				s.progress()
			}
		}
	}
}

// rewriteRaw replaces the object of r by one sealed to pub (key version
// version) and reports whether it did. body returns the bytes to seal from
// the stored ones, or nil when the object needs nothing.
//
// Order: the new object is written, then, in one transaction under the
// user's lock, the rows that name the old key are pointed at the new one and
// the old object is deleted before the commit. A stop between the delete and
// the commit leaves rows naming a key that is gone beside a sealed object
// under the new key: the next run finds it (adoptRaw) and only fixes the rows.
// Nothing stored clear outlives a commit.
func (s *sealRun) rewriteRaw(ctx context.Context, u sealUser, r rawRow, pub PublicKey, version uint32,
	body func(stored []byte) ([]byte, error)) (bool, error) {
	newKey := sealedKey(r.key, version)
	stored, err := s.getRaw(ctx, r.key)
	if errors.Is(err, rawstore.ErrNotFound) {
		return s.adoptRaw(ctx, u, r, newKey)
	}
	if err != nil {
		return false, err
	}
	plain, err := body(stored)
	if err != nil || plain == nil {
		return false, err
	}
	digest := RawDigest(pub, plain)
	var out bytes.Buffer
	out.Grow(len(plain) + len(plain)/256 + 4096)
	if err := SealObject(&out, pub, version, digest, bytes.NewReader(plain)); err != nil {
		return false, err
	}
	err = s.raw.Put(ctx, newKey, bytes.NewReader(out.Bytes()))
	if errors.Is(err, rawstore.ErrExists) {
		// Left by a run stopped before its commit: written again.
		if err = s.raw.DeletePrefix(ctx, newKey); err == nil {
			err = s.raw.Put(ctx, newKey, bytes.NewReader(out.Bytes()))
		}
	}
	if err != nil {
		return false, fmt.Errorf("write %s: %w", newKey, err)
	}
	return s.renameRaw(ctx, u, r, newKey, digest, out.Len(), true)
}

func (s *sealRun) getRaw(ctx context.Context, key string) ([]byte, error) {
	rc, err := s.raw.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// adoptRaw handles a registered object the store no longer holds under its
// key. When the sealed object a stopped run wrote is there under newKey, the
// rows are pointed at it. Otherwise the object is lost, which this run did not
// cause and cannot repair: it is counted and left.
func (s *sealRun) adoptRaw(ctx context.Context, u sealUser, r rawRow, newKey string) (bool, error) {
	stored, err := s.getRaw(ctx, newKey)
	if errors.Is(err, rawstore.ErrNotFound) {
		s.report.RawMissing++
		return false, nil
	}
	if err != nil {
		return false, err
	}
	h, sealed, err := ReadObjectHeader(bytes.NewReader(stored))
	if err != nil || !sealed {
		return false, fmt.Errorf("%s is not a sealed object", newKey)
	}
	return s.renameRaw(ctx, u, r, newKey, h.Digest, len(stored), false)
}

// renameRaw points every row that names the object of r at newKey, and
// deletes the old object before it commits when deleteOld is set.
func (s *sealRun) renameRaw(ctx context.Context, u sealUser, r rawRow, newKey string, digest [32]byte, size int, deleteOld bool) (bool, error) {
	renamed := false
	err := inUserTx(ctx, s.pool, u, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ohs_sync.raw_objects SET key = $3, content_hash = $4, size_bytes = $5
			WHERE id = $1 AND key = $2`, r.id, r.key, newKey, hex.EncodeToString(digest[:]), size)
		if err != nil {
			return fmt.Errorf("update raw object: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Another run renamed it first.
			return nil
		}
		for _, q := range []string{
			`UPDATE ohs_sync.etl_jobs SET key = $4 WHERE tenant_id = $1 AND user_id = $2 AND key = $3`,
			`UPDATE ohs_sync.sync_log SET raw_key = $4 WHERE tenant_id = $1 AND user_id = $2 AND raw_key = $3`,
			`UPDATE ohs_data.failed_records SET raw_key = $4 WHERE tenant_id = $1 AND user_id = $2 AND raw_key = $3`,
		} {
			if _, err := tx.Exec(ctx, q, u.tenant, u.user, r.key, newKey); err != nil {
				return fmt.Errorf("rename raw object: %w", err)
			}
		}
		if deleteOld {
			if err := s.raw.DeletePrefix(ctx, r.key); err != nil {
				return fmt.Errorf("delete %s: %w", r.key, err)
			}
		}
		renamed = true
		return nil
	})
	return renamed, err
}

// clearRowsEvery is how often a serving process counts again what is still
// stored clear, while something is.
const clearRowsEvery = 6 * time.Hour

// watchClearRows keeps the ohs_vault_clear_rows gauge of a serving process:
// it counts at startup, then again every clearRowsEvery until nothing is left
// in clear. It stops there, because a user with keys gets nothing written
// clear: the count cannot rise again.
func (v *Vault) watchClearRows(ctx context.Context) {
	for {
		st, err := Status(ctx, v.pool)
		if err == nil {
			v.metrics.SetClearRows(st)
			if st.Sealed() {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(clearRowsEvery):
		}
	}
}
