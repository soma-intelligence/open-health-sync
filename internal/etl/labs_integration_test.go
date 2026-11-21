// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/labs"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// labUpload maps a simple form lab upload of the env's user.
func (e *env) labUpload(body string) *model.MapResult {
	e.t.Helper()
	reports, err := labs.Decode([]byte(body))
	require.NoError(e.t, err)
	return &model.MapResult{Observations: labs.Observations(e.user.String(), reports)}
}

type labRow struct {
	ts       time.Time
	value    *float64
	text     *string
	unit     *string
	norm     *float64
	status   string
	captured time.Time
}

// labRows reads the env's stored lab results by external id, opened.
func (e *env) labRows() map[string]labRow {
	e.t.Helper()
	ctx := context.Background()
	rows, err := e.read().Query(ctx, `SELECT external_id, ts, value_num, value_text, unit_ucum, value_canonical,
			metadata::text, captured_at
		FROM ohs_data.observations WHERE tenant_id = $1 AND user_id = $2 AND canonical_code LIKE 'lab:%'`, e.tenant, e.user)
	require.NoError(e.t, err)
	defer rows.Close()
	out := map[string]labRow{}
	for rows.Next() {
		var ext, meta string
		var r labRow
		require.NoError(e.t, rows.Scan(&ext, &r.ts, &r.value, &r.text, &r.unit, &r.norm, &meta, &r.captured))
		var md map[string]any
		require.NoError(e.t, json.Unmarshal([]byte(meta), &md))
		r.status, _ = md[labs.MetaStatus].(string)
		_, dup := out[ext]
		require.False(e.t, dup, "one row per result: %s is stored twice", ext)
		out[ext] = r
	}
	require.NoError(e.t, rows.Err())
	return out
}

const labV1 = `{"reports":[{"id":"r1","loinc":"24331-1","collected_at":"2026-03-04T08:30:00Z","results":[
	{"id":"chol","loinc":"2093-3","value":5.2,"unit":"mmol/L","reference_high":5.0,"interpretation":"H"},
	{"id":"note","code":"COMMENT","value_string":"haemolysed"}]}]}`

const labV2 = `{"reports":[{"id":"r1","loinc":"24331-1","status":"amended","collected_at":"2026-03-04T09:00:00Z","results":[
	{"id":"chol","loinc":"2093-3","value":4.8,"unit":"mmol/L","reference_high":5.0,"interpretation":"N","status":"corrected"},
	{"id":"note","code":"COMMENT","status":"cancelled"}]}]}`

// An amended result replaces the stored one whole (value, unit, flag, a
// moved collection time), a cancelled one clears its value, the same copy
// again changes nothing, and a copy from an older object is held back.
func TestWriter_LabResultsReplaceWhole(t *testing.T) {
	eachMode(t, func(t *testing.T, e *env) {
		t1 := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
		t2 := t1.Add(24 * time.Hour)

		c := e.writeAt(t1, e.labUpload(labV1))
		assert.Equal(t, TableCount{Written: 2}, c[tObservation])
		got := e.labRows()
		require.Len(t, got, 2)
		require.NotNil(t, got["chol"].value)
		assert.Equal(t, 5.2, *got["chol"].value)
		require.NotNil(t, got["chol"].norm)
		assert.InDelta(t, 5.2*38.665, *got["chol"].norm, 1e-9)
		require.NotNil(t, got["note"].text)
		assert.Equal(t, "haemolysed", *got["note"].text)

		// The same upload again (a retried job): nothing is written.
		c = e.writeAt(t1, e.labUpload(labV1))
		assert.Equal(t, TableCount{Skipped: 2}, c[tObservation])
		assert.Equal(t, got, e.labRows())

		// The amended report, from a later object.
		c = e.writeAt(t2, e.labUpload(labV2))
		assert.Equal(t, TableCount{Written: 2}, c[tObservation])
		got = e.labRows()
		require.Len(t, got, 2, "an amended result replaces, it does not add")
		assert.Equal(t, 4.8, *got["chol"].value)
		assert.Equal(t, "corrected", got["chol"].status)
		assert.True(t, got["chol"].ts.Equal(time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)), "the collection time moved")
		assert.True(t, got["chol"].captured.Equal(t2))
		assert.Nil(t, got["note"].value)
		assert.Nil(t, got["note"].text, "a cancelled result keeps no value of the earlier copy")
		assert.Equal(t, "cancelled", got["note"].status)

		// The first upload processed again after the amendment: held back.
		c = e.writeAt(t1, e.labUpload(labV1))
		assert.Equal(t, TableCount{Skipped: 2}, c[tObservation])
		assert.Equal(t, got, e.labRows(), "an older object never brings the earlier value back")

		// Two copies of one result in one batch: the last one wins.
		both := e.labUpload(labV1)
		both.Observations = append(both.Observations, e.labUpload(labV2).Observations...)
		c = e.writeAt(t2.Add(time.Hour), both)
		assert.Equal(t, TableCount{Skipped: 4}, c[tObservation], "two in-batch copies, two unchanged")
		assert.Equal(t, 4.8, *e.labRows()["chol"].value)
	})
}
