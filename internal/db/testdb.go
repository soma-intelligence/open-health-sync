// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestImage is the TimescaleDB image used by integration tests: the image
// deploy/docker-compose.yml pins, digest included, so tests run what users run.
const TestImage = "timescale/timescaledb:latest-pg17@sha256:b56a3f51991df1f79d0c1a0841cad5c5fa8058fdd6a3ee6886394a6d4c174538"

var (
	containerOnce sync.Once
	containerURL  string
	containerErr  error
	dbSeq         atomic.Int64
)

// TestDB returns the URL of a fresh, fully migrated database. One TimescaleDB
// container is started per test process; each call gets its own database in it.
// OHS_TEST_DATABASE_URL, when set, names a running TimescaleDB server to use
// instead of a container (a machine without Docker); its user needs CREATEDB.
func TestDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	containerOnce.Do(func() {
		if u := os.Getenv("OHS_TEST_DATABASE_URL"); u != "" {
			containerURL = u
			return
		}
		c, err := postgres.Run(ctx, TestImage,
			postgres.WithDatabase("ohs"),
			postgres.WithUsername("ohs"),
			postgres.WithPassword("ohs"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
		)
		if err != nil {
			containerErr = fmt.Errorf("start %s: %w", TestImage, err)
			return
		}
		containerURL, containerErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if containerErr != nil {
		t.Fatalf("test db: %v", containerErr)
	}

	name := fmt.Sprintf("t%d_%d", time.Now().UnixNano(), dbSeq.Add(1))
	conn, err := pgx.Connect(ctx, containerURL)
	if err != nil {
		t.Fatalf("test db: connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("test db: create database: %v", err)
	}

	u, err := url.Parse(containerURL)
	if err != nil {
		t.Fatalf("test db: parse url: %v", err)
	}
	u.Path = "/" + name
	dbURL := u.String()

	if err := Migrate(dbURL, "up"); err != nil {
		t.Fatalf("test db: %v", err)
	}
	return dbURL
}
