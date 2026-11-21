// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB records the statements a Writer sends.
type fakeDB struct {
	mu    sync.Mutex
	rows  []int // rows per statement
	args  [][]any
	fail  func(rows int) error
	block chan struct{}
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.block != nil {
		<-f.block
	}
	rows := len(args) / eventColumns
	if strings.Count(sql, "$") != len(args) {
		return pgconn.CommandTag{}, errors.New("placeholders and arguments differ")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		if err := f.fail(rows); err != nil {
			return pgconn.CommandTag{}, err
		}
	}
	f.rows = append(f.rows, rows)
	f.args = append(f.args, args)
	return pgconn.CommandTag{}, nil
}

func (f *fakeDB) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.rows {
		n += r
	}
	return n
}

func testEvent() Event { return Event{ActorType: APIKey, Action: DataRead, Status: 200} }

func closeWriter(t *testing.T, w *Writer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWriterFlushesAFullBatch(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db, WriterOptions{Batch: 3, Interval: time.Hour})
	for range 3 {
		if err := w.Enqueue(testEvent()); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.total() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.total() != 3 || len(db.rows) != 1 {
		t.Fatalf("rows per statement = %v, want one statement of 3", db.rows)
	}
	closeWriter(t, w)
}

func TestWriterFlushesOnInterval(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db, WriterOptions{Batch: 100, Interval: 10 * time.Millisecond})
	if err := w.Enqueue(testEvent()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for db.total() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.total() != 1 {
		t.Fatal("the event was not written after the interval")
	}
	closeWriter(t, w)
}

func TestWriterCloseFlushesAndRefuses(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db, WriterOptions{Batch: 4, Interval: time.Hour})
	for range 10 {
		if err := w.Enqueue(testEvent()); err != nil {
			t.Fatal(err)
		}
	}
	closeWriter(t, w)
	if db.total() != 10 || w.Written() != 10 {
		t.Fatalf("written %d (counter %d), want 10", db.total(), w.Written())
	}
	if err := w.Enqueue(testEvent()); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("Enqueue after Close = %v, want ErrWriterClosed", err)
	}
	closeWriter(t, w) // twice is fine
}

// A full queue refuses the event without losing it: the caller writes it
// itself (ErrQueueFull), and nothing is counted dropped.
func TestWriterRefusesWhenFull(t *testing.T) {
	db := &fakeDB{block: make(chan struct{})}
	w := NewWriter(db, WriterOptions{Queue: 2, Batch: 1, Interval: time.Hour})
	// The first event is taken by the loop, which then waits in Exec; two
	// more fill the queue.
	full := 0
	for range 20 {
		if err := w.Enqueue(testEvent()); errors.Is(err, ErrQueueFull) {
			full++
		}
		time.Sleep(time.Millisecond)
	}
	if full < 17 {
		t.Fatalf("refused %d, want at least 17", full)
	}
	close(db.block)
	closeWriter(t, w)
	if db.total() != 20-full || w.Dropped() != 0 {
		t.Fatalf("written %d, dropped %d, want %d and 0", db.total(), w.Dropped(), 20-full)
	}
}

// A batch the database does not take (an outage, a failover) is tried
// again before its events are counted dropped.
func TestWriterRetriesAFailedBatch(t *testing.T) {
	calls := 0
	db := &fakeDB{fail: func(int) error {
		if calls++; calls <= 4 { // the batch, then each of its 3 events alone
			return errors.New("down")
		}
		return nil
	}}
	var lost int
	w := NewWriter(db, WriterOptions{Batch: 3, Interval: time.Hour, Backoff: time.Millisecond,
		OnError: func(_ error, n int) { lost += n }})
	for range 3 {
		if err := w.Enqueue(testEvent()); err != nil {
			t.Fatal(err)
		}
	}
	closeWriter(t, w)
	if db.total() != 3 || w.Written() != 3 || w.Dropped() != 0 || lost != 0 {
		t.Fatalf("written %d (counter %d), dropped %d, lost %d", db.total(), w.Written(), w.Dropped(), lost)
	}
	if len(db.rows) != 1 || db.rows[0] != 3 {
		t.Fatalf("rows per statement = %v, want the batch written whole on the second attempt", db.rows)
	}

	// Still down after every attempt: dropped, counted and reported once.
	calls = 0
	db = &fakeDB{fail: func(int) error { calls++; return errors.New("down") }}
	w = NewWriter(db, WriterOptions{Batch: 5, Interval: time.Hour, Attempts: 3, Backoff: time.Millisecond,
		OnError: func(_ error, n int) { lost += n }})
	for range 2 {
		_ = w.Enqueue(testEvent())
	}
	closeWriter(t, w)
	if w.Dropped() != 2 || lost != 2 || w.Written() != 0 {
		t.Fatalf("dropped %d, lost %d, written %d, want 2, 2 and 0", w.Dropped(), lost, w.Written())
	}
	if calls != 3*3 {
		t.Fatalf("%d statements, want 3 attempts of the batch and of its 2 events", calls)
	}
}

func TestWriterKeepsTheTimeOfTheEvent(t *testing.T) {
	db := &fakeDB{}
	w := NewWriter(db, WriterOptions{Interval: time.Hour})
	before := time.Now()
	if err := w.Enqueue(testEvent()); err != nil {
		t.Fatal(err)
	}
	closeWriter(t, w)
	at, ok := db.args[0][eventColumns-2].(*time.Time)
	if !ok || at == nil || at.Before(before) || at.After(time.Now()) {
		t.Fatalf("at = %v, want the time the event was queued", db.args[0][eventColumns-2])
	}
}

// A batch the database refuses is retried event by event, so one bad event
// does not lose the others.
func TestWriterRetriesEventsAlone(t *testing.T) {
	var lost int
	db := &fakeDB{fail: func(rows int) error {
		if rows > 1 {
			return errors.New("batch refused")
		}
		return nil
	}}
	w := NewWriter(db, WriterOptions{Batch: 5, Interval: time.Hour, OnError: func(_ error, n int) { lost += n }})
	for range 5 {
		if err := w.Enqueue(testEvent()); err != nil {
			t.Fatal(err)
		}
	}
	closeWriter(t, w)
	if db.total() != 5 || w.Dropped() != 0 || lost != 0 {
		t.Fatalf("written %d, dropped %d, lost %d", db.total(), w.Dropped(), lost)
	}

	// One event the database refuses each time: the others are written.
	bad := 0
	db = &fakeDB{fail: func(rows int) error {
		if rows > 1 {
			return errors.New("batch refused")
		}
		if bad++; bad%3 == 0 {
			return errors.New("event refused")
		}
		return nil
	}}
	w = NewWriter(db, WriterOptions{Batch: 3, Interval: time.Hour, Attempts: 1,
		OnError: func(_ error, n int) { lost += n }})
	for range 3 {
		_ = w.Enqueue(testEvent())
	}
	closeWriter(t, w)
	if db.total() != 2 || w.Written() != 2 || w.Dropped() != 1 || lost != 1 {
		t.Fatalf("written %d (counter %d), dropped %d, lost %d", db.total(), w.Written(), w.Dropped(), lost)
	}
}

func TestBufferedActions(t *testing.T) {
	for _, a := range []string{DataRead, DataWrite, UserRead, ConfigRead, AuthFailed} {
		if !Buffered(a) {
			t.Errorf("%s should be buffered", a)
		}
	}
	for _, a := range []string{KeyCreate, KeyRevoke, TokenCreate, TokenRevoke, UserDelete, UserErased, DataExport,
		ConsentWrite, ShareCreate, ShareRevoke, ShareAccess, TenantCreate, TenantProviderKeys, WebhookWrite,
		ConfigWrite, ConnectionCreate, ConnectionDelete, UserCreate, "something.new"} {
		if Buffered(a) {
			t.Errorf("%s must be written synchronously", a)
		}
	}
}
