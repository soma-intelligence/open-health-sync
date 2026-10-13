// SPDX-License-Identifier: AGPL-3.0-only

// Command migrateto moves a database to one migration version, up or down,
// with the migration files of a directory. The upgrade test
// (test/upgrade/upgrade.sh) uses it to run the down migrations back to the
// level of an older release: `ohs migrate` only goes all the way up or all the
// way down.
//
//	go run ./test/upgrade/migrateto DIR DATABASE_URL VERSION
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5:// driver
	_ "github.com/golang-migrate/migrate/v4/source/file"     // file:// source
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrateto:", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	if len(args) != 3 {
		return errors.New("usage: migrateto DIR DATABASE_URL VERSION")
	}
	dir, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	version, err := strconv.ParseUint(args[2], 10, 32)
	if err != nil || version == 0 {
		return fmt.Errorf("version %q: want a positive migration number", args[2])
	}
	url := args[1]
	for _, p := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(url, p) {
			url = "pgx5://" + strings.TrimPrefix(url, p)
		}
	}
	m, err := migrate.New("file://"+dir, url)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if err == nil {
			err = errors.Join(srcErr, dbErr)
		}
	}()
	if err := m.Migrate(uint(version)); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate to %d: %w", version, err)
	}
	return nil
}
