// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// envelope is the answer of the metrics endpoint. The Partnership API answers
// {"data":{"metric_data":[...]}} (as published third-party integrations
// read it, unverified). The personal token API is read by third parties as
// {"data":{"metrics":{"YYYY-MM-DD":[...]}}}; both shapes are accepted so a
// line of either maps the same.
type envelope struct {
	Data struct {
		MetricData []metricItem            `json:"metric_data"`
		Metrics    map[string][]metricItem `json:"metrics"`
	} `json:"data"`
}

// items returns the metric list of the answer for date.
func (e envelope) items(date string) []metricItem {
	if len(e.Data.MetricData) > 0 {
		return e.Data.MetricData
	}
	return e.Data.Metrics[date]
}

// metricItem is one entry of the metric list: a type ("hr", "Sleep", ...)
// and its object, decoded by type.
type metricItem struct {
	Type   string          `json:"type"`
	Object json.RawMessage `json:"object"`
}

// num is a lenient number: a JSON number, a numeric string, or nothing (null,
// absent, "NaN", "Inf" or anything else, which reads as missing rather than
// failing the whole day).
type num struct {
	V   float64
	Set bool
}

// UnmarshalJSON never fails: what is not a number is left unset.
func (n *num) UnmarshalJSON(b []byte) error {
	*n = num{}
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		var str string
		if json.Unmarshal(b, &str) != nil {
			return nil
		}
		s = strings.TrimSpace(str)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	*n = num{V: v, Set: true}
	return nil
}

// point is one reading of a series.
type point struct {
	Value     num `json:"value"`
	Timestamp num `json:"timestamp"`
}

// seriesObject is the object of a series or a daily scalar: hr, hrv, temp,
// steps, night_rhr, glucose (values), sleep_rhr, vo2_max,
// temperature_deviation (value).
type seriesObject struct {
	DayStart num     `json:"day_start_timestamp"`
	Values   []point `json:"values"`
	Unit     string  `json:"unit"`
	Value    num     `json:"value"`
	Avg      num     `json:"avg"`
	Total    num     `json:"total"`
}

// sleepObject is the object of the "Sleep" entry. Field names follow
// published third-party fixtures (unverified against a real account).
type sleepObject struct {
	DayStart     num `json:"day_start_timestamp"`
	BedtimeStart num `json:"bedtime_start"`
	BedtimeEnd   num `json:"bedtime_end"`
	QuickMetrics []struct {
		Type  string `json:"type"`
		Value num    `json:"value"`
	} `json:"quick_metrics"`
	SleepStages []struct {
		Type      string `json:"type"`
		StageTime num    `json:"stage_time"`
	} `json:"sleep_stages"`
	SleepGraph struct {
		Data []struct {
			Start num    `json:"start"`
			End   num    `json:"end"`
			Type  string `json:"type"`
		} `json:"data"`
	} `json:"sleep_graph"`
	SpO2 struct {
		Value num `json:"value"`
	} `json:"spo2"`
	SleepScore struct {
		Score num `json:"score"`
	} `json:"sleep_score"`
}
