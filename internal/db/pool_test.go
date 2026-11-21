// SPDX-License-Identifier: AGPL-3.0-only

package db

import "testing"

// The pool size: DefaultMaxConns unless the connection string or an option
// names one.
func TestPoolMaxConns(t *testing.T) {
	const base = "postgres://ohs:ohs@127.0.0.1:5432/ohs?sslmode=disable"
	for _, tc := range []struct {
		name string
		url  string
		opts []Option
		want int32
	}{
		{"default", base, nil, DefaultMaxConns},
		{"url", base + "&pool_max_conns=7", nil, 7},
		{"url above the default", base + "&pool_max_conns=64", nil, 64},
		{"keyword form", "host=127.0.0.1 user=ohs dbname=ohs pool_max_conns=3", nil, 3},
		{"keyword form default", "host=127.0.0.1 user=ohs dbname=ohs", nil, DefaultMaxConns},
		{"option over the url", base + "&pool_max_conns=7", []Option{WithMaxConns(12)}, 12},
		{"zero option keeps the default", base, []Option{WithMaxConns(0)}, DefaultMaxConns},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := poolConfig(tc.url, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConns != tc.want {
				t.Fatalf("MaxConns = %d, want %d", cfg.MaxConns, tc.want)
			}
		})
	}
	if DefaultMaxConns < 20 {
		t.Fatalf("DefaultMaxConns = %d: the ETL role alone runs 8 jobs at once", DefaultMaxConns)
	}
}
