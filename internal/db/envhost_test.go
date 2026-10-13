// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The Render Blueprint (render.yaml) cannot build a URL around a generated
// password, so it sets OHS_DATABASE_URL without host or password and passes
// them in PGHOST and PGPASSWORD. Both the pool and the migrator must pick
// them up.
func TestURLWithoutHostReadsPGEnv(t *testing.T) {
	t.Setenv("PGHOST", "ohs-db-abc1")
	t.Setenv("PGPASSWORD", "a+b/c=d@e:f")
	t.Setenv("PGPORT", "")
	t.Setenv("PGUSER", "")
	t.Setenv("PGDATABASE", "")
	const dsn = "postgres://ohs@/ohs?sslmode=disable&pool_max_conns=20"

	pool, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, c *pgx.ConnConfig) {
		t.Helper()
		if c.Host != "ohs-db-abc1" || c.Port != 5432 || c.User != "ohs" ||
			c.Password != "a+b/c=d@e:f" || c.Database != "ohs" {
			t.Errorf("%s: host=%q port=%d user=%q db=%q password set=%v",
				name, c.Host, c.Port, c.User, c.Database, c.Password != "")
		}
	}
	check("pool", pool.ConnConfig)
	if pool.MaxConns != 20 {
		t.Errorf("pool: MaxConns = %d, want 20", pool.MaxConns)
	}

	// What the golang-migrate pgx/v5 driver does with the URL newMigrator
	// hands it: parse, set the scheme back to postgres, connect.
	u, err := url.Parse(pgx5URL(ConnURL(dsn)))
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "postgres"
	mc, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	check("migrate", mc)
}
