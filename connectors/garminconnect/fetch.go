// SPDX-License-Identifier: AGPL-3.0-only

package garminconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Pulls.
//
// The connector reads the same Connect service endpoints the Garmin Connect
// website calls, for the user of the bearer token:
//   - usersummary-service/v2/dailysummaries over the whole window, one object
//     per day (steps, distance, calories, floors, resting heart rate, stress);
//   - sleep-service/sleep/dailySleepData per day (stages, score, SpO2, HRV,
//     breathing);
//   - hrv-service/hrv per day (the night's HRV);
//   - activitylist-service activity search over the window (workouts).
// From python-garminconnect; the shapes are unverified against Garmin's own
// specification, which Garmin does not publish.

// maxPull bounds the window of one Fetch (the core asks for at most 30 days
// at once).
const maxPull = 90 * 24 * time.Hour

// activityPageSize is the page size of the activity search.
const activityPageSize = 100

// maxActivityPages bounds the activity pagination of one Fetch.
const maxActivityPages = 50

// Fetch pulls the Connect data of [from, to) and emits the raw lines, grouped
// by data type and UTC day. A token Garmin rejects gives an error wrapping
// connector.ErrNeedsReauth; a rate limit a *connector.RateLimitError; an
// anti-bot block ErrBlocked.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("garmin_connect: connection has no access token: %w", connector.ErrNeedsReauth)
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
	first, last := utcDay(from), utcDay(to.Add(-time.Nanosecond))

	if err := c.fetchDailies(ctx, token, first, last, emit); err != nil {
		return err
	}
	paced := false
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		for _, step := range []func(context.Context, string, time.Time, connector.RawSink) error{
			c.fetchSleep, c.fetchHRV,
		} {
			if paced {
				if err := pause(ctx, c.chunkDelay); err != nil {
					return err
				}
			}
			paced = true
			if err := step(ctx, token, d, emit); err != nil {
				return err
			}
		}
	}
	return c.fetchActivities(ctx, token, first, last, emit)
}

// fetchDailies pulls the daily summaries of [first, last] in one call and
// emits each day's object.
func (c *Connector) fetchDailies(ctx context.Context, token string, first, last time.Time, emit connector.RawSink) error {
	q := url.Values{}
	q.Set("startDate", first.Format(time.DateOnly))
	q.Set("endDate", last.Format(time.DateOnly))
	body, err := c.pull(ctx, token, "/usersummary-service/v2/dailysummaries", q)
	if err != nil {
		return fmt.Errorf("garmin_connect: dailies: %w", err)
	}
	for _, raw := range unwrapList(body, "data") {
		var obj struct {
			CalendarDate string `json:"calendarDate"`
		}
		if json.Unmarshal(raw, &obj) != nil || obj.CalendarDate == "" {
			continue
		}
		day, err := parseDate(obj.CalendarDate)
		if err != nil {
			continue
		}
		if err := emit.Emit(ctx, DataTypeDailies, day, [][]byte{raw}); err != nil {
			return fmt.Errorf("garmin_connect: emit dailies %s: %w", obj.CalendarDate, err)
		}
	}
	return nil
}

// fetchSleep pulls one day's sleep and emits it when present.
func (c *Connector) fetchSleep(ctx context.Context, token string, day time.Time, emit connector.RawSink) error {
	q := url.Values{}
	q.Set("date", day.Format(time.DateOnly))
	q.Set("nonSleepBufferMinutes", "60")
	body, err := c.pull(ctx, token, "/sleep-service/sleep/dailySleepData", q)
	if err != nil {
		return fmt.Errorf("garmin_connect: sleep: %w", err)
	}
	if !hasSleep(body) {
		return nil
	}
	if err := emit.Emit(ctx, DataTypeSleeps, day, [][]byte{body}); err != nil {
		return fmt.Errorf("garmin_connect: emit sleep %s: %w", day.Format(time.DateOnly), err)
	}
	return nil
}

// fetchHRV pulls one day's HRV and emits it when present.
func (c *Connector) fetchHRV(ctx context.Context, token string, day time.Time, emit connector.RawSink) error {
	body, err := c.pull(ctx, token, "/hrv-service/hrv/"+day.Format(time.DateOnly), nil)
	if err != nil {
		return fmt.Errorf("garmin_connect: hrv: %w", err)
	}
	if !hasHRV(body) {
		return nil
	}
	if err := emit.Emit(ctx, DataTypeHRV, day, [][]byte{body}); err != nil {
		return fmt.Errorf("garmin_connect: emit hrv %s: %w", day.Format(time.DateOnly), err)
	}
	return nil
}

// fetchActivities pages the activity search over [first, last] and emits the
// activities grouped by their UTC start day.
func (c *Connector) fetchActivities(ctx context.Context, token string, first, last time.Time, emit connector.RawSink) error {
	byDay := map[time.Time][][]byte{}
	for page := 0; page < maxActivityPages; page++ {
		q := url.Values{}
		q.Set("start", fmt.Sprint(page*activityPageSize))
		q.Set("limit", fmt.Sprint(activityPageSize))
		q.Set("startDate", first.Format(time.DateOnly))
		q.Set("endDate", last.Format(time.DateOnly))
		body, err := c.pull(ctx, token, "/activitylist-service/activities/search/activities", q)
		if err != nil {
			return fmt.Errorf("garmin_connect: activities: %w", err)
		}
		list := unwrapList(body, "")
		for _, raw := range list {
			var obj struct {
				StartTimeGMT string `json:"startTimeGMT"`
			}
			day := first
			if json.Unmarshal(raw, &obj) == nil {
				if t, err := parseGMT(obj.StartTimeGMT); err == nil {
					day = utcDay(t)
				}
			}
			byDay[day] = append(byDay[day], raw)
		}
		if len(list) < activityPageSize {
			break
		}
		if err := pause(ctx, c.chunkDelay); err != nil {
			return err
		}
	}
	for _, day := range sortDays(byDay) {
		if err := emit.Emit(ctx, DataTypeActivities, day, byDay[day]); err != nil {
			return fmt.Errorf("garmin_connect: emit activities %s: %w", day.Format(time.DateOnly), err)
		}
	}
	return nil
}

// pull sends one GET and maps a rejected token to ErrNeedsReauth.
func (c *Connector) pull(ctx context.Context, token, path string, q url.Values) ([]byte, error) {
	body, err := c.get(ctx, token, path, q)
	if errors.Is(err, errUnauthorized) {
		return nil, fmt.Errorf("%w: %w", err, connector.ErrNeedsReauth)
	}
	return body, err
}

// unwrapList returns the JSON objects of a body that is either a bare array
// or, when key is set, an object with that array under key. Anything else
// gives no objects.
func unwrapList(body []byte, key string) []json.RawMessage {
	body = trimSpace(body)
	if len(body) == 0 {
		return nil
	}
	if body[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(body, &arr) == nil {
			return arr
		}
		return nil
	}
	if key == "" {
		return nil
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(body, &wrap) != nil {
		return nil
	}
	inner, ok := wrap[key]
	if !ok {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(inner, &arr) == nil {
		return arr
	}
	return nil
}

// hasSleep reports whether a sleep body carries a dated dailySleepDTO.
func hasSleep(body []byte) bool {
	if len(trimSpace(body)) == 0 {
		return false
	}
	var s SleepLine
	return json.Unmarshal(body, &s) == nil && s.DailySleepDTO.CalendarDate != ""
}

// hasHRV reports whether an HRV body carries a dated hrvSummary.
func hasHRV(body []byte) bool {
	if len(trimSpace(body)) == 0 {
		return false
	}
	var h HRVLine
	return json.Unmarshal(body, &h) == nil && h.HRVSummary.CalendarDate != ""
}

// trimSpace drops leading and trailing ASCII whitespace.
func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
