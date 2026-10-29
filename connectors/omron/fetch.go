// SPDX-License-Identifier: AGPL-3.0-only

package omron

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
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Data types of the stored lines: one measurement per line, as the API
// returned it. They are the API's own type names.
const (
	DataTypeBloodPressure = "bloodpressure"
	DataTypeWeight        = "weight"
)

// AllDataTypes lists every data type Fetch pulls.
var AllDataTypes = []string{DataTypeBloodPressure, DataTypeWeight}

// kinds names, per data type, the result array and the pagination key.
var kinds = map[string]struct{ array, pageKey string }{
	DataTypeBloodPressure: {array: "bloodPressure", pageKey: "bloodpressurePaginationKey"},
	DataTypeWeight:        {array: "weight", pageKey: "weightPaginationKey"},
}

// maxPages bounds the pages of one data type in one pull, so an API that
// keeps handing out the same key cannot loop forever.
const maxPages = 1000

// Fetch pulls the blood pressure and weight measurements of [from, to) and
// emits one raw object per data type and UTC day. Pages are read oldest
// first (sortOrder=asc, since the day before from, then the pagination key)
// until a page reaches to or holds no new key. A token OMRON rejects gives an
// error wrapping connector.ErrNeedsReauth; a rate limit gives a
// *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("omron: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	for _, dt := range AllDataTypes {
		lines, err := c.fetchType(ctx, token, dt, from.UTC(), to.UTC())
		if errors.Is(err, errUnauthorized) {
			return fmt.Errorf("omron: fetch %s: %w: %w", dt, err, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("omron: fetch %s: %w", dt, err)
		}
		if err := emitByDay(ctx, emit, dt, lines); err != nil {
			return err
		}
	}
	return nil
}

// dayLine is one raw line and the UTC day it belongs to.
type dayLine struct {
	day  time.Time
	line []byte
}

func (c *Connector) fetchType(ctx context.Context, token, dt string, from, to time.Time) ([]dayLine, error) {
	k := kinds[dt]
	var (
		out     []dayLine
		key     string
		seen    = map[string]bool{}
		skipped int
	)
	defer func() {
		if skipped > 0 {
			slog.Warn("omron: skipped measurements without a readable dateTime", "provider", Slug, "type", dt, "count", skipped)
		}
	}()
	for page := 0; page < maxPages; page++ {
		form := url.Values{}
		form.Set("type", dt)
		// since is a date: start the day before, so a reading late in the
		// evening west of UTC is not cut, and filter on the instant below.
		form.Set("since", from.AddDate(0, 0, -1).Format(time.DateOnly))
		form.Set("limit", strconv.Itoa(c.pageSize))
		form.Set("sortOrder", "asc")
		if key != "" {
			form.Set(k.pageKey, key)
		}
		body, err := c.post(ctx, token, form)
		if err != nil {
			return nil, err
		}
		var ans struct {
			Status *int                       `json:"status"`
			Result map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(body, &ans); err != nil {
			return nil, fmt.Errorf("decode answer: %w", err)
		}
		if ans.Status == nil || *ans.Status != 0 {
			st := -1
			if ans.Status != nil {
				st = *ans.Status
			}
			return nil, fmt.Errorf("answer status %d", st)
		}
		var items []json.RawMessage
		if raw, ok := ans.Result[k.array]; ok && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("decode %s: %w", k.array, err)
			}
		}
		reachedEnd := false
		for _, it := range items {
			var head struct {
				DateTime string `json:"dateTime"`
			}
			// A measurement without a readable time cannot be placed on a
			// day. Failing the type for it would fail every retry too, so it
			// is skipped and counted instead.
			if err := json.Unmarshal(it, &head); err != nil {
				skipped++
				continue
			}
			at, err := parseUTC(head.DateTime)
			if err != nil {
				skipped++
				continue
			}
			if !at.Before(to) {
				reachedEnd = true
				continue
			}
			if at.Before(from) {
				continue
			}
			var line bytes.Buffer
			if err := json.Compact(&line, it); err != nil {
				return nil, err
			}
			out = append(out, dayLine{day: at.Truncate(24 * time.Hour), line: line.Bytes()})
		}
		next := pageKey(ans.Result["paginationKey"], k.pageKey)
		if reachedEnd || len(items) == 0 || next == "" || seen[next] {
			return out, nil
		}
		seen[next] = true
		key = next
	}
	return out, nil
}

// pageKey reads name from the paginationKey object, a number or a string.
func pageKey(raw json.RawMessage, name string) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	v := strings.TrimSpace(string(obj[name]))
	if v == "" || v == "null" {
		return ""
	}
	if s, err := strconv.Unquote(v); err == nil {
		return s
	}
	return v
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
			return fmt.Errorf("omron: emit %s %s: %w", dataType, d.Format(time.DateOnly), err)
		}
	}
	return nil
}

// parseUTC reads dateTime, the measurement instant in UTC without offset.
func parseUTC(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable dateTime %q", s)
}
