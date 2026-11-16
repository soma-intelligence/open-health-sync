// SPDX-License-Identifier: AGPL-3.0-only

// Package db opens the Postgres pool and applies the embedded schema migrations.
package db

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultConnectTimeout bounds opening one connection of the pool when the
// URL sets no connect_timeout.
var DefaultConnectTimeout = 10 * time.Second

// DefaultMaxConns is the pool size when the URL sets no pool_max_conns. The
// pgx default (the larger of 4 and the CPU count) is too small on a small
// machine: the ETL role alone runs 8 jobs at once, next to the API requests
// and the collectors. Every process opens its own pool, so the server's
// max_connections must cover DefaultMaxConns times the number of processes.
const DefaultMaxConns int32 = 20

// Option configures the pool Open creates.
type Option func(*pgxpool.Config)

// WithMaxConns sets the pool size, over the URL and DefaultMaxConns. A value
// below 1 changes nothing.
func WithMaxConns(n int32) Option {
	return func(c *pgxpool.Config) {
		if n > 0 {
			c.MaxConns = n
		}
	}
}

// WithTracer traces the queries of every connection of the pool with t.
func WithTracer(t pgx.QueryTracer) Option {
	return func(c *pgxpool.Config) { c.ConnConfig.Tracer = t }
}

// Open connects to url and checks the connection with a ping.
func Open(ctx context.Context, url string, opts ...Option) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url, opts...)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// poolConfig is the pool configuration of url with the defaults of this
// package and then opts applied.
func poolConfig(url string, opts ...Option) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		// pgx dials with no timeout: a database host that drops packets
		// would hold every new connection, and the requests waiting on the
		// pool, until the kernel gives up minutes later. connect_timeout in
		// the URL overrides it.
		cfg.ConnConfig.ConnectTimeout = DefaultConnectTimeout
	}
	if !setsPoolMaxConns(url) {
		cfg.MaxConns = DefaultMaxConns
	}
	for _, o := range opts {
		o(cfg)
	}
	return cfg, nil
}

// setsPoolMaxConns reports whether the connection string (URL or
// keyword/value form) names pool_max_conns itself.
func setsPoolMaxConns(connString string) bool {
	return strings.Contains(connString, "pool_max_conns=")
}

// ConnURL is dbURL without the pgxpool settings (pool_max_conns and the other
// pool_ parameters). URL form only (postgres://...). Those are read by the pool only; a single connection
// (pgx.Connect, the migrator) would send them to the server as run-time
// parameters, and Postgres refuses unknown ones. A URL that does not parse,
// or a keyword/value string, is returned unchanged.
func ConnURL(dbURL string) string {
	u, err := url.Parse(dbURL)
	if err != nil || u.Scheme == "" || u.RawQuery == "" {
		return dbURL
	}
	q := u.Query()
	changed := false
	for k := range q {
		if strings.HasPrefix(k, "pool_") {
			q.Del(k)
			changed = true
		}
	}
	if !changed {
		return dbURL
	}
	u.RawQuery = q.Encode()
	return u.String()
}
