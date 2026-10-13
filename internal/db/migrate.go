// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
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
// applied returns 0, false, nil.
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
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("db: version: %w", err)
	}
	return version, dirty, nil
}
