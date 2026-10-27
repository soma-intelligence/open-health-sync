// SPDX-License-Identifier: AGPL-3.0-only

package withings

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

// maxResponseBytes caps one Withings answer. Its size is provider controlled,
// so a runaway page must fail loudly rather than be buffered whole.
const maxResponseBytes = 32 << 20

// maxPagesPerChunk bounds the pagination of one request window.
const maxPagesPerChunk = 500

// ymdChunkDays is the size of a request window on the endpoints that take
// calendar dates (getactivity, sleep getsummary, getworkouts).
const ymdChunkDays = 30

// sleepDetailSpan is the longest window sleep action=get answers for: it
// returns at most 24 hours after startdate.
const sleepDetailSpan = 24 * time.Hour

// maxNightSpan bounds the detail window of one summary night. A summary whose
// end lies days after its start is corrupt; without the bound it would cost
// one sleep action=get call per day of the bogus span.
const maxNightSpan = 48 * time.Hour

// errUnauthorized is an HTTP 401 or inner status 401 from a data endpoint.
var errUnauthorized = errors.New("withings: access token rejected")

// fatal reports an error that must stop the pull rather than be logged and
// skipped: a rejected token, a rate limit, a cancelled context.
func fatal(err error) bool {
	if errors.Is(err, errUnauthorized) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	_, limited := connector.AsRateLimit(err)
	return limited
}

// Fetch pulls every data type for [from, to) and emits the raw lines, one
// Emit per data type and UTC day. An access token Withings rejects gives an
// error wrapping connector.ErrNeedsReauth; a rate limit gives a
// *connector.RateLimitError. Fetch never refreshes on its own: Withings
// rotates refresh tokens, so only the caller, which stores them, may.
func (c *Connector) Fetch(ctx context.Context, conn connector.Connection, from, to time.Time, emit connector.RawSink) error {
	return c.FetchTypes(ctx, conn, from, to, AllDataTypes, emit)
}

// FetchTypes is Fetch restricted to the given data types (for example the one
// a push notification named).
func (c *Connector) FetchTypes(ctx context.Context, conn connector.Connection, from, to time.Time, types []string, emit connector.RawSink) error {
	token := conn.Tokens.Access
	if token == "" {
		return fmt.Errorf("withings: connection has no access token: %w", connector.ErrNeedsReauth)
	}
	for _, dt := range types {
		var (
			lines []dayLine
			err   error
		)
		switch dt {
		case DataTypeMeasure:
			lines, err = c.fetchMeasure(ctx, token, from, to)
		case DataTypeActivity:
			lines, err = c.fetchActivity(ctx, token, from, to)
		case DataTypeSleep:
			lines, err = c.fetchSleep(ctx, token, from, to)
		case DataTypeWorkout:
			lines, err = c.fetchWorkouts(ctx, token, from, to)
		case DataTypeECG:
			lines, err = c.fetchHeart(ctx, token, from, to)
		case DataTypeIntraday:
			lines, err = c.fetchIntraday(ctx, token, from, to)
		default:
			return fmt.Errorf("withings: unknown data type %q", dt)
		}
		if errors.Is(err, errUnauthorized) {
			return fmt.Errorf("withings: fetch %s: %w: %w", dt, errUnauthorized, connector.ErrNeedsReauth)
		}
		if err != nil {
			return fmt.Errorf("withings: fetch %s: %w", dt, err)
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
			return fmt.Errorf("withings: emit %s %s: %w", dataType, d.Format("2006-01-02"), err)
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

// isNull reports a list entry that is JSON null: it holds no record, and
// stored it would be a raw line that is not an object.
func isNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// withField returns obj (a JSON object) with key set to val, unless obj
// already has key. The other bytes of obj are kept as they are.
func withField(obj []byte, key string, val any) ([]byte, error) {
	obj = bytes.TrimSpace(obj)
	if len(obj) < 2 || obj[0] != '{' {
		return nil, fmt.Errorf("withings: line is not a JSON object")
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

// pause waits d (skipped when d <= 0), or returns early on cancellation.
func pause(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func warnTruncated(endpoint string, start, end string) {
	slog.Warn("withings: pagination truncated at max pages, more data available",
		"provider", Slug, "endpoint", endpoint, "window_start", start, "window_end", end, "pages", maxPagesPerChunk)
}

// ymdSpan is one request window of a calendar date endpoint, both dates
// inclusive, as Withings reads startdateymd and enddateymd.
type ymdSpan struct {
	first, last string
}

// ymdChunks splits [from, to) into consecutive, non-overlapping windows of at
// most days calendar days (UTC). The last day is the day of the last instant
// before to, so an exclusive end at midnight does not pull the next day.
func ymdChunks(from, to time.Time, days int) []ymdSpan {
	if !from.Before(to) {
		return nil
	}
	first := utcDay(from)
	last := utcDay(to.Add(-time.Nanosecond))
	var out []ymdSpan
	for d := first; !d.After(last); d = d.AddDate(0, 0, days) {
		e := d.AddDate(0, 0, days-1)
		if e.After(last) {
			e = last
		}
		out = append(out, ymdSpan{first: d.Format("2006-01-02"), last: e.Format("2006-01-02")})
	}
	return out
}

// pager runs one paginated request window: page is called with the offset to
// ask for and reports the next offset and whether more pages exist.
func (c *Connector) pager(ctx context.Context, endpoint, start, end string, page func(offset int) (items int, more bool, next int, err error)) error {
	offset := 0
	for p := 0; p < maxPagesPerChunk; p++ {
		items, more, next, err := page(offset)
		if err != nil {
			return err
		}
		if items == 0 && more {
			slog.Warn("withings: empty page but more is set, stopping", "provider", Slug, "endpoint", endpoint)
			return nil
		}
		if !more || next == 0 {
			return nil
		}
		offset = next
		if p == maxPagesPerChunk-1 {
			warnTruncated(endpoint, start, end)
			return nil
		}
		if err := pause(ctx, c.pageDelay); err != nil {
			return err
		}
	}
	return nil
}

// ── getmeas ──────────────────────────────────────────────────────────────────

// fetchMeasure pulls every meastype at once (no meastype filter), in 30 day
// windows, and keeps each group as one line.
func (c *Connector) fetchMeasure(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for i, span := range getChunksBetween(from, to, 30*24*time.Hour) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		start := strconv.FormatInt(span.start.Unix(), 10)
		end := strconv.FormatInt(span.end.Unix(), 10)
		err := c.pager(ctx, "getmeas", start, end, func(offset int) (int, bool, int, error) {
			params := url.Values{}
			params.Set("action", "getmeas")
			params.Set("startdate", start)
			params.Set("enddate", end)
			if offset > 0 {
				params.Set("offset", strconv.Itoa(offset))
			}
			var body measurePage
			if err := c.call(ctx, token, "/measure", params, &body); err != nil {
				return 0, false, 0, err
			}
			for _, g := range body.MeasureGrps {
				if isNull(g) {
					continue
				}
				line, err := compact(g)
				if err != nil {
					return 0, false, 0, err
				}
				if body.Timezone != "" {
					if line, err = withField(line, "timezone", body.Timezone); err != nil {
						return 0, false, 0, err
					}
				}
				var head struct {
					Date int64 `json:"date"`
				}
				if err := json.Unmarshal(line, &head); err != nil {
					return 0, false, 0, err
				}
				out = append(out, dayLine{day: utcDay(ParseWithingsTimestamp(head.Date)), line: line})
			}
			return len(body.MeasureGrps), body.More != 0, body.Offset, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── getactivity ──────────────────────────────────────────────────────────────

func (c *Connector) fetchActivity(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for i, span := range ymdChunks(from, to, ymdChunkDays) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		err := c.pager(ctx, "getactivity", span.first, span.last, func(offset int) (int, bool, int, error) {
			params := url.Values{}
			params.Set("action", "getactivity")
			params.Set("startdateymd", span.first)
			params.Set("enddateymd", span.last)
			params.Set("data_fields", "steps,distance,elevation,calories,totalcalories,intense,moderate,soft")
			if offset > 0 {
				params.Set("offset", strconv.Itoa(offset))
			}
			var body activityPage
			if err := c.call(ctx, token, "/v2/measure", params, &body); err != nil {
				return 0, false, 0, err
			}
			for _, a := range body.Activities {
				if isNull(a) {
					continue
				}
				line, err := compact(a)
				if err != nil {
					return 0, false, 0, err
				}
				var head struct {
					Date string `json:"date"`
				}
				if err := json.Unmarshal(line, &head); err != nil {
					return 0, false, 0, err
				}
				d, err := time.Parse("2006-01-02", head.Date)
				if err != nil {
					return 0, false, 0, fmt.Errorf("activity date %q: %w", head.Date, err)
				}
				out = append(out, dayLine{day: d, line: line})
			}
			return len(body.Activities), body.More != 0, body.Offset, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── getintradayactivity ──────────────────────────────────────────────────────

// fetchIntraday pulls per-interval activity. Withings limits one call to 24 h.
func (c *Connector) fetchIntraday(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for i, span := range getChunksBetween(from, to, 24*time.Hour) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		params := url.Values{}
		params.Set("action", "getintradayactivity")
		params.Set("startdate", strconv.FormatInt(span.start.Unix(), 10))
		params.Set("enddate", strconv.FormatInt(span.end.Unix(), 10))
		params.Set("data_fields", "steps,elevation,calories,distance,heart_rate,duration")
		var body intradayBody
		if err := c.call(ctx, token, "/v2/measure", params, &body); err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(body.Series))
		for k := range body.Series {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, _ := strconv.ParseInt(keys[i], 10, 64)
			b, _ := strconv.ParseInt(keys[j], 10, 64)
			return a < b
		})
		for _, k := range keys {
			ts, err := strconv.ParseInt(k, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("intraday key %q is not an epoch", k)
			}
			if isNull(body.Series[k]) {
				continue
			}
			line, err := compact(body.Series[k])
			if err != nil {
				return nil, err
			}
			if line, err = withField(line, "timestamp", ts); err != nil {
				return nil, err
			}
			out = append(out, dayLine{day: utcDay(ParseWithingsTimestamp(ts)), line: line})
		}
	}
	return out, nil
}

// ── sleep ────────────────────────────────────────────────────────────────────

// fetchSleep pulls nightly summaries, then for each night its detailed phase
// segments (sleep action=get over the night's own start and end, since that
// endpoint answers at most 24 hours after startdate) and attaches them. When a
// window has no summary at all, it asks for segments day by day; segments
// found that way are grouped into nights (a gap over two hours starts a new
// one) and each becomes a synthetic summary line whose durations are summed
// from its segments.
//
// A failed detail call only costs the hypnogram (the summary is still
// stored), except a rejected token or a rate limit, which stop the pull.
func (c *Connector) fetchSleep(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for i, span := range ymdChunks(from, to, ymdChunkDays) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		var summaries []json.RawMessage
		err := c.pager(ctx, "getsummary", span.first, span.last, func(offset int) (int, bool, int, error) {
			params := url.Values{}
			params.Set("action", "getsummary")
			params.Set("startdateymd", span.first)
			params.Set("enddateymd", span.last)
			params.Set("data_fields", sleepDataFields)
			if offset > 0 {
				params.Set("offset", strconv.Itoa(offset))
			}
			var body sleepPage
			if err := c.call(ctx, token, "/v2/sleep", params, &body); err != nil {
				return 0, false, 0, err
			}
			summaries = append(summaries, body.Series...)
			return len(body.Series), body.More != 0, body.Offset, nil
		})
		if err != nil {
			return nil, err
		}

		if len(summaries) == 0 {
			first, _ := time.Parse("2006-01-02", span.first)
			last, _ := time.Parse("2006-01-02", span.last)
			phases, err := c.fetchSleepDetail(ctx, token, first, last.AddDate(0, 0, 1))
			if err != nil {
				return nil, err
			}
			for _, synth := range synthesizeNights(phases) {
				line, err := json.Marshal(synth)
				if err != nil {
					return nil, err
				}
				out = append(out, dayLine{day: utcDay(ParseWithingsTimestamp(synth.Startdate)), line: line})
			}
			continue
		}

		for _, s := range summaries {
			if isNull(s) {
				continue
			}
			line, err := compact(s)
			if err != nil {
				return nil, err
			}
			var head struct {
				Startdate int64 `json:"startdate"`
				Enddate   int64 `json:"enddate"`
			}
			if err := json.Unmarshal(line, &head); err != nil {
				return nil, err
			}
			start, end := ParseWithingsTimestamp(head.Startdate), ParseWithingsTimestamp(head.Enddate)
			if end.Sub(start) > maxNightSpan {
				slog.Warn("withings: sleep summary spans more than 48 h, phases asked for its first 48 h only",
					"provider", Slug, "hours", int(end.Sub(start).Hours()))
				end = start.Add(maxNightSpan)
			}
			phases, err := c.fetchSleepDetail(ctx, token, start, end)
			if err != nil {
				return nil, err
			}
			var inside []SleepPhaseSegment
			for _, ph := range phases {
				if ph.Startdate >= head.Startdate && ph.Enddate <= head.Enddate {
					inside = append(inside, ph)
				}
			}
			if len(inside) > 0 {
				if line, err = withField(line, "sleep_phases", inside); err != nil {
					return nil, err
				}
			}
			out = append(out, dayLine{day: utcDay(start), line: line})
		}
	}
	return out, nil
}

// fetchSleepDetail asks sleep action=get for [start, end) in steps of at most
// 24 hours. A non fatal failure is logged and gives the segments found so
// far.
func (c *Connector) fetchSleepDetail(ctx context.Context, token string, start, end time.Time) ([]SleepPhaseSegment, error) {
	var out []SleepPhaseSegment
	for _, step := range getChunksBetween(start, end, sleepDetailSpan) {
		params := url.Values{}
		params.Set("action", "get")
		params.Set("startdate", strconv.FormatInt(step.start.Unix(), 10))
		params.Set("enddate", strconv.FormatInt(step.end.Unix(), 10))
		var body sleepDetailBody
		if err := c.call(ctx, token, "/v2/sleep", params, &body); err != nil {
			if fatal(err) {
				return nil, err
			}
			slog.Warn("withings: sleep detail failed, keeping the summary without phases", "provider", Slug, "error", err)
			return out, nil
		}
		out = append(out, body.Series...)
		if err := pause(ctx, c.pageDelay); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// syntheticNight is the line written for a night that has phase segments but
// no summary.
type syntheticNight struct {
	ID          int64               `json:"id"`
	Startdate   int64               `json:"startdate"`
	Enddate     int64               `json:"enddate"`
	State       int                 `json:"state"`
	Data        map[string]int64    `json:"data"`
	SleepPhases []SleepPhaseSegment `json:"sleep_phases"`
	Synthetic   bool                `json:"synthetic"`
}

// synthesizeNights groups segments into nights: a gap of more than two hours
// starts a new night. The start epoch serves as id. Segments of an unknown
// state are dropped (counted, one log line per call).
func synthesizeNights(phases []SleepPhaseSegment) []syntheticNight {
	sorted := append([]SleepPhaseSegment(nil), phases...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Startdate < sorted[j].Startdate })
	var nights []syntheticNight
	var cur *syntheticNight
	dropped := 0
	for _, ph := range sorted {
		key, known := sleepDurationKeys[ph.State]
		if !known {
			dropped++
			continue
		}
		if cur == nil || ph.Startdate-cur.Enddate > 7200 {
			if cur != nil {
				nights = append(nights, *cur)
			}
			cur = &syntheticNight{ID: ph.Startdate, Startdate: ph.Startdate, Enddate: ph.Enddate, State: 1, Data: map[string]int64{}, Synthetic: true}
		}
		if ph.Enddate > cur.Enddate {
			cur.Enddate = ph.Enddate
		}
		cur.SleepPhases = append(cur.SleepPhases, ph)
		cur.Data[key] += ph.Enddate - ph.Startdate
	}
	if cur != nil {
		nights = append(nights, *cur)
	}
	noteDroppedSleepStates(dropped)
	return nights
}

// sleepDurationKeys names the summary duration field of each known state.
var sleepDurationKeys = map[int]string{
	0: "wakeupduration",
	1: "lightsleepduration",
	2: "deepsleepduration",
	3: "remsleepduration",
}

// ── getworkouts ──────────────────────────────────────────────────────────────

func (c *Connector) fetchWorkouts(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for i, span := range ymdChunks(from, to, ymdChunkDays) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		err := c.pager(ctx, "getworkouts", span.first, span.last, func(offset int) (int, bool, int, error) {
			params := url.Values{}
			params.Set("action", "getworkouts")
			params.Set("startdateymd", span.first)
			params.Set("enddateymd", span.last)
			params.Set("data_fields", workoutDataFields)
			if offset > 0 {
				params.Set("offset", strconv.Itoa(offset))
			}
			var body workoutPage
			if err := c.call(ctx, token, "/v2/measure", params, &body); err != nil {
				return 0, false, 0, err
			}
			for _, w := range body.Series {
				if isNull(w) {
					continue
				}
				line, err := compact(w)
				if err != nil {
					return 0, false, 0, err
				}
				var head struct {
					Startdate int64 `json:"startdate"`
				}
				if err := json.Unmarshal(line, &head); err != nil {
					return 0, false, 0, err
				}
				out = append(out, dayLine{day: utcDay(ParseWithingsTimestamp(head.Startdate)), line: line})
			}
			return len(body.Series), body.More != 0, body.Offset, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── heart (ECG) ──────────────────────────────────────────────────────────────

// fetchHeart lists ECG recordings and downloads each waveform. A failed
// waveform download keeps the list record (the AFib classification) without
// a signal, except a rejected token or a rate limit, which stop the pull.
func (c *Connector) fetchHeart(ctx context.Context, token string, from, to time.Time) ([]dayLine, error) {
	var out []dayLine
	for i, span := range getChunksBetween(from, to, 30*24*time.Hour) {
		if i > 0 {
			if err := pause(ctx, c.chunkDelay); err != nil {
				return nil, err
			}
		}
		start := strconv.FormatInt(span.start.Unix(), 10)
		end := strconv.FormatInt(span.end.Unix(), 10)
		err := c.pager(ctx, "heart list", start, end, func(offset int) (int, bool, int, error) {
			params := url.Values{}
			params.Set("action", "list")
			params.Set("startdate", start)
			params.Set("enddate", end)
			if offset > 0 {
				params.Set("offset", strconv.Itoa(offset))
			}
			var body heartListPage
			if err := c.call(ctx, token, "/v2/heart", params, &body); err != nil {
				return 0, false, 0, err
			}
			for _, r := range body.Series {
				if isNull(r) {
					continue
				}
				line, err := compact(r)
				if err != nil {
					return 0, false, 0, err
				}
				var head struct {
					Timestamp int64    `json:"timestamp"`
					ECG       HeartECG `json:"ecg"`
				}
				if err := json.Unmarshal(line, &head); err != nil {
					return 0, false, 0, err
				}
				if head.ECG.SignalID > 0 {
					sig, err := c.fetchSignal(ctx, token, head.ECG.SignalID)
					switch {
					case err != nil && fatal(err):
						return 0, false, 0, err
					case err != nil:
						slog.Warn("withings: ECG waveform download failed, keeping the classification only", "provider", Slug, "error", err)
					case sig != nil && len(sig.Signal) > 0:
						if line, err = withField(line, "signal", sig); err != nil {
							return 0, false, 0, err
						}
					}
					if err := pause(ctx, c.pageDelay); err != nil {
						return 0, false, 0, err
					}
				}
				out = append(out, dayLine{day: utcDay(ParseWithingsTimestamp(head.Timestamp)), line: line})
			}
			return len(body.Series), body.More != 0, body.Offset, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Connector) fetchSignal(ctx context.Context, token string, signalID int64) (*HeartSignal, error) {
	params := url.Values{}
	params.Set("action", "get")
	params.Set("signalid", strconv.FormatInt(signalID, 10))
	var body HeartSignal
	if err := c.call(ctx, token, "/v2/heart", params, &body); err != nil {
		return nil, err
	}
	return &body, nil
}
