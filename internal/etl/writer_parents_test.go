// SPDX-License-Identifier: AGPL-3.0-only

package etl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A Replaceable workout upserts its figures; any other workout is first write
// wins. Both return the row id so route points can attach.
func TestWorkoutInsertStatements(t *testing.T) {
	assert.Contains(t, workoutInsert, "DO NOTHING")
	assert.Contains(t, workoutReplace, "DO UPDATE SET")
	for _, f := range workoutFigures {
		assert.Contains(t, workoutReplace, f+" = COALESCE(EXCLUDED."+f+", workouts."+f+")")
	}
	assert.NotContains(t, workoutReplace, "content_hash = ", "the identity is never rewritten")
	assert.True(t, strings.HasSuffix(workoutInsert, " RETURNING id"))
	assert.True(t, strings.HasSuffix(workoutReplace, " RETURNING id"))
}

// A Replaceable sleep session upserts its figures; any other session is first
// write wins. Both return the row id so the phases can attach.
func TestSleepSessionInsertStatements(t *testing.T) {
	assert.Contains(t, sleepSessionInsert, "DO NOTHING")
	assert.Contains(t, sleepSessionReplace, "ON CONFLICT (tenant_id, user_id, content_hash, bedtime_start) DO UPDATE SET")
	for _, f := range sleepFigures {
		assert.Contains(t, sleepSessionReplace, f+" = COALESCE(EXCLUDED."+f+", sleep_sessions."+f+")")
	}
	assert.Contains(t, sleepSessionReplace, "metadata = EXCLUDED.metadata")
	assert.NotContains(t, sleepSessionReplace, "content_hash = ", "the identity is never rewritten")
	assert.NotContains(t, sleepSessionReplace, "bedtime_start = ", "the partition key is never rewritten")
	assert.True(t, strings.HasSuffix(sleepSessionInsert, " RETURNING id"))
	assert.True(t, strings.HasSuffix(sleepSessionReplace, " RETURNING id"))

	// Every figure column the insert writes is replaced, except the identity
	// and attribution columns.
	notFigures := map[string]bool{
		"tenant_id": true, "user_id": true, "provider": true, "source_slug": true, "external_id": true,
		"bedtime_start": true, "content_hash": true, "metadata": true,
	}
	var want []string
	for _, c := range sleepSessionColumns {
		if !notFigures[c] {
			want = append(want, c)
		}
	}
	assert.ElementsMatch(t, want, sleepFigures)
}

// Every statement of the replace path stays inside the tenant and user.
func TestReplaceStatementsScopedToTenantAndUser(t *testing.T) {
	for name, q := range map[string]string{
		"sleepSessionSupersede": sleepSessionSupersede,
		"sleepPhasesReplace":    sleepPhasesReplace,
		"sleepPhasesAdopt":      sleepPhasesAdopt,
		"workoutSupersede":      workoutSupersede,
		"workoutRoutesAdopt":    workoutRoutesAdopt,
		"workoutRoutesDrop":     workoutRoutesDrop,
		"workoutTracksDrop":     workoutTracksDrop,
	} {
		assert.Contains(t, q, "tenant_id = $1 AND user_id = $2", name)
	}
	// A superseded copy is the same provider record under another row.
	for _, q := range []string{sleepSessionSupersede, workoutSupersede} {
		assert.Contains(t, q, "provider = $3 AND external_id = $4 AND id <> $5")
		assert.True(t, strings.HasSuffix(q, "RETURNING id"))
	}
	// The kept row loses only the phases missing from the new copy.
	assert.Contains(t, sleepPhasesReplace, "session_id = ANY($3) OR (session_id = $4 AND content_hash <> ALL($5))")
}
