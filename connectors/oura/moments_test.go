// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// --- daily_sleep ---

func TestMapDailySleep(t *testing.T) {
	res := mustMap(t, DataTypeDailySleep, fixture(t, DataTypeDailySleep, 0))
	require.Len(t, res.QuantitySamples, 1)
	assertCanonical(t, res.QuantitySamples)
	s := res.QuantitySamples[0]
	assert.Equal(t, model.QTSleepScore, s.QuantityType)
	assert.InDelta(t, 82.0, s.Value, 1e-9)
	assert.Equal(t, "score", s.Unit)
	assert.True(t, time.Date(2026, 3, 2, 0, 0, 0, 0, paris(t)).Equal(s.StartDate))
	assert.Equal(t, "Etc/GMT-1", s.Metadata["timezone"])
	assert.Equal(t, "daily-sleep-0001", s.Metadata["oura_id"])
	contributors, ok := s.Metadata["contributors"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 95.0, contributors["timing"])
	assert.True(t, s.Replaceable)
	assert.Empty(t, res.SleepSessions, "the day's score is not a session")

	upd := mustMap(t, DataTypeDailySleep, edit(t, fixture(t, DataTypeDailySleep, 0), map[string]any{"score": 70}))
	assert.Equal(t, s.ContentHash, upd.QuantitySamples[0].ContentHash, "the newest score replaces the stored one")

	assert.True(t, isEmpty(mustMap(t, DataTypeDailySleep, fixture(t, DataTypeDailySleep, 1))), "no score, nothing stored")
}

// --- daily_stress ---

func TestMapDailyStress(t *testing.T) {
	res := mustMap(t, DataTypeDailyStress, fixture(t, DataTypeDailyStress, 0))
	require.Len(t, res.QuantitySamples, 2)
	for _, s := range res.QuantitySamples {
		assert.Equal(t, "s", s.Unit, "sent in seconds, converted by the pipeline")
	}
	require.Empty(t, model.CanonicalizeUnits(&res), "seconds convert to the storage unit")
	assertCanonical(t, res.QuantitySamples)
	m := byType(res.QuantitySamples)
	assert.InDelta(t, 90.0, m[model.QTStressHighTime].Value, 1e-9)
	assert.InDelta(t, 120.0, m[model.QTRecoveryHighTime].Value, 1e-9)
	for _, s := range res.QuantitySamples {
		assert.Equal(t, "min", s.Unit)
		assert.Equal(t, "normal", s.Metadata["day_summary"])
		assert.True(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Equal(s.StartDate), "no timestamp: a UTC day")
		assert.True(t, s.Replaceable)
	}
	assert.NotEqual(t, m[model.QTStressHighTime].ContentHash, m[model.QTRecoveryHighTime].ContentHash)

	// A missing figure is skipped, and the other still lands.
	res = mustMap(t, DataTypeDailyStress, fixture(t, DataTypeDailyStress, 1))
	require.Len(t, res.QuantitySamples, 1)
	assert.Equal(t, model.QTRecoveryHighTime, res.QuantitySamples[0].QuantityType)
	assert.NotContains(t, res.QuantitySamples[0].Metadata, "day_summary")
}

// --- session ---

func TestMapSession_Mindful(t *testing.T) {
	res := mustMap(t, DataTypeSession, fixture(t, DataTypeSession, 0))
	require.Len(t, res.CategorySamples, 1)
	assert.Empty(t, res.QuantitySamples)
	c := res.CategorySamples[0]
	assert.Equal(t, "mindful_session", c.CategoryType)
	assert.Equal(t, "meditation", c.ValueLabel)
	assert.Equal(t, "session-0001", c.ExternalID, "a delete event names this id")
	assert.Equal(t, Slug, c.Provider)
	assert.True(t, time.Date(2026, 3, 1, 17, 0, 0, 0, time.UTC).Equal(c.StartDate))
	assert.True(t, time.Date(2026, 3, 1, 17, 10, 0, 0, time.UTC).Equal(c.EndDate))
	assert.Equal(t, "good", c.Metadata["mood"])
	assert.Equal(t, "Etc/GMT-1", c.Metadata["timezone"])
	assert.NotContains(t, c.Metadata, "heart_rate", "the series stay in the raw line")
	assert.NotEmpty(t, c.ContentHash)

	res = mustMap(t, DataTypeSession, fixture(t, DataTypeSession, 2))
	require.Len(t, res.CategorySamples, 1)
	assert.Equal(t, "breathing", res.CategorySamples[0].ValueLabel)
}

// A nap, rest or body status session is not a mindful practice.
func TestMapSession_OtherTypesGiveNothing(t *testing.T) {
	assert.True(t, isEmpty(mustMap(t, DataTypeSession, fixture(t, DataTypeSession, 1))), "a nap")
	for _, typ := range []string{"rest", "body_status"} {
		assert.True(t, isEmpty(mustMap(t, DataTypeSession, edit(t, fixture(t, DataTypeSession, 0), map[string]any{"type": typ}))), typ)
	}
}

// --- vo2_max ---

func TestMapVO2Max(t *testing.T) {
	res := mustMap(t, DataTypeVO2Max, fixture(t, DataTypeVO2Max, 0))
	require.Len(t, res.QuantitySamples, 1)
	assertCanonical(t, res.QuantitySamples)
	s := res.QuantitySamples[0]
	assert.Equal(t, "vo2_max", s.QuantityType)
	assert.InDelta(t, 45.5, s.Value, 1e-9)
	assert.Equal(t, "mL/kg/min", s.Unit)
	assert.True(t, time.Date(2026, 3, 2, 0, 0, 0, 0, paris(t)).Equal(s.StartDate))
	assert.True(t, s.Replaceable)

	assert.True(t, isEmpty(mustMap(t, DataTypeVO2Max, fixture(t, DataTypeVO2Max, 1))), "no estimate, nothing stored")
}

// vo2_max is served at Oura's spelling of the endpoint.
func TestFetch_VO2MaxEndpoint(t *testing.T) {
	f := newFakeOura(t)
	sink := &recordingSink{}
	require.NoError(t, f.connector().FetchTypes(context.Background(), testConn(), fetchFrom, fetchTo, []string{DataTypeVO2Max}, sink))
	assert.Len(t, f.Calls("/v2/usercollection/vO2_max"), 1)
	assert.Len(t, sink.linesOf(DataTypeVO2Max), 2)
}

// An endpoint that answers 404 is skipped and the other types still come.
func TestFetch_NotFoundEndpointIsSkipped(t *testing.T) {
	f := newFakeOura(t)
	f.Status = map[string]int{"vO2_max": http.StatusNotFound}
	sink := &recordingSink{}
	require.NoError(t, f.connector().Fetch(context.Background(), testConn(), fetchFrom, fetchTo, sink))
	assert.Empty(t, sink.linesOf(DataTypeVO2Max))
	assert.NotEmpty(t, sink.linesOf(DataTypeSession))
}
