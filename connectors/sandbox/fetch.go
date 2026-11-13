// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Data types: one per data API endpoint, each raw line one record of it.
const (
	DataTypeSleep     = "sleep"
	DataTypeHeartRate = "heart_rate"
	DataTypeSteps     = "steps"
	DataTypeWorkouts  = "workouts"
)

// AllDataTypes is every data type Fetch pulls, in order.
var AllDataTypes = []string{DataTypeSleep, DataTypeHeartRate, DataTypeSteps, DataTypeWorkouts}

// Fetch pulls every data type over [from, to), in spans of at most 31 days,
// and emits the records by data type and UTC day of their start. An access
// token the sandbox refuses gives an error wrapping connector.ErrNeedsReauth.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	if conn.Tokens.Access == "" {
		return fmt.Errorf("sandbox: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	for _, dt := range AllDataTypes {
		byDay := map[time.Time][][]byte{}
		for s := from; s.Before(to); s = s.Add(maxWindow) {
			e := s.Add(maxWindow)
			if e.After(to) {
				e = to
			}
			items, err := c.list(ctx, conn.Tokens.Access, dt, s, e)
			if err != nil {
				return fmt.Errorf("sandbox: fetch %s: %w", dt, err)
			}
			for _, it := range items {
				var head struct {
					Start string `json:"start"`
					Time  string `json:"time"`
				}
				if err := json.Unmarshal(it, &head); err != nil {
					return fmt.Errorf("sandbox: fetch %s: record is not an object: %w", dt, err)
				}
				at := head.Start
				if at == "" {
					at = head.Time
				}
				day := utcDay(s)
				if t, err := time.Parse(time.RFC3339, at); err == nil {
					day = utcDay(t)
				}
				byDay[day] = append(byDay[day], it)
			}
		}
		days := make([]time.Time, 0, len(byDay))
		for d := range byDay {
			days = append(days, d)
		}
		sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
		for _, d := range days {
			if err := emit.Emit(ctx, dt, d, byDay[d]); err != nil {
				return fmt.Errorf("sandbox: emit %s %s: %w", dt, d.Format(time.DateOnly), err)
			}
		}
	}
	return nil
}

// list GETs one data endpoint and returns its records as compact JSON lines.
func (c *Connector) list(ctx context.Context, token, dataType string, from, to time.Time) ([][]byte, error) {
	_, base, httpc := c.state()
	q := url.Values{}
	q.Set("start", from.UTC().Format(time.RFC3339))
	q.Set("end", to.UTC().Format(time.RFC3339))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/"+dataType+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("access token refused: %w", connector.ErrNeedsReauth)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var p struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([][]byte, 0, len(p.Data))
	for _, it := range p.Data {
		var b bytes.Buffer
		if err := json.Compact(&b, it); err != nil {
			return nil, err
		}
		out = append(out, b.Bytes())
	}
	return out, nil
}

// utcDay is t's calendar day in UTC, at midnight.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
