// SPDX-License-Identifier: AGPL-3.0-only

package strava

import "encoding/json"

// Data types this connector stores. Each raw object holds one of them, and Map
// dispatches on it.
const (
	// DataTypeActivity: one SummaryActivity of GET /athlete/activities per
	// line, verbatim.
	DataTypeActivity = "activity"
)

// AllDataTypes is every data type Fetch pulls.
var AllDataTypes = []string{DataTypeActivity}

// tokenResponse is the answer of POST /oauth/token. The athlete is only in
// the answer to an authorization code.
type tokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Athlete      *struct {
		ID json.Number `json:"id"`
	} `json:"athlete"`
}

// apiError is Strava's error body: {"message": "...", "errors": [{"resource",
// "field", "code"}]}.
type apiError struct {
	Message string `json:"message"`
	Errors  []struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
	} `json:"errors"`
}

// Activity is the part of a SummaryActivity the mapper reads. Every figure is
// a pointer: Strava leaves out what the recording device did not measure
// (heart rate without a strap, kilojoules without power).
type Activity struct {
	ID                 json.Number `json:"id"`
	SportType          string      `json:"sport_type"`
	Type               string      `json:"type"`
	StartDate          string      `json:"start_date"`
	Timezone           string      `json:"timezone"`
	Distance           *float64    `json:"distance"`
	MovingTime         *int        `json:"moving_time"`
	ElapsedTime        *int        `json:"elapsed_time"`
	TotalElevationGain *float64    `json:"total_elevation_gain"`
	AverageSpeed       *float64    `json:"average_speed"`
	MaxSpeed           *float64    `json:"max_speed"`
	AverageHeartrate   *float64    `json:"average_heartrate"`
	MaxHeartrate       *float64    `json:"max_heartrate"`
	Kilojoules         *float64    `json:"kilojoules"`
	Calories           *float64    `json:"calories"`
	AverageWatts       *float64    `json:"average_watts"`
	DeviceWatts        *bool       `json:"device_watts"`
	Trainer            *bool       `json:"trainer"`
	Manual             *bool       `json:"manual"`
	WorkoutType        *int        `json:"workout_type"`
}

// event is one push subscription event.
type event struct {
	ObjectType     string          `json:"object_type"`
	ObjectID       json.Number     `json:"object_id"`
	AspectType     string          `json:"aspect_type"`
	Updates        json.RawMessage `json:"updates"`
	OwnerID        json.Number     `json:"owner_id"`
	SubscriptionID json.Number     `json:"subscription_id"`
	EventTime      int64           `json:"event_time"`
}
