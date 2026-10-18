// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

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

// Pulls.
//
// GET /users/{userId}/trends?tz=..&from=YYYY-MM-DD&to=YYYY-MM-DD answers
// {"days": [...]}, one object per night the user slept on the Pod, keyed by
// "day", each with its sessions (sleep stages and time series). The query
// parameters follow pyEight (include-main=false, include-all-sessions=true,
// model-version=v2). Days are asked in UTC: every time in a session is an
// absolute timestamp, so the zone only decides which date a night is filed
// under.

// DataTypeTrends is the raw data type of one trends day.
const DataTypeTrends = "trends"

// maxChunk is the number of days one trends request asks for.
const maxChunk = 14

// maxPull bounds the window of one Fetch (the core asks for at most 30 days
// at once).
const maxPull = 90 * 24 * time.Hour

// trendsPath is the trends endpoint of a user.
func trendsPath(userID string) string { return "/users/" + url.PathEscape(userID) + "/trends" }

// Fetch pulls the trends days of [from, to) and emits them, one Emit per UTC
// day. A token Eight Sleep rejects gives an error wrapping
// connector.ErrNeedsReauth (the core then signs in again and retries once);
// a rate limit a *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token, userID := conn.Tokens.Access, conn.Tokens.ProviderUserID
	if token == "" || userID == "" {
		return fmt.Errorf("eight_sleep: connection has no access token or user id: %w", connector.ErrNeedsReauth)
	}
	from, to = from.UTC(), to.UTC()
	if now := c.now().UTC(); to.After(now) {
		to = now
	}
	if to.Sub(from) > maxPull {
		from = to.Add(-maxPull)
	}
	if !to.After(from) {
		return nil
	}
	// A night that started the evening before from is filed under the date
	// it ended on, so the first day asked is from's own date.
	first, last := utcDay(from), utcDay(to.Add(-time.Nanosecond))
	var lines []dayLine
	for cs := first; !cs.After(last); cs = cs.AddDate(0, 0, maxChunk) {
		ce := cs.AddDate(0, 0, maxChunk-1)
		if ce.After(last) {
			ce = last
		}
		if !cs.Equal(first) {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return err
			}
		}
		got, err := c.fetchChunk(ctx, token, userID, cs, ce)
		if errors.Is(err, errUnauthorized) {
			return fmt.Errorf("eight_sleep: fetch trends: %w: %w", err, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("eight_sleep: fetch trends: %w", err)
		}
		lines = append(lines, got...)
	}
	return emitByDay(ctx, emit, lines)
}

// fetchChunk pulls the trends days from first to last (both included).
func (c *Connector) fetchChunk(ctx context.Context, token, userID string, first, last time.Time) ([]dayLine, error) {
	q := url.Values{}
	q.Set("tz", "UTC")
	q.Set("from", first.Format(time.DateOnly))
	q.Set("to", last.Format(time.DateOnly))
	q.Set("include-main", "false")
	q.Set("include-all-sessions", "true")
	q.Set("model-version", "v2")
	body, err := c.get(ctx, token, trendsPath(userID), q)
	if err != nil {
		return nil, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	var answer struct {
		Days []json.RawMessage `json:"days"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("decode answer: %w", err)
	}
	out := make([]dayLine, 0, len(answer.Days))
	for _, d := range answer.Days {
		var obj struct {
			Day string `json:"day"`
		}
		if err := json.Unmarshal(d, &obj); err != nil {
			return nil, errors.New("trends day is not a JSON object")
		}
		day, err := time.Parse(time.DateOnly, obj.Day)
		if err != nil {
			// A day without a usable date is kept, filed under the
			// chunk's first day; Map refuses it with a clear error.
			day = first
		}
		var b bytes.Buffer
		if err := json.Compact(&b, d); err != nil {
			return nil, err
		}
		out = append(out, dayLine{day: day, line: b.Bytes()})
	}
	return out, nil
}

// dayLine is one raw line and the UTC day it is filed under.
type dayLine struct {
	day  time.Time
	line []byte
}

// emitByDay groups lines by day and emits each day once, in day order.
func emitByDay(ctx context.Context, emit connector.RawSink, lines []dayLine) error {
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
		if err := emit.Emit(ctx, DataTypeTrends, d, byDay[d]); err != nil {
			return fmt.Errorf("eight_sleep: emit %s: %w", d.Format(time.DateOnly), err)
		}
	}
	return nil
}

// utcDay is the start of t's UTC day.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
