// SPDX-License-Identifier: AGPL-3.0-only

package dexcom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// DataTypeEGV is the data type of the stored EGV lines: one estimated
// glucose value per line, as the API returned it.
const DataTypeEGV = "egvs"

// AllDataTypes lists every data type Fetch pulls.
var AllDataTypes = []string{DataTypeEGV}

// maxWindow is the longest startDate..endDate window the API answers (30
// days; longer windows get 400).
const maxWindow = 30 * 24 * time.Hour

// queryLayout formats startDate and endDate: UTC, without offset, as the API
// examples do.
const queryLayout = "2006-01-02T15:04:05"

// Fetch pulls the EGVs of [from, to) in windows of at most 30 days and emits
// one raw object per UTC day of systemTime. A token Dexcom rejects gives an
// error wrapping connector.ErrNeedsReauth; a rate limit gives a
// *connector.RateLimitError. Nothing is committed at Dexcom, so a failed pull
// can simply run again.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("dexcom: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	from, to = from.UTC(), to.UTC()
	if now := c.now().UTC(); to.After(now) {
		to = now
	}
	byDay := map[time.Time][][]byte{}
	skipped := 0
	for start, i := from, 0; start.Before(to); start, i = start.Add(maxWindow), i+1 {
		end := start.Add(maxWindow)
		if end.After(to) {
			end = to
		}
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return err
			}
		}
		q := url.Values{}
		q.Set("startDate", start.Format(queryLayout))
		q.Set("endDate", end.Format(queryLayout))
		body, err := c.get(ctx, token, "/v3/users/self/egvs", q)
		if errors.Is(err, errUnauthorized) {
			return fmt.Errorf("dexcom: fetch egvs: %w: %w", err, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("dexcom: fetch egvs: %w", err)
		}
		var page struct {
			Records []json.RawMessage `json:"records"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("dexcom: fetch egvs: decode answer: %w", err)
		}
		for _, rec := range page.Records {
			var head struct {
				SystemTime string `json:"systemTime"`
			}
			// A record without a readable time cannot be placed on a day.
			// Failing the window for it would fail every retry of the window
			// too, so it is skipped and counted instead.
			if err := json.Unmarshal(rec, &head); err != nil {
				skipped++
				continue
			}
			at, _, err := parseTime(head.SystemTime)
			if err != nil {
				skipped++
				continue
			}
			var line bytes.Buffer
			if err := json.Compact(&line, rec); err != nil {
				return fmt.Errorf("dexcom: fetch egvs: %w", err)
			}
			day := at.UTC().Truncate(24 * time.Hour)
			byDay[day] = append(byDay[day], line.Bytes())
		}
	}
	if skipped > 0 {
		slog.Warn("dexcom: skipped records without a readable systemTime", "provider", Slug, "count", skipped)
	}
	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	for _, d := range days {
		if err := emit.Emit(ctx, DataTypeEGV, d, byDay[d]); err != nil {
			return fmt.Errorf("dexcom: emit egvs %s: %w", d.Format(time.DateOnly), err)
		}
	}
	return nil
}

// parseTime reads a Dexcom timestamp. Records from the mobile apps carry a
// UTC offset, records from a receiver do not; a timestamp without offset is
// read as UTC, and hasOffset says which it was.
func parseTime(s string) (t time.Time, hasOffset bool, err error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true, nil
	}
	t, err = time.Parse("2006-01-02T15:04:05.999999999", s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("unreadable time %q", s)
	}
	return t, false, nil
}
