// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// DataTypeMetrics is the raw data type of one day of the metrics endpoint.
const DataTypeMetrics = "daily_metrics"

// maxDays bounds the days of one pull, so a mistaken window cannot turn into
// thousands of calls. The metrics endpoint answers one date per call.
const maxDays = 400

// rawDay is one stored raw line: the date asked for and the endpoint's
// answer, verbatim.
type rawDay struct {
	Date string          `json:"date"`
	Body json.RawMessage `json:"body"`
}

// Fetch pulls the metrics of every date from from to to (both UTC dates,
// inclusive; a to at midnight excludes that date) with one call per date,
// and emits each day's answer as one raw line of DataTypeMetrics. A date
// without data (404, 204, or an empty metric list) emits nothing.
//
// A token Ultrahuman refuses (401, 403) gives an error wrapping
// connector.ErrNeedsReauth: the scheduler then refreshes once and retries.
// HTTP 429 gives a *connector.RateLimitError. Days already emitted stay
// stored: a retried pull stores them again under the same keys.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("ultrahuman: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	days := dates(from, to)
	for i, day := range days {
		if i > 0 {
			if err := c.sleep(ctx, c.dayDelay); err != nil {
				return err
			}
		}
		date := day.Format("2006-01-02")
		body, _, err := c.get(ctx, token, metricsPath, url.Values{"date": {date}})
		if errors.Is(err, errUnauthorized) || errors.Is(err, errForbidden) {
			return fmt.Errorf("ultrahuman: metrics %s: %w: %w", date, err, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("ultrahuman: metrics %s: %w", date, err)
		}
		line, err := dayLine(date, body)
		if err != nil {
			return fmt.Errorf("ultrahuman: metrics %s: %w", date, err)
		}
		if line == nil {
			continue
		}
		if err := emit.Emit(ctx, DataTypeMetrics, day, [][]byte{line}); err != nil {
			return err
		}
	}
	return nil
}

// dates lists the UTC dates a pull of [from, to] covers, oldest first, at
// most maxDays (the most recent ones).
func dates(from, to time.Time) []time.Time {
	from, to = from.UTC(), to.UTC()
	if to.After(from) && to.Equal(utcDay(to)) {
		to = to.Add(-time.Nanosecond)
	}
	first, last := utcDay(from), utcDay(to)
	if last.Before(first) {
		return nil
	}
	if n := int(last.Sub(first).Hours()/24) + 1; n > maxDays {
		first = last.AddDate(0, 0, -(maxDays - 1))
	}
	var out []time.Time
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// utcDay is midnight UTC of t's UTC date.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// dayLine builds the raw line of one date: nil when the answer holds no
// metric. An answer that is not a JSON object is an error.
func dayLine(date string, body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode answer: %w", err)
	}
	if len(env.items(date)) == 0 {
		return nil, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		return nil, err
	}
	return json.Marshal(rawDay{Date: date, Body: compact.Bytes()})
}
