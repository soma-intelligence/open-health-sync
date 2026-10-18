// SPDX-License-Identifier: AGPL-3.0-only

package garmin

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

// Pulls.
//
// The Health API summary endpoints answer
// GET /wellness-api/rest/<type>?uploadStartTimeInSeconds=..&uploadEndTimeInSeconds=..
// with a JSON array of the summaries uploaded to Garmin Connect in that
// window, for the user of the bearer token. The window is upload time, not
// the time the data describes, and may be at most 24 hours long, so a pull
// is cut in windows of maxWindow. (From older public copies of the Health
// API specification; whether apps created today still get these endpoints is
// unverified.)

// maxWindow is the longest upload window one request may ask for.
const maxWindow = 86400 * time.Second

// maxPull bounds the window of one Fetch (the core asks for at most 30
// days at once).
const maxPull = 90 * 24 * time.Hour

// summaryPath is the pull endpoint of a data type.
func summaryPath(dataType string) string { return "/wellness-api/rest/" + dataType }

// Fetch pulls every summary type uploaded in [from, to) and emits the raw
// lines, one Emit per data type and UTC day. A type the user did not grant
// (HTTP 400 or 403) is skipped. A token Garmin rejects gives an error
// wrapping connector.ErrNeedsReauth; a rate limit a *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchNotified pulls only the summary type a notification named. An
// unknown type pulls everything.
func (c *Connector) FetchNotified(ctx context.Context, conn connector.Connection, from, to time.Time, dataType string, emit connector.RawSink) error {
	if dt, ok := notificationKeys[dataType]; ok {
		return c.FetchTypes(ctx, conn, from, to, []string{dt}, emit)
	}
	return c.Fetch(ctx, conn, from, to, emit)
}

// FetchTypes is Fetch restricted to the given data types.
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("garmin: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	from, to = from.UTC().Truncate(time.Second), to.UTC().Truncate(time.Second)
	if now := c.now().UTC().Truncate(time.Second); to.After(now) {
		to = now
	}
	if to.Sub(from) > maxPull {
		from = to.Add(-maxPull)
	}
	if !to.After(from) {
		return nil
	}
	first := true
	for _, dt := range types {
		if _, ok := notificationKeys[dt]; !ok {
			return fmt.Errorf("garmin: unknown data type %q", dt)
		}
		var lines []dayLine
		for ws := from; ws.Before(to); ws = ws.Add(maxWindow) {
			we := ws.Add(maxWindow)
			if we.After(to) {
				we = to
			}
			if !first {
				if err := pause(ctx, c.chunkDelay); err != nil {
					return err
				}
			}
			first = false
			got, err := c.fetchWindow(ctx, token, dt, ws, we)
			if errors.Is(err, errNotGranted) {
				slog.Info("garmin: summary type not granted, skipped", "provider", Slug, "data_type", dt)
				lines = nil
				break
			}
			if errors.Is(err, errUnauthorized) {
				return fmt.Errorf("garmin: fetch %s: %w: %w", dt, err, connector.ErrNeedsReauth)
			}
			if err != nil {
				return fmt.Errorf("garmin: fetch %s: %w", dt, err)
			}
			lines = append(lines, got...)
		}
		if err := emitByDay(ctx, emit, dt, lines); err != nil {
			return err
		}
	}
	return nil
}

// fetchWindow pulls one data type for one upload window of at most a day.
func (c *Connector) fetchWindow(ctx context.Context, token, dataType string, from, to time.Time) ([]dayLine, error) {
	q := url.Values{}
	q.Set("uploadStartTimeInSeconds", strconv.FormatInt(from.Unix(), 10))
	q.Set("uploadEndTimeInSeconds", strconv.FormatInt(to.Unix(), 10))
	body, err := c.get(ctx, token, summaryPath(dataType), q)
	if err != nil {
		return nil, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode answer: %w", err)
	}
	out := make([]dayLine, 0, len(items))
	for _, it := range items {
		line, day, err := cleanLine(it)
		if err != nil {
			return nil, err
		}
		if day.IsZero() {
			day = utcDay(from)
		}
		out = append(out, dayLine{day: day, line: line})
	}
	return out, nil
}

// dayLine is one raw line and the UTC day it is filed under.
type dayLine struct {
	day  time.Time
	line []byte
}

// emitByDay groups lines by day and emits each day once, in day order.
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
			return fmt.Errorf("garmin: emit %s %s: %w", dataType, d.Format("2006-01-02"), err)
		}
	}
	return nil
}

// cleanLine checks that raw is one JSON object, removes a
// "userAccessToken" field (a credential has no place in the raw store) and
// returns it as one compact line, with the UTC day it describes (zero when
// it names none).
func cleanLine(raw []byte) ([]byte, time.Time, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, time.Time{}, errors.New("summary is not a JSON object")
	}
	var line []byte
	if _, has := obj["userAccessToken"]; has {
		delete(obj, "userAccessToken")
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, time.Time{}, err
		}
		line = b
	} else {
		var b bytes.Buffer
		if err := json.Compact(&b, raw); err != nil {
			return nil, time.Time{}, err
		}
		line = b.Bytes()
	}
	return line, summaryDay(obj), nil
}

// summaryDay is the UTC day a summary describes: its calendarDate, else the
// day of its start or measurement time; zero when it names none.
func summaryDay(obj map[string]json.RawMessage) time.Time {
	var date string
	if json.Unmarshal(obj["calendarDate"], &date) == nil && date != "" {
		if d, err := parseDate(date); err == nil {
			return d
		}
	}
	for _, k := range []string{"startTimeInSeconds", "measurementTimeInSeconds"} {
		var secs int64
		if json.Unmarshal(obj[k], &secs) == nil {
			if t, err := unixTime(k, &secs); err == nil {
				return utcDay(t)
			}
		}
	}
	return time.Time{}
}
