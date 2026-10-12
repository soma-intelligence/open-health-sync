// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
	"strconv"
	"time"
	// Embed the zone database so LoadLocation works in minimal images.
	_ "time/tzdata"
)

// parseTime reads an Oura timestamp: ISO 8601 with a UTC offset, with or
// without fractional seconds.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %s is not RFC 3339", strconv.Quote(clip(s, 40)))
	}
	if !plausible(t) {
		return time.Time{}, fmt.Errorf("timestamp %s is out of range", strconv.Quote(clip(s, 40)))
	}
	return t, nil
}

// minYear and maxYear bound the instants a record may carry. Oura has no
// data from before 2000, and a year past 2199 (or one the RFC 3339 form
// cannot write, 0 or 10000) is a corrupt value: a record built on it could
// not even be encoded.
const (
	minYear = 2000
	maxYear = 2199
)

// plausible reports whether t falls in [minYear, maxYear] in UTC and in its
// own zone.
func plausible(t time.Time) bool {
	for _, y := range []int{t.Year(), t.UTC().Year()} {
		if y < minYear || y > maxYear {
			return false
		}
	}
	return true
}

// clip shortens s to at most n bytes, so a malformed provider value cannot
// make an error message (and a log line) arbitrarily long.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// offsetZone names a zone for the offset a timestamp was recorded at. Oura
// gives a UTC offset, never an IANA name, so the name is one whose offset at
// that instant is the same, which is all the ETL needs to put the record on
// the user's local day:
//
//   - a whole number of hours: the fixed-offset IANA zone, "UTC", or
//     "Etc/GMT-1" for +01:00 (the Etc sign is inverted);
//   - a fraction of an hour (+05:30, +05:45, -03:30...): the IANA zone of a
//     place that uses that offset, checked at t ("Asia/Kolkata" for +05:30).
//     The name says nothing about where the user is; utc_offset keeps the
//     offset as Oura wrote it.
//
// An offset no zone has at t gives "" (the ETL then falls back to UTC). The
// second result is the offset as Oura wrote it ("+01:00"), kept in metadata.
func offsetZone(t time.Time) (name, offset string) {
	_, secs := t.Zone()
	offset = t.Format("-07:00")
	if secs == 0 {
		return "UTC", offset
	}
	if secs%3600 != 0 {
		return fractionalZone(t, secs), offset
	}
	h := secs / 3600
	if h < -12 || h > 14 {
		return "", offset
	}
	if h > 0 {
		return fmt.Sprintf("Etc/GMT-%d", h), offset
	}
	return fmt.Sprintf("Etc/GMT+%d", -h), offset
}

// fractionalOffsetZones are IANA zones that use an offset which is not a
// whole number of hours, in standard or in daylight saving time. A place that
// uses an offset only part of the year is listed after the one that uses it
// all year.
var fractionalOffsetZones = []string{
	"Pacific/Marquesas",   // -09:30
	"America/St_Johns",    // -03:30, -02:30 in summer
	"Asia/Tehran",         // +03:30
	"Asia/Kabul",          // +04:30
	"Asia/Kolkata",        // +05:30
	"Asia/Kathmandu",      // +05:45
	"Asia/Yangon",         // +06:30
	"Australia/Eucla",     // +08:45
	"Australia/Darwin",    // +09:30
	"Australia/Adelaide",  // +09:30, +10:30 in summer
	"Australia/Lord_Howe", // +10:30, +11:00 in summer
	"Pacific/Chatham",     // +12:45, +13:45 in summer
}

// fractionalZone returns the first zone of fractionalOffsetZones whose offset
// at t is secs, or "".
func fractionalZone(t time.Time, secs int) string {
	for _, name := range fractionalOffsetZones {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		if _, off := t.In(loc).Zone(); off == secs {
			return name
		}
	}
	return ""
}

// zoneMetadata sets metadata "timezone" (and "utc_offset") from the offset of
// t. It returns the location the zone names, UTC when it has none.
func zoneMetadata(md map[string]any, t time.Time) *time.Location {
	name, offset := offsetZone(t)
	md["utc_offset"] = offset
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	md["timezone"] = name
	return loc
}

// localDay returns the span of the calendar day day ("2006-01-02") in loc:
// local midnight to one second before the next.
func localDay(day string, loc *time.Location) (time.Time, time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, day, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("day %s is not YYYY-MM-DD", strconv.Quote(clip(day, 40)))
	}
	if !plausible(d) || !plausible(d.AddDate(0, 0, 1)) {
		return time.Time{}, time.Time{}, fmt.Errorf("day %s is out of range", strconv.Quote(clip(day, 40)))
	}
	return d, d.AddDate(0, 0, 1).Add(-time.Second), nil
}

// utcDay is t's calendar day in UTC, at midnight. Raw objects are grouped by
// it.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
