// SPDX-License-Identifier: AGPL-3.0-only

package strava

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// perPage is the default page size asked of GET /athlete/activities,
// Strava's maximum. A 30 day backfill of an active athlete is one request.
const perPage = 200

// maxPages bounds the pagination of one pull (100,000 activities).
const maxPages = 500

// activitiesPath lists the authenticated athlete's activities.
const activitiesPath = "/athlete/activities"

// Fetch pulls the summary activities that started in [from, to) and emits the
// raw lines, one Emit per UTC day of the activity start, one activity per
// line. An access token Strava rejects gives an error wrapping
// connector.ErrNeedsReauth; a rate limit gives a *connector.RateLimitError.
// Fetch never refreshes on its own: Strava rotates refresh tokens, so only the
// caller, which stores them, may.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("strava: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	lines, err := c.fetchActivities(ctx, token, from, to)
	if errors.Is(err, errUnauthorized) {
		return fmt.Errorf("strava: fetch %s: %w: %w", DataTypeActivity, errUnauthorized, connector.ErrNeedsReauth)
	}
	if err != nil {
		return fmt.Errorf("strava: fetch %s: %w", DataTypeActivity, err)
	}
	return emitByDay(ctx, emit, DataTypeActivity, lines)
}

// fetchActivities pages through GET /athlete/activities. Strava's after and
// before are exclusive epoch seconds on the activity start, so the request
// asks for (from-1s, to) and the answer is filtered to [from, to). The last
// page is the first one holding fewer than a full page of activities.
func (c *Connector) fetchActivities(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	if !from.Before(to) {
		return nil, nil
	}
	var out []dayLine
	for page := 1; page <= maxPages; page++ {
		if page > 1 {
			if err := pause(ctx, c.pageDelay); err != nil {
				return nil, err
			}
		}
		q := url.Values{}
		q.Set("after", strconv.FormatInt(from.Unix()-1, 10))
		q.Set("before", strconv.FormatInt(to.Unix(), 10))
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(c.perPage))
		body, err := c.get(ctx, token, activitiesPath, q)
		if err != nil {
			return nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("decode page %d: %w", page, err)
		}
		for _, it := range items {
			// A null entry holds no activity; stored, it would be a raw line
			// that is not an object.
			if bytes.Equal(bytes.TrimSpace(it), []byte("null")) {
				continue
			}
			line, err := compact(it)
			if err != nil {
				return nil, fmt.Errorf("page %d: %w", page, err)
			}
			var head struct {
				StartDate string `json:"start_date"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, fmt.Errorf("page %d: activity is not an object: %w", page, err)
			}
			day := utcDay(from)
			if start, err := time.Parse(time.RFC3339, head.StartDate); err == nil {
				if start.Before(from) || !start.Before(to) {
					continue
				}
				day = utcDay(start)
			}
			// An unreadable start keeps the line (raw first): Map reports it.
			out = append(out, dayLine{day: day, line: line})
		}
		if len(items) < c.perPage {
			return out, nil
		}
	}
	slog.Warn("strava: pagination truncated at max pages, more data available",
		"provider", Slug, "endpoint", activitiesPath, "pages", maxPages)
	return out, nil
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
			return fmt.Errorf("strava: emit %s %s: %w", dataType, d.Format("2006-01-02"), err)
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
