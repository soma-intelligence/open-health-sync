// SPDX-License-Identifier: AGPL-3.0-only

package withings

import (
	"encoding/json"
	"fmt"
)

// Data types this connector stores. Each raw object holds one of them (R16),
// and Map dispatches on it. A line of each type is one provider JSON object,
// kept as Withings sent it except for the additions documented on each
// constant.
const (
	// DataTypeMeasure: one getmeas measure group per line. The response's
	// body-level "timezone" is added to the group when the group has none, so
	// the line keeps the zone it was recorded in.
	DataTypeMeasure = "measure"
	// DataTypeActivity: one getactivity day per line, verbatim.
	DataTypeActivity = "activity"
	// DataTypeIntraday: one getintradayactivity interval per line. Withings
	// keys the series by epoch, so the key is added to the point as
	// "timestamp".
	DataTypeIntraday = "intraday"
	// DataTypeSleep: one sleep getsummary series per line. The detailed phase
	// segments of the same night (sleep action=get) are added as
	// "sleep_phases". A night that only has segments becomes a synthetic
	// summary line (see fetchSleep).
	DataTypeSleep = "sleep"
	// DataTypeWorkout: one getworkouts series per line, verbatim.
	DataTypeWorkout = "workout"
	// DataTypeECG: one heart list series per line. The waveform of that
	// recording (heart action=get body) is added as "signal" when Withings
	// returns one.
	DataTypeECG = "ecg"
)

// AllDataTypes is every data type Fetch pulls, in the order it pulls them.
var AllDataTypes = []string{
	DataTypeMeasure,
	DataTypeActivity,
	DataTypeSleep,
	DataTypeWorkout,
	DataTypeECG,
	DataTypeIntraday,
}

// MeasTypeInfo holds the canonical mapping for a Withings meastype code.
type MeasTypeInfo struct {
	QuantityType string
	Unit         string
}

// withingsMeasTypes maps Withings numeric meastype codes to canonical types.
// Source: Withings API documentation, getmeas meastype table (mirrored at
// https://pkg.go.dev/github.com/tgrangeray/withings-go/withings). Checked
// 2026-09-23: VO2 max and visceral fat had been keyed on 135 and 169.
//
// 121 (basal metabolic rate) is not in the published table, so it is left
// unmapped rather than guessed.
var withingsMeasTypes = map[int]MeasTypeInfo{
	1:  {QuantityType: "body_mass", Unit: "kg"},
	4:  {QuantityType: "height", Unit: "m"},
	5:  {QuantityType: "lean_body_mass", Unit: "kg"},
	6:  {QuantityType: "body_fat_percentage", Unit: "percent"},
	8:  {QuantityType: "body_fat_mass", Unit: "kg"},
	9:  {QuantityType: "blood_pressure_diastolic", Unit: "mmHg"},
	10: {QuantityType: "blood_pressure_systolic", Unit: "mmHg"},
	11: {QuantityType: "heart_rate", Unit: "bpm"},
	12: {QuantityType: "body_temperature", Unit: "celsius"},
	54: {QuantityType: "oxygen_saturation", Unit: "percent"},
	71: {QuantityType: "body_temperature", Unit: "celsius"},
	73: {QuantityType: "skin_temperature", Unit: "celsius"},
	76: {QuantityType: "muscle_mass", Unit: "kg"},
	77: {QuantityType: "water_mass", Unit: "kg"},
	88: {QuantityType: "bone_mass", Unit: "kg"},
	91: {QuantityType: "pulse_wave_velocity", Unit: "m/s"},
	// 123 is VO2 max; 135 is an ECG's QRS interval duration and stays unmapped.
	123: {QuantityType: "vo2_max", Unit: "mL/kg/min"},
	// 155 is vascular age. 130 is a different, unidentified measurement and
	// stays unmapped rather than guessed.
	155: {QuantityType: "vascular_age", Unit: "years"},
	// 170 is visceral fat; 169 is intracellular water and stays unmapped.
	170: {QuantityType: "visceral_fat_rating", Unit: "index"},
}

// IntOrBool handles Withings' inconsistent "more" field, an int (0/1) on some
// endpoints and a bool on others.
type IntOrBool int

// UnmarshalJSON accepts true, false or an integer.
func (iob *IntOrBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true":
		*iob = 1
		return nil
	case "false", "null":
		*iob = 0
		return nil
	}
	var i int
	if err := json.Unmarshal(data, &i); err != nil {
		return err
	}
	*iob = IntOrBool(i)
	return nil
}

// FlexString decodes a JSON string or number into a string. Withings sends
// userid and some ids as either.
type FlexString string

// UnmarshalJSON accepts a string, a number or null.
func (f *FlexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("withings: %s is neither a string nor a number", data)
	}
	*f = FlexString(n.String())
	return nil
}

// envelope is the shape every Withings API answer shares: an inner status
// (0 = success) and a body.
type envelope struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
	Error  string          `json:"error"`
}

// ── getmeas ──────────────────────────────────────────────────────────────────

// measurePage is one getmeas page. Groups stay raw: they are stored verbatim.
type measurePage struct {
	MeasureGrps []json.RawMessage `json:"measuregrps"`
	Timezone    string            `json:"timezone"`
	More        IntOrBool         `json:"more"`
	Offset      int               `json:"offset"`
}

// MeasureGroup is one getmeas measure group as Map reads it back.
type MeasureGroup struct {
	GrpID int64 `json:"grpid"`
	Date  int64 `json:"date"` // Unix timestamp

	// Attrib is Withings' attribution of the group to this user. A household
	// scale is used by several people, and the API distinguishes a reading it
	// is confident about from one it could not attribute and from one typed in
	// by hand. Carried verbatim into metadata, not interpreted.
	Attrib int `json:"attrib"`

	// Category is 1 for a real measurement and 2 for a user objective (a
	// target weight, not a reading). Objectives are not stored.
	Category int `json:"category"`

	DeviceID string    `json:"deviceid"`
	Timezone string    `json:"timezone"`
	Measures []Measure `json:"measures"`
}

// Measure is one value of a group, encoded as value * 10^unit.
type Measure struct {
	Value int `json:"value"`
	Type  int `json:"type"`
	Unit  int `json:"unit"` // power of ten
}

// Float64 converts the Withings integer value + power of ten to a float.
// Dividing (rather than multiplying by math.Pow(10, -n)) keeps 7234e-2 at
// exactly 72.34.
func (m Measure) Float64() float64 {
	base := float64(m.Value)
	for i := 0; i < -m.Unit; i++ {
		base /= 10
	}
	for i := 0; i < m.Unit; i++ {
		base *= 10
	}
	return base
}

// ── getactivity ──────────────────────────────────────────────────────────────

type activityPage struct {
	Activities []json.RawMessage `json:"activities"`
	More       IntOrBool         `json:"more"`
	Offset     int               `json:"offset"`
}

// Activity is one getactivity day. Pointers tell a missing figure from a zero.
type Activity struct {
	Date          string   `json:"date"` // YYYY-MM-DD, the user's local day
	Timezone      string   `json:"timezone"`
	DeviceID      string   `json:"deviceid"`
	Steps         *float64 `json:"steps"`
	Distance      *float64 `json:"distance"`
	Calories      *float64 `json:"calories"`
	TotalCalories *float64 `json:"totalcalories"`
	Intense       *float64 `json:"intense"`
	Moderate      *float64 `json:"moderate"`
	Soft          *float64 `json:"soft"`
	Elevation     *float64 `json:"elevation"`
}

// ── getintradayactivity ──────────────────────────────────────────────────────

type intradayBody struct {
	Series map[string]json.RawMessage `json:"series"`
}

// IntradayPoint is one intraday interval line (the series key added as
// "timestamp").
type IntradayPoint struct {
	Timestamp int64    `json:"timestamp"`
	Steps     *float64 `json:"steps"`
	Elevation *float64 `json:"elevation"`
	Calories  *float64 `json:"calories"`
	Distance  *float64 `json:"distance"`
	HeartRate *float64 `json:"heart_rate"`
	Duration  *float64 `json:"duration"`
	DeviceID  string   `json:"deviceid"`
}

// ── sleep ────────────────────────────────────────────────────────────────────

type sleepPage struct {
	Series []json.RawMessage `json:"series"`
	More   IntOrBool         `json:"more"`
	Offset int               `json:"offset"`
}

// SleepSeries is one sleep summary line as Map reads it back.
type SleepSeries struct {
	ID          FlexString          `json:"id"`
	Timezone    string              `json:"timezone"`
	Startdate   int64               `json:"startdate"`
	Enddate     int64               `json:"enddate"`
	State       int                 `json:"state"`
	Data        map[string]any      `json:"data"`
	SleepPhases []SleepPhaseSegment `json:"sleep_phases"`
}

// SleepPhaseSegment is a single phase transition from the detailed sleep
// endpoint. State: 0 awake, 1 light, 2 deep, 3 REM.
type SleepPhaseSegment struct {
	Startdate int64 `json:"startdate"`
	Enddate   int64 `json:"enddate"`
	State     int   `json:"state"`
}

type sleepDetailBody struct {
	Series []sleepDetailSegment `json:"series"`
}

// sleepDetailSegment is one sleep action=get segment with the id of the model
// that recorded it, which the stored phases do not keep.
type sleepDetailSegment struct {
	SleepPhaseSegment
	ModelID json.RawMessage `json:"model_id"`
}

// sleepDataFields is the getsummary data_fields list.
const sleepDataFields = "deepsleepduration,remsleepduration,lightsleepduration,wakeupduration," +
	"hr_average,hr_min,hr_max,rr_average,rr_min,rr_max,sleep_score,nb_rem_episodes," +
	"sleep_efficiency,sleep_latency,total_sleep_time,total_timeinbed,wakeup_latency,waso," +
	"wakeupcount,snoring,snoringepisodecount,breathing_disturbances_intensity," +
	"apnea_hypopnea_index,out_of_bed_count"

// ── getworkouts ──────────────────────────────────────────────────────────────

type workoutPage struct {
	Series []json.RawMessage `json:"series"`
	More   IntOrBool         `json:"more"`
	Offset int               `json:"offset"`
}

// Workout is one getworkouts series line as Map reads it back. Withings nests
// the metrics under "data".
type Workout struct {
	ID        FlexString     `json:"id"`
	Category  int            `json:"category"`
	Timezone  string         `json:"timezone"`
	Startdate int64          `json:"startdate"`
	Enddate   int64          `json:"enddate"`
	DeviceID  string         `json:"deviceid"`
	Data      map[string]any `json:"data"`
}

// workoutDataFields is the getworkouts data_fields list.
const workoutDataFields = "calories,steps,distance,hr_average,hr_min,hr_max,hr_zone_0,hr_zone_1," +
	"hr_zone_2,hr_zone_3,hr_zone_4,spo2_average,effduration,intensity,manual_distance," +
	"manual_calories,pause_duration,elevation"

// ── heart (ECG) ──────────────────────────────────────────────────────────────

type heartListPage struct {
	Series []json.RawMessage `json:"series"`
	More   IntOrBool         `json:"more"`
	Offset int               `json:"offset"`
}

// HeartRecord is one ECG line as Map reads it back.
type HeartRecord struct {
	DeviceID  string       `json:"deviceid"`
	Model     int          `json:"model"`
	ECG       HeartECG     `json:"ecg"`
	HeartRate float64      `json:"heart_rate"`
	Timestamp int64        `json:"timestamp"`
	Timezone  string       `json:"timezone"`
	Signal    *HeartSignal `json:"signal"`
}

// HeartECG is the classification part of a heart list record.
type HeartECG struct {
	SignalID int64 `json:"signalid"`
	AFib     int   `json:"afib"`
}

// HeartSignal is the heart action=get body: the raw waveform, already in
// microvolts, with its sampling frequency.
type HeartSignal struct {
	Signal            []float64 `json:"signal"`
	SamplingFrequency float64   `json:"sampling_frequency"`
	WearPosition      int       `json:"wearposition"`
}

// ── oauth ────────────────────────────────────────────────────────────────────

// tokenBody is the body of a requesttoken answer.
type tokenBody struct {
	UserID       FlexString `json:"userid"`
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	ExpiresIn    int64      `json:"expires_in"`
	Scope        string     `json:"scope"`
	TokenType    string     `json:"token_type"`
}
