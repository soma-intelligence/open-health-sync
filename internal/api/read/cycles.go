// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"
	_ "time/tzdata" // a sample's zone must resolve on a host without a zone database

	"github.com/google/uuid"
)

// Menstrual cycles are derived at read time from the menstrual_flow category
// samples, so they always agree with the stored samples, deletions included.
// docs/menstrual-cycles.md states the rules; in short:
//
//   - A flow day is a local calendar day (the sample's "timezone" metadata,
//     else UTC) with at least one menstrual_flow sample whose label is not
//     "none". Other category types, intermenstrual bleeding included, are
//     not read.
//   - A period is a run of flow days in which no gap is longer than
//     PeriodGapDays days.
//   - A cycle starts on a period's first day and ends the day before the
//     next period's first day. The latest cycle is open: no end, no length.
//
// Nothing is predicted or estimated: every field restates recorded days.

// PeriodGapDays is the longest run of days without recorded flow that still
// keeps two flow days in one period. A day or two without a log inside a
// period is common, and no cycle is that short, so a gap this size is never
// a new period.
const PeriodGapDays = 2

// MaxCycleScanSamples bounds the flow samples one call reads from its lower
// bound on (the few days read before it are not counted). A page that needs
// more ends early with a next_cursor; a single cycle that needs more is
// refused. A variable so tests can lower it.
var MaxCycleScanSamples = 50000

// cycleChunk is how many samples one query of a call reads.
var cycleChunk = 1000

const categoryMenstrualFlow = "menstrual_flow"

// flowRank orders the flow labels; the heaviest of a day is kept. A label
// outside the list (or none) is flow of unknown amount, "unspecified".
var flowRank = map[string]int{"unspecified": 1, "light": 2, "medium": 3, "heavy": 4}

// FlowDay is one local calendar day with recorded menstrual flow.
type FlowDay struct {
	Date string `json:"date"` // YYYY-MM-DD, the user's local day
	// Flow is the heaviest label recorded that day: unspecified, light,
	// medium or heavy.
	Flow string `json:"flow"`
	// Providers reported flow that day, sorted.
	Providers []string `json:"providers"`
}

// MenstrualCycle is one cycle derived from the recorded flow days.
type MenstrualCycle struct {
	// StartDate is the first day of the cycle's period.
	StartDate string `json:"start_date"`
	// PeriodEndDate is the period's last flow day.
	PeriodEndDate    string `json:"period_end_date"`
	PeriodLengthDays int    `json:"period_length_days"`
	// EndDate is the day before the next period starts; nil while the cycle
	// is open (no later period recorded).
	EndDate         *string `json:"end_date"`
	CycleLengthDays *int    `json:"cycle_length_days"`
	// Providers reported at least one of the period's flow days, sorted.
	Providers []string `json:"providers"`
	// FlowDays are the period's flow days in order; a gap day is absent.
	FlowDays []FlowDay `json:"flow_days"`
}

// MenstrualCycles derives the user's menstrual cycles from the stored
// menstrual_flow samples, oldest first. Start and End compare as calendar
// dates and select cycles by their start day; q.Type is ignored. The cursor
// is the start day of the previous page's last cycle.
func MenstrualCycles(ctx context.Context, db Querier, tenantID, userID uuid.UUID, q Query) (Page[MenstrualCycle], error) {
	n, err := q.normalize(false)
	if err != nil {
		return Page[MenstrualCycle]{}, err
	}
	if err := userExists(ctx, db, tenantID, userID); err != nil {
		return Page[MenstrualCycle]{}, err
	}
	w := cycleWindow{limit: n.Limit}
	if !n.Start.IsZero() {
		w.lower, w.hasLower = dayNumber(n.Start.UTC()), true
	}
	if n.paged {
		if after := dayNumber(n.afterT.UTC()) + 1; !w.hasLower || after > w.lower {
			w.lower, w.hasLower = after, true
		}
	}
	if !n.End.IsZero() {
		w.end, w.hasEnd = dayNumber(n.End.UTC()), true
	}

	s := newCycleScan(w)
	var from time.Time
	if w.hasLower {
		// A flow day within PeriodGapDays+1 days before the lower bound
		// decides whether the bound's first flow day starts a period. A
		// sample's local day is at most 14 hours ahead of UTC, so reading
		// one more UTC day covers every sample of those days.
		from = dayTime(w.lower - PeriodGapDays - 2)
	}
	var lastT time.Time
	var lastID int64
	// scanned counts toward MaxCycleScanSamples: the samples of the days
	// before the lower bound, read only to place it, do not.
	scanned := 0
	for first := true; ; first = false {
		size, probe := cycleChunk, false
		if rem := MaxCycleScanSamples - scanned; rem <= 0 {
			size, probe = 1, true
		} else if rem < size {
			size = rem
		}
		args := []any{tenantID, userID, categoryMenstrualFlow}
		where := "tenant_id = $1 AND user_id = $2 AND category_type = $3"
		switch {
		case !first:
			args = append(args, lastT, lastID)
			where += " AND start_date >= $4 AND (start_date, id) > ($4, $5)"
		case w.hasLower:
			args = append(args, from)
			where += " AND start_date >= $4"
		}
		rows, err := db.Query(ctx, `SELECT id, start_date, coalesce(value_label, ''), provider,
				coalesce(metadata->>'timezone', '')
			FROM ohs_data.category_samples WHERE `+where+`
			ORDER BY start_date, id LIMIT `+strconv.Itoa(size), args...)
		if err != nil {
			return Page[MenstrualCycle]{}, fmt.Errorf("read: menstrual cycles: %w", err)
		}
		got, counted := 0, 0
		for rows.Next() {
			r := flowRow{Type: categoryMenstrualFlow}
			if err := rows.Scan(&lastID, &r.Start, &r.Label, &r.Provider, &r.TZ); err != nil {
				rows.Close()
				return Page[MenstrualCycle]{}, fmt.Errorf("read: menstrual cycles: scan: %w", err)
			}
			lastT = r.Start
			got++
			if !w.hasLower || !r.Start.Before(dayTime(w.lower)) {
				counted++
			}
			if probe {
				break
			}
			s.feed(r)
			if s.done {
				break
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Page[MenstrualCycle]{}, fmt.Errorf("read: menstrual cycles: %w", err)
		}
		if s.done {
			break
		}
		if probe {
			if got > 0 {
				s.truncate()
			} else {
				s.finish()
			}
			break
		}
		scanned += counted
		if got < size {
			s.finish()
			break
		}
	}
	if s.truncated && len(s.out) == 0 {
		return Page[MenstrualCycle]{}, &QueryError{Msg: fmt.Sprintf(
			"more than %d menstrual flow samples in one cycle: the cycle cannot be derived", MaxCycleScanSamples)}
	}
	out := Page[MenstrualCycle]{Records: s.out}
	if s.more {
		last, _ := time.Parse(time.DateOnly, s.out[len(s.out)-1].StartDate)
		out.NextCursor = EncodeCursor(last, 0)
	}
	return out, nil
}

// flowRow is one stored category sample, as the scan reads it.
type flowRow struct {
	Type     string
	Label    string
	Provider string
	Start    time.Time
	TZ       string // IANA name from the "timezone" metadata, or ""
}

// cycleWindow is what one call selects: cycles whose start day is in
// [lower, end), at most limit of them. Days are dayNumber values.
type cycleWindow struct {
	lower, end       int
	hasLower, hasEnd bool
	limit            int
}

type pendingDay struct {
	rank      int
	providers map[string]bool
}

// cycleScan derives cycles from rows fed in start order. A local day is
// complete once a row starts two UTC days after it (no zone is more than 14
// hours from UTC); complete days are folded into periods in order, and the
// scan is done as soon as the page is known.
type cycleScan struct {
	w       cycleWindow
	zones   map[string]*time.Location
	pending map[int]*pendingDay

	prev    int // last complete flow day
	hasPrev bool
	cur     *MenstrualCycle // the cycle being built; nil inside a period that started before lower

	out       []MenstrualCycle
	done      bool // the page is known: stop reading
	more      bool // another cycle follows in the window
	truncated bool
}

func newCycleScan(w cycleWindow) cycleScan {
	return cycleScan{w: w, zones: map[string]*time.Location{}, pending: map[int]*pendingDay{}, out: []MenstrualCycle{}}
}

// feed adds one row, then folds the days it proves complete.
func (s *cycleScan) feed(r flowRow) {
	if r.Type == categoryMenstrualFlow && r.Label != "none" {
		d := dayNumber(r.Start.In(s.zone(r.TZ)))
		if !s.w.hasLower || d >= s.w.lower-PeriodGapDays-1 {
			p := s.pending[d]
			if p == nil {
				p = &pendingDay{providers: map[string]bool{}}
				s.pending[d] = p
			}
			p.rank = max(p.rank, rankOf(r.Label))
			p.providers[r.Provider] = true
		}
	}
	s.fold(dayNumber(r.Start.UTC()) - 1)
}

// finish folds every day: the data is complete, the last cycle is open.
func (s *cycleScan) finish() {
	s.fold(int(^uint(0) >> 1))
	if !s.done && s.cur != nil {
		s.out = append(s.out, *s.cur)
		s.cur = nil
	}
	s.done = true
}

// truncate ends a scan that hit MaxCycleScanSamples: the cycle in progress
// is dropped and the next page starts after the last complete one.
func (s *cycleScan) truncate() {
	s.truncated = true
	if !s.done {
		s.more = len(s.out) > 0
	}
	s.done = true
}

// fold processes the pending days before day, in order.
func (s *cycleScan) fold(before int) {
	if s.done || len(s.pending) == 0 {
		return
	}
	var days []int
	for d := range s.pending {
		if d < before {
			days = append(days, d)
		}
	}
	slices.Sort(days)
	for _, d := range days {
		p := s.pending[d]
		delete(s.pending, d)
		s.day(d, p)
		if s.done {
			return
		}
	}
}

// day adds one complete flow day.
func (s *cycleScan) day(d int, p *pendingDay) {
	inPeriod := s.hasPrev && d-s.prev <= PeriodGapDays+1
	s.prev, s.hasPrev = d, true
	if !inPeriod {
		// A new period: it closes the cycle in progress.
		if s.cur != nil {
			end, length := dayString(d-1), d-day0(s.cur.StartDate)
			s.cur.EndDate, s.cur.CycleLengthDays = &end, &length
			s.out = append(s.out, *s.cur)
			s.cur = nil
		}
		switch {
		case s.w.hasLower && d < s.w.lower:
			return // started before the window or the cursor
		case s.w.hasEnd && d >= s.w.end:
			s.done = true
			return
		case len(s.out) == s.w.limit:
			s.done, s.more = true, true
			return
		}
		s.cur = &MenstrualCycle{StartDate: dayString(d), Providers: []string{}, FlowDays: []FlowDay{}}
	}
	if s.cur == nil {
		return
	}
	fd := FlowDay{Date: dayString(d), Flow: labelOf(p.rank), Providers: sortedKeys(p.providers)}
	s.cur.FlowDays = append(s.cur.FlowDays, fd)
	s.cur.PeriodEndDate = fd.Date
	s.cur.PeriodLengthDays = d - day0(s.cur.StartDate) + 1
	for _, pr := range fd.Providers {
		if !slices.Contains(s.cur.Providers, pr) {
			s.cur.Providers = append(s.cur.Providers, pr)
		}
	}
	slices.Sort(s.cur.Providers)
}

func (s *cycleScan) zone(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, ok := s.zones[name]; ok {
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	s.zones[name] = loc
	return loc
}

func rankOf(label string) int {
	if r, ok := flowRank[label]; ok {
		return r
	}
	return flowRank["unspecified"]
}

func labelOf(rank int) string {
	for l, r := range flowRank {
		if r == rank {
			return l
		}
	}
	return "unspecified"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dayNumber is the calendar date of t, in t's location, as days since
// 1970-01-01.
func dayNumber(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// dayTime is midnight UTC of a dayNumber.
func dayTime(n int) time.Time { return time.Unix(int64(n)*86400, 0).UTC() }

func dayString(n int) string { return dayTime(n).Format(time.DateOnly) }

// day0 reverses dayString.
func day0(s string) int {
	t, _ := time.Parse(time.DateOnly, s)
	return dayNumber(t)
}
