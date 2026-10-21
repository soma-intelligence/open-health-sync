// SPDX-License-Identifier: AGPL-3.0-only

package garmin

import (
	"fmt"
	"strconv"
	"time"
)

// Garmin times are Unix seconds (UTC) plus, separately, the offset of the
// user's local time in seconds (startTimeOffsetInSeconds). Garmin gives no
// IANA zone name.

// Bounds of a plausible Garmin timestamp: 2000-01-01 to 2200-01-01.
const (
	minUnix = 946684800
	maxUnix = 7258118400
)

// maxOffset bounds a plausible UTC offset (UTC-12 to UTC+14, with margin).
const maxOffset = 18 * 3600

// unixTime converts Unix seconds, refusing what cannot be a Garmin time.
func unixTime(name string, secs *int64) (time.Time, error) {
	if secs == nil {
		return time.Time{}, fmt.Errorf("missing %s", name)
	}
	if *secs < minUnix || *secs > maxUnix {
		return time.Time{}, fmt.Errorf("%s out of range", name)
	}
	return time.Unix(*secs, 0).UTC(), nil
}

// seconds returns a duration in seconds, or def when it is missing or not
// plausible (negative or over two days).
func seconds(v *int64, def int64) int64 {
	if v == nil || *v < 0 || *v > 2*86400 {
		return def
	}
	return *v
}

// offsetOf returns a plausible UTC offset in seconds.
func offsetOf(v *int64) (int64, bool) {
	if v == nil || *v < -maxOffset || *v > maxOffset {
		return 0, false
	}
	return *v, true
}

// withOffset records the UTC offset in md ("utc_offset_seconds"), plus the
// matching fixed Etc/GMT zone as "timezone" when the offset is a whole hour,
// so the ETL derives the local day. The zone names the offset of that moment
// only, not daylight saving rules.
func withOffset(md map[string]any, v *int64) map[string]any {
	off, ok := offsetOf(v)
	if !ok {
		return md
	}
	md["utc_offset_seconds"] = off
	if z := offsetZone(off); z != "" {
		md["timezone"] = z
	}
	return md
}

// offsetZone names the IANA zone of a fixed UTC offset in seconds: "Etc/UTC"
// or "Etc/GMT-3" for +03:00 (the Etc names invert the sign), "" for an
// offset that is not a whole hour or has no Etc zone.
func offsetZone(secs int64) string {
	if secs == 0 {
		return "Etc/UTC"
	}
	if secs%3600 != 0 {
		return ""
	}
	h := secs / 3600
	if h < -12 || h > 14 {
		return ""
	}
	if h > 0 {
		return "Etc/GMT-" + strconv.FormatInt(h, 10)
	}
	return "Etc/GMT+" + strconv.FormatInt(-h, 10)
}

// parseDate reads a calendarDate (YYYY-MM-DD) as midnight UTC.
func parseDate(s string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("calendarDate %q is not YYYY-MM-DD", s)
	}
	if d.Year() < 2000 || d.Year() > 2199 {
		return time.Time{}, fmt.Errorf("calendarDate %q out of range", s)
	}
	return d, nil
}

// localDate is the user's local date of t, given the offset when known, else
// the UTC date.
func localDate(t time.Time, off *int64) string {
	if o, ok := offsetOf(off); ok {
		t = t.Add(time.Duration(o) * time.Second)
	}
	return t.UTC().Format("2006-01-02")
}

// utcDay truncates t to midnight UTC.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// timeOffsets turns a "seconds from start" keyed map into sorted (time,
// value) readings, dropping keys that do not parse or fall outside two days.
func timeOffsets(start time.Time, m map[string]float64) []reading {
	out := make([]reading, 0, len(m))
	for k, v := range m {
		off, err := strconv.ParseInt(k, 10, 64)
		if err != nil || off < 0 || off > 2*86400 {
			continue
		}
		out = append(out, reading{at: start.Add(time.Duration(off) * time.Second), v: v})
	}
	sortReadings(out)
	return out
}
