// SPDX-License-Identifier: AGPL-3.0-only

package intervalsicu

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Data types this connector stores. Each raw object holds one of them, and Map
// dispatches on it.
const (
	// DataTypeActivity: one activity of GET /athlete/0/activities per line,
	// verbatim.
	DataTypeActivity = "activity"
	// DataTypeWellness: one daily wellness record of GET
	// /athlete/0/wellness per line, verbatim.
	DataTypeWellness = "wellness"
)

// AllDataTypes is every data type Fetch pulls.
var AllDataTypes = []string{DataTypeActivity, DataTypeWellness}

// tokenResponse is the answer of POST /api/oauth/token. The documented
// example carries no expiry and no refresh token; both are read when present
// so a later change of the API is picked up.
type tokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Athlete      *struct {
		ID flexID `json:"id"`
	} `json:"athlete"`
}

// flexID is an id intervals.icu may send as a JSON string ("i12345") or a
// number. It keeps the text either way.
type flexID string

// UnmarshalJSON accepts a string, a number or null.
func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexID(strings.TrimSpace(s))
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexID(n.String())
	return nil
}

// Activity is the part of an intervals.icu activity the mapper reads. Every
// figure is a pointer: intervals.icu leaves out, or writes null for, what the
// recording device did not measure.
type Activity struct {
	ID                 flexID   `json:"id"`
	Type               string   `json:"type"`
	StartDate          string   `json:"start_date"`
	StartDateLocal     string   `json:"start_date_local"`
	Timezone           string   `json:"timezone"`
	MovingTime         *int     `json:"moving_time"`
	ElapsedTime        *int     `json:"elapsed_time"`
	Distance           *float64 `json:"distance"`
	Calories           *float64 `json:"calories"`
	Joules             *float64 `json:"icu_joules"`
	TotalElevationGain *float64 `json:"total_elevation_gain"`
	AverageHeartrate   *float64 `json:"average_heartrate"`
	MaxHeartrate       *float64 `json:"max_heartrate"`
	AverageSpeed       *float64 `json:"average_speed"`
	MaxSpeed           *float64 `json:"max_speed"`
	AverageWatts       *float64 `json:"icu_average_watts"`
	TrainingLoad       *float64 `json:"icu_training_load"`
	Trainer            *bool    `json:"trainer"`
	DeviceName         string   `json:"device_name"`
	Source             string   `json:"source"`
	ExternalID         string   `json:"external_id"`
}

// Wellness is one day of wellness data. ID is the athlete's local date,
// "YYYY-MM-DD". All units are metric.
type Wellness struct {
	ID            string   `json:"id"`
	Weight        *float64 `json:"weight"`        // kg
	RestingHR     *float64 `json:"restingHR"`     // bpm
	HRV           *float64 `json:"hrv"`           // rMSSD, ms
	HRVSDNN       *float64 `json:"hrvSDNN"`       // SDNN, ms
	SleepSecs     *float64 `json:"sleepSecs"`     // s
	SleepScore    *float64 `json:"sleepScore"`    // 0-100
	SleepQuality  *float64 `json:"sleepQuality"`  // 1-4
	AvgSleepingHR *float64 `json:"avgSleepingHR"` // bpm
	Steps         *float64 `json:"steps"`         // count
	SpO2          *float64 `json:"spO2"`          // percent
	Respiration   *float64 `json:"respiration"`   // breaths/min
	BodyFat       *float64 `json:"bodyFat"`       // percent
	VO2Max        *float64 `json:"vo2max"`        // mL/kg/min
	Systolic      *float64 `json:"systolic"`      // mmHg
	Diastolic     *float64 `json:"diastolic"`     // mmHg
	KcalConsumed  *float64 `json:"kcalConsumed"`  // kcal
	Updated       string   `json:"updated"`
}

// webhookBody is one webhook POST: a secret and a list of events.
type webhookBody struct {
	Secret string         `json:"secret"`
	Events []webhookEvent `json:"events"`
}

// webhookEvent is one event of a webhook POST. Activity events carry the
// activity; wellness events may carry the record.
type webhookEvent struct {
	AthleteID flexID `json:"athlete_id"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Activity  *struct {
		ID             flexID `json:"id"`
		StartDate      string `json:"start_date"`
		StartDateLocal string `json:"start_date_local"`
	} `json:"activity"`
	Wellness *struct {
		ID string `json:"id"`
	} `json:"wellness"`
}
