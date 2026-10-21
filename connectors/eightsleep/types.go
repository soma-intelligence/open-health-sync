// SPDX-License-Identifier: AGPL-3.0-only

package eightsleep

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Trends payload shapes, from pyEight's reading of the API (field names as
// the API sends them). Every field is optional: an away side, or a night
// still being processed, sends some of them only.

// TrendDay is one night of the trends answer.
type TrendDay struct {
	Day        string `json:"day"`
	Score      *num   `json:"score"`
	TNT        *num   `json:"tnt"`
	Processing bool   `json:"processing"`

	PresenceDuration *num `json:"presenceDuration"`
	SleepDuration    *num `json:"sleepDuration"`
	LightDuration    *num `json:"lightDuration"`
	DeepDuration     *num `json:"deepDuration"`
	REMDuration      *num `json:"remDuration"`

	SleepQualityScore *QualityScore `json:"sleepQualityScore"`
	SleepRoutineScore *struct {
		Total *num `json:"total"`
	} `json:"sleepRoutineScore"`

	Sessions []Session `json:"sessions"`
}

// QualityScore is the sleep quality block of a day: a total and the night's
// figures with their sub scores.
type QualityScore struct {
	Total           *num    `json:"total"`
	HRV             *Figure `json:"hrv"`
	RespiratoryRate *Figure `json:"respiratoryRate"`
	HeartRate       *Figure `json:"heartRate"`
}

// Figure is one night's value of a metric, the user's average and a score.
type Figure struct {
	Current *num `json:"current"`
	Average *num `json:"average"`
	Score   *num `json:"score"`
}

// Session is one stretch in bed. Stages follow each other from Ts.
type Session struct {
	ID         json.RawMessage   `json:"id"`
	Ts         string            `json:"ts"`
	Stages     []Stage           `json:"stages"`
	Timeseries map[string]series `json:"timeseries"`
}

// Stage is one sleep stage and its length in seconds: "awake", "light",
// "deep", "rem", or "out" (out of bed).
type Stage struct {
	Stage    string `json:"stage"`
	Duration num    `json:"duration"`
}

// Time series keys of a session.
const (
	seriesHeartRate   = "heartRate"
	seriesHRV         = "hrv"
	seriesRespiratory = "respiratoryRate"
	seriesBedTemp     = "tempBedC"
	seriesRoomTemp    = "tempRoomC"
)

// point is one reading of a time series.
type point struct {
	at time.Time
	v  float64
}

// series is a time series sent as [[timestamp, value], ...]. Readings with an
// unparsable time or a missing value ("None", null) are dropped.
type series []point

func (s *series) UnmarshalJSON(b []byte) error {
	var raw [][]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		*s = nil
		return nil // a series of another shape carries nothing we read
	}
	out := make(series, 0, len(raw))
	for _, pair := range raw {
		if len(pair) != 2 {
			continue
		}
		var ts string
		var v num
		if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &v) != nil || !v.ok {
			continue
		}
		at, err := parseTime(ts)
		if err != nil {
			continue
		}
		out = append(out, point{at: at, v: v.v})
	}
	*s = out
	return nil
}

// num is a number that may arrive as a JSON number, a numeric string, or the
// string "None" the API sends for an absent value.
type num struct {
	v  float64
	ok bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	*n = num{}
	var f float64
	if json.Unmarshal(b, &f) == nil {
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			*n = num{v: f, ok: true}
		}
		return nil
	}
	var s json.Number
	if json.Unmarshal(b, &s) == nil {
		if f, err := s.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			*n = num{v: f, ok: true}
		}
	}
	return nil
}

// get returns the value of n, or nil when n is absent.
func (n *num) get() *float64 {
	if n == nil || !n.ok {
		return nil
	}
	v := n.v
	return &v
}

// parseTime reads the API's timestamps: RFC 3339 with or without fractional
// seconds, always in UTC ("2026-03-01T06:30:00.000Z").
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
