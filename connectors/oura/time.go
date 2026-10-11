// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"fmt"
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
		return time.Time{}, fmt.Errorf("timestamp %q: %w", s, err)
	}
	return t, nil
}

// offsetZone names the zone a timestamp was recorded in. Oura gives a UTC
// offset, never an IANA name, so the name is the fixed-offset IANA zone of
// that offset: "UTC", or "Etc/GMT-1" for +01:00 (the Etc sign is inverted).
// An offset that is not a whole number of hours, or out of the Etc range,
// has no IANA name and gives "" (the ETL then falls back to UTC). The second
// result is the offset as Oura wrote it ("+01:00"), kept in metadata.
func offsetZone(t time.Time) (name, offset string) {
	_, secs := t.Zone()
	offset = t.Format("-07:00")
	if secs == 0 {
		return "UTC", offset
	}
	if secs%3600 != 0 {
		return "", offset
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
		return time.Time{}, time.Time{}, fmt.Errorf("day %q: %w", day, err)
	}
	return d, d.AddDate(0, 0, 1).Add(-time.Second), nil
}

// utcDay is t's calendar day in UTC, at midnight. Raw objects are grouped by
// it.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
