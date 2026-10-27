// SPDX-License-Identifier: AGPL-3.0-only

package ultrahuman

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
	// Embed the zone database so the Etc/GMT zones load in minimal images.
	_ "time/tzdata"
)

// sourceName is the source_name of every record: the vendor.
const sourceName = "Ultrahuman"

// Metric types of the metrics answer that this connector maps. Every other
// type (recovery_index, movement_index, metabolic_score, active_minutes,
// inactive_time, weekly_active_minutes, movements, morning_alertness, the
// CGM summaries...) stays in the raw line only: there is no canonical name
// for them.
const (
	typeHR          = "hr"
	typeHRV         = "hrv"
	typeTemp        = "temp"
	typeSteps       = "steps"
	typeNightRHR    = "night_rhr"
	typeSleepRHR    = "sleep_rhr"
	typeAvgSleepHRV = "avg_sleep_hrv"
	typeTempDev     = "temperature_deviation"
	typeVO2Max      = "vo2_max"
	typeGlucose     = "glucose"
	typeSleep       = "sleep" // the API spells it "Sleep"; types are compared lowercased
)

// sleepStages maps the stage types of sleep_graph to canonical phases.
// Anything else is dropped.
var sleepStages = map[string]string{
	"deep_sleep":  model.SleepStageDeep,
	"light_sleep": model.SleepStageLight,
	"rem_sleep":   model.SleepStageREM,
	"awake":       model.SleepStageAwake,
}

// day is the context of one raw line.
type day struct {
	userID string
	date   string    // YYYY-MM-DD, the user's local date
	start  time.Time // local midnight of date, in UTC
	end    time.Time // start + 24 h
	base   map[string]any
}

// mapMetricsDay maps one stored day: {"date": "...", "body": <answer>}.
func mapMetricsDay(fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("ultrahuman: %s: re-encode line: %w", DataTypeMetrics, err)
	}
	var rd rawDay
	if err := json.Unmarshal(line, &rd); err != nil {
		return nil, fmt.Errorf("ultrahuman: %s: %w", DataTypeMetrics, err)
	}
	date, err := time.Parse("2006-01-02", rd.Date)
	if err != nil || date.IsZero() {
		return nil, fmt.Errorf("ultrahuman: %s: date %q not understood", DataTypeMetrics, rd.Date)
	}
	var env envelope
	if len(rd.Body) > 0 {
		if err := json.Unmarshal(rd.Body, &env); err != nil {
			return nil, fmt.Errorf("ultrahuman: %s: body: %w", DataTypeMetrics, err)
		}
	}
	items := env.items(rd.Date)
	if len(items) == 0 {
		return nil, nil
	}

	// Group the objects by lowercased type. A type sent twice (sleep_rhr is)
	// keeps its first object.
	objects := map[string]json.RawMessage{}
	for _, it := range items {
		t := strings.ToLower(strings.TrimSpace(it.Type))
		if _, seen := objects[t]; !seen && len(it.Object) > 0 {
			objects[t] = it.Object
		}
	}

	d := day{userID: userID, date: rd.Date, start: date, base: map[string]any{"date": rd.Date}}
	if off, ok := dayOffset(date, objects); ok {
		d.start = date.Add(-time.Duration(off) * time.Minute)
		d.base["utc_offset_minutes"] = off
		if z := offsetZone(off); z != "" {
			d.base["timezone"] = z
		}
	} else {
		d.base["local_date_as_utc"] = true
	}
	d.end = d.start.Add(24 * time.Hour)

	res := &model.MapResult{}
	series := func(t, qt, unit string, conv func(v float64, unit string) (float64, string, bool)) {
		var o seriesObject
		if raw, ok := objects[t]; ok && json.Unmarshal(raw, &o) == nil {
			res.QuantitySamples = append(res.QuantitySamples, d.series(t, qt, unit, o, conv)...)
		}
	}
	series(typeHR, model.QTHeartRate, "bpm", nil)
	series(typeHRV, model.QTHRVRMSSD, "ms", nil)
	series(typeTemp, model.QTSkinTemperature, "celsius", temperature)
	series(typeSteps, model.QTStepCountIntraday, "count", nil)
	series(typeGlucose, "blood_glucose", "mg/dL", glucose)

	var steps seriesObject
	if raw, ok := objects[typeSteps]; ok && json.Unmarshal(raw, &steps) == nil && steps.Total.Set && steps.Total.V >= 0 {
		res.QuantitySamples = append(res.QuantitySamples, d.daily(model.QTStepCount, "count", steps.Total.V))
	}

	// Resting heart rate: the night's sleep_rhr, else the night_rhr average.
	var sleepRHR, nightRHR seriesObject
	_ = decodeObject(objects, typeSleepRHR, &sleepRHR)
	_ = decodeObject(objects, typeNightRHR, &nightRHR)
	rhr := sleepRHR.Value
	if !rhr.Set || rhr.V <= 0 {
		rhr = nightRHR.Avg
	}
	if rhr.Set && rhr.V > 0 {
		res.QuantitySamples = append(res.QuantitySamples, d.daily(model.QTRestingHeartRate, "bpm", rhr.V))
	}

	var tempDev seriesObject
	if decodeObject(objects, typeTempDev, &tempDev) && tempDev.Value.Set {
		if v, _, ok := temperatureDelta(tempDev.Value.V, tempDev.Unit); ok {
			res.QuantitySamples = append(res.QuantitySamples, d.daily(model.QTBodyTemperatureDeviation, "celsius", v))
		}
	}

	var vo2 seriesObject
	if decodeObject(objects, typeVO2Max, &vo2) && vo2.Value.Set && vo2.Value.V > 0 {
		res.QuantitySamples = append(res.QuantitySamples, d.daily("vo2_max", "mL/kg/min", vo2.Value.V))
	}

	var avgSleepHRV seriesObject
	_ = decodeObject(objects, typeAvgSleepHRV, &avgSleepHRV)
	var so sleepObject
	if decodeObject(objects, typeSleep, &so) {
		d.sleep(res, so, rhr, avgSleepHRV.Value)
	}

	if len(res.QuantitySamples) == 0 && len(res.SleepSessions) == 0 {
		return nil, nil
	}
	return res, nil
}

// decodeObject decodes the object of type t into v; false when absent or not
// an object of that shape.
func decodeObject(objects map[string]json.RawMessage, t string, v any) bool {
	raw, ok := objects[t]
	return ok && json.Unmarshal(raw, v) == nil
}

// dayOffset derives the user's UTC offset on date from a day_start_timestamp
// (local midnight of date, as a Unix time): midnight UTC of date minus it.
// An offset outside -12 h..+14 h is ignored.
func dayOffset(date time.Time, objects map[string]json.RawMessage) (int, bool) {
	keys := make([]string, 0, len(objects))
	for k := range objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var o struct {
			DayStart num `json:"day_start_timestamp"`
		}
		if json.Unmarshal(objects[k], &o) != nil || !o.DayStart.Set {
			continue
		}
		start, ok := unixTime(o.DayStart.V)
		if !ok {
			continue
		}
		off := int(date.Sub(start) / time.Minute)
		if off < -12*60 || off > 14*60 {
			continue
		}
		return off, true
	}
	return 0, false
}

// series maps the values of one series object to point samples of qt. A
// non-positive reading is a missing one and is skipped (temperature excepted:
// conv decides). A timestamp sent twice keeps its last reading. Samples are
// hashed on their time, not their value, and Replaceable: a corrected
// reading replaces the stored one.
func (d day) series(t, qt, unit string, o seriesObject, conv func(v float64, unit string) (float64, string, bool)) []model.QuantitySample {
	type reading struct {
		at time.Time
		v  float64
		md map[string]any
	}
	byTime := map[int64]reading{}
	for _, p := range o.Values {
		if !p.Value.Set || !p.Timestamp.Set {
			continue
		}
		at, ok := unixTime(p.Timestamp.V)
		if !ok {
			continue
		}
		v := p.Value.V
		var extra map[string]any
		if conv != nil {
			var ok bool
			if v, _, ok = conv(v, o.Unit); !ok {
				continue
			}
			if o.Unit == "" {
				extra = map[string]any{"unit_assumed": true}
			}
		} else if v <= 0 {
			continue
		}
		byTime[at.Unix()] = reading{at: at, v: v, md: extra}
	}
	keys := make([]int64, 0, len(byTime))
	for k := range byTime {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]model.QuantitySample, 0, len(keys))
	for _, k := range keys {
		r := byTime[k]
		md := copyMap(d.base)
		md["ultrahuman_type"] = t
		for mk, mv := range r.md {
			md[mk] = mv
		}
		s := d.sample(qt, unit, r.v, r.at, r.at, md)
		s.ExternalID = t + "_" + d.date
		s.Replaceable = true
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id":       d.userID,
			"provider":      Slug,
			"data_type":     DataTypeMetrics,
			"quantity_type": qt,
			"start_date":    r.at.Format(time.RFC3339),
		})
		out = append(out, s)
	}
	return out
}

// daily builds one figure of the day, covering the local day. It is hashed on
// the date and Replaceable: a day pulled again while it is still running
// replaces its figure.
func (d day) daily(qt, unit string, v float64) model.QuantitySample {
	s := d.sample(qt, unit, v, d.start, d.end, copyMap(d.base))
	s.ExternalID = d.date + "_" + qt
	s.Replaceable = true
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       d.userID,
		"provider":      Slug,
		"data_type":     DataTypeMetrics,
		"quantity_type": qt,
		"date":          d.date,
	})
	return s
}

// sample builds one quantity sample of this connector.
func (d day) sample(qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	return model.QuantitySample{
		UserID:          d.userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      sourceName,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        md,
	}
}

// sleep maps the Sleep object to one SleepSession and its phases. The night
// is keyed by the date it ends on (the line's date): hashed on it, and
// Replaceable, so a re-scored night replaces the stored one.
func (d day) sleep(res *model.MapResult, so sleepObject, rhr, avgHRV num) {
	if !so.BedtimeStart.Set || !so.BedtimeEnd.Set {
		return
	}
	start, ok1 := unixTime(so.BedtimeStart.V)
	end, ok2 := unixTime(so.BedtimeEnd.V)
	if !ok1 || !ok2 || !end.After(start) {
		return
	}
	md := copyMap(d.base)
	quick := map[string]num{}
	for _, q := range so.QuickMetrics {
		if q.Type != "" && q.Value.Set {
			quick[q.Type] = q.Value
		}
	}
	if v, ok := quick["sleep_efic"]; ok {
		md["sleep_efficiency"] = v.V
	}
	if v, ok := quick["time_in_bed"]; ok {
		md["time_in_bed_seconds"] = v.V
	}
	stages := map[string]*int{}
	for _, st := range so.SleepStages {
		if st.StageTime.Set && st.StageTime.V >= 0 {
			v := int(math.Round(st.StageTime.V))
			stages[st.Type] = &v
		}
	}
	session := model.SleepSession{
		UserID:            d.userID,
		Provider:          Slug,
		ExternalID:        d.date,
		BedtimeStart:      start,
		BedtimeEnd:        end,
		DeepSleepSeconds:  stages["deep_sleep"],
		REMSleepSeconds:   stages["rem_sleep"],
		LightSleepSeconds: stages["light_sleep"],
		AwakeSeconds:      stages["awake"],
		Metadata:          md,
		Replaceable:       true,
	}
	if v, ok := quick["total_sleep"]; ok && v.V >= 0 {
		total := int(math.Round(v.V))
		session.TotalSleepSeconds = &total
	} else if session.DeepSleepSeconds != nil || session.REMSleepSeconds != nil || session.LightSleepSeconds != nil {
		total := 0
		for _, p := range []*int{session.DeepSleepSeconds, session.REMSleepSeconds, session.LightSleepSeconds} {
			if p != nil {
				total += *p
			}
		}
		session.TotalSleepSeconds = &total
	}
	if so.SleepScore.Score.Set {
		v := so.SleepScore.Score.V
		session.SleepScore = &v
	}
	hrv := avgHRV
	if !hrv.Set {
		hrv = quick["avg_hrv"]
	}
	if hrv.Set && hrv.V > 0 {
		v := hrv.V
		session.HRVAvgMs = &v
	}
	if rhr.Set && rhr.V > 0 {
		v := rhr.V
		session.RestingHR = &v
	}
	if so.SpO2.Value.Set && so.SpO2.Value.V > 0 {
		v := so.SpO2.Value.V
		session.SpO2Avg = &v
	}
	session.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":   d.userID,
		"provider":  Slug,
		"data_type": DataTypeMetrics + "/sleep",
		"date":      d.date,
	})
	res.SleepSessions = append(res.SleepSessions, session)

	type seg struct {
		start, end time.Time
		phase      string
	}
	var segs []seg
	for _, g := range so.SleepGraph.Data {
		phase, ok := sleepStages[strings.ToLower(g.Type)]
		if !ok || !g.Start.Set || !g.End.Set {
			continue
		}
		s, ok1 := unixTime(g.Start.V)
		e, ok2 := unixTime(g.End.V)
		if !ok1 || !ok2 || !e.After(s) {
			continue
		}
		segs = append(segs, seg{start: s, end: e, phase: phase})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].start.Before(segs[j].start) })
	for _, sg := range segs {
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID:            d.userID,
			Phase:             sg.phase,
			StartDate:         sg.start,
			EndDate:           sg.end,
			DurationS:         int(sg.end.Sub(sg.start) / time.Second),
			ParentContentHash: session.ContentHash,
		})
	}
}

// glucose converts a CGM reading to mg/dL. The unit comes from the object's
// "unit" (mg/dL or mmol/L, depending on the user's region); without one it is
// read as mg/dL, the unit Ultrahuman's field list gives. A non-positive or
// unconvertible reading is dropped.
func glucose(v float64, unit string) (float64, string, bool) {
	if v <= 0 {
		return 0, "", false
	}
	u := normUnit(unit)
	switch u {
	case "", "mg/dl":
		return v, "mg/dL", true
	case "mmol/l":
		out, outUnit, err := model.ToStorageUnit("blood_glucose", v, "mmol/L")
		if err != nil || math.IsInf(out, 0) {
			return 0, "", false
		}
		return out, outUnit, true
	}
	return 0, "", false
}

// temperature reads an absolute skin temperature in Celsius (the API sends
// "°C"); a Fahrenheit unit is converted. Readings outside 20..45 °C are not
// skin temperatures and are dropped.
func temperature(v float64, unit string) (float64, string, bool) {
	if isFahrenheit(unit) {
		v = (v - 32) * 5 / 9
	}
	if v < 20 || v > 45 {
		return 0, "", false
	}
	return v, "celsius", true
}

// temperatureDelta reads a deviation from baseline in Celsius; a Fahrenheit
// delta is scaled. Deviations beyond ±10 °C are dropped.
func temperatureDelta(v float64, unit string) (float64, string, bool) {
	if isFahrenheit(unit) {
		v = v * 5 / 9
	}
	if v < -10 || v > 10 {
		return 0, "", false
	}
	return v, "celsius", true
}

func isFahrenheit(unit string) bool {
	u := normUnit(unit)
	return u == "f" || u == "°f" || u == "fahrenheit" || u == "degf"
}

// normUnit lowercases a unit and drops spaces.
func normUnit(u string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(u), " ", ""))
}

// unixTime reads Unix seconds (milliseconds when the number is too large to
// be seconds). Times before 2015 or after 2100 are refused.
func unixTime(v float64) (time.Time, bool) {
	if v > 1e11 {
		v /= 1000
	}
	if v < 1420070400 || v > 4102444800 {
		return time.Time{}, false
	}
	sec, frac := math.Modf(v)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
}

// offsetZone returns the Etc/GMT zone of a whole hour offset, or "".
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

// copyMap returns a shallow copy of md.
func copyMap(md map[string]any) map[string]any {
	out := make(map[string]any, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}
