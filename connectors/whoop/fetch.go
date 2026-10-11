// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// bodyFreshness is how close to now a window must end for Fetch to read the
// body measurements. WHOOP keeps only the current values, so reading them for
// every backfill window would store the same snapshot again and again.
const bodyFreshness = 24 * time.Hour

// fatal reports an error that must stop the pull rather than be logged and
// skipped: a rejected token, a rate limit, a cancelled context.
func fatal(err error) bool {
	if errors.Is(err, errUnauthorized) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	_, limited := connector.AsRateLimit(err)
	return limited
}

// Fetch pulls every data type for [from, to) and emits the raw lines, one
// Emit per data type and page. An access token WHOOP rejects gives an error
// wrapping connector.ErrNeedsReauth; a rate limit gives a
// *connector.RateLimitError. Fetch never refreshes on its own: WHOOP rotates
// refresh tokens, so only the caller, which stores them, may.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchTypes is Fetch restricted to the given data types (for example the one
// a webhook named).
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("whoop: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	f := &fetch{c: c, token: token, emit: emit, cycles: map[string]cycleRef{}}
	for _, dt := range types {
		var err error
		switch dt {
		case DataTypeCycle:
			err = f.collection(ctx, dt, pathCycle, from, to, f.keepCycles)
		case DataTypeRecovery:
			err = f.collection(ctx, dt, pathRecovery, from, to, f.addCycle)
		case DataTypeSleep:
			err = f.collection(ctx, dt, pathSleep, from, to, nil)
		case DataTypeWorkout:
			err = f.collection(ctx, dt, pathWorkout, from, to, nil)
		case DataTypeBody:
			err = f.body(ctx, to)
		default:
			return fmt.Errorf("whoop: unknown data type %q", dt)
		}
		if errors.Is(err, errUnauthorized) {
			return fmt.Errorf("whoop: fetch %s: %w: %w", dt, errUnauthorized, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("whoop: fetch %s: %w", dt, err)
		}
	}
	return nil
}

// fetch is the state of one pull: the cycles seen so far, so a recovery
// finds its cycle without a request of its own, and whether a request was
// already sent (the first one is not paced).
type fetch struct {
	c      *Connector
	token  string
	emit   connector.RawSink
	cycles map[string]cycleRef
	sent   bool
}

// call paces and sends one GET.
func (f *fetch) call(ctx context.Context, path string, q url.Values, out any) error {
	if f.sent {
		if err := pause(ctx, f.c.pageDelay); err != nil {
			return err
		}
	}
	f.sent = true
	return f.c.get(ctx, f.token, path, q, out)
}

// collection walks one v2 collection endpoint over [from, to) with nextToken
// and emits each page as one raw object of dataType. prepare, when set, sees
// and may rewrite each compacted line before it is emitted.
func (f *fetch) collection(ctx context.Context, dataType, path string, from, to time.Time, prepare func(ctx context.Context, line []byte) ([]byte, error)) error {
	if !from.Before(to) {
		return nil
	}
	next := ""
	for p := 0; p < maxPagesPerFetch; p++ {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(maxPageLimit))
		q.Set("start", from.UTC().Format(time.RFC3339Nano))
		q.Set("end", to.UTC().Format(time.RFC3339Nano))
		if next != "" {
			q.Set("nextToken", next)
		}
		var page collectionPage
		if err := f.call(ctx, path, q, &page); err != nil {
			return err
		}
		lines := make([][]byte, 0, len(page.Records))
		for _, r := range page.Records {
			line, err := compact(r)
			if err != nil {
				return err
			}
			if prepare != nil {
				if line, err = prepare(ctx, line); err != nil {
					return err
				}
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			day := pageDay(lines, from)
			if err := f.emit.Emit(ctx, dataType, day, lines); err != nil {
				return fmt.Errorf("whoop: emit %s %s: %w", dataType, day.Format("2006-01-02"), err)
			}
		}
		if page.NextToken == "" {
			return nil
		}
		if len(page.Records) == 0 {
			slog.Warn("whoop: empty page with a next token, stopping", "provider", Slug, "endpoint", path)
			return nil
		}
		next = page.NextToken
	}
	slog.Warn("whoop: pagination truncated at max pages, more data available",
		"provider", Slug, "endpoint", path, "pages", maxPagesPerFetch)
	return nil
}

// keepCycles remembers the time and zone of each cycle for the recoveries.
func (f *fetch) keepCycles(_ context.Context, line []byte) ([]byte, error) {
	var cy Cycle
	if err := json.Unmarshal(line, &cy); err != nil {
		return nil, fmt.Errorf("cycle: %w", err)
	}
	if cy.ID != "" && cy.Start != "" {
		f.cycles[string(cy.ID)] = cycleRef{Start: cy.Start, End: cy.End, TimezoneOffset: cy.TimezoneOffset}
	}
	return line, nil
}

// addCycle adds the start, end and zone of a recovery's cycle as "cycle". A
// cycle the pull has not seen is asked for by id; one WHOOP no longer has
// leaves the line as it is. A rejected token or a rate limit stops the pull.
func (f *fetch) addCycle(ctx context.Context, line []byte) ([]byte, error) {
	var r struct {
		CycleID FlexString `json:"cycle_id"`
	}
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("recovery: %w", err)
	}
	if r.CycleID == "" {
		return line, nil
	}
	ref, ok := f.cycles[string(r.CycleID)]
	if !ok {
		var cy Cycle
		err := f.call(ctx, pathCycle+"/"+url.PathEscape(string(r.CycleID)), nil, &cy)
		switch {
		case err != nil && fatal(err):
			return nil, err
		case err != nil:
			slog.Warn("whoop: cycle of a recovery could not be read, keeping the recovery without it", "provider", Slug, "error", err)
			return line, nil
		case cy.Start == "":
			return line, nil
		}
		ref = cycleRef{Start: cy.Start, End: cy.End, TimezoneOffset: cy.TimezoneOffset}
		f.cycles[string(r.CycleID)] = ref
	}
	return withField(line, "cycle", ref)
}

// body reads the current body measurements when the window ends within
// bodyFreshness of now, and emits them as one line dated today (UTC).
func (f *fetch) body(ctx context.Context, to time.Time) error {
	now := f.c.now().UTC()
	if to.Before(now.Add(-bodyFreshness)) {
		return nil
	}
	var raw json.RawMessage
	if err := f.call(ctx, pathBody, nil, &raw); err != nil {
		if errors.Is(err, errNotFound) {
			return nil
		}
		return err
	}
	line, err := compact(raw)
	if err != nil {
		return err
	}
	day := utcDay(now)
	if line, err = withField(line, "snapshot_date", day.Format("2006-01-02")); err != nil {
		return err
	}
	if err := f.emit.Emit(ctx, DataTypeBody, day, [][]byte{line}); err != nil {
		return fmt.Errorf("whoop: emit %s %s: %w", DataTypeBody, day.Format("2006-01-02"), err)
	}
	return nil
}

// pageDay is the UTC day a page is filed under: the day of its first
// record's start (created_at for a recovery without its cycle), else the
// window's first day.
func pageDay(lines [][]byte, from time.Time) time.Time {
	var head struct {
		Start     string    `json:"start"`
		CreatedAt string    `json:"created_at"`
		Cycle     *cycleRef `json:"cycle"`
	}
	if json.Unmarshal(lines[0], &head) == nil {
		for _, s := range []string{head.Start, cycleStart(head.Cycle), head.CreatedAt} {
			if t, err := parseTime(s); err == nil {
				return utcDay(t)
			}
		}
	}
	return utcDay(from)
}

func cycleStart(r *cycleRef) string {
	if r == nil {
		return ""
	}
	return r.Start
}

// compact returns raw as one line of compact JSON (a pretty printed answer
// would otherwise break NDJSON).
func compact(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// withField returns obj (a JSON object) with key set to val, unless obj
// already has key. The other bytes of obj are kept as they are.
func withField(obj []byte, key string, val any) ([]byte, error) {
	obj = bytes.TrimSpace(obj)
	if len(obj) < 2 || obj[0] != '{' {
		return nil, fmt.Errorf("whoop: line is not a JSON object")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(obj, &probe); err != nil {
		return nil, err
	}
	if _, has := probe[key]; has {
		return obj, nil
	}
	k, _ := json.Marshal(key)
	v, err := json.Marshal(val)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(obj)+len(k)+len(v)+2)
	out = append(out, '{')
	out = append(out, k...)
	out = append(out, ':')
	out = append(out, v...)
	if len(probe) > 0 {
		out = append(out, ',')
	}
	out = append(out, bytes.TrimSpace(obj[1:])...)
	return out, nil
}
