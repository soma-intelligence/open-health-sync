// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embed the zone database so LoadLocation works in minimal images.
	_ "time/tzdata"
)

// parseTime reads a WHOOP timestamp (RFC 3339, milliseconds, UTC) as UTC.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// offsetSeconds reads a WHOOP timezone_offset ("+HH:MM", "-HH:MM" or "Z").
func offsetSeconds(s string) (int, bool) {
	if s == "Z" {
		return 0, true
	}
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[1:3])
	m, err2 := strconv.Atoi(s[4:6])
	if err1 != nil || err2 != nil || h > 14 || m > 59 {
		return 0, false
	}
	secs := h*3600 + m*60
	if s[0] == '-' {
		secs = -secs
	}
	return secs, true
}

// offsetZone is the fixed zone of a timezone_offset, or UTC when it is
// missing or unreadable.
func offsetZone(s string) *time.Location {
	secs, ok := offsetSeconds(s)
	if !ok {
		return time.UTC
	}
	return time.FixedZone(s, secs)
}

// ianaForOffset names a whole hour offset with its IANA Etc zone, whose sign
// is inverted by convention (+02:00 is Etc/GMT-2). WHOOP sends an offset, not
// a zone name, so this is the closest IANA name that keeps the local day
// right; a half hour offset (+05:30) has none and gives "".
func ianaForOffset(s string) string {
	secs, ok := offsetSeconds(s)
	if !ok || secs%3600 != 0 {
		return ""
	}
	h := secs / 3600
	var name string
	switch {
	case h == 0:
		name = "Etc/UTC"
	case h > 0:
		name = "Etc/GMT-" + strconv.Itoa(h)
	default:
		name = "Etc/GMT+" + strconv.Itoa(-h)
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

// withTimezone records the WHOOP offset as "timezone_offset" and, when it
// has one, its IANA name as "timezone" (R17), which the ETL reads for the
// local day.
func withTimezone(md map[string]any, offset string) map[string]any {
	offset = strings.TrimSpace(offset)
	if offset == "" {
		return md
	}
	if md == nil {
		md = map[string]any{}
	}
	md["timezone_offset"] = offset
	if name := ianaForOffset(offset); name != "" {
		md["timezone"] = name
	}
	return md
}

// cycleDay is the local calendar day a cycle belongs to, as midnight UTC. A
// WHOOP cycle starts when the user falls asleep, usually the evening before
// the day it scores, so the day is the local date twelve hours after the
// start: a cycle starting at 23:00 is the next day's, one starting at 01:00
// that same day's.
func cycleDay(start time.Time, offset string) time.Time {
	return dateOnly(start.In(offsetZone(offset)).Add(12 * time.Hour))
}

// dateOnly is t's wall clock date as midnight UTC.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// utcDay is t's calendar day in UTC, at midnight. Raw objects are grouped by
// it.
func utcDay(t time.Time) time.Time {
	return dateOnly(t.UTC())
}
