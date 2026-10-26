// SPDX-License-Identifier: AGPL-3.0-only

package queue

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// flakyPool fails its first fails calls with err, then reports one row
// updated.
type flakyPool struct {
	fails int
	err   error
	calls int
}

func (p *flakyPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	p.calls++
	if p.calls <= p.fails {
		return pgconn.CommandTag{}, p.err
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func noSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := settleSleep
	settleSleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { settleSleep = old })
	return &waits
}

var errDial = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}

// A job settled while Postgres restarts is recorded once the database is
// back, instead of staying running until the visibility timeout (ten
// minutes) reclaims it.
func TestSettleRetriesWhileTheDatabaseIsDown(t *testing.T) {
	waits := noSleep(t)
	for name, settle := range map[string]func(poolExec) error{
		"complete": func(p poolExec) error { return complete(context.Background(), "etl_jobs", p, 1, time.Now()) },
		"fail": func(p poolExec) error {
			return fail(context.Background(), "etl_jobs", p, 1, time.Now(), errors.New("x"))
		},
	} {
		*waits = nil
		p := &flakyPool{fails: 4, err: errDial}
		if err := settle(p); err != nil {
			t.Fatalf("%s: %v, want nil once the database answers", name, err)
		}
		if p.calls != 5 {
			t.Errorf("%s: %d calls, want 5", name, p.calls)
		}
		want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
		if len(*waits) != len(want) {
			t.Fatalf("%s: waits %v, want %v", name, *waits, want)
		}
		for i := range want {
			if (*waits)[i] != want[i] {
				t.Errorf("%s: waits %v, want %v", name, *waits, want)
			}
		}
	}
	// The server's own connection classes are retried too.
	p := &flakyPool{fails: 1, err: &pgconn.PgError{Code: "57P01"}}
	if err := complete(context.Background(), "etl_jobs", p, 1, time.Now()); err != nil || p.calls != 2 {
		t.Errorf("admin shutdown: err %v after %d calls, want nil after 2", err, p.calls)
	}
}

// An SQL error the server returned is not retried: it would fail again.
func TestSettleDoesNotRetrySQLErrors(t *testing.T) {
	noSleep(t)
	p := &flakyPool{fails: 10, err: &pgconn.PgError{Code: "22021"}}
	if err := complete(context.Background(), "etl_jobs", p, 1, time.Now()); err == nil || p.calls != 1 {
		t.Errorf("err %v after %d calls, want the error after 1", err, p.calls)
	}
}

// A cancelled context (shutdown) stops the retries.
func TestSettleStopsWithTheContext(t *testing.T) {
	noSleep(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &flakyPool{fails: 10, err: errDial}
	if err := complete(ctx, "etl_jobs", p, 1, time.Now()); err == nil || p.calls != 1 {
		t.Errorf("err %v after %d calls, want the error after 1", err, p.calls)
	}
}

// The retries stop within the budget, well before the visibility timeout.
func TestSettleGivesUpAfterTheBudget(t *testing.T) {
	var slept time.Duration
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	old, oldNow, oldBudget := settleSleep, settleNow, settleBudget
	settleBudget = 30 * time.Second
	settleNow = func() time.Time { return now }
	settleSleep = func(_ context.Context, d time.Duration) error { slept += d; now = now.Add(d); return nil }
	t.Cleanup(func() { settleSleep, settleNow, settleBudget = old, oldNow, oldBudget })
	p := &flakyPool{fails: 1000, err: errDial}
	if err := complete(context.Background(), "etl_jobs", p, 1, time.Now()); err == nil {
		t.Fatal("want the outage error once the budget is spent")
	}
	if slept > 30*time.Second {
		t.Errorf("slept %s, want at most the 30 s budget", slept)
	}
}
