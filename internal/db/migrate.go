// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func newMigrator(url string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("db: migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, pgx5URL(ConnURL(url)))
	if err != nil {
		return nil, fmt.Errorf("db: migrator: %w", err)
	}
	return m, nil
}

// pgx5URL rewrites a postgres:// or postgresql:// URL to the scheme the
// golang-migrate pgx/v5 driver registers.
func pgx5URL(url string) string {
	for _, p := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(url, p) {
			return "pgx5://" + strings.TrimPrefix(url, p)
		}
	}
	return url
}

// Migrate applies every pending migration ("up") or reverts all of them ("down").
// Having nothing to do is not an error.
func Migrate(url string, direction string) (err error) {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("db: unknown migration direction %q (want up or down)", direction)
	}
	m, err := newMigrator(url)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if err == nil {
			err = errors.Join(srcErr, dbErr)
		}
	}()
	if direction == "up" {
		err = m.Up()
	} else {
		err = m.Down()
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: migrate %s: %w", direction, err)
	}
	return nil
}

// LatestVersion reports the highest migration version embedded in this
// binary: the schema version `ohs migrate up` leaves a database at. It needs no
// database. scripts/restore.sh uses it to refuse a backup taken by a newer
// release.
func LatestVersion() (uint, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return 0, fmt.Errorf("db: migration source: %w", err)
	}
	defer src.Close()
	v, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("db: first migration: %w", err)
	}
	for {
		next, err := src.Next(v)
		if errors.Is(err, fs.ErrNotExist) {
			return v, nil
		}
		if err != nil {
			return 0, fmt.Errorf("db: next migration after %d: %w", v, err)
		}
		v = next
	}
}

// Version reports the applied migration version. A database with no migration
// applied returns 0, false, nil; one whose first migration was being reverted
// when it stopped returns 0, true, nil.
func Version(url string) (version uint, dirty bool, err error) {
	m, err := newMigrator(url)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if err == nil {
			err = errors.Join(srcErr, dbErr)
		}
	}()
	version, dirty, err = m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		// No version is also what a failed down of the first migration
		// leaves, with the dirty mark, which golang-migrate does not report.
		st, err := ReadSchemaState(context.Background(), url)
		return 0, st.Dirty, err
	}
	if err != nil {
		return 0, false, fmt.Errorf("db: version: %w", err)
	}
	return version, dirty, nil
}

// Force sets the recorded schema version to version and clears the dirty
// flag, without running any migration. It is the way out of a migration that
// was killed half way: the schema is then marked dirty and neither up nor
// down runs until the mark is cleared. It does nothing else: a schema that
// is not dirty is refused, since a lower version would make the next up run
// migrations again and a higher one would skip some.
//
// golang-migrate marks dirty the version a migration leads to, before it
// runs it. An up to N that fails leaves N dirty: the schema is at the
// version before N, or at N once the migration was finished by hand. A down
// of N that fails leaves the version before N dirty (no version when N is
// the first): the schema is at that version once the revert was finished by
// hand, or back at N. So with D dirty, version must be D or one of its two
// neighbours among the migrations this binary embeds (0, no migration
// applied, below the first); the caller is responsible for the schema
// really being at that version.
func Force(url string, version uint) (err error) {
	if version != 0 {
		known, err := embedsVersion(version)
		if err != nil {
			return err
		}
		if !known {
			return fmt.Errorf("db: force: this binary has no migration %d (see `ohs migrate latest`)", version)
		}
	}
	st, err := ReadSchemaState(context.Background(), url)
	if err != nil {
		return err
	}
	if !st.Dirty {
		return fmt.Errorf("db: force: the schema is not dirty (version %d): force only clears the mark of a migration "+
			"that stopped half way; `ohs migrate up` applies what is pending", st.Database)
	}
	versions, err := embeddedVersions()
	if err != nil {
		return err
	}
	allowed, err := forceTargets(st.Database, versions)
	if err != nil {
		return err
	}
	if !slices.Contains(allowed, version) {
		list := make([]string, len(allowed))
		for i, v := range allowed {
			list[i] = strconv.FormatUint(uint64(v), 10)
		}
		return fmt.Errorf("db: force: the schema is dirty at version %d: only %s can be recorded, not %d "+
			"(see \"Recover from a failed migration\" in docs/self-hosting.md)",
			st.Database, strings.Join(list, ", "), version)
	}
	m, err := newMigrator(url)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if err == nil {
			err = errors.Join(srcErr, dbErr)
		}
	}()
	v := int(version)
	if version == 0 {
		v = -1 // golang-migrate's "no version"
	}
	if err := m.Force(v); err != nil {
		return fmt.Errorf("db: force %d: %w", version, err)
	}
	return nil
}

// forceTargets lists the versions a schema dirty at version dirty may be
// forced to: dirty itself and its neighbours in versions (the migrations of
// the binary, ascending), 0 standing for no migration applied.
func forceTargets(dirty uint, versions []uint) ([]uint, error) {
	i, found := slices.BinarySearch(versions, dirty)
	if dirty != 0 && !found {
		return nil, fmt.Errorf("db: force: the schema is dirty at version %d, which this binary has no migration for: "+
			"run the release that migrated it", dirty)
	}
	var out []uint
	switch {
	case dirty == 0:
	case i == 0:
		out = append(out, 0)
	default:
		out = append(out, versions[i-1])
	}
	out = append(out, dirty)
	if next := i + 1; dirty == 0 && len(versions) > 0 {
		out = append(out, versions[0])
	} else if dirty != 0 && next < len(versions) {
		out = append(out, versions[next])
	}
	return out, nil
}

// embeddedVersions lists the migrations of this binary, ascending.
func embeddedVersions() ([]uint, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("db: migration source: %w", err)
	}
	defer src.Close()
	v, err := src.First()
	if err != nil {
		return nil, fmt.Errorf("db: first migration: %w", err)
	}
	out := []uint{v}
	for {
		next, err := src.Next(v)
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("db: next migration after %d: %w", v, err)
		}
		out = append(out, next)
		v = next
	}
}

// embedsVersion reports whether an up migration numbered version is embedded.
func embedsVersion(version uint) (bool, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return false, fmt.Errorf("db: migration source: %w", err)
	}
	defer src.Close()
	r, _, err := src.ReadUp(version)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: read migration %d: %w", version, err)
	}
	return true, r.Close()
}

// Schema mismatches CheckSchema reports, for errors.Is.
var (
	// ErrSchemaBehind: the database lacks migrations this binary needs.
	ErrSchemaBehind = errors.New("database schema is behind this binary")
	// ErrSchemaAhead: the database was migrated by a newer release.
	ErrSchemaAhead = errors.New("database schema is ahead of this binary")
	// ErrSchemaDirty: a migration stopped half way.
	ErrSchemaDirty = errors.New("database schema is dirty")
)

// SchemaState is the schema version of a database next to the one a binary
// migrates to.
type SchemaState struct {
	Database uint // version recorded in the database, 0 when none
	Dirty    bool // a migration stopped half way
	Binary   uint // LatestVersion of this binary
}

// Err is nil when the database is exactly at the binary's version and clean.
// Otherwise it wraps ErrSchemaDirty, ErrSchemaBehind or ErrSchemaAhead in a
// message that names both versions and the command to run.
func (s SchemaState) Err() error {
	switch {
	case s.Dirty:
		return fmt.Errorf("%w: a migration to version %d stopped half way (this binary migrates to version %d): "+
			"see \"Recover from a failed migration\" in docs/self-hosting.md, then run "+
			"`ohs migrate force <version> --yes` and `ohs migrate up`", ErrSchemaDirty, s.Database, s.Binary)
	case s.Database < s.Binary:
		return fmt.Errorf("%w: the database is at schema version %d, this binary needs version %d: "+
			"run `ohs migrate up` (or start with `ohs up`, which migrates first)", ErrSchemaBehind, s.Database, s.Binary)
	case s.Database > s.Binary:
		return fmt.Errorf("%w: the database is at schema version %d, this binary only knows version %d: "+
			"a newer release migrated it. Run that release (its `ohs migrate latest` prints %d), or restore "+
			"a backup taken before the upgrade; `ohs migrate version` prints the database version",
			ErrSchemaAhead, s.Database, s.Binary, s.Database)
	}
	return nil
}

// ReadSchemaState reads the schema version of the database at url with one
// plain query: unlike Version it creates nothing and takes no migration
// lock, so it is safe on every start and while a migration runs elsewhere.
func ReadSchemaState(ctx context.Context, url string) (SchemaState, error) {
	latest, err := LatestVersion()
	if err != nil {
		return SchemaState{}, err
	}
	st := SchemaState{Binary: latest}
	// One connection and one query: bounded like a pool connection, so a
	// silent database host fails the start instead of hanging it.
	ctx, cancel := context.WithTimeout(ctx, 2*DefaultConnectTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, ConnURL(url))
	if err != nil {
		return st, fmt.Errorf("db: schema version: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var v int64
	err = conn.QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&v, &st.Dirty)
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.As(err, &pgErr) && pgErr.Code == "42P01":
		return st, nil // never migrated
	case err != nil:
		return st, fmt.Errorf("db: schema version: %w", err)
	}
	if v > 0 {
		st.Database = uint(v)
	}
	return st, nil
}

// CheckSchema refuses a database whose schema is not exactly the one this
// binary was built for: see SchemaState.Err. serve calls it before starting
// any role, so a process never runs queries against tables it does not know.
func CheckSchema(ctx context.Context, url string) error {
	st, err := ReadSchemaState(ctx, url)
	if err != nil {
		return err
	}
	return st.Err()
}
