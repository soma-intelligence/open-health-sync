// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of a Writer.
const (
	DefaultWriterQueue    = 4096
	DefaultWriterBatch    = 200
	DefaultWriterInterval = time.Second
	// DefaultWriterAttempts is how many times a batch is tried before its
	// events are dropped, DefaultWriterBackoff the wait before the second
	// try; each further wait is twice the one before.
	DefaultWriterAttempts = 3
	DefaultWriterBackoff  = 250 * time.Millisecond
	// writerFlushTimeout bounds one try of a batch.
	writerFlushTimeout = 5 * time.Second
)

// ErrQueueFull means the Writer's queue is full: the event was not taken,
// write it with Record instead.
var ErrQueueFull = errors.New("audit: the writer queue is full")

// ErrWriterClosed means the Writer no longer takes events: write the event
// with Record instead.
var ErrWriterClosed = errors.New("audit: the writer is closed")

// WriterOptions tune a Writer. A zero field takes its default.
type WriterOptions struct {
	// Queue is how many events may wait; one more is refused
	// (ErrQueueFull) and left to the caller.
	Queue int
	// Batch is how many events one INSERT writes at most: a full batch is
	// written at once.
	Batch int
	// Interval is the longest an event waits before it is written.
	Interval time.Duration
	// Attempts is how many times a batch is tried, Backoff the wait before
	// the second try (doubled before each further one).
	Attempts int
	Backoff  time.Duration
	// OnError is told about a batch that could not be written after every
	// attempt, with the number of events lost. Nil ignores it; Dropped
	// counts them either way.
	OnError func(err error, lost int)
}

// Writer writes events in batches from a bounded queue, so a request does
// not wait for its audit row: one INSERT per batch, when Batch events wait
// or Interval after the first one. The events of Buffered actions go through
// it; the others are written with Record. Nothing is dropped for lack of
// room: an event over the queue is refused and the caller writes it itself.
// A batch the database does not take is tried again (Attempts), then its
// events are dropped and counted. Close writes what is left. Queues are per
// process: an event queued in a process that is killed (not stopped) is
// lost, about Interval of them while the database answers.
type Writer struct {
	db   Execer
	opts WriterOptions
	ch   chan Event

	mu     sync.RWMutex
	closed bool
	stop   chan struct{}
	done   chan struct{}

	dropped atomic.Int64
	written atomic.Int64
}

// NewWriter starts a Writer on db. Close it when the process stops.
func NewWriter(db Execer, o WriterOptions) *Writer {
	if o.Queue <= 0 {
		o.Queue = DefaultWriterQueue
	}
	if o.Batch <= 0 {
		o.Batch = DefaultWriterBatch
	}
	if o.Interval <= 0 {
		o.Interval = DefaultWriterInterval
	}
	if o.Attempts <= 0 {
		o.Attempts = DefaultWriterAttempts
	}
	if o.Backoff <= 0 {
		o.Backoff = DefaultWriterBackoff
	}
	w := &Writer{db: db, opts: o, ch: make(chan Event, o.Queue), stop: make(chan struct{}), done: make(chan struct{})}
	go w.loop()
	return w
}

// Enqueue queues ev, stamped with the current time unless it has one. It
// never blocks: with the queue full (ErrQueueFull) or after Close
// (ErrWriterClosed) the event is refused, and the caller writes it with
// Record.
func (w *Writer) Enqueue(ev Event) error {
	if ev.Action == "" || ev.ActorType == "" {
		return errors.New("audit: an event needs an action and an actor type")
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return ErrWriterClosed
	}
	select {
	case w.ch <- ev:
		return nil
	default:
		return ErrQueueFull
	}
}

// Dropped is how many queued events were lost: the database did not take
// them after every attempt.
func (w *Writer) Dropped() int64 { return w.dropped.Load() }

// Written is how many queued events were written.
func (w *Writer) Written() int64 { return w.written.Load() }

// Close stops taking events, writes the ones still queued and waits for
// that, at most until ctx is done: the error then says how many events were
// still queued. Closing twice is fine.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.stop)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("audit: writer close: %d events still queued: %w", len(w.ch), ctx.Err())
	}
}

func (w *Writer) loop() {
	defer close(w.done)
	batch := make([]Event, 0, w.opts.Batch)
	timer := time.NewTimer(w.opts.Interval)
	timer.Stop()
	armed := false
	flush := func() {
		if armed {
			timer.Stop()
			armed = false
		}
		if len(batch) > 0 {
			w.write(batch)
			batch = batch[:0]
		}
	}
	for {
		select {
		case ev := <-w.ch:
			batch = append(batch, ev)
			if len(batch) >= w.opts.Batch {
				flush()
			} else if !armed {
				timer.Reset(w.opts.Interval)
				armed = true
			}
		case <-timer.C:
			armed = false
			flush()
		case <-w.stop:
			// Enqueue holds the read lock while it sends and Close took the
			// write lock before closing stop: nothing is sent from here on.
			for {
				select {
				case ev := <-w.ch:
					batch = append(batch, ev)
					if len(batch) >= w.opts.Batch {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// write inserts batch with one statement. When that fails, each event is
// tried alone, so one event the database refuses does not lose the others;
// what is still not written is tried again after a short wait, Attempts
// times in all, and then dropped and counted.
func (w *Writer) write(batch []Event) {
	pending := batch
	var err error
	for attempt, wait := 1, w.opts.Backoff; ; attempt, wait = attempt+1, 2*wait {
		if pending, err = w.try(pending); len(pending) == 0 {
			return
		}
		if attempt >= w.opts.Attempts {
			break
		}
		time.Sleep(wait)
	}
	w.dropped.Add(int64(len(pending)))
	if w.opts.OnError != nil {
		w.opts.OnError(err, len(pending))
	}
}

// try writes events once, together and then one by one if that fails, and
// returns the ones that were not written with the last error.
func (w *Writer) try(events []Event) ([]Event, error) {
	ctx, cancel := context.WithTimeout(context.Background(), writerFlushTimeout)
	defer cancel()
	err := insertBatch(ctx, w.db, events)
	if err == nil {
		w.written.Add(int64(len(events)))
		return nil, nil
	}
	if len(events) == 1 {
		return events, err
	}
	var left []Event
	for _, ev := range events {
		if rerr := Record(ctx, w.db, ev); rerr != nil {
			left = append(left, ev)
			err = rerr
		}
	}
	w.written.Add(int64(len(events) - len(left)))
	return left, err
}

// insertBatch writes events with one multi-row INSERT.
func insertBatch(ctx context.Context, db Execer, events []Event) error {
	var q strings.Builder
	q.WriteString(`INSERT INTO ohs_core.audit_events
	    (tenant_id, user_id, actor_type, actor_id, action, method, route, status, request_id, detail, at, actor_tenant_id)
	VALUES `)
	args := make([]any, 0, len(events)*eventColumns)
	for i, ev := range events {
		detail := []byte("{}")
		if len(ev.Detail) > 0 {
			var err error
			if detail, err = detailJSON(ev.Detail); err != nil {
				return fmt.Errorf("audit: detail: %w", err)
			}
		}
		if i > 0 {
			q.WriteString(", ")
		}
		n := i * eventColumns
		fmt.Fprintf(&q, "($%d::uuid, $%d::uuid, $%d::text, $%d::text, $%d::text, $%d::text, $%d::text, $%d::integer, $%d::text, $%d::jsonb, coalesce($%d::timestamptz, now()), $%d::uuid)",
			n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10, n+11, n+12)
		args = append(args, eventArgs(ev, detail)...)
	}
	if _, err := db.Exec(ctx, q.String(), args...); err != nil {
		return fmt.Errorf("audit: record batch: %w", err)
	}
	return nil
}
