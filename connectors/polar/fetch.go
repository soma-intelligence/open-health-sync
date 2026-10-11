// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// rangeChunkDays is the longest from/to window the date range endpoints
// (daily activity, continuous heart rate) answer.
const rangeChunkDays = 28

// activityHistoryDays is how far back the daily activity endpoint reaches.
const activityHistoryDays = 365

// offsetReach is how many days away a known UTC offset may be borrowed for a
// day of continuous heart rate that has none of its own.
const offsetReach = 3

// Fetch pulls every data type for [from, to) and emits the raw lines, one
// Emit per data type and UTC day.
//
// Only the non-transactional AccessLink endpoints are used, so nothing is
// committed at Polar and a failed pull can simply run again. A token Polar
// rejects (401), or a user no longer registered or without the mandatory
// consents (403), gives an error wrapping connector.ErrNeedsReauth; a rate
// limit gives a *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchTypes is Fetch restricted to the given data types.
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("polar: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	st := &pullState{offsets: map[string]int{}}
	for i, dt := range types {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return err
			}
		}
		var (
			lines []dayLine
			err   error
		)
		switch dt {
		case DataTypeExercise:
			lines, err = c.fetchExercises(ctx, token, from, to, st)
		case DataTypeSleep:
			lines, err = c.fetchSleep(ctx, token, from, to, st)
		case DataTypeNightlyRecharge:
			lines, err = c.fetchRecharge(ctx, token, from, to)
		case DataTypeActivity:
			lines, err = c.fetchActivity(ctx, token, from, to)
		case DataTypeContinuousHR:
			lines, err = c.fetchContinuousHR(ctx, token, from, to, st)
		case DataTypePhysicalInfo:
			lines, err = c.fetchPhysicalInfo(ctx, token)
		default:
			return fmt.Errorf("polar: unknown data type %q", dt)
		}
		if isUnauthorized(err) {
			return fmt.Errorf("polar: fetch %s: %w: %w", dt, err, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("polar: fetch %s: %w", dt, err)
		}
		if err := emitByDay(ctx, emit, dt, lines); err != nil {
			return err
		}
	}
	return nil
}

// pullState carries what one pull learned for later data types: the user's
// UTC offset in minutes per local date, from sleep and exercises.
type pullState struct {
	offsets map[string]int
}

// offsetFor returns the offset known for date, else the one of the nearest
// date at most offsetReach days away (the earlier one on a tie).
func (s *pullState) offsetFor(date string) (int, bool) {
	if off, ok := s.offsets[date]; ok {
		return off, true
	}
	d, err := parseDate(date)
	if err != nil {
		return 0, false
	}
	for n := 1; n <= offsetReach; n++ {
		for _, k := range []string{d.AddDate(0, 0, -n).Format("2006-01-02"), d.AddDate(0, 0, n).Format("2006-01-02")} {
			if off, ok := s.offsets[k]; ok {
				return off, true
			}
		}
	}
	return 0, false
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
			return fmt.Errorf("polar: emit %s %s: %w", dataType, d.Format("2006-01-02"), err)
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

// withField returns obj (a JSON object) with key set to val, unless obj
// already has key. The other bytes of obj are kept as they are.
func withField(obj []byte, key string, val any) ([]byte, error) {
	obj = bytes.TrimSpace(obj)
	if len(obj) < 2 || obj[0] != '{' {
		return nil, fmt.Errorf("polar: line is not a JSON object")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(obj, &probe); err != nil {
		return nil, err
	}
	if _, has := probe[key]; has {
		return obj, nil
	}
	k, _ := json.Marshal(key)
	v, err := json.Marshal(val)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(obj)+len(k)+len(v)+2)
	out = append(out, '{')
	out = append(out, k...)
	out = append(out, ':')
	out = append(out, v...)
	if len(probe) > 0 {
		out = append(out, ',')
	}
	out = append(out, bytes.TrimSpace(obj[1:])...)
	return out, nil
}

// decodeList reads a list answer: a bare JSON array, or an object holding
// the array under key. An empty body is an empty list.
func decodeList(body []byte, key string) ([]json.RawMessage, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	if body[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	raw, ok := obj[key]
	if !ok {
		return nil, fmt.Errorf("answer holds no %q list", key)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// inWindow reports whether t lies in [from, to).
func inWindow(t, from, to time.Time) bool {
	return !t.Before(from) && t.Before(to)
}

// inDays reports whether the date (YYYY-MM-DD) lies in the calendar days of
// [from, to), both ends inclusive.
func inDays(date string, from, to time.Time) bool {
	first := utcDay(from).Format("2006-01-02")
	last := utcDay(to.Add(-time.Nanosecond)).Format("2006-01-02")
	return date >= first && date <= last
}

// ── exercises ───────────────────────────────────────────────────────────────

// fetchExercises lists the exercises of the last 30 days (GET /v3/exercises
// takes no date range) and keeps those that started or were uploaded in
// [from, to). An exercise whose start cannot be read is kept when its upload
// time is in the window, so Map reports it rather than it vanishing.
func (c *Connector) fetchExercises(ctx context.Context, token string, from, to time.Time, st *pullState) ([]dayLine, error) {
	body, _, err := c.get(ctx, token, "/v3/exercises", nil)
	if err != nil {
		return nil, err
	}
	items, err := decodeList(body, "exercises")
	if err != nil {
		return nil, fmt.Errorf("decode exercises: %w", err)
	}
	var out []dayLine
	for _, it := range items {
		line, err := compact(it)
		if err != nil {
			return nil, err
		}
		var e Exercise
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		start, startErr := exerciseStart(e)
		upload, _, upErr := parseTime(e.UploadTime)
		keep := (startErr == nil && inWindow(start, from, to)) || (upErr == nil && inWindow(upload, from, to))
		if !keep {
			continue
		}
		day := upload
		if startErr == nil {
			day = start
			if e.StartTimeUTCOffset != nil {
				local := start.Add(time.Duration(*e.StartTimeUTCOffset) * time.Minute)
				st.offsets[local.Format("2006-01-02")] = *e.StartTimeUTCOffset
			}
		}
		out = append(out, dayLine{day: utcDay(day), line: line})
	}
	return out, nil
}

// ── sleep and Nightly Recharge ──────────────────────────────────────────────

// fetchSleep lists the nights of the last 28 days (GET /v3/users/sleep) and
// keeps those whose date or start lies in [from, to). Each night's start
// tells the user's UTC offset for its dates.
func (c *Connector) fetchSleep(ctx context.Context, token string, from, to time.Time, st *pullState) ([]dayLine, error) {
	body, _, err := c.get(ctx, token, "/v3/users/sleep", nil)
	if err != nil {
		return nil, err
	}
	items, err := decodeList(body, "nights")
	if err != nil {
		return nil, fmt.Errorf("decode sleep: %w", err)
	}
	var out []dayLine
	for _, it := range items {
		line, err := compact(it)
		if err != nil {
			return nil, err
		}
		var head struct {
			Date           string `json:"date"`
			SleepStartTime string `json:"sleep_start_time"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			return nil, err
		}
		d, err := parseDate(head.Date)
		if err != nil {
			return nil, err
		}
		start, zoned, startErr := parseTime(head.SleepStartTime)
		if !inDays(head.Date, from, to) && (startErr != nil || !inWindow(start, from, to)) {
			continue
		}
		if startErr == nil && zoned {
			off := offsetMinutes(start)
			st.offsets[head.Date] = off
			st.offsets[start.Format("2006-01-02")] = off
		}
		out = append(out, dayLine{day: d, line: line})
	}
	return out, nil
}

// fetchRecharge lists the Nightly Recharge results of the last 28 days and
// keeps those dated in [from, to).
func (c *Connector) fetchRecharge(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	body, _, err := c.get(ctx, token, "/v3/users/nightly-recharge", nil)
	if err != nil {
		return nil, err
	}
	items, err := decodeList(body, "recharges")
	if err != nil {
		return nil, fmt.Errorf("decode nightly recharge: %w", err)
	}
	var out []dayLine
	for _, it := range items {
		line, err := compact(it)
		if err != nil {
			return nil, err
		}
		var head struct {
			Date string `json:"date"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			return nil, err
		}
		d, err := parseDate(head.Date)
		if err != nil {
			return nil, err
		}
		if !inDays(head.Date, from, to) {
			continue
		}
		out = append(out, dayLine{day: d, line: line})
	}
	return out, nil
}

// ── daily activity ──────────────────────────────────────────────────────────

// fetchActivity pulls the daily summaries in windows of at most 28 days,
// never earlier than the 365 days the endpoint reaches back.
func (c *Connector) fetchActivity(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	earliest := utcDay(c.now()).AddDate(0, 0, -activityHistoryDays)
	if from.Before(earliest) {
		from = earliest
	}
	var out []dayLine
	for i, span := range ymdChunks(from, to, rangeChunkDays) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		q := url.Values{}
		q.Set("from", span.first)
		q.Set("to", span.last)
		body, _, err := c.get(ctx, token, "/v3/users/activities", q)
		if err != nil {
			return nil, err
		}
		items, err := decodeList(body, "activities")
		if err != nil {
			return nil, fmt.Errorf("decode activities: %w", err)
		}
		for _, it := range items {
			line, err := compact(it)
			if err != nil {
				return nil, err
			}
			var head struct {
				StartTime string `json:"start_time"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, err
			}
			start, _, err := parseTime(head.StartTime)
			if err != nil {
				return nil, err
			}
			out = append(out, dayLine{day: utcDay(start), line: line})
		}
	}
	return out, nil
}

// ── continuous heart rate ───────────────────────────────────────────────────

// fetchContinuousHR pulls continuous heart rate days in windows of at most
// 28 days. A 404 means no data in the window. Each day gets the UTC offset
// the pull learned for it, when it learned one.
func (c *Connector) fetchContinuousHR(ctx context.Context, token string, from, to time.Time, st *pullState) ([]dayLine, error) {
	var out []dayLine
	for i, span := range ymdChunks(from, to, rangeChunkDays) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		q := url.Values{}
		q.Set("from", span.first)
		q.Set("to", span.last)
		body, _, err := c.get(ctx, token, "/v3/users/continuous-heart-rate", q)
		if err != nil {
			return nil, err
		}
		items, err := decodeList(body, "heart_rates")
		if err != nil {
			return nil, fmt.Errorf("decode continuous heart rate: %w", err)
		}
		for _, it := range items {
			line, err := compact(it)
			if err != nil {
				return nil, err
			}
			var head struct {
				Date string `json:"date"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, err
			}
			d, err := parseDate(head.Date)
			if err != nil {
				return nil, err
			}
			if off, ok := st.offsetFor(head.Date); ok {
				if line, err = withField(line, "utc_offset_minutes", off); err != nil {
					return nil, err
				}
			}
			out = append(out, dayLine{day: d, line: line})
		}
	}
	return out, nil
}

// ── physical info ───────────────────────────────────────────────────────────

// fetchPhysicalInfo reads the user's current physical information. It has
// no history, so it is emitted on every pull with the pull time added as
// "fetched_at"; Map dedupes it.
func (c *Connector) fetchPhysicalInfo(ctx context.Context, token string) ([]dayLine, error) {
	body, _, err := c.get(ctx, token, "/v3/users/physical-info", nil)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	line, err := compact(body)
	if err != nil {
		return nil, err
	}
	now := c.now().UTC().Truncate(time.Second)
	if line, err = withField(line, "fetched_at", now.Format(time.RFC3339)); err != nil {
		return nil, err
	}
	var p PhysicalInfo
	if err := json.Unmarshal(line, &p); err != nil {
		return nil, err
	}
	at, err := physicalInfoTime(p)
	if err != nil {
		return nil, err
	}
	return []dayLine{{day: utcDay(at), line: line}}, nil
}
