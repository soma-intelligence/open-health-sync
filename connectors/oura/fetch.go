// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// collectionPath is where every data type lives: <collectionPath>/<data type>.
const collectionPath = "/v2/usercollection/"

// maxPagesPerWindow bounds the pagination of one request window.
const maxPagesPerWindow = 500

// maxEmptyPages is how many pages in a row may come back empty while still
// naming a next page before the window is given up.
const maxEmptyPages = 3

// dateChunkDays is the size of a request window on the endpoints that take
// calendar dates (start_date, end_date). Oura suggests asking for one to
// three months at a time.
const dateChunkDays = 30

// heartRateChunk is the size of a request window on heartrate, which takes
// datetimes and answers one reading every few minutes.
const heartRateChunk = 7 * 24 * time.Hour

// errEmit wraps a failure of the raw sink: the pull must stop, whatever the
// data type.
var errEmit = errors.New("oura: store raw page")

// fatal reports an error that must stop the pull rather than let the other
// data types go on: a rejected token, a rate limit, a cancelled context, a
// raw store that refuses pages.
func fatal(err error) bool {
	if errors.Is(err, errUnauthorized) || errors.Is(err, errEmit) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	_, limited := connector.AsRateLimit(err)
	return limited
}

// Fetch pulls every data type for [from, to) and emits the raw lines, one
// Emit per data type and page. An access token Oura rejects gives an error
// wrapping connector.ErrNeedsReauth; a rate limit gives a
// *connector.RateLimitError. Fetch never refreshes on its own: Oura refresh
// tokens are single use, so only the caller, which stores them, may (the
// collect role refreshes and runs the pull again on ErrNeedsReauth).
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchTypes is Fetch restricted to the given data types.
//
// A data type Oura forbids (HTTP 403: the user did not grant its scope) is
// skipped with a warning and the others are still pulled, as is one whose
// endpoint answers 404 (an endpoint Oura moved or retired must not stop the
// pull of the others). When every asked type is forbidden, which is what a
// lapsed Oura Membership looks like, Fetch fails so the job shows the problem
// instead of succeeding empty.
//
// Partial failure. Any other failure of one data type (a server error that
// outlasted its retries, a page that does not decode) is logged, the other
// types are still pulled, and Fetch then returns the failures: the job is
// retried, and the raw store skips the pages it already holds. A rejected
// token, a rate limit, a cancelled context or a failing raw sink stops the
// pull at once.
//
// Pages are emitted as they arrive. A pull stopped half way (a rate limit)
// has stored the pages before it; the next pull fetches them again and the
// raw store skips the bytes it already holds.
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("oura: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	for _, dt := range types {
		if !knownDataType(dt) {
			return fmt.Errorf("oura: unknown data type %q", dt)
		}
	}
	if !from.Before(to) {
		return nil
	}
	forbidden := 0
	var failed []error
	for _, dt := range types {
		err := c.fetchType(ctx, token, dt, from, to, emit)
		switch {
		case err == nil:
		case errors.Is(err, errUnauthorized):
			return fmt.Errorf("oura: fetch %s: %w: %w", dt, errUnauthorized, connector.ErrNeedsReauth)
		case fatal(err):
			return fmt.Errorf("oura: fetch %s: %w", dt, err)
		case errors.Is(err, errForbidden):
			forbidden++
			slog.Warn("oura: data type forbidden, skipped (scope not granted or membership lapsed)", "provider", Slug, "data_type", dt)
		case errors.Is(err, errNotFound):
			slog.Warn("oura: data type endpoint not found, skipped", "provider", Slug, "data_type", dt)
		default:
			slog.Warn("oura: data type failed, the others are still pulled", "provider", Slug, "data_type", dt, "error", err)
			failed = append(failed, fmt.Errorf("oura: fetch %s: %w", dt, err))
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	if forbidden > 0 && forbidden == len(types) {
		return fmt.Errorf("oura: every data type answered 403: %w", errForbidden)
	}
	return nil
}

func knownDataType(dt string) bool {
	for _, k := range AllDataTypes {
		if k == dt {
			return true
		}
	}
	return false
}

// fetchType walks the request windows of one data type. A document that
// comes back twice (on two pages, or in two windows that overlap) is emitted
// once.
func (c *Connector) fetchType(ctx context.Context, token, dataType string, from, to time.Time, emit connector.RawSink) error {
	seen := map[[sha256.Size]byte]struct{}{}
	for i, params := range windows(dataType, from, to) {
		if i > 0 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return err
			}
		}
		if err := c.fetchWindow(ctx, token, dataType, params, seen, emit); err != nil {
			return err
		}
	}
	return nil
}

// windows returns the query parameters of each request window of dataType
// over [from, to).
//
// heartrate takes datetimes: the window is asked for as is, in steps of
// heartRateChunk. Every other endpoint filters on the document's "day", which
// is the user's local day (the wake-up day for a sleep), so a UTC window can
// miss a document on either edge: the window is widened by one day on each
// side. Oura does not document whether end_date is inclusive, so end_date is
// asked one day further still, and consecutive windows share their boundary
// day: either way no day is missed. What that brings in twice is skipped
// (fetchType) or deduplicated downstream (same content hash).
func windows(dataType string, from, to time.Time) []url.Values {
	var out []url.Values
	if dataType == DataTypeHeartRate {
		for s := from.UTC(); s.Before(to); s = s.Add(heartRateChunk) {
			e := s.Add(heartRateChunk)
			if e.After(to) {
				e = to.UTC()
			}
			q := url.Values{}
			q.Set("start_datetime", s.Format(time.RFC3339))
			q.Set("end_datetime", e.Format(time.RFC3339))
			out = append(out, q)
		}
		return out
	}
	first := utcDay(from).AddDate(0, 0, -1)
	// The last day wanted is the day after to's; asked as an exclusive bound.
	end := utcDay(to.Add(-time.Nanosecond)).AddDate(0, 0, 2)
	for d := first; d.Before(end); d = d.AddDate(0, 0, dateChunkDays) {
		e := d.AddDate(0, 0, dateChunkDays)
		if e.After(end) {
			e = end
		}
		q := url.Values{}
		q.Set("start_date", d.Format(time.DateOnly))
		q.Set("end_date", e.Format(time.DateOnly))
		out = append(out, q)
	}
	return out
}

// fetchWindow follows next_token through one request window and emits each
// page as one raw object, without the documents seen already. It stops when
// no next page is named, when a token comes back that it already followed (a
// loop), after maxEmptyPages empty pages in a row, or after
// maxPagesPerWindow pages.
func (c *Connector) fetchWindow(ctx context.Context, token, dataType string, params url.Values, seen map[[sha256.Size]byte]struct{}, emit connector.RawSink) error {
	next := ""
	followed := map[string]bool{}
	empty := 0
	fallbackDay := windowDay(params)
	for p := 0; p < maxPagesPerWindow; p++ {
		q := url.Values{}
		for k, v := range params {
			q[k] = v
		}
		if next != "" {
			q.Set("next_token", next)
		}
		var body page
		if err := c.get(ctx, token, collectionPath+endpoint(dataType), q, &body); err != nil {
			return err
		}
		if err := emitPage(ctx, emit, dataType, body.Data, seen, fallbackDay); err != nil {
			return err
		}
		nt := ""
		if body.NextToken != nil {
			nt = *body.NextToken
		}
		if len(body.Data) == 0 {
			empty++
		} else {
			empty = 0
		}
		switch {
		case nt == "":
			return nil
		case followed[nt] || nt == next:
			slog.Warn("oura: next_token repeated, stopping", "provider", Slug, "data_type", dataType)
			return nil
		case empty >= maxEmptyPages:
			slog.Warn("oura: empty pages but next_token is set, stopping", "provider", Slug, "data_type", dataType)
			return nil
		}
		followed[nt] = true
		next = nt
		if err := pause(ctx, c.pageDelay); err != nil {
			return err
		}
	}
	slog.Warn("oura: pagination truncated at max pages, more data available",
		"provider", Slug, "data_type", dataType, "pages", maxPagesPerWindow)
	return nil
}

// windowDay is the first day a request window asks for, the day a page is
// filed under when none of its documents names one.
func windowDay(params url.Values) time.Time {
	if d, err := time.Parse(time.DateOnly, params.Get("start_date")); err == nil {
		return d
	}
	if t, err := time.Parse(time.RFC3339, params.Get("start_datetime")); err == nil {
		return utcDay(t)
	}
	return time.Time{}
}

// emitPage emits one page as one raw object: each document not seen before
// compacted to one line, in page order. The object's day is the day of its
// first document that names one, else fallback.
func emitPage(ctx context.Context, emit connector.RawSink, dataType string, docs []json.RawMessage, seen map[[sha256.Size]byte]struct{}, fallback time.Time) error {
	if len(docs) == 0 {
		return nil
	}
	lines := make([][]byte, 0, len(docs))
	for _, d := range docs {
		line, err := compact(d)
		if err != nil {
			return fmt.Errorf("oura: %s: %w", dataType, err)
		}
		key := sha256.Sum256(line)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	day := fallback
	for _, l := range lines {
		if d, ok := lineDay(l); ok {
			day = d
			break
		}
	}
	if err := emit.Emit(ctx, dataType, day, lines); err != nil {
		return fmt.Errorf("%w: %s %s: %w", errEmit, dataType, day.Format(time.DateOnly), err)
	}
	return nil
}

// lineDay is the day a document is filed under: its "day", else the UTC day
// of its "timestamp" (heartrate). ok is false when it names neither.
func lineDay(line []byte) (time.Time, bool) {
	var head struct {
		Day       any `json:"day"`
		Timestamp any `json:"timestamp"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return time.Time{}, false
	}
	if s, ok := head.Day.(string); ok {
		if d, err := time.Parse(time.DateOnly, s); err == nil {
			return d, true
		}
	}
	if s, ok := head.Timestamp.(string); ok {
		if ts, err := parseTime(s); err == nil {
			return utcDay(ts), true
		}
	}
	return time.Time{}, false
}

// compact returns raw as one line of compact JSON (a pretty printed answer
// would otherwise break NDJSON). A document must be a JSON object.
func compact(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, err
	}
	if b.Len() == 0 || b.Bytes()[0] != '{' {
		return nil, errors.New("document is not a JSON object")
	}
	return b.Bytes(), nil
}
