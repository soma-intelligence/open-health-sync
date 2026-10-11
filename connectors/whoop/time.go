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

// offsetSeconds reads a WHOOP timezone_offset, documented as the TZD format
// of ISO 8601: "+hh:mm", "-hh:mm" or "Z".
func offsetSeconds(s string) (int, bool) {
	if s == "Z" {
		return 0, true
	}
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' ||
		!digits(s[1:3]) || !digits(s[4:6]) {
		return 0, false
	}
	h, _ := strconv.Atoi(s[1:3])
	m, _ := strconv.Atoi(s[4:6])
	secs := h*3600 + m*60
	// Offsets in use run from -12:00 to +14:00; past ±14:00 is corrupt.
	if m > 59 || secs > 14*3600 {
		return 0, false
	}
	if s[0] == '-' {
		secs = -secs
	}
	return secs, true
}

// digits reports whether s is made of ASCII digits only (strconv.Atoi would
// also take a sign).
func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
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

// ianaForOffset names the zone the ETL reads the local day in, for a WHOOP
// timezone_offset in force at instant at. WHOOP sends an offset, not a zone
// name.
//
// A whole hour offset takes its IANA Etc zone, whose sign is inverted by
// convention (+02:00 is Etc/GMT-2): a fixed offset, right whatever the date.
// The other offsets in use (+05:30, +05:45, -03:30, ...) have no Etc zone, so
// they take a zone that had exactly that offset at that instant (+05:30 is
// Asia/Kolkata). The zone is checked at the record's own time, so the local
// day comes out right across a daylight saving change too. The name says
// which offset applied, not where the user was. An offset no listed zone had
// at that instant, or an unreadable one, gives "" (the ETL then uses UTC).
func ianaForOffset(s string, at time.Time) string {
	secs, ok := offsetSeconds(s)
	if !ok {
		return ""
	}
	if secs%3600 == 0 {
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
	for _, name := range fractionalZones[secs] {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		if _, off := at.In(loc).Zone(); off == secs {
			return name
		}
	}
	return ""
}

// fractionalZones lists, for each offset in use that is not a whole number of
// hours, zones that use it as standard or daylight saving time. The first one
// whose offset at the record's instant matches is taken.
var fractionalZones = map[int][]string{
	-(9*3600 + 30*60): {"Pacific/Marquesas"},
	-(3*3600 + 30*60): {"America/St_Johns"},
	-(2*3600 + 30*60): {"America/St_Johns"},
	3*3600 + 30*60:    {"Asia/Tehran"},
	4*3600 + 30*60:    {"Asia/Kabul"},
	5*3600 + 30*60:    {"Asia/Kolkata", "Asia/Colombo"},
	5*3600 + 45*60:    {"Asia/Kathmandu"},
	6*3600 + 30*60:    {"Asia/Yangon", "Indian/Cocos"},
	8*3600 + 45*60:    {"Australia/Eucla"},
	9*3600 + 30*60:    {"Australia/Darwin", "Australia/Adelaide"},
	10*3600 + 30*60:   {"Australia/Adelaide", "Australia/Lord_Howe"},
	12*3600 + 45*60:   {"Pacific/Chatham"},
	13*3600 + 45*60:   {"Pacific/Chatham"},
}

// withTimezone records the WHOOP offset as "timezone_offset" and, when it
// has one, the zone ianaForOffset names for it at instant at as "timezone"
// (R17), which the ETL reads for the local day.
func withTimezone(md map[string]any, offset string, at time.Time) map[string]any {
	offset = strings.TrimSpace(offset)
	if offset == "" {
		return md
	}
	if md == nil {
		md = map[string]any{}
	}
	md["timezone_offset"] = offset
	if name := ianaForOffset(offset, at); name != "" {
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
