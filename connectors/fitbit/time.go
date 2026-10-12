// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embed the zone database so LoadLocation works in minimal images.
	_ "time/tzdata"
)

// minYear and maxYear bound the instants a record may carry.
const (
	minYear = 2000
	maxYear = 2199
)

// plausible reports whether t falls in [minYear, maxYear].
func plausible(t time.Time) bool {
	y := t.UTC().Year()
	return y >= minYear && y <= maxYear
}

// parseTime reads an RFC 3339 timestamp (google-datetime: UTC, "Z", with up
// to nine fractional digits).
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %s is not RFC 3339", strconv.Quote(clip(s, 40)))
	}
	if !plausible(t) {
		return time.Time{}, fmt.Errorf("timestamp %s is out of range", strconv.Quote(clip(s, 40)))
	}
	return t.UTC(), nil
}

// parseDuration reads a google-duration ("3600s", "-25200s", "1.5s").
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, "s") {
		return 0, fmt.Errorf("duration %s has no seconds suffix", strconv.Quote(clip(s, 40)))
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
	if err != nil {
		return 0, fmt.Errorf("duration %s is not a number of seconds", strconv.Quote(clip(s, 40)))
	}
	if f > 1e9 || f < -1e9 {
		return 0, fmt.Errorf("duration %s is out of range", strconv.Quote(clip(s, 40)))
	}
	return time.Duration(f * float64(time.Second)), nil
}

// zoneMetadata records the UTC offset a point was observed at
// (startUtcOffset or utcOffset, a google-duration): "utc_offset" as written
// (+01:00 form) and, when the offset maps to a zone, "timezone". The API
// gives an offset, never an IANA name, so the name is one with the same
// offset at t (see offsetZone), which is all the ETL needs to find the
// user's local day. A missing or unreadable offset sets nothing.
func zoneMetadata(md map[string]any, t time.Time, offset string) {
	if offset == "" {
		return
	}
	d, err := parseDuration(offset)
	if err != nil {
		return
	}
	secs := int(d / time.Second)
	if secs < -14*3600 || secs > 14*3600 {
		return
	}
	md["utc_offset"] = formatOffset(secs)
	if name := offsetZone(t, secs); name != "" {
		md["timezone"] = name
	}
}

// formatOffset writes secs as "+01:00".
func formatOffset(secs int) string {
	sign := '+'
	if secs < 0 {
		sign, secs = '-', -secs
	}
	return fmt.Sprintf("%c%02d:%02d", sign, secs/3600, secs%3600/60)
}

// offsetZone names a zone whose offset at t is secs: "UTC", a fixed-offset
// Etc zone for whole hours ("Etc/GMT-1" for +01:00, the Etc sign is
// inverted), or a place using that fraction of an hour ("Asia/Kolkata" for
// +05:30). "" when none has it.
func offsetZone(t time.Time, secs int) string {
	if secs == 0 {
		return "UTC"
	}
	if secs%3600 != 0 {
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
	h := secs / 3600
	if h < -12 || h > 14 {
		return ""
	}
	if h > 0 {
		return fmt.Sprintf("Etc/GMT-%d", h)
	}
	return fmt.Sprintf("Etc/GMT+%d", -h)
}

// fractionalOffsetZones are IANA zones that use an offset which is not a
// whole number of hours.
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

// civilDate is a google.type.Date: the user's calendar day.
type civilDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

// time returns the date at midnight UTC.
func (d civilDate) time() (time.Time, error) {
	if d.Year == 0 || d.Month < 1 || d.Month > 12 || d.Day < 1 || d.Day > 31 {
		return time.Time{}, fmt.Errorf("incomplete date %04d-%02d-%02d", d.Year, d.Month, d.Day)
	}
	t := time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
	if t.Day() != d.Day || !plausible(t) {
		return time.Time{}, fmt.Errorf("date %04d-%02d-%02d is not valid", d.Year, d.Month, d.Day)
	}
	return t, nil
}

// utcDay is t's calendar day in UTC, at midnight.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
