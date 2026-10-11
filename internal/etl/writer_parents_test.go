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
