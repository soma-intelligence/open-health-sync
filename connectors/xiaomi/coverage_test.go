// SPDX-License-Identifier: AGPL-3.0-only

package xiaomi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// parse decodes an invented upload line.
func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestEnabledNeedsNoKeys(t *testing.T) {
	require.True(t, New().Enabled(nil))
	require.True(t, New().Enabled(connector.Config{"ANY": "x"}))
}

func TestOffsetZone(t *testing.T) {
	for minutes, want := range map[int]string{
		0: "Etc/UTC", 60: "Etc/GMT-1", -300: "Etc/GMT+5", 840: "Etc/GMT-14", -720: "Etc/GMT+12",
		330: "", -45: "",
	} {
		require.Equal(t, want, offsetZone(minutes), "offset %d", minutes)
	}
}

func TestMapTimezoneQuarters(t *testing.T) {
	cases := []struct {
		tz       string
		minutes  any
		zone     any
		hasZone  bool
		hasDelta bool
	}{
		{`-20`, -300, "Etc/GMT+5", true, true},
		{`"22"`, 330, nil, false, true},
		{`0`, 0, "Etc/UTC", true, true},
		{`57`, nil, nil, false, false},
		{`-49`, nil, nil, false, false},
	}
	for _, tc := range cases {
		line := `{"table":"fitness_data","sid":"9","key":"spo2","time":1772935200,"value":{"spo2":96,"timezone":` + tc.tz + `}}`
		s := one(t, mapOne(t, parse(t, line)))
		require.Equal(t, 96.0, s.Value, tc.tz)
		if tc.hasDelta {
			require.Equal(t, tc.minutes, s.Metadata["utc_offset_minutes"], tc.tz)
		} else {
			require.NotContains(t, s.Metadata, "utc_offset_minutes", tc.tz)
		}
		if tc.hasZone {
			require.Equal(t, tc.zone, s.Metadata["timezone"], tc.tz)
		} else {
			require.NotContains(t, s.Metadata, "timezone", tc.tz)
		}
	}
}

func TestMapInstantFallbacks(t *testing.T) {
	// heart_rate reads "hr" when "bpm" is absent, and the row time when the
	// value has none.
	s := one(t, mapOne(t, parse(t, `{"table":"fitness_data","sid":"7","key":"heart_rate","time":1772928240,"value":{"hr":"64"}}`)))
	require.Equal(t, model.QTHeartRate, s.QuantityType)
	require.Equal(t, 64.0, s.Value)
	require.Equal(t, at("2026-03-08T00:04:00Z"), s.StartDate)
	require.Equal(t, "7_heart_rate_1772928240", s.ExternalID)
	require.Empty(t, s.DeviceID, "no device joined in")
	require.Equal(t, "fitness_data", s.Metadata["mi_table"])
	require.Equal(t, "heart_rate", s.Metadata["mi_key"])
	require.Equal(t, "7", s.Metadata["mi_sid"])
	require.NotContains(t, s.Metadata, "mi_update_time")

	// A value time before 2000 is not a time; date_time is used next.
	s = one(t, mapOne(t, parse(t, `{"table":"fitness_data","sid":"7","key":"resting_heart_rate","value":{"time":12,"date_time":1772928000,"bpm":50}}`)))
	require.Equal(t, at("2026-03-08T00:00:00Z"), s.StartDate)

	// Out of the plausible range for a resting heart rate.
	require.Empty(t, mapOne(t, parse(t, `{"table":"fitness_data","key":"resting_heart_rate","time":1772928000,"value":{"bpm":210}}`)).QuantitySamples)
	require.Empty(t, mapOne(t, parse(t, `{"table":"fitness_data","key":"vo2_max","time":1772928000,"value":{"vo2_max":"n/a"}}`)).QuantitySamples)
}

func TestMapWeightEdges(t *testing.T) {
	res := mapOne(t, parse(t, `{"table":"fitness_data","sid":"5","key":"weight","time":1772953200,"value":{"weight":"68.25","body_fat_rate":100}}`))
	require.Len(t, res.QuantitySamples, 1, "a body fat of 100 percent is not a reading")
	require.Equal(t, "body_mass", res.QuantitySamples[0].QuantityType)
	require.Equal(t, 68.25, res.QuantitySamples[0].Value)
	require.Equal(t, "kg", res.QuantitySamples[0].Unit)
	require.Equal(t, at("2026-03-08T07:00:00Z"), res.QuantitySamples[0].StartDate)

	require.Empty(t, mapOne(t, parse(t, `{"table":"fitness_data","key":"weight","time":1772953200,"value":{"weight":612}}`)).QuantitySamples)
	require.Empty(t, mapOne(t, parse(t, `{"table":"fitness_data","key":"weight","time":1772953200,"value":{"weight":0}}`)).QuantitySamples)

	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: parse(t, `{"table":"fitness_data","key":"weight","value":{"weight":70}}`)})
	require.ErrorContains(t, err, "no readable time")
}

func TestMapEmptyMinuteRows(t *testing.T) {
	require.Empty(t, mapOne(t, parse(t, `{"table":"fitness_data","key":"steps","time":1772960400,"value":{"steps":0,"distance":0}}`)).QuantitySamples)
	require.Empty(t, mapOne(t, parse(t, `{"table":"fitness_data","key":"calories","time":1772960400,"value":{"calories":0}}`)).QuantitySamples)

	res := mapOne(t, parse(t, `{"table":"fitness_data","sid":"3","key":"steps","time":1772960400,"value":{"distance":12.5}}`))
	require.Len(t, res.QuantitySamples, 1)
	require.Equal(t, model.QTDistanceWalkingRunning, res.QuantitySamples[0].QuantityType)
	require.Equal(t, "3_steps_1772960400_distance", res.QuantitySamples[0].ExternalID)

	_, err := New().Map(connector.RawRecord{UserID: testUser, Fields: parse(t, `{"table":"fitness_data","key":"calories","value":{"calories":2}}`)})
	require.Error(t, err)
	_, err = New().Map(connector.RawRecord{UserID: testUser, Fields: parse(t, `{"table":"fitness_data","key":"steps","value":{"steps":2}}`)})
	require.Error(t, err)
}
