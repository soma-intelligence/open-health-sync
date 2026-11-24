// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// API paths.
const (
	userPath     = "/v1/user"
	workoutsPath = "/v1/workouts"
)

// defaultPerPage is Wahoo's default page size of GET /v1/workouts. The API
// documents no maximum, so the connector does not ask for more.
const defaultPerPage = 30

// maxPages bounds the pagination of one pull.
const maxPages = 200

// Fetch pulls the user's body figures and the workouts that started in
// [from, to), and emits the raw lines: one Emit per data type and UTC day,
// one record per line. An access token Wahoo rejects gives an error wrapping
// connector.ErrNeedsReauth, a missing scope connector.ErrMissingScope, a
// rate limit a *connector.RateLimitError.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("wahoo: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	user, err := c.fetchUser(ctx, token, from)
	if err != nil {
		return wrapFetch(DataTypeUser, err)
	}
	if err := emitByDay(ctx, emit, DataTypeUser, user); err != nil {
		return err
	}
	workouts, err := c.fetchWorkouts(ctx, token, from, to)
	if err != nil {
		return wrapFetch(DataTypeWorkout, err)
	}
	return emitByDay(ctx, emit, DataTypeWorkout, workouts)
}

// wrapFetch names the data type of a failed pull and turns the API's
// refusals into the errors the core acts on.
func wrapFetch(dataType string, err error) error {
	switch {
	case errors.Is(err, errUnauthorized):
		return fmt.Errorf("wahoo: fetch %s: %w: %w", dataType, err, connector.ErrNeedsReauth)
	case errors.Is(err, errForbidden):
		return fmt.Errorf("wahoo: fetch %s: %w: %w", dataType, err, connector.ErrMissingScope)
	}
	return fmt.Errorf("wahoo: fetch %s: %w", dataType, err)
}

// fetchUser reads GET /v1/user and keeps its body figures only: the name,
// email and birth date are not stored. The line is filed under the day of
// updated_at (the day of from when it has none).
func (c *Connector) fetchUser(ctx context.Context, token string, from time.Time) ([]dayLine, error) {
	body, err := c.get(ctx, token, userPath, nil)
	if err != nil {
		return nil, err
	}
	var u struct {
		ID        json.RawMessage `json:"id"`
		Height    json.RawMessage `json:"height"`
		Weight    json.RawMessage `json:"weight"`
		UpdatedAt json.RawMessage `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}
	kept := map[string]json.RawMessage{}
	for k, v := range map[string]json.RawMessage{"id": u.ID, "height": u.Height, "weight": u.Weight, "updated_at": u.UpdatedAt} {
		if len(v) > 0 {
			kept[k] = v
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	line, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	day := utcDay(from)
	var at string
	if json.Unmarshal(u.UpdatedAt, &at) == nil {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			day = utcDay(t)
		}
	}
	return []dayLine{{day: day, line: line}}, nil
}

// fetchWorkouts pages through GET /v1/workouts, which Wahoo always sorts by
// start, newest first, and cannot filter: paging stops at the first page that
// reaches past from, at a short page, or once total workouts were listed.
// A workout listed without its summary gets it from
// GET /v1/workouts/{id}/workout_summary (none yet, 404, keeps the workout
// without one).
func (c *Connector) fetchWorkouts(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	if !from.Before(to) {
		return nil, nil
	}
	var out []dayLine
	seen := map[string]bool{}
	listed := 0
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(c.perPage))
		body, err := c.get(ctx, token, workoutsPath, q)
		if err != nil {
			return nil, err
		}
		var p struct {
			Workouts []json.RawMessage `json:"workouts"`
			Total    *int              `json:"total"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("decode page %d: %w", page, err)
		}
		older := false
		for _, raw := range p.Workouts {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			var w map[string]json.RawMessage
			if err := json.Unmarshal(raw, &w); err != nil {
				return nil, fmt.Errorf("page %d: workout is not an object: %w", page, err)
			}
			var head struct {
				ID     json.Number `json:"id"`
				Starts string      `json:"starts"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				return nil, fmt.Errorf("page %d: workout: %w", page, err)
			}
			start, err := time.Parse(time.RFC3339, head.Starts)
			if err != nil {
				// An unreadable start keeps the line (raw first): Map
				// reports it.
				start = from
			}
			if start.Before(from) {
				older = true
				continue
			}
			if !start.Before(to) {
				continue
			}
			id := head.ID.String()
			if id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			if s, ok := w["workout_summary"]; id != "" && (!ok || bytes.Equal(bytes.TrimSpace(s), []byte("null"))) {
				sum, err := c.get(ctx, token, workoutsPath+"/"+url.PathEscape(id)+"/workout_summary", nil)
				switch {
				case errors.Is(err, errNotFound):
				case err != nil:
					return nil, err
				default:
					w["workout_summary"] = json.RawMessage(sum)
				}
			}
			line, err := json.Marshal(w)
			if err != nil {
				return nil, err
			}
			line, err = compact(line)
			if err != nil {
				return nil, err
			}
			out = append(out, dayLine{day: utcDay(start), line: line})
		}
		listed += len(p.Workouts)
		if older || len(p.Workouts) < c.perPage || (p.Total != nil && listed >= *p.Total) {
			return out, nil
		}
	}
	modlog().Warn().Str("endpoint", workoutsPath).Int("pages", maxPages).Msg("pagination_truncated")
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
			return fmt.Errorf("wahoo: emit %s %s: %w", dataType, d.Format(time.DateOnly), err)
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
