// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Data types this connector stores. Each raw object holds one of them, and Map
// dispatches on it.
const (
	// DataTypeWorkout: one workout of GET /v1/workouts per line, with its
	// workout_summary embedded (fetched on its own when the list left it
	// out).
	DataTypeWorkout = "workout"
	// DataTypeUser: the body figures of GET /v1/user (id, height, weight,
	// updated_at). Name, email and birth date are left out before the line
	// is stored.
	DataTypeUser = "user"
	// DataTypeWebhook: one workout_summary webhook body per line, without its
	// webhook_token, stored by the inbound route. It is never fetched; Map
	// turns it into the same workout a pull gives.
	DataTypeWebhook = "webhook"
)

// AllDataTypes is every data type Fetch pulls.
var AllDataTypes = []string{DataTypeUser, DataTypeWorkout}

// tokenResponse is the answer of POST /oauth/token.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// User is the part of GET /v1/user the connector reads. Height is in metres,
// weight in kilograms, both decimal strings.
type User struct {
	ID        json.Number `json:"id"`
	Height    *flexFloat  `json:"height"`
	Weight    *flexFloat  `json:"weight"`
	UpdatedAt string      `json:"updated_at"`
}

// Workout is one workout. Wahoo leaves plan_ids, route_id and the summary out
// of some answers (workout create, webhooks).
type Workout struct {
	ID            json.Number `json:"id"`
	Starts        string      `json:"starts"`
	Minutes       *flexFloat  `json:"minutes"`
	Name          string      `json:"name"`
	WorkoutTypeID *int        `json:"workout_type_id"`
	PlanID        json.Number `json:"plan_id"`
	Summary       *Summary    `json:"workout_summary"`
	CreatedAt     string      `json:"created_at"`
	UpdatedAt     string      `json:"updated_at"`
}

// Summary is a workout summary. Every figure is a decimal string, absent when
// the head unit did not measure it. Units: m, s, kcal, bpm, W, m/s, J, rpm.
type Summary struct {
	ID                  json.Number `json:"id"`
	AscentAccum         *flexFloat  `json:"ascent_accum"`
	CadenceAvg          *flexFloat  `json:"cadence_avg"`
	CaloriesAccum       *flexFloat  `json:"calories_accum"`
	DistanceAccum       *flexFloat  `json:"distance_accum"`
	DurationActiveAccum *flexFloat  `json:"duration_active_accum"`
	DurationPausedAccum *flexFloat  `json:"duration_paused_accum"`
	DurationTotalAccum  *flexFloat  `json:"duration_total_accum"`
	HeartRateAvg        *flexFloat  `json:"heart_rate_avg"`
	PowerBikeNPLast     *flexFloat  `json:"power_bike_np_last"`
	PowerBikeTSSLast    *flexFloat  `json:"power_bike_tss_last"`
	PowerAvg            *flexFloat  `json:"power_avg"`
	SpeedAvg            *flexFloat  `json:"speed_avg"`
	WorkAccum           *flexFloat  `json:"work_accum"`
	TimeZone            string      `json:"time_zone"`
	Manual              *bool       `json:"manual"`
	Edited              *bool       `json:"edited"`
	FitnessAppID        *int        `json:"fitness_app_id"`
	// Workout is only set in a webhook, where the summary carries its
	// workout instead of the other way round.
	Workout *Workout `json:"workout"`
}

// webhookBody is one webhook POST.
type webhookBody struct {
	EventType    string          `json:"event_type"`
	WebhookToken json.RawMessage `json:"webhook_token"`
	User         *struct {
		ID json.Number `json:"id"`
	} `json:"user"`
	WorkoutSummary *Summary `json:"workout_summary"`
}

// flexFloat reads a decimal that Wahoo sends as a string ("24909.71") or,
// in places, as a JSON number. An empty string or null is no value.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	s := string(b)
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("wahoo: %q is not a decimal", s)
	}
	*f = flexFloat(v)
	return nil
}

// val returns the value of f, or nil.
func (f *flexFloat) val() *float64 {
	if f == nil {
		return nil
	}
	v := float64(*f)
	return &v
}
