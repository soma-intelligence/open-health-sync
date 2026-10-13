// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

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

// maxPagesPerType bounds the pagination of one data type in one pull. At
// 10,000 points a page it is far more than a 30 day backfill of one second
// heart rate; for exercise and sleep (25 a page) it is 50,000 sessions.
const maxPagesPerType = 2000

// errEmit wraps a failure of the raw sink: the pull must stop.
var errEmit = errors.New("fitbit: store raw page")

// listAnswer is the dataPoints.list answer.
type listAnswer struct {
	DataPoints    []json.RawMessage `json:"dataPoints"`
	NextPageToken string            `json:"nextPageToken"`
}

// fatal reports an error that stops the pull rather than let the other data
// types go on.
func fatal(err error) bool {
	if errors.Is(err, errUnauthorized) || errors.Is(err, errEmit) || errors.Is(err, ErrAccountNotLinked) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	_, limited := connector.AsRateLimit(err)
	return limited
}

// Fetch pulls every data type the user granted for [from, to) and emits the
// raw lines, one Emit per data type and page. A rejected access token gives
// an error wrapping connector.ErrNeedsReauth, HTTP 429 or 504 a
// *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, grantedTypes(conn.Tokens.Scopes), emit)
}

// FetchNotified pulls only the data type a notification named. A data type
// this connector does not pull, or whose scope the user did not grant, pulls
// nothing; an empty or unknown one pulls everything granted.
func (c *Connector) FetchNotified(ctx context.Context, conn connector.Connection, from, to time.Time, dt string, emit connector.RawSink) error {
	id := normalizeDataType(dt)
	if _, ok := lookupDataType(id); !ok {
		return c.Fetch(ctx, conn, from, to, emit)
	}
	for _, g := range grantedTypes(conn.Tokens.Scopes) {
		if g == id {
			return c.FetchTypes(ctx, conn, from, to, []string{id}, emit)
		}
	}
	slog.Info("fitbit: notified data type not granted, nothing pulled", "provider", Slug, "data_type", id)
	return nil
}

// FetchTypes is Fetch restricted to the given data types.
//
// A data type Google forbids (HTTP 403: scope not granted) or does not know
// (404) is skipped with a warning; when every asked type is forbidden the
// grant lacks the scopes and the pull fails with connector.ErrMissingScope,
// so the connection asks the user to connect again. Any other failure of one type is logged,
// the others are still pulled, and the failures are returned so the job is
// retried. A rejected token, a rate limit, an unlinked account, a cancelled
// context or a failing sink stops the pull at once.
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("fitbit: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	specs := make([]dataType, 0, len(types))
	for _, id := range types {
		d, ok := lookupDataType(id)
		if !ok {
			return fmt.Errorf("fitbit: unknown data type %q", id)
		}
		specs = append(specs, d)
	}
	if !from.Before(to) || len(specs) == 0 {
		return nil
	}
	forbidden := 0
	var failed []error
	for i, d := range specs {
		if i > 0 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return err
			}
		}
		err := c.fetchType(ctx, token, d, from, to, emit)
		switch {
		case err == nil:
		case errors.Is(err, errUnauthorized):
			return fmt.Errorf("fitbit: fetch %s: %w: %w", d.ID, err, connector.ErrNeedsReauth)
		case fatal(err):
			return fmt.Errorf("fitbit: fetch %s: %w", d.ID, err)
		case errors.Is(err, errForbidden):
			forbidden++
			slog.Warn("fitbit: data type forbidden, skipped (scope not granted)", "provider", Slug, "data_type", d.ID)
		case errors.Is(err, errNotFound):
			slog.Warn("fitbit: data type not found, skipped", "provider", Slug, "data_type", d.ID)
		default:
			slog.Warn("fitbit: data type failed, the others are still pulled", "provider", Slug, "data_type", d.ID, "error", err)
			failed = append(failed, fmt.Errorf("fitbit: fetch %s: %w", d.ID, err))
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	if forbidden > 0 && forbidden == len(specs) {
		return fmt.Errorf("fitbit: every data type answered 403: %w: %w", errForbidden, connector.ErrMissingScope)
	}
	return nil
}

// fetchType follows nextPageToken through one data type over [from, to) and
// emits each page as one raw object. A point seen twice is emitted once.
func (c *Connector) fetchType(ctx context.Context, token string, d dataType, from, to time.Time, emit connector.RawSink) error {
	seen := map[[sha256.Size]byte]struct{}{}
	followed := map[string]bool{}
	next := ""
	path := "/v4/users/me/dataTypes/" + d.ID + "/dataPoints"
	for p := 0; p < maxPagesPerType; p++ {
		q := url.Values{}
		q.Set("filter", listFilter(d, from, to))
		q.Set("pageSize", fmt.Sprint(d.pageSize()))
		if next != "" {
			q.Set("pageToken", next)
		}
		var page listAnswer
		if err := c.get(ctx, token, path, q, &page); err != nil {
			return err
		}
		if err := emitPage(ctx, emit, d, page.DataPoints, seen, from); err != nil {
			return err
		}
		switch nt := page.NextPageToken; {
		case nt == "":
			return nil
		case followed[nt]:
			slog.Warn("fitbit: nextPageToken repeated, stopping", "provider", Slug, "data_type", d.ID)
			return nil
		default:
			followed[nt] = true
			next = nt
		}
		if err := pause(ctx, c.pageDelay); err != nil {
			return err
		}
	}
	slog.Warn("fitbit: pagination truncated at max pages, more data available", "provider", Slug, "data_type", d.ID, "pages", maxPagesPerType)
	return nil
}

// listFilter is the AIP-160 filter of dataPoints.list for d over [from, to).
//
// Interval and sample types filter on the physical (UTC) time. Daily types
// and exercise filter on the user's civil date, which a UTC window can miss
// at either edge, so the dates are widened by one day before and after (the
// duplicates are skipped downstream by content hash). Sleep filters on its
// physical end, so a night is pulled with the day it ends.
func listFilter(d dataType, from, to time.Time) string {
	ts := func(t time.Time) string { return `"` + t.UTC().Format(time.RFC3339) + `"` }
	firstDay := `"` + utcDay(from).AddDate(0, 0, -1).Format(time.DateOnly) + `"`
	endDay := `"` + utcDay(to.Add(-time.Nanosecond)).AddDate(0, 0, 2).Format(time.DateOnly) + `"`
	rng := func(field, lo, hi string) string {
		return field + " >= " + lo + " AND " + field + " < " + hi
	}
	switch d.Kind {
	case kindInterval:
		return rng(d.Filter+".interval.start_time", ts(from), ts(to))
	case kindSample:
		return rng(d.Filter+".sample_time.physical_time", ts(from), ts(to))
	case kindDaily:
		return rng(d.Filter+".date", firstDay, endDay)
	case kindSession:
		return rng(d.Filter+".interval.civil_start_time", firstDay, endDay)
	default: // kindSleep
		return rng(d.Filter+".interval.end_time", ts(from), ts(to))
	}
}

// emitPage emits one page as one raw object: each point not seen before,
// compacted to one line. The object's day is the day of the first point that
// names one, else fallback's.
func emitPage(ctx context.Context, emit connector.RawSink, d dataType, points []json.RawMessage, seen map[[sha256.Size]byte]struct{}, fallback time.Time) error {
	if len(points) == 0 {
		return nil
	}
	lines := make([][]byte, 0, len(points))
	day, dayKnown := utcDay(fallback), false
	for _, p := range points {
		var b bytes.Buffer
		if err := json.Compact(&b, p); err != nil {
			return fmt.Errorf("fitbit: %s: %w", d.ID, err)
		}
		line := b.Bytes()
		if len(line) == 0 || line[0] != '{' {
			return fmt.Errorf("fitbit: %s: data point is not a JSON object", d.ID)
		}
		key := sha256.Sum256(line)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, line)
		if !dayKnown {
			if t, ok := pointDay(d, line); ok {
				day, dayKnown = t, true
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if err := emit.Emit(ctx, d.ID, day, lines); err != nil {
		return fmt.Errorf("%w: %s %s: %w", errEmit, d.ID, day.Format(time.DateOnly), err)
	}
	return nil
}

// pointDay is the UTC day a data point is filed under: its start, sample time
// or date.
func pointDay(d dataType, line []byte) (time.Time, bool) {
	var p map[string]json.RawMessage
	if json.Unmarshal(line, &p) != nil {
		return time.Time{}, false
	}
	var body struct {
		Interval   *struct{ StartTime string }    `json:"interval"`
		SampleTime *struct{ PhysicalTime string } `json:"sampleTime"`
		Date       *civilDate                     `json:"date"`
	}
	if json.Unmarshal(p[d.Field], &body) != nil {
		return time.Time{}, false
	}
	switch {
	case body.Interval != nil:
		if t, err := parseTime(body.Interval.StartTime); err == nil {
			return utcDay(t), true
		}
	case body.SampleTime != nil:
		if t, err := parseTime(body.SampleTime.PhysicalTime); err == nil {
			return utcDay(t), true
		}
	case body.Date != nil:
		if t, err := body.Date.time(); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
