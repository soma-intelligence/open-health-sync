// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// The list endpoints. Athlete id "0" names the athlete the token belongs to.
const (
	activitiesPath = "/athlete/0/activities"
	wellnessPath   = "/athlete/0/wellness"
)

// dateLayout is the oldest and newest parameter format and the wellness id.
const dateLayout = "2006-01-02"

// Fetch pulls the activities that started in [from, to) and the wellness
// records of the days the window touches, and emits the raw lines: one Emit
// per data type and day, one record per line. An access token intervals.icu
// rejects gives an error wrapping connector.ErrNeedsReauth; a rate limit gives
// a *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	for _, dt := range AllDataTypes {
		if err := c.fetchType(ctx, conn, from, to, dt, emit); err != nil {
			return err
		}
	}
	return nil
}

// FetchNotified pulls only the data type a webhook named: activities for an
// activity event, wellness for a wellness event. Anything else pulls both.
func (c *Connector) FetchNotified(ctx context.Context, conn connector.Connection, from, to time.Time, dataType string, emit connector.RawSink) error {
	switch dataType {
	case DataTypeActivity, DataTypeWellness:
		return c.fetchType(ctx, conn, from, to, dataType, emit)
	}
	return c.Fetch(ctx, conn, from, to, emit)
}

// fetchType pulls one data type over [from, to) and emits it by day.
func (c *Connector) fetchType(ctx context.Context, conn connector.Connection, from, to time.Time, dataType string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("intervals_icu: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	var (
		lines []dayLine
		err   error
	)
	switch dataType {
	case DataTypeActivity:
		lines, err = c.fetchActivities(ctx, token, from, to)
	case DataTypeWellness:
		lines, err = c.fetchWellness(ctx, token, from, to)
	default:
		return fmt.Errorf("intervals_icu: unknown data type %q", dataType)
	}
	if errors.Is(err, errUnauthorized) {
		return fmt.Errorf("intervals_icu: fetch %s: %w: %w", dataType, errUnauthorized, connector.ErrNeedsReauth)
	}
	if err != nil {
		return fmt.Errorf("intervals_icu: fetch %s: %w", dataType, err)
	}
	return emitByDay(ctx, emit, dataType, lines)
}

// chunks splits [from, to) into spans of at most days days, so one answer
// stays bounded on a long backfill.
func chunks(from, to time.Time, days int) [][2]time.Time {
	var out [][2]time.Time
	step := time.Duration(days) * 24 * time.Hour
	for s := from; s.Before(to); s = s.Add(step) {
		e := s.Add(step)
		if e.After(to) {
			e = to
		}
		out = append(out, [2]time.Time{s, e})
	}
	return out
}

// fetchActivities lists the activities of [from, to). The API filters on the
// athlete's local start date (oldest and newest, inclusive), so each request
// asks one day more on both sides, and the answer is filtered on the UTC
// start_date. An activity listed by two chunks is kept once.
func (c *Connector) fetchActivities(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	if !from.Before(to) {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []dayLine
	for _, ch := range chunks(from, to, c.chunkDays) {
		q := url.Values{}
		q.Set("oldest", ch[0].UTC().AddDate(0, 0, -1).Format(dateLayout))
		q.Set("newest", ch[1].UTC().AddDate(0, 0, 1).Format(dateLayout))
		items, err := c.list(ctx, token, activitiesPath, q)
		if err != nil {
			return nil, err
		}
		for _, line := range items {
			var head struct {
				ID             flexID `json:"id"`
				StartDate      string `json:"start_date"`
				StartDateLocal string `json:"start_date_local"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, fmt.Errorf("activity is not an object: %w", err)
			}
			day := utcDay(from)
			if start, err := time.Parse(time.RFC3339, head.StartDate); err == nil {
				if start.Before(from) || !start.Before(to) {
					continue
				}
				day = utcDay(start)
			} else if local, err := parseLocal(head.StartDateLocal); err == nil {
				// No UTC start: the local date stands in for the day.
				day = utcDay(local)
			}
			// An unreadable start keeps the line (raw first): Map decides.
			if head.ID != "" {
				if seen[string(head.ID)] {
					continue
				}
				seen[string(head.ID)] = true
			}
			out = append(out, dayLine{day: day, line: line})
		}
	}
	return out, nil
}

// fetchWellness lists the wellness records of the UTC days [from, to)
// touches. A record is one local day, keyed by its date.
func (c *Connector) fetchWellness(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	if !from.Before(to) {
		return nil, nil
	}
	first := utcDay(from)
	last := utcDay(to.Add(-time.Nanosecond))
	seen := map[string]bool{}
	var out []dayLine
	for _, ch := range chunks(from, to, c.chunkDays) {
		q := url.Values{}
		q.Set("oldest", utcDay(ch[0]).Format(dateLayout))
		q.Set("newest", utcDay(ch[1].Add(-time.Nanosecond)).Format(dateLayout))
		items, err := c.list(ctx, token, wellnessPath, q)
		if err != nil {
			return nil, err
		}
		for _, line := range items {
			var head struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, fmt.Errorf("wellness record is not an object: %w", err)
			}
			day := first
			if d, err := time.Parse(dateLayout, head.ID); err == nil {
				if d.Before(first) || d.After(last) {
					continue
				}
				day = d
			}
			if head.ID != "" {
				if seen[head.ID] {
					continue
				}
				seen[head.ID] = true
			}
			out = append(out, dayLine{day: day, line: line})
		}
	}
	return out, nil
}

// list GETs one list endpoint and returns its items as compact JSON lines.
func (c *Connector) list(ctx context.Context, token, path string, q url.Values) ([][]byte, error) {
	body, err := c.get(ctx, token, path, q)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	out := make([][]byte, 0, len(items))
	for _, it := range items {
		line, err := compact(it)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, line)
	}
	return out, nil
}

// dayLine is one raw line and the day it belongs to.
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
			return fmt.Errorf("intervals_icu: emit %s %s: %w", dataType, d.Format(dateLayout), err)
		}
	}
	return nil
}

// compact returns raw as one line of compact JSON.
func compact(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// utcDay is t's calendar day in UTC, at midnight.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// parseLocal reads a local wall clock time without offset
// ("2026-03-01T08:00:00"), as UTC wall clock.
func parseLocal(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC), nil
		}
	}
	return time.Time{}, fmt.Errorf("intervals_icu: %q is not a local date-time", s)
}
