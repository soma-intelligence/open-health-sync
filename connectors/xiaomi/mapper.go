// SPDX-License-Identifier: AGPL-3.0-only

package xiaomi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// line is one uploaded line: one row of a Mi Fitness export table, with the
// Value column decoded and the device of its data source joined in.
type line struct {
	Table      string `json:"table"`
	SID        string `json:"sid"`
	Key        string `json:"key"`
	Category   string `json:"category"`
	Time       int64  `json:"time"`
	UpdateTime int64  `json:"updateTime"`
	Device     *struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	} `json:"device"`
	Value value `json:"value"`
}

// value holds every field this connector reads from a Value object. A key
// reads only its own. Numbers may come as JSON numbers or numeric strings.
type value struct {
	Time     num `json:"time"`
	DateTime num `json:"date_time"`
	Timezone num `json:"timezone"` // offset from UTC in quarters of an hour

	BPM      num `json:"bpm"`
	HR       num `json:"hr"`
	SpO2     num `json:"spo2"`
	Steps    num `json:"steps"`
	Distance num `json:"distance"`
	Calories num `json:"calories"`
	Weight   num `json:"weight"`
	BodyFat  num `json:"body_fat_rate"`
	VO2Max   num `json:"vo2_max"`

	// Sleep: one segment at the top level, or several in segment_details.
	segment
	Segments []segment `json:"segment_details"`

	// Workouts (sport_record).
	StartTime  num `json:"start_time"`
	EndTime    num `json:"end_time"`
	Duration   num `json:"duration"`
	TotalCal   num `json:"total_cal"`
	AvgHRM     num `json:"avg_hrm"`
	MaxHRM     num `json:"max_hrm"`
	RiseHeight num `json:"rise_height"`
}

// segment is one sleep: a night or a nap. Durations are in minutes, times in
// Unix seconds.
type segment struct {
	Bedtime      num `json:"bedtime"`
	WakeUpTime   num `json:"wake_up_time"`
	Timezone     num `json:"timezone"`
	DeepMinutes  num `json:"sleep_deep_duration"`
	LightMinutes num `json:"sleep_light_duration"`
	REMMinutes   num `json:"sleep_rem_duration"`
	AwakeMinutes num `json:"sleep_awake_duration"`
	SleepScore   num `json:"sleep_score"`
	AvgHR        num `json:"avg_hr"`
	Items        []struct {
		StartTime num `json:"start_time"`
		EndTime   num `json:"end_time"`
		State     num `json:"state"`
	} `json:"items"`
}

// num is a number that may be written as a JSON number or a numeric string.
// The zero value means absent.
type num struct {
	v  float64
	ok bool
}

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil // an unreadable figure is absent, not a broken line
	}
	n.v, n.ok = f, true
	return nil
}

// pos returns the value when it is present and above zero.
func (n num) pos() (float64, bool) { return n.v, n.ok && n.v > 0 }

// unix returns the value as a time when it is a plausible Unix time in
// seconds (2000 to 2100).
func (n num) unix() (time.Time, bool) {
	if !n.ok || n.v < 946684800 || n.v > 4102444800 {
		return time.Time{}, false
	}
	return time.Unix(int64(n.v), 0).UTC(), true
}

// sleepKeys are the fitness_data keys holding a sleep report.
var sleepKeys = map[string]bool{"sleep": true, "watch_night_sleep": true, "watch_daytime_sleep": true}

// sleepStates maps the state of a sleep item to the canonical phase.
var sleepStates = map[int]string{
	2: model.SleepStageDeep,
	3: model.SleepStageLight,
	4: model.SleepStageREM,
	5: model.SleepStageAwake,
}

// mapLine maps one line. An unknown table or key gives nil, nil.
func mapLine(fields map[string]any, userID string) (*model.MapResult, error) {
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("xiaomi: re-encode line: %w", err)
	}
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("xiaomi: %w", err)
	}
	bl := newBuilder(&l, userID)
	switch l.Table {
	case TableFitnessData:
		switch {
		case l.Key == "heart_rate" || l.Key == "single_heart_rate":
			return bl.instant(model.QTHeartRate, "bpm", firstPos(l.Value.BPM, l.Value.HR), 20, 250)
		case l.Key == "resting_heart_rate":
			return bl.instant(model.QTRestingHeartRate, "bpm", firstPos(l.Value.BPM), 20, 200)
		case l.Key == "spo2":
			return bl.instant(model.QTOxygenSaturation, "percent", firstPos(l.Value.SpO2), 50, 100)
		case l.Key == "vo2_max":
			return bl.instant("vo2_max", "mL/kg/min", firstPos(l.Value.VO2Max), 1, 100)
		case l.Key == "weight":
			return bl.weight()
		case l.Key == "steps":
			return bl.steps()
		case l.Key == "calories":
			return bl.minute(model.QTActiveEnergyBurned, "kcal", l.Value.Calories)
		case sleepKeys[l.Key]:
			return bl.sleep()
		}
	case TableSportRecord:
		return bl.workout()
	}
	noteUnknown(l.Table + "/" + l.Key)
	return nil, nil
}

func firstPos(ns ...num) num {
	for _, n := range ns {
		if _, ok := n.pos(); ok {
			return n
		}
	}
	return num{}
}

// builder holds what every record of one line shares.
type builder struct {
	l      *line
	userID string
	md     map[string]any
}

func newBuilder(l *line, userID string) *builder {
	md := map[string]any{"mi_table": l.Table, "mi_key": l.Key}
	if l.SID != "" {
		md["mi_sid"] = l.SID
	}
	if l.UpdateTime > 0 {
		md["mi_update_time"] = l.UpdateTime
	}
	if d := l.Device; d != nil && d.Name != "" {
		md["device_name"] = d.Name
	}
	b := &builder{l: l, userID: userID, md: md}
	b.setZone(md, l.Value.Timezone)
	return b
}

// setZone records the offset of a timezone figure (quarters of an hour) and,
// for a whole hour offset, its Etc/GMT zone.
func (b *builder) setZone(md map[string]any, tz num) {
	if !tz.ok {
		return
	}
	m := int(tz.v) * 15
	if m < -12*60 || m > 14*60 {
		return
	}
	md["utc_offset_minutes"] = m
	if z := offsetZone(m); z != "" {
		md["timezone"] = z
	}
}

// at is the time of an instant reading: its own time, else its date_time,
// else the row's.
func (b *builder) at() (time.Time, error) {
	for _, n := range []num{b.l.Value.Time, b.l.Value.DateTime, {v: float64(b.l.Time), ok: b.l.Time > 0}} {
		if t, ok := n.unix(); ok {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("xiaomi: %s %s: no readable time", b.l.Table, b.l.Key)
}

// externalID names one reading: data source, key and time. Exports have no
// record id, and a data source writes one reading of a key per second.
func (b *builder) externalID(at time.Time) string {
	return b.l.SID + "_" + b.l.Key + "_" + strconv.FormatInt(at.Unix(), 10)
}

// sample builds one quantity sample. A reading Xiaomi corrects comes back in
// a later export under the same id and time, so samples are Replaceable and
// hashed on their id and start rather than their value.
func (b *builder) sample(externalID, qt, unit string, v float64, start, end time.Time) model.QuantitySample {
	s := model.QuantitySample{
		UserID:          b.userID,
		Provider:        Slug,
		SourceSlug:      Slug,
		SourceName:      "Mi Fitness",
		ExternalID:      externalID,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        maps.Clone(b.md),
		Replaceable:     true,
	}
	if d := b.l.Device; d != nil && d.Model != "" {
		s.DeviceID = "Xiaomi " + d.Model
		s.DeviceModel = d.Model
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       b.userID,
		"provider":      Slug,
		"quantity_type": qt,
		"external_id":   externalID,
		"start_date":    start.UTC().Format(time.RFC3339Nano),
	})
	return s
}

// instant maps one reading in [lo, hi]. A reading out of range is dropped,
// like a missing one: the export holds placeholder zeros.
func (b *builder) instant(qt, unit string, n num, lo, hi float64) (*model.MapResult, error) {
	v, ok := n.pos()
	if !ok || v < lo || v > hi {
		return nil, nil
	}
	at, err := b.at()
	if err != nil {
		return nil, err
	}
	s := b.sample(b.externalID(at), qt, unit, v, at, at)
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

// minute maps one per-minute delta to a one-minute interval.
func (b *builder) minute(qt, unit string, n num) (*model.MapResult, error) {
	v, ok := n.pos()
	if !ok {
		return nil, nil
	}
	at, err := b.at()
	if err != nil {
		return nil, err
	}
	s := b.sample(b.externalID(at), qt, unit, v, at, at.Add(time.Minute))
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

// steps maps a per-minute steps row: its steps and, when present, its
// distance in meters. Its calories are left raw: the calories key carries
// the active energy, and counting both would count it twice.
func (b *builder) steps() (*model.MapResult, error) {
	at, err := b.at()
	if err != nil {
		return nil, err
	}
	id, end := b.externalID(at), at.Add(time.Minute)
	res := &model.MapResult{}
	if v, ok := b.l.Value.Steps.pos(); ok {
		res.QuantitySamples = append(res.QuantitySamples, b.sample(id, model.QTStepCount, "count", v, at, end))
	}
	if v, ok := b.l.Value.Distance.pos(); ok {
		res.QuantitySamples = append(res.QuantitySamples, b.sample(id+"_distance", model.QTDistanceWalkingRunning, "m", v, at, end))
	}
	if len(res.QuantitySamples) == 0 {
		return nil, nil
	}
	return res, nil
}

// weight maps a weigh-in: body mass and, from a body composition scale, body
// fat percentage.
func (b *builder) weight() (*model.MapResult, error) {
	kg, ok := b.l.Value.Weight.pos()
	if !ok || kg > 500 {
		return nil, nil
	}
	// A weigh-in's own time is the row's; its value may repeat it.
	at, err := b.at()
	if err != nil {
		return nil, err
	}
	id := b.externalID(at)
	res := &model.MapResult{QuantitySamples: []model.QuantitySample{b.sample(id, "body_mass", "kg", kg, at, at)}}
	if pct, ok := b.l.Value.BodyFat.pos(); ok && pct < 100 {
		res.QuantitySamples = append(res.QuantitySamples, b.sample(id+"_body_fat", "body_fat_percentage", "percent", pct, at, at))
	}
	return res, nil
}

// sleep maps a sleep report to one session per segment, with its phases.
func (b *builder) sleep() (*model.MapResult, error) {
	segs := b.l.Value.Segments
	if len(segs) == 0 {
		segs = []segment{b.l.Value.segment}
	} else if len(segs) == 1 && !segs[0].SleepScore.ok {
		// A daily report scores its one sleep at the top level.
		segs[0].SleepScore = b.l.Value.SleepScore
	}
	res := &model.MapResult{}
	for _, sg := range segs {
		b.session(res, sg)
	}
	if len(res.SleepSessions) == 0 {
		return nil, nil
	}
	return res, nil
}

func (b *builder) session(res *model.MapResult, sg segment) {
	start, ok1 := sg.Bedtime.unix()
	end, ok2 := sg.WakeUpTime.unix()
	if !ok1 || !ok2 || !end.After(start) {
		return // a report without a night: placeholder rows exist
	}
	md := maps.Clone(b.md)
	b.setZone(md, sg.Timezone)
	if v, ok := sg.AvgHR.pos(); ok {
		md["avg_heart_rate"] = v
	}
	id := b.l.SID + "_sleep_" + strconv.FormatInt(start.Unix(), 10)
	s := model.SleepSession{
		UserID:       b.userID,
		Provider:     Slug,
		ExternalID:   id,
		BedtimeStart: start,
		BedtimeEnd:   end,
		Metadata:     md,
		Replaceable:  true,
	}
	if v, ok := sg.SleepScore.pos(); ok && v <= 100 {
		s.SleepScore = &v
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id": b.userID, "provider": Slug, "data_type": "sleep", "external_id": id,
	})
	totals := map[string]int{}
	for _, it := range sg.Items {
		phase, ok := sleepStates[int(it.State.v)]
		ps, ok1 := it.StartTime.unix()
		pe, ok2 := it.EndTime.unix()
		if !ok || !ok1 || !ok2 || !pe.After(ps) {
			continue
		}
		d := int(pe.Sub(ps) / time.Second)
		totals[phase] += d
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID: b.userID, Phase: phase, StartDate: ps, EndDate: pe, DurationS: d,
			ParentContentHash: s.ContentHash,
		})
	}
	if len(totals) == 0 {
		// No hypnogram: the report's own stage minutes. A stage longer
		// than the night is not a reading (and would overflow the sum).
		night := end.Sub(start).Minutes()
		for phase, n := range map[string]num{
			model.SleepStageDeep: sg.DeepMinutes, model.SleepStageLight: sg.LightMinutes,
			model.SleepStageREM: sg.REMMinutes, model.SleepStageAwake: sg.AwakeMinutes,
		} {
			if v, ok := n.pos(); ok && v <= night {
				totals[phase] = int(v * 60)
			}
		}
	}
	if len(totals) > 0 {
		ptr := func(v int) *int { return &v }
		s.DeepSleepSeconds = ptr(totals[model.SleepStageDeep])
		s.REMSleepSeconds = ptr(totals[model.SleepStageREM])
		s.LightSleepSeconds = ptr(totals[model.SleepStageLight])
		s.AwakeSeconds = ptr(totals[model.SleepStageAwake])
		s.TotalSleepSeconds = ptr(totals[model.SleepStageDeep] + totals[model.SleepStageREM] + totals[model.SleepStageLight])
	}
	res.SleepSessions = append(res.SleepSessions, s)
}

// sportTypes maps Mi Fitness sport keys whose canonical activity has another
// name. Others are kept as they are, lowercased.
var sportTypes = map[string]string{
	"outdoor_running":     "running",
	"indoor_running":      "running",
	"treadmill":           "running",
	"trail_running":       "running",
	"outdoor_walking":     "walking",
	"indoor_walking":      "walking",
	"outdoor_riding":      "cycling",
	"indoor_riding":       "cycling",
	"outdoor_cycling":     "cycling",
	"indoor_cycling":      "cycling",
	"spinning":            "cycling",
	"pool_swimming":       "swimming",
	"open_water_swimming": "swimming",
	"mountaineering":      "hiking",
	"climbing":            "hiking",
	"elliptical_machine":  "elliptical",
	"rowing_machine":      "rowing",
	"dumbbell_training":   "traditional_strength_training",
	"strength_training":   "traditional_strength_training",
	"free_training":       "other",
	"":                    "other",
}

func (b *builder) workout() (*model.MapResult, error) {
	v := &b.l.Value
	start, ok := v.StartTime.unix()
	if !ok {
		var err error
		if start, err = b.at(); err != nil {
			return nil, err
		}
	}
	end, ok := v.EndTime.unix()
	if !ok {
		d, has := v.Duration.pos()
		if !has {
			return nil, fmt.Errorf("xiaomi: sport_record %s: no end_time and no duration", b.l.Key)
		}
		end = start.Add(time.Duration(d) * time.Second)
	}
	if !end.After(start) {
		return nil, fmt.Errorf("xiaomi: sport_record %s: end is not after start", b.l.Key)
	}
	kind := strings.ToLower(strings.TrimSpace(b.l.Key))
	activity, ok := sportTypes[kind]
	if !ok {
		activity = kind
	}
	md := maps.Clone(b.md)
	if b.l.Category != "" {
		md["mi_category"] = b.l.Category
	}
	if d := b.l.Device; d != nil && d.Model != "" {
		md["device_model"] = d.Model
	}
	dur := int(end.Sub(start) / time.Second)
	if d, ok := v.Duration.pos(); ok {
		dur = int(d) // moving time; pauses are not in it
	}
	id := b.l.SID + "_" + kind + "_" + strconv.FormatInt(start.Unix(), 10)
	w := model.Workout{
		UserID:       b.userID,
		Provider:     Slug,
		ExternalID:   id,
		ActivityType: activity,
		StartDate:    start,
		EndDate:      end,
		DurationS:    &dur,
		Metadata:     md,
		Replaceable:  true,
	}
	opt := func(n num) *float64 {
		if x, ok := n.pos(); ok {
			return &x
		}
		return nil
	}
	w.ActiveEnergyKcal = opt(v.Calories)
	w.DistanceM = opt(v.Distance)
	w.AvgHeartRate = opt(v.AvgHRM)
	w.MaxHeartRate = opt(v.MaxHRM)
	w.ElevationGainM = opt(v.RiseHeight)
	if x, ok := v.VO2Max.pos(); ok && x <= 100 {
		w.VO2Max = &x
	}
	if x, ok := v.TotalCal.pos(); ok {
		md["total_kcal"] = x
	}
	w.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id": b.userID, "provider": Slug, "data_type": "sport_record", "external_id": id,
	})
	return &model.MapResult{Workouts: []model.Workout{w}}, nil
}

// offsetZone is the Etc/GMT zone of a whole hour offset ("Etc/GMT-2" is
// UTC+2), "Etc/UTC" for 0, and "" otherwise.
func offsetZone(minutes int) string {
	if minutes == 0 {
		return "Etc/UTC"
	}
	if minutes%60 != 0 {
		return ""
	}
	h := minutes / 60
	if h > 0 {
		return "Etc/GMT-" + strconv.Itoa(h)
	}
	return "Etc/GMT+" + strconv.Itoa(-h)
}

// unknownKeys counts table/key pairs seen without a mapping, so each is
// logged once rather than once per line.
var (
	unknownMu   sync.Mutex
	unknownKeys = map[string]bool{}
)

func noteUnknown(k string) {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	if unknownKeys[k] || len(unknownKeys) >= 64 {
		return
	}
	unknownKeys[k] = true
	slog.Info("xiaomi: key not stored", "provider", Slug, "key", k)
}
