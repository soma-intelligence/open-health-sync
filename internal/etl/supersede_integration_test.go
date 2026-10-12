// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// movedMap maps one line to one Replaceable quantity sample whose hash names
// the external id and the quantity type, never the start or the value (the
// WHOOP and Oura convention). "moved" sets SupersedeMoved.
func movedMap(r connector.RawRecord) (model.MapResult, error) {
	start, err := time.Parse(time.RFC3339, r.Fields["start"].(string))
	if err != nil {
		return model.MapResult{}, err
	}
	id := r.Fields["id"].(string)
	qt := r.Fields["type"].(string)
	moved, _ := r.Fields["moved"].(bool)
	unit := "index"
	if qt == model.QTHeartRate {
		unit = "bpm"
	}
	return model.MapResult{QuantitySamples: []model.QuantitySample{{
		Provider: r.Provider, ExternalID: id, QuantityType: qt, Value: r.Fields["value"].(float64), Unit: unit,
		StartDate: start.UTC(), EndDate: start.UTC(), Replaceable: true, SupersedeMoved: moved,
		ContentHash: model.ComputeContentHash(map[string]string{"user_id": r.UserID, "id": id, "type": qt}),
	}}}, nil
}

func mv(id, qt string, v float64, start time.Time, moved bool) string {
	return line(map[string]any{"id": id, "type": qt, "value": v, "start": start.UTC().Format(time.RFC3339), "moved": moved})
}

type storedSample struct {
	ExternalID string
	Type       string
	Value      float64
	Start      time.Time
}

func (e *env) storedSamples() []storedSample {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT external_id, quantity_type, value, start_date
		FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2
		ORDER BY external_id, quantity_type, start_date`, e.tenant, e.user)
	require.NoError(e.t, err)
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (storedSample, error) {
		var s storedSample
		err := r.Scan(&s.ExternalID, &s.Type, &s.Value, &s.Start)
		s.Start = s.Start.UTC()
		return s, err
	})
	require.NoError(e.t, err)
	return out
}

// A SupersedeMoved sample whose start moved replaces the stored copy of the
// same record and quantity instead of adding a second row. Samples that
// share an external id across starts without the flag (a day's heart rate
// series) all stay, and so do the other quantities of the record.
func TestQuantitySample_SupersedeMoved(t *testing.T) {
	e := newEnv(t)
	e.register("prov", movedMap)
	t0 := time.Date(2026, 9, 1, 22, 0, 0, 0, time.UTC)
	e.process(e.upload(e.tenant, e.user, "prov", "q",
		mv("c-1_strain", "strain", 10, t0, true),
		mv("c-1_heart_rate", "heart_rate", 60, t0, true),
		mv("continuous_hr_2026-09-01", "heart_rate", 61, t0, false),
	))
	other := e.newUser(e.tenant)
	e.process(e.upload(e.tenant, other, "prov", "q", mv("c-1_strain", "strain", 10, t0, true)))

	// The cycle's start shifts by an hour; the series gets another reading.
	c := e.process(e.upload(e.tenant, e.user, "prov", "q",
		mv("c-1_strain", "strain", 11, t0.Add(time.Hour), true),
		mv("continuous_hr_2026-09-01", "heart_rate", 62, t0.Add(5*time.Minute), false),
	))
	assert.Equal(t, TableCount{Written: 2}, c[tQuantity])
	assert.Equal(t, []storedSample{
		{"c-1_heart_rate", "heart_rate", 60, t0},
		{"c-1_strain", "strain", 11, t0.Add(time.Hour)},
		{"continuous_hr_2026-09-01", "heart_rate", 61, t0},
		{"continuous_hr_2026-09-01", "heart_rate", 62, t0.Add(5 * time.Minute)},
	}, e.storedSamples())
	assert.Equal(t, 1, e.countFor("quantity_samples", e.tenant, other), "another user's copy stays")

	// Two copies of one record in one object: the last one is kept.
	c = e.process(e.upload(e.tenant, e.user, "prov", "q",
		mv("c-1_strain", "strain", 12, t0.Add(2*time.Hour), true),
		mv("c-1_strain", "strain", 13, t0.Add(3*time.Hour), true),
	))
	assert.Equal(t, TableCount{Written: 1, Skipped: 1}, c[tQuantity])
	got := e.storedSamples()
	require.Len(t, got, 4)
	assert.Equal(t, storedSample{"c-1_strain", "strain", 13, t0.Add(3 * time.Hour)}, got[1])

	// Moving back to a start already stored is an update of that row.
	e.process(e.upload(e.tenant, e.user, "prov", "q", mv("c-1_strain", "strain", 14, t0, true)))
	got = e.storedSamples()
	require.Len(t, got, 4)
	assert.Equal(t, storedSample{"c-1_strain", "strain", 14, t0}, got[1])

	plan := e.explain("EXPLAIN "+quantitySupersede, e.tenant, e.user,
		[]string{"prov"}, []string{"c-1_strain"}, []string{"strain"}, []time.Time{t0})
	assert.Contains(t, plan, "_external on", "uses the external id index: %s", plan)
}
