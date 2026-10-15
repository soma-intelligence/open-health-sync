// SPDX-License-Identifier: AGPL-3.0-only

package suunto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// perPage is the default page size asked of GET /v3/workouts.
const perPage = 100

// maxPages bounds the paging of one pull (50,000 workouts).
const maxPages = 500

// maxSpan247 is the longest window the 24/7 endpoints accept (28 days).
const maxSpan247 = 28 * 24 * time.Hour

// API paths.
const (
	workoutsPath      = "/v3/workouts"
	sleepPath         = "/247samples/sleep"
	recoveryPath      = "/247samples/recovery"
	dailyActivityPath = "/247/daily-activity-statistics"
)

// dailyDateLayout is the startdate/enddate format of the daily activity
// statistics (UTC, no zone suffix).
const dailyDateLayout = "2006-01-02T15:04:05"

// Fetch pulls every data type for [from, to) and emits the raw lines, one
// Emit per data type and UTC day. An access token Suunto rejects gives an
// error wrapping connector.ErrNeedsReauth; a rate limit or spent quota gives
// a *connector.RateLimitError. Fetch never refreshes on its own.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchNotified pulls only the data type a notification named (see
// HandleInbound), one or a few requests instead of four or more. An unknown
// data type pulls everything.
func (c *Connector) FetchNotified(ctx context.Context, conn connector.Connection, from, to time.Time, dataType string, emit connector.RawSink) error {
	if slices.Contains(AllDataTypes, dataType) {
		return c.FetchTypes(ctx, conn, from, to, []string{dataType}, emit)
	}
	return c.Fetch(ctx, conn, from, to, emit)
}

// FetchTypes pulls the given data types for [from, to).
//
// The 24/7 types are stored raw only. A plain HTTP error on one of them (an
// endpoint the operator's subscription does not include, for example) is
// logged and skipped so workouts still arrive; a rate limit, a refused token
// or a refused subscription key stops the pull.
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("suunto: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	if !from.Before(to) {
		return nil
	}
	for i, dt := range types {
		if i > 0 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return err
			}
		}
		var lines []dayLine
		var err error
		switch dt {
		case DataTypeWorkout:
			lines, err = c.fetchWorkouts(ctx, token, from, to)
		case DataTypeSleep, DataTypeRecovery, DataTypeDailyActivity:
			lines, err = c.fetch247(ctx, token, dt, from, to)
		default:
			continue
		}
		switch {
		case errors.Is(err, errUnauthorized):
			return fmt.Errorf("suunto: fetch %s: %w: %w", dt, errUnauthorized, connector.ErrNeedsReauth)
		case err != nil && dt != DataTypeWorkout && skippable(err):
			slog.Warn("suunto: raw-only data type skipped", "provider", Slug, "data_type", dt, "error", err)
			continue
		case err != nil:
			return fmt.Errorf("suunto: fetch %s: %w", dt, err)
		}
		if err := emitByDay(ctx, emit, dt, lines); err != nil {
			return err
		}
	}
	return nil
}

// skippable reports an error a raw-only type may be skipped on: not a rate
// limit, not the subscription key, not a cancellation.
func skippable(err error) bool {
	if _, rl := connector.AsRateLimit(err); rl {
		return false
	}
	return !errors.Is(err, ErrSubscriptionKey) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// fetchWorkouts pages through GET /v3/workouts?since&until&limit&offset
// (epoch milliseconds). Suunto's reference describes the window as one of
// last modification, so every workout returned is kept, filed under the UTC
// day of its start, whatever its start: a workout synced late, or edited,
// still lands, and Map replaces the stored copy (Replaceable). The last page
// is the first one shorter than the page size. A workout listed twice (the
// list shifting between pages) is kept once.
func (c *Connector) fetchWorkouts(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		if page > 0 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return nil, err
			}
		}
		q := url.Values{}
		q.Set("since", strconv.FormatInt(from.UnixMilli(), 10))
		q.Set("until", strconv.FormatInt(to.UnixMilli(), 10))
		q.Set("limit", strconv.Itoa(c.perPage))
		q.Set("offset", strconv.Itoa(page*c.perPage))
		body, err := c.get(ctx, token, workoutsPath, q)
		if err != nil {
			return nil, err
		}
		items, err := workoutItems(body)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page+1, err)
		}
		for _, it := range items {
			line, err := compact(it)
			if err != nil {
				return nil, fmt.Errorf("page %d: %w", page+1, err)
			}
			// Raw fields: a start or an id of an unexpected JSON type must not
			// fail the page.
			var head struct {
				WorkoutKey json.RawMessage `json:"workoutKey"`
				WorkoutID  json.RawMessage `json:"workoutId"`
				StartTime  json.RawMessage `json:"startTime"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, fmt.Errorf("page %d: workout is not an object: %w", page+1, err)
			}
			id := scalar(head.WorkoutKey)
			if id == "" {
				id = scalar(head.WorkoutID)
			}
			if id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			day := utcDay(from)
			if ms, err := strconv.ParseInt(scalar(head.StartTime), 10, 64); err == nil && ms > 0 {
				day = utcDay(time.UnixMilli(ms))
			}
			// An unreadable start keeps the line (raw first): Map reports it.
			out = append(out, dayLine{day: day, line: line})
		}
		if len(items) < c.perPage {
			return out, nil
		}
	}
	slog.Warn("suunto: paging truncated at max pages, more data available",
		"provider", Slug, "endpoint", workoutsPath, "pages", maxPages)
	return out, nil
}

// scalar is a JSON string's text or a number's literal; "" for anything else.
func scalar(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// workoutItems reads a workout list answer: {"error": null, "payload": [...]}
// (the shape of Suunto's workout API), or a bare array. A non-null error is
// an error.
func workoutItems(body []byte) ([]json.RawMessage, error) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("decode workout list: %w", err)
		}
		return items, nil
	}
	var env struct {
		Error   json.RawMessage   `json:"error"`
		Payload []json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode workout list: %w", err)
	}
	if e := bytes.TrimSpace(env.Error); len(e) > 0 && !bytes.Equal(e, []byte("null")) {
		return nil, fmt.Errorf("workout list error: %s", clip(string(e), 200))
	}
	return env.Payload, nil
}

// fetch247 pulls one 24/7 data type for [from, to) in windows of at most 28
// days. Suunto does not publish the answers' fields, so each answer is
// stored as is: an array as one line per element, anything else as one line,
// filed under the UTC day the window starts.
func (c *Connector) fetch247(ctx context.Context, token, dataType string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for start := from; start.Before(to); start = start.Add(maxSpan247) {
		end := start.Add(maxSpan247)
		if end.After(to) {
			end = to
		}
		if start.After(from) {
			if err := pause(ctx, c.pageDelay); err != nil {
				return nil, err
			}
		}
		path, q := request247(dataType, start, end)
		body, err := c.get(ctx, token, path, q)
		if err != nil {
			return nil, err
		}
		lines, err := rawLines(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		day := utcDay(start)
		for _, l := range lines {
			out = append(out, dayLine{day: day, line: l})
		}
	}
	return out, nil
}

// request247 builds the path and query of one 24/7 request.
func request247(dataType string, from, to time.Time) (string, url.Values) {
	q := url.Values{}
	switch dataType {
	case DataTypeDailyActivity:
		// Inclusive bounds, to the second.
		q.Set("startdate", from.UTC().Format(dailyDateLayout))
		q.Set("enddate", to.UTC().Add(-time.Second).Format(dailyDateLayout))
		return dailyActivityPath, q
	case DataTypeRecovery:
		q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
		q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
		return recoveryPath, q
	default:
		q.Set("from", strconv.FormatInt(from.UnixMilli(), 10))
		q.Set("to", strconv.FormatInt(to.UnixMilli(), 10))
		return sleepPath, q
	}
}

// rawLines splits an answer of unknown shape into NDJSON lines of objects: a
// JSON array of objects gives one line each; an object gives one line;
// anything else (an array of scalars, a bare value) is wrapped as
// {"response": ...}. An empty answer gives no line.
func rawLines(body []byte) ([][]byte, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return nil, nil
	}
	if !json.Valid(body) {
		return nil, errors.New("answer is not JSON")
	}
	if body[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
		objects := true
		for _, it := range items {
			if t := bytes.TrimSpace(it); len(t) == 0 || t[0] != '{' {
				objects = false
				break
			}
		}
		if objects {
			out := make([][]byte, 0, len(items))
			for _, it := range items {
				l, err := compact(it)
				if err != nil {
					return nil, err
				}
				out = append(out, l)
			}
			return out, nil
		}
	}
	if body[0] == '{' {
		l, err := compact(body)
		if err != nil {
			return nil, err
		}
		return [][]byte{l}, nil
	}
	l, err := compact(body)
	if err != nil {
		return nil, err
	}
	return [][]byte{append(append([]byte(`{"response":`), l...), '}')}, nil
}

// dayLine is one raw line and the UTC day it belongs to.
type dayLine struct {
	day  time.Time
	line []byte
}

// emitByDay groups lines by day and emits each day once, in day order, lines
// in fetch order.
func emitByDay(ctx context.Context, emit connector.RawSink, dataType string, lines []dayLine) error {
	byDay := map[time.Time][][]byte{}
	var days []time.Time
	for _, l := range lines {
		if _, ok := byDay[l.day]; !ok {
			days = append(days, l.day)
		}
		byDay[l.day] = append(byDay[l.day], l.line)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	for _, d := range days {
		if err := emit.Emit(ctx, dataType, d, byDay[d]); err != nil {
			return fmt.Errorf("suunto: emit %s %s: %w", dataType, d.Format("2006-01-02"), err)
		}
	}
	return nil
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

// utcDay is t's calendar day in UTC, at midnight. Raw objects are grouped by
// it.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// clip shortens s to at most n bytes for an error message.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
