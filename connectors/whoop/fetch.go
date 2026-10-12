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
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// bodyFreshness is how close to now a window must end for Fetch to read the
// body measurements. WHOOP keeps only the current values, so reading them for
// every backfill window would store the same snapshot again and again.
const bodyFreshness = 24 * time.Hour

// scopeOf is the OAuth scope each data type is read with.
var scopeOf = map[string]string{
	DataTypeCycle:    "read:cycles",
	DataTypeRecovery: "read:recovery",
	DataTypeSleep:    "read:sleep",
	DataTypeWorkout:  "read:workout",
	DataTypeBody:     "read:body_measurement",
}

// fatal reports an error that must stop the pull rather than let it go on with
// the next data type: a rejected token, a rate limit, a cancelled context.
func fatal(err error) bool {
	if errors.Is(err, errUnauthorized) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	_, limited := connector.AsRateLimit(err)
	return limited
}

// Fetch pulls every data type for [from, to) and emits the raw lines, one
// Emit per data type and page.
//
// Errors:
//   - an access token WHOOP rejects gives an error wrapping
//     connector.ErrNeedsReauth, a rate limit a *connector.RateLimitError, and
//     a cancelled context its error: each stops the pull at once;
//   - any other failure of one data type (a server error that outlived the
//     retries, an answer that does not decode) is logged, the other data
//     types are still pulled, and Fetch returns the failures joined at the
//     end, so the job is retried. What was emitted stays: raw objects are
//     idempotent and Map hashes each record on its WHOOP id;
//   - a data type whose scope the grant lacks (from the scopes WHOOP
//     returned, or an HTTP 403) is skipped with a warning and no error:
//     retrying would not help.
//
// Fetch never refreshes on its own: WHOOP rotates refresh tokens, so only the
// caller, which stores them, may.
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
	for _, dt := range types {
		if _, ok := scopeOf[dt]; !ok {
			return fmt.Errorf("whoop: unknown data type %q", dt)
		}
	}
	f := &fetch{c: c, token: token, emit: emit, cycles: map[string]cycleRef{}}
	var failed []error
	for _, dt := range types {
		if !granted(conn.Tokens.Scopes, scopeOf[dt]) {
			slog.Warn("whoop: data type skipped, its scope was not granted", "provider", Slug, "data_type", dt, "scope", scopeOf[dt])
			continue
		}
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
		}
		switch {
		case err == nil:
		case errors.Is(err, errUnauthorized):
			return fmt.Errorf("whoop: fetch %s: %w: %w", dt, errUnauthorized, connector.ErrNeedsReauth)
		case fatal(err):
			return fmt.Errorf("whoop: fetch %s: %w", dt, err)
		case errors.Is(err, errForbidden):
			slog.Warn("whoop: data type skipped, WHOOP refused access (HTTP 403)", "provider", Slug, "data_type", dt, "scope", scopeOf[dt])
		default:
			slog.Warn("whoop: data type failed, pulling the others", "provider", Slug, "data_type", dt, "error", err)
			failed = append(failed, fmt.Errorf("whoop: fetch %s: %w", dt, err))
		}
	}
	return errors.Join(failed...)
}

// granted reports whether scope is among the granted scopes. Scopes that name
// no read scope at all (none recorded, or only "offline") say nothing, so they
// grant everything: the API answers for itself.
func granted(scopes []string, scope string) bool {
	known := false
	for _, s := range scopes {
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' }) {
			if part == scope {
				return true
			}
			if strings.HasPrefix(part, "read:") {
				known = true
			}
		}
	}
	return !known
}

// fetch is the state of one pull: the cycles seen so far, so a recovery
// finds its cycle without a request of its own.
type fetch struct {
	c      *Connector
	token  string
	emit   connector.RawSink
	cycles map[string]cycleRef
}

// call sends one GET (paced by the connector, see Connector.pace).
func (f *fetch) call(ctx context.Context, path string, q url.Values, out any) error {
	return f.c.get(ctx, f.token, path, q, out)
}

// collection walks one v2 collection endpoint over [from, to) with nextToken
// and emits each page as one raw object of dataType. prepare, when set, sees
// and may rewrite each compacted line before it is emitted.
//
// It stops at the last page (no next_token), and also, with a warning, on a
// page that is empty but names a next page, on a next_token already followed
// (a loop), and after maxPagesPerFetch pages. A record already emitted by an
// earlier page of the same walk (same id) is not emitted again.
func (f *fetch) collection(ctx context.Context, dataType, path string, from, to time.Time, prepare func(ctx context.Context, line []byte) ([]byte, error)) error {
	if !from.Before(to) {
		return nil
	}
	next := ""
	followed := map[string]bool{}
	seen := map[string]bool{}
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
			if len(line) == 0 || line[0] != '{' {
				slog.Warn("whoop: record that is not a JSON object skipped", "provider", Slug, "endpoint", path)
				continue
			}
			if id := recordKey(line); id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
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
		if followed[page.NextToken] {
			slog.Warn("whoop: next token repeats, stopping", "provider", Slug, "endpoint", path)
			return nil
		}
		followed[page.NextToken] = true
		next = page.NextToken
	}
	slog.Warn("whoop: pagination truncated at max pages, more data available",
		"provider", Slug, "endpoint", path, "pages", maxPagesPerFetch)
	return nil
}

// recordKey is the identity of a collection record: its id, or for a
// recovery (which has none) its sleep and cycle ids. "" when it has none.
func recordKey(line []byte) string {
	var r struct {
		ID      FlexString `json:"id"`
		SleepID FlexString `json:"sleep_id"`
		CycleID FlexString `json:"cycle_id"`
	}
	if json.Unmarshal(line, &r) != nil {
		return ""
	}
	if r.ID != "" {
		return string(r.ID)
	}
	if r.SleepID == "" && r.CycleID == "" {
		return ""
	}
	return string(r.SleepID) + "/" + string(r.CycleID)
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
// cycle the pull has not seen (it started before the window) is asked for by
// id; one WHOOP no longer has, or cannot serve right now, leaves the line as
// it is. A rejected token or a rate limit stops the pull.
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
