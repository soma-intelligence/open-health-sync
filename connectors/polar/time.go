// SPDX-License-Identifier: AGPL-3.0-only

package polar

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embed the zone database so the Etc/GMT zones load in minimal images.
	_ "time/tzdata"
)

// localLayouts are the offset-less timestamps AccessLink uses for local
// wall clock times.
var localLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
}

// parseTime reads an AccessLink timestamp. One with an offset or a Z is
// returned as that instant (hasZone true); one without is a local wall clock
// time, returned with its fields in UTC (hasZone false).
func parseTime(s string) (t time.Time, hasZone bool, err error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true, nil
	}
	for _, l := range localLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("polar: time %q not understood", s)
}

// parseDate reads a YYYY-MM-DD date at midnight UTC.
func parseDate(s string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("polar: date %q not understood", s)
	}
	return d, nil
}

// parseISODuration reads the ISO 8601 durations AccessLink sends
// ("PT2H44M", "PT210.026S", "P1DT2H"). Years, months and weeks are refused:
// AccessLink does not use them and their length is ambiguous.
func parseISODuration(s string) (time.Duration, error) {
	bad := fmt.Errorf("polar: duration %q not understood", s)
	if len(s) < 2 || s[0] != 'P' {
		return 0, bad
	}
	rest := s[1:]
	inTime := false
	var total float64
	seen := false
	for len(rest) > 0 {
		if rest[0] == 'T' {
			if inTime {
				return 0, bad
			}
			inTime = true
			rest = rest[1:]
			continue
		}
		i := 0
		for i < len(rest) && (rest[i] >= '0' && rest[i] <= '9' || rest[i] == '.') {
			i++
		}
		if i == 0 || i == len(rest) {
			return 0, bad
		}
		n, err := strconv.ParseFloat(rest[:i], 64)
		if err != nil {
			return 0, bad
		}
		unit := rest[i]
		rest = rest[i+1:]
		switch {
		case !inTime && unit == 'D':
			total += n * 86400
		case inTime && unit == 'H':
			total += n * 3600
		case inTime && unit == 'M':
			total += n * 60
		case inTime && unit == 'S':
			total += n
		default:
			return 0, bad
		}
		seen = true
	}
	if !seen {
		return 0, bad
	}
	return time.Duration(total * float64(time.Second)), nil
}

// offsetZone names the IANA zone of a fixed UTC offset in minutes: "Etc/UTC"
// or "Etc/GMT-3" for +03:00 (the Etc names invert the sign). An offset that
// is not a whole hour has no such zone and gives "". The zone carries the
// offset of that moment only, not daylight saving rules.
func offsetZone(minutes int) string {
	if minutes == 0 {
		return "Etc/UTC"
	}
	if minutes%60 != 0 {
		return ""
	}
	h := minutes / 60
	if h < -12 || h > 14 {
		return ""
	}
	if h > 0 {
		return "Etc/GMT-" + strconv.Itoa(h)
	}
	return "Etc/GMT+" + strconv.Itoa(-h)
}

// offsetMinutes is the UTC offset of t's zone in minutes.
func offsetMinutes(t time.Time) int {
	_, off := t.Zone()
	return off / 60
}

// clockOnDay places an "HH:MM" or "HH:MM:SS" wall clock time on day (a
// midnight UTC date), as a time whose fields are local wall clock.
func clockOnDay(day time.Time, clock string) (time.Time, error) {
	parts := strings.Split(clock, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return time.Time{}, fmt.Errorf("polar: clock time %q not understood", clock)
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("polar: clock time %q not understood", clock)
		}
		v[i] = n
	}
	if v[0] > 23 || v[1] > 59 || v[2] > 59 {
		return time.Time{}, fmt.Errorf("polar: clock time %q not understood", clock)
	}
	return day.Add(time.Duration(v[0])*time.Hour + time.Duration(v[1])*time.Minute + time.Duration(v[2])*time.Second), nil
}

// utcDay is t's calendar day in UTC, at midnight. Raw objects are grouped by
// it.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ymdSpan is one request window of a calendar date endpoint, both dates
// inclusive.
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
