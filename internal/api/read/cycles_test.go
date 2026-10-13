// SPDX-License-Identifier: AGPL-3.0-only

package read

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flow is one menstrual_flow row at noon UTC of date, as Apple Health writes it.
func flow(date, label, provider string) flowRow {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		panic(err)
	}
	return flowRow{Type: categoryMenstrualFlow, Label: label, Provider: provider, Start: d.Add(12 * time.Hour)}
}

// flowDays returns one row per date, all medium from apple_health.
func flowDays(dates ...string) []flowRow {
	var out []flowRow
	for _, d := range dates {
		out = append(out, flow(d, "medium", "apple_health"))
	}
	return out
}

func day(s string) int {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return dayNumber(d)
}

// derive runs one call over rows (sorted by start, as the query reads them).
func derive(rows []flowRow, w cycleWindow) cycleScan {
	sorted := append([]flowRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	s := newCycleScan(w)
	for _, r := range sorted {
		if s.done {
			break
		}
		s.feed(r)
	}
	if !s.done {
		s.finish()
	}
	return s
}

func starts(cs []MenstrualCycle) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.StartDate)
	}
	return out
}

func intp(v int) *int { return &v }

func strp(v string) *string { return &v }

func TestCyclesBasic(t *testing.T) {
	rows := flowDays("2026-01-03", "2026-01-04", "2026-01-05", "2026-01-31", "2026-02-01", "2026-03-01")
	s := derive(rows, cycleWindow{limit: 10})
	require.Len(t, s.out, 3)
	assert.False(t, s.more)

	c := s.out[0]
	assert.Equal(t, "2026-01-03", c.StartDate)
	assert.Equal(t, "2026-01-05", c.PeriodEndDate)
	assert.Equal(t, 3, c.PeriodLengthDays)
	assert.Equal(t, strp("2026-01-30"), c.EndDate)
	assert.Equal(t, intp(28), c.CycleLengthDays)
	assert.Equal(t, []string{"apple_health"}, c.Providers)
	require.Len(t, c.FlowDays, 3)
	assert.Equal(t, FlowDay{Date: "2026-01-03", Flow: "medium", Providers: []string{"apple_health"}}, c.FlowDays[0])

	assert.Equal(t, intp(29), s.out[1].CycleLengthDays)
	assert.Equal(t, 2, s.out[1].PeriodLengthDays)

	// The latest cycle is open: no end, no length.
	last := s.out[2]
	assert.Equal(t, "2026-03-01", last.StartDate)
	assert.Nil(t, last.EndDate)
	assert.Nil(t, last.CycleLengthDays)
	assert.Equal(t, 1, last.PeriodLengthDays)
}

func TestCyclesGaps(t *testing.T) {
	// Up to PeriodGapDays days without flow keep one period; the gap days
	// count in its length but are not flow days.
	s := derive(flowDays("2026-01-01", "2026-01-02", "2026-01-05", "2026-01-06"), cycleWindow{limit: 10})
	require.Len(t, s.out, 1)
	assert.Equal(t, "2026-01-06", s.out[0].PeriodEndDate)
	assert.Equal(t, 6, s.out[0].PeriodLengthDays)
	assert.Len(t, s.out[0].FlowDays, 4)

	// One more day without flow starts a new period.
	s = derive(flowDays("2026-01-01", "2026-01-02", "2026-01-06"), cycleWindow{limit: 10})
	assert.Equal(t, []string{"2026-01-01", "2026-01-06"}, starts(s.out))
	assert.Equal(t, intp(5), s.out[0].CycleLengthDays)
	assert.Equal(t, PeriodGapDays, 2, "the documented gap")
}

func TestCyclesNoneAndUnknownLabels(t *testing.T) {
	rows := []flowRow{
		flow("2026-01-01", "none", "apple_health"),
		flow("2026-01-10", "light", "apple_health"),
		flow("2026-01-11", "none", "apple_health"),
		flow("2026-01-11", "", "apple_health"),  // no label: flow of unknown amount
		flow("2026-01-12", "spotting", "other"), // a label outside the list
		flow("2026-02-20", "none", "apple_health"),
	}
	s := derive(rows, cycleWindow{limit: 10})
	require.Len(t, s.out, 1, "none never makes a flow day")
	c := s.out[0]
	assert.Equal(t, "2026-01-10", c.StartDate)
	assert.Equal(t, "2026-01-12", c.PeriodEndDate)
	assert.Equal(t, []FlowDay{
		{Date: "2026-01-10", Flow: "light", Providers: []string{"apple_health"}},
		{Date: "2026-01-11", Flow: "unspecified", Providers: []string{"apple_health"}},
		{Date: "2026-01-12", Flow: "unspecified", Providers: []string{"other"}},
	}, c.FlowDays)
	assert.Nil(t, c.CycleLengthDays)
}

func TestCyclesIntermenstrualBleeding(t *testing.T) {
	rows := flowDays("2026-01-01", "2026-01-02", "2026-01-29")
	bleed := flow("2026-01-15", "", "apple_health")
	bleed.Type = "intermenstrual_bleeding"
	rows = append(rows, bleed)
	s := derive(rows, cycleWindow{limit: 10})
	assert.Equal(t, []string{"2026-01-01", "2026-01-29"}, starts(s.out))
	assert.Equal(t, intp(28), s.out[0].CycleLengthDays)
}

func TestCyclesSeveralProviders(t *testing.T) {
	rows := []flowRow{
		flow("2026-01-01", "light", "oura"),
		flow("2026-01-01", "heavy", "apple_health"),
		flow("2026-01-01", "medium", "apple_health"),
		flow("2026-01-02", "unspecified", "health_connect"),
		flow("2026-01-02", "light", "apple_health"),
	}
	s := derive(rows, cycleWindow{limit: 10})
	require.Len(t, s.out, 1)
	c := s.out[0]
	assert.Equal(t, 2, c.PeriodLengthDays, "a day reported by several providers counts once")
	assert.Equal(t, []FlowDay{
		{Date: "2026-01-01", Flow: "heavy", Providers: []string{"apple_health", "oura"}},
		{Date: "2026-01-02", Flow: "light", Providers: []string{"apple_health", "health_connect"}},
	}, c.FlowDays)
	assert.Equal(t, []string{"apple_health", "health_connect", "oura"}, c.Providers)
}

func TestCyclesLocalDay(t *testing.T) {
	// 23:30 UTC on 1 January is 2 January in Paris and 1 January in UTC.
	at := time.Date(2026, 1, 1, 23, 30, 0, 0, time.UTC)
	paris := flowRow{Type: categoryMenstrualFlow, Label: "light", Provider: "apple_health", Start: at, TZ: "Europe/Paris"}
	s := derive([]flowRow{paris}, cycleWindow{limit: 10})
	require.Len(t, s.out, 1)
	assert.Equal(t, "2026-01-02", s.out[0].StartDate)

	for _, tz := range []string{"", "Not/AZone"} {
		r := paris
		r.TZ = tz
		s = derive([]flowRow{r}, cycleWindow{limit: 10})
		assert.Equal(t, "2026-01-01", s.out[0].StartDate, "no usable zone is UTC: %q", tz)
	}

	// 01:00 UTC on 2 January is 1 January in New York (UTC-5): one day with
	// the 1 January sample, not two.
	ny := flowRow{Type: categoryMenstrualFlow, Label: "heavy", Provider: "health_connect",
		Start: time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC), TZ: "America/New_York"}
	s = derive([]flowRow{flow("2026-01-01", "light", "apple_health"), ny}, cycleWindow{limit: 10})
	require.Len(t, s.out, 1)
	assert.Equal(t, []FlowDay{{Date: "2026-01-01", Flow: "heavy", Providers: []string{"apple_health", "health_connect"}}},
		s.out[0].FlowDays)
}

func TestCyclesWindow(t *testing.T) {
	rows := flowDays("2026-01-01", "2026-01-02", "2026-01-29", "2026-01-30", "2026-02-26", "2026-03-27")

	// [start, end) selects cycles by their start day; the cycle at the edge
	// still gets its length from the next period, outside the window.
	s := derive(rows, cycleWindow{lower: day("2026-01-15"), hasLower: true, end: day("2026-02-01"), hasEnd: true, limit: 10})
	assert.Equal(t, []string{"2026-01-29"}, starts(s.out))
	assert.Equal(t, intp(28), s.out[0].CycleLengthDays)
	assert.False(t, s.more)

	// A window starting inside a period does not cut it into a new cycle.
	s = derive(rows, cycleWindow{lower: day("2026-01-30"), hasLower: true, limit: 10})
	assert.Equal(t, []string{"2026-02-26", "2026-03-27"}, starts(s.out))
	s = derive(rows, cycleWindow{lower: day("2026-01-02"), hasLower: true, end: day("2026-01-29"), hasEnd: true, limit: 10})
	assert.Empty(t, s.out)
}

func TestCyclesPaging(t *testing.T) {
	rows := flowDays("2026-01-01", "2026-01-02", "2026-01-29", "2026-01-30", "2026-02-26", "2026-03-27", "2026-03-28")
	var got []MenstrualCycle
	w := cycleWindow{limit: 1}
	for range 10 {
		s := derive(rows, w)
		got = append(got, s.out...)
		if !s.more {
			break
		}
		require.Len(t, s.out, 1)
		// The next call starts the day after the last cycle's start.
		w.lower, w.hasLower = day(s.out[len(s.out)-1].StartDate)+1, true
	}
	assert.Equal(t, []string{"2026-01-01", "2026-01-29", "2026-02-26", "2026-03-27"}, starts(got))
	assert.Equal(t, []*int{intp(28), intp(28), intp(29), nil},
		[]*int{got[0].CycleLengthDays, got[1].CycleLengthDays, got[2].CycleLengthDays, got[3].CycleLengthDays})
	assert.Equal(t, 2, got[3].PeriodLengthDays)

	// A full page whose next cycle lies past the window end has no next page.
	s := derive(rows, cycleWindow{limit: 2, end: day("2026-02-26"), hasEnd: true})
	assert.Equal(t, []string{"2026-01-01", "2026-01-29"}, starts(s.out))
	assert.False(t, s.more)
	assert.Equal(t, intp(28), s.out[1].CycleLengthDays)
	s = derive(rows, cycleWindow{limit: 2, end: day("2026-02-27"), hasEnd: true})
	assert.True(t, s.more)
}

func TestCyclesTruncated(t *testing.T) {
	rows := flowDays("2026-01-01", "2026-01-29", "2026-02-26")
	s := newCycleScan(cycleWindow{limit: 10})
	for _, r := range rows {
		s.feed(r)
	}
	s.truncate()
	// The cycle still in progress is dropped; the next call continues after
	// the last complete one.
	assert.Equal(t, []string{"2026-01-01"}, starts(s.out))
	assert.True(t, s.more)

	s = newCycleScan(cycleWindow{limit: 10})
	s.feed(rows[0])
	s.truncate()
	assert.Empty(t, s.out)
	assert.False(t, s.more)
}

func TestCyclesEmpty(t *testing.T) {
	s := derive(nil, cycleWindow{limit: 10})
	assert.Empty(t, s.out)
	assert.NotNil(t, s.out)
	assert.False(t, s.more)
}
