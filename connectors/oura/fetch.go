// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"bytes"
	"context"
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

// dateChunkDays is the size of a request window on the endpoints that take
// calendar dates (start_date, end_date).
const dateChunkDays = 30

// heartRateChunk is the size of a request window on heartrate, which takes
// datetimes and answers one reading every few minutes.
const heartRateChunk = 7 * 24 * time.Hour

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
// Emit per data type and page. An access token Oura rejects gives an error
// wrapping connector.ErrNeedsReauth; a rate limit gives a
// *connector.RateLimitError. Fetch never refreshes on its own: Oura refresh
// tokens are single use, so only the caller, which stores them, may.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchTypes is Fetch restricted to the given data types.
//
// A data type Oura forbids (HTTP 403: the user did not grant its scope) is
// skipped with a warning and the others are still pulled. When every asked
// type is forbidden, which is what a lapsed Oura Membership looks like, Fetch
// fails so the job shows the problem instead of succeeding empty.
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
	for _, dt := range types {
		err := c.fetchType(ctx, token, dt, from, to, emit)
		switch {
		case errors.Is(err, errUnauthorized):
			return fmt.Errorf("oura: fetch %s: %w: %w", dt, errUnauthorized, connector.ErrNeedsReauth)
		case errors.Is(err, errForbidden):
			forbidden++
			slog.Warn("oura: data type forbidden, skipped (scope not granted or membership lapsed)", "provider", Slug, "data_type", dt)
		case err != nil:
			return fmt.Errorf("oura: fetch %s: %w", dt, err)
		}
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

// fetchType walks the request windows of one data type.
func (c *Connector) fetchType(ctx context.Context, token, dataType string, from, to time.Time, emit connector.RawSink) error {
	for i, params := range windows(dataType, from, to) {
		if i > 0 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return err
			}
		}
		if err := c.fetchWindow(ctx, token, dataType, params, emit); err != nil {
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
// side. What that brings in twice is skipped downstream (same bytes, same
// content hash).
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
	last := utcDay(to.Add(-time.Nanosecond)).AddDate(0, 0, 1)
	for d := first; !d.After(last); d = d.AddDate(0, 0, dateChunkDays) {
		e := d.AddDate(0, 0, dateChunkDays-1)
		if e.After(last) {
			e = last
		}
		q := url.Values{}
		q.Set("start_date", d.Format(time.DateOnly))
		q.Set("end_date", e.Format(time.DateOnly))
		out = append(out, q)
	}
	return out
}

// fetchWindow follows next_token through one request window and emits each
// non-empty page as one raw object.
func (c *Connector) fetchWindow(ctx context.Context, token, dataType string, params url.Values, emit connector.RawSink) error {
	next := ""
	for p := 0; p < maxPagesPerWindow; p++ {
		q := url.Values{}
		for k, v := range params {
			q[k] = v
		}
		if next != "" {
			q.Set("next_token", next)
		}
		var body page
		if err := c.get(ctx, token, collectionPath+dataType, q, &body); err != nil {
			return err
		}
		if err := emitPage(ctx, emit, dataType, body.Data); err != nil {
			return err
		}
		nt := ""
		if body.NextToken != nil {
			nt = *body.NextToken
		}
		switch {
		case nt == "":
			return nil
		case len(body.Data) == 0:
			slog.Warn("oura: empty page but next_token is set, stopping", "provider", Slug, "data_type", dataType)
			return nil
		case nt == next:
			slog.Warn("oura: next_token repeated, stopping", "provider", Slug, "data_type", dataType)
			return nil
		}
		next = nt
		if err := pause(ctx, c.pageDelay); err != nil {
			return err
		}
	}
	slog.Warn("oura: pagination truncated at max pages, more data available",
		"provider", Slug, "data_type", dataType, "pages", maxPagesPerWindow)
	return nil
}

// emitPage emits one page as one raw object: each document compacted to one
// line, in page order. The object's day is the day of its first document.
func emitPage(ctx context.Context, emit connector.RawSink, dataType string, docs []json.RawMessage) error {
	if len(docs) == 0 {
		return nil
	}
	lines := make([][]byte, 0, len(docs))
	for _, d := range docs {
		line, err := compact(d)
		if err != nil {
			return fmt.Errorf("oura: %s: %w", dataType, err)
		}
		lines = append(lines, line)
	}
	day, err := lineDay(lines[0])
	if err != nil {
		return fmt.Errorf("oura: %s: %w", dataType, err)
	}
	if err := emit.Emit(ctx, dataType, day, lines); err != nil {
		return fmt.Errorf("oura: emit %s %s: %w", dataType, day.Format(time.DateOnly), err)
	}
	return nil
}

// lineDay is the day a document is filed under: its "day", else the UTC day
// of its "timestamp" (heartrate).
func lineDay(line []byte) (time.Time, error) {
	var head struct {
		Day       string `json:"day"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return time.Time{}, err
	}
	if head.Day != "" {
		d, err := time.Parse(time.DateOnly, head.Day)
		if err != nil {
			return time.Time{}, fmt.Errorf("day %q: %w", head.Day, err)
		}
		return d, nil
	}
	ts, err := parseTime(head.Timestamp)
	if err != nil {
		return time.Time{}, err
	}
	return utcDay(ts), nil
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
