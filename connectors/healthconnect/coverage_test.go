// SPDX-License-Identifier: AGPL-3.0-only

package healthconnect

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

func TestEnabledNeedsNoKeys(t *testing.T) {
	require.True(t, New().Enabled(nil))
	require.True(t, New().Enabled(connector.Config{"ANY": "x"}))
	require.Equal(t, "health_connect", New().Slug())
}

func TestOffsetZone(t *testing.T) {
	for minutes, want := range map[int]string{
		0: "Etc/UTC", 120: "Etc/GMT-2", -300: "Etc/GMT+5", 840: "Etc/GMT-14", -720: "Etc/GMT+12",
		330: "", -570: "",
	} {
		require.Equal(t, want, offsetZone(minutes), "offset %d", minutes)
	}
}

// instantLine is an instant record of recordType with one value field.
func instantLine(recordType, field string, v any, offset string) map[string]any {
	return map[string]any{
		"recordType": recordType,
		"metadata":   map[string]any{"id": "id-" + recordType, "dataOrigin": "com.example.app"},
		"time":       "2026-05-04T06:30:00Z", "zoneOffset": offset,
		field: v,
	}
}

func TestMapInstantTypes(t *testing.T) {
	cases := []struct {
		recordType, field string
		v                 float64
		qt, unit          string
	}{
		{"RestingHeartRate", "beatsPerMinute", 54, model.QTRestingHeartRate, "bpm"},
		{"HeartRateVariabilityRmssd", "heartRateVariabilityMillis", 41.5, model.QTHRVRMSSD, "ms"},
		{"OxygenSaturation", "percentage", 97, model.QTOxygenSaturation, "percent"},
		{"RespiratoryRate", "rate", 14.5, model.QTRespiratoryRate, "bpm"},
		{"BodyTemperature", "temperatureCelsius", 36.6, model.QTBodyTemperature, "celsius"},
		{"Height", "heightMeters", 1.72, "height", "m"},
		{"BodyFat", "percentage", 21.3, "body_fat_percentage", "percent"},
	}
	at := time.Date(2026, 5, 4, 6, 30, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.recordType, func(t *testing.T) {
			res := mapOne(t, instantLine(tc.recordType, tc.field, tc.v, "-05:00"))
			require.Len(t, res.QuantitySamples, 1)
			s := res.QuantitySamples[0]
			require.Equal(t, tc.qt, s.QuantityType)
			require.Equal(t, tc.unit, s.Unit)
			require.Equal(t, tc.v, s.Value)
			require.Equal(t, at, s.StartDate)
			require.Equal(t, at, s.EndDate)
			require.Equal(t, "id-"+tc.recordType, s.ExternalID)
			require.Equal(t, "com_example_app", s.SourceSlug)
			require.Equal(t, -300, s.Metadata["utc_offset_minutes"])
			require.Equal(t, "Etc/GMT+5", s.Metadata["timezone"])
			require.Equal(t, tc.recordType, s.Metadata["record_type"])
			require.Nil(t, s.WasUserEntered)
			require.Empty(t, s.DeviceID)
		})
	}
}

func TestMapInstantOffsetsAndLocation(t *testing.T) {
	// A half hour offset keeps its minutes but has no Etc zone.
	line := instantLine("BodyTemperature", "temperatureCelsius", 36.9, "+05:30")
	line["measurementLocation"] = "forehead"
	s := mapOne(t, line).QuantitySamples[0]
	require.Equal(t, 330, s.Metadata["utc_offset_minutes"])
	require.NotContains(t, s.Metadata, "timezone")
	require.Equal(t, "forehead", s.Metadata["measurement_location"])

	s = mapOne(t, instantLine("OxygenSaturation", "percentage", 99, "Z")).QuantitySamples[0]
	require.Equal(t, 0, s.Metadata["utc_offset_minutes"])
	require.Equal(t, "Etc/UTC", s.Metadata["timezone"])

	// An offset beyond +14:00 is not a real zone and is dropped.
	s = mapOne(t, instantLine("OxygenSaturation", "percentage", 99, "+15:00")).QuantitySamples[0]
	require.NotContains(t, s.Metadata, "utc_offset_minutes")
	require.NotContains(t, s.Metadata, "timezone")
}

func TestMapInstantErrors(t *testing.T) {
	noValue := instantLine("RestingHeartRate", "percentage", 50, "")
	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: noValue})
	require.EqualError(t, err, "health_connect: RestingHeartRate id-RestingHeartRate: no value")

	badTime := instantLine("Weight", "weightKilograms", 70, "")
	badTime["time"] = "2026-05-04 06:30"
	_, err = New().Map(connector.RawRecord{UserID: testUser, Fields: badTime})
	require.ErrorContains(t, err, `unreadable time "2026-05-04 06:30"`)

	_, err = New().Map(connector.RawRecord{UserID: testUser, Fields: map[string]any{"recordType": "Steps", "count": "many"}})
	require.ErrorContains(t, err, "health_connect:")
}

func TestMapIntervalTypes(t *testing.T) {
	line := func(recordType, field string, v float64) map[string]any {
		return map[string]any{
			"recordType": recordType, "metadata": map[string]any{"id": "iv-1", "dataOrigin": "com.garmin.android.apps.connectmobile"},
			"startTime": "2026-05-04T10:00:00Z", "endTime": "2026-05-04T10:30:00Z", "startZoneOffset": "+01:00",
			field: v,
		}
	}
	s := mapOne(t, line("Distance", "distanceMeters", 4200)).QuantitySamples[0]
	require.Equal(t, model.QTDistanceWalkingRunning, s.QuantityType)
	require.Equal(t, "m", s.Unit)
	require.Equal(t, 4200.0, s.Value)
	require.Equal(t, "garmin", s.SourceSlug)
	require.Equal(t, 30*time.Minute, s.EndDate.Sub(s.StartDate))
	require.Equal(t, "Etc/GMT-1", s.Metadata["timezone"], "startZoneOffset is read when zoneOffset is absent")

	s = mapOne(t, line("ActiveCaloriesBurned", "energyKilocalories", 180.5)).QuantitySamples[0]
	require.Equal(t, model.QTActiveEnergyBurned, s.QuantityType)
	require.Equal(t, "kcal", s.Unit)
	require.Equal(t, 180.5, s.Value)

	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: line("Steps", "distanceMeters", 1)})
	require.ErrorContains(t, err, "no value")

	missing := line("Steps", "count", 10)
	delete(missing, "endTime")
	_, err = New().Map(connector.RawRecord{UserID: testUser, Fields: missing})
	require.ErrorContains(t, err, "startTime and endTime are required")
}

func TestMapHeartRateEdges(t *testing.T) {
	samples := make([]any, 0, 102)
	for i := range 101 {
		samples = append(samples, map[string]any{"time": fmt.Sprintf("2026-05-04T10:%02d:%02dZ", i/60, i%60), "beatsPerMinute": 60 + i%5})
	}
	samples = append(samples, map[string]any{"time": "2026-05-04T10:59:00Z"})
	line := map[string]any{"recordType": "HeartRate", "metadata": map[string]any{"id": "hr-9"}, "samples": samples}
	res := mapOne(t, line)
	require.True(t, res.HighFrequency, "a series of more than 100 points is high frequency")
	require.Len(t, res.QuantitySamples, 101, "a point without a value is skipped")
	last := res.QuantitySamples[100]
	require.Equal(t, "hr-9_100", last.ExternalID)
	require.Equal(t, time.Date(2026, 5, 4, 10, 1, 40, 0, time.UTC), last.StartDate)
	require.Equal(t, 60.0, last.Value)
	require.Equal(t, "bpm", last.Unit)

	bad := map[string]any{"recordType": "HeartRate", "metadata": map[string]any{"id": "hr-10"},
		"samples": []any{map[string]any{"time": "later", "beatsPerMinute": 70}}}
	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: bad})
	require.ErrorContains(t, err, "HeartRate hr-10 sample 0")
}
