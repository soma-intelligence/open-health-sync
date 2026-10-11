// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// recordsMap is a provider whose lines are whole records: a sleep with two
// phases and a derived sample ("<id>_resting_heart_rate"), a workout with
// two route points, a mindful category sample, or a deletion of an id.
func recordsMap(r connector.RawRecord) (model.MapResult, error) {
	id, _ := r.Fields["id"].(string)
	kind, _ := r.Fields["kind"].(string)
	if kind == "delete" {
		return model.MapResult{Deletions: []model.Deletion{{ExternalID: id}}}, nil
	}
	start, err := time.Parse(time.RFC3339, r.Fields["start"].(string))
	if err != nil {
		return model.MapResult{}, err
	}
	end := start.Add(time.Hour)
	hash := model.ComputeContentHash(map[string]string{"user_id": r.UserID, "provider": r.Provider, "id": id, "kind": kind})
	switch kind {
	case "sleep":
		hr := 50.0
		return model.MapResult{
			SleepSessions: []model.SleepSession{{
				Provider: r.Provider, ExternalID: id, BedtimeStart: start, BedtimeEnd: end,
				RestingHR: &hr, ContentHash: hash, Replaceable: true,
			}},
			SleepPhases: []model.SleepPhase{
				{Phase: "deep", StartDate: start, EndDate: start.Add(30 * time.Minute), DurationS: 1800, ParentContentHash: hash},
				{Phase: "rem", StartDate: start.Add(30 * time.Minute), EndDate: end, DurationS: 1800, ParentContentHash: hash},
			},
			QuantitySamples: []model.QuantitySample{{
				Provider: r.Provider, ExternalID: id + "_" + model.QTRestingHeartRate, QuantityType: model.QTRestingHeartRate,
				Value: hr, Unit: "bpm", StartDate: start, EndDate: end, ContentHash: hash + "-rhr", Replaceable: true,
			}},
		}, nil
	case "workout":
		return model.MapResult{
			Workouts: []model.Workout{{
				Provider: r.Provider, ExternalID: id, ActivityType: "running", StartDate: start, EndDate: end,
				ContentHash: hash, Replaceable: true,
			}},
			RoutePoints: []model.RoutePoint{
				{Timestamp: start, Latitude: 48.1, Longitude: 2.1, ParentContentHash: hash},
				{Timestamp: start.Add(time.Minute), Latitude: 48.2, Longitude: 2.2, ParentContentHash: hash},
			},
		}, nil
	case "mindful":
		return model.MapResult{CategorySamples: []model.CategorySample{{
			Provider: r.Provider, ExternalID: id, CategoryType: "mindful_session", StartDate: start, EndDate: end, ContentHash: hash,
		}}}, nil
	}
	return model.MapResult{}, fmt.Errorf("unknown kind %q", kind)
}

func rec(kind, id string, start time.Time) string {
	return line(map[string]any{"kind": kind, "id": id, "start": start.UTC().Format(time.RFC3339)})
}

func del(id string) string { return line(map[string]any{"kind": "delete", "id": id}) }

// recordCounts is the rows of every table a deletion touches, for one user.
func (e *env) recordCounts(tenant, user uuid.UUID) map[string]int {
	e.t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"sleep_sessions", "sleep_phases", "workouts", "route_points", "quantity_samples", "category_samples"} {
		out[tbl] = e.countFor(tbl, tenant, user)
	}
	return out
}

func (e *env) tombstones(tenant, user uuid.UUID) int {
	e.t.Helper()
	return e.countFor("deleted_records", tenant, user)
}

var deletionBase = time.Date(2026, 9, 1, 22, 0, 0, 0, time.UTC)

func TestDeletion_RemovesRecordAndChildren(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.process(e.upload(e.tenant, e.user, "prov", "records",
		rec("sleep", "s-1", deletionBase), rec("sleep", "s-2", deletionBase.Add(24*time.Hour)),
		rec("workout", "w-1", deletionBase.Add(-6*time.Hour)), rec("mindful", "m-1", deletionBase.Add(-2*time.Hour))))
	// A workout track summary, as the track builder leaves for a traced workout.
	e.exec(`INSERT INTO ohs_data.workout_tracks (tenant_id, user_id, workout_id, length_m, point_count, started_at, ended_at)
		SELECT tenant_id, user_id, id, 100, 2, start_date, end_date FROM ohs_data.workouts WHERE tenant_id = $1 AND user_id = $2`,
		e.tenant, e.user)
	require.Equal(t, map[string]int{
		"sleep_sessions": 2, "sleep_phases": 4, "workouts": 1, "route_points": 2, "quantity_samples": 2, "category_samples": 1,
	}, e.recordCounts(e.tenant, e.user))

	c := e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1"), del("w-1"), del("m-1"), del("s-1")))
	assert.Equal(t, TableCount{Written: 3, Skipped: 1}, c[tDeleted], "the repeated deletion is applied once")
	assert.Equal(t, map[string]int{
		"sleep_sessions": 1, "sleep_phases": 2, "workouts": 0, "route_points": 0, "quantity_samples": 1, "category_samples": 0,
	}, e.recordCounts(e.tenant, e.user))
	assert.Equal(t, 0, e.count("workout_tracks"))
	assert.Equal(t, 3, e.tombstones(e.tenant, e.user))
	var left string
	require.NoError(t, e.pool.QueryRow(context.Background(),
		`SELECT external_id FROM ohs_data.quantity_samples WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&left))
	assert.Equal(t, "s-2_resting_heart_rate", left)

	// The same deletion again changes nothing and fails nothing.
	c = e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1")))
	assert.Equal(t, TableCount{Skipped: 1}, c[tDeleted])
	assert.Equal(t, 1, e.count("sleep_sessions"))
}

func TestDeletion_ReprocessingTheOriginalStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	orig := e.upload(e.tenant, e.user, "prov", "records", rec("sleep", "s-1", deletionBase), rec("workout", "w-1", deletionBase.Add(-6*time.Hour)))
	e.process(orig)
	e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1"), del("w-1")))

	c := e.process(orig)
	assert.Equal(t, 0, c.TotalWritten())
	assert.Equal(t, TableCount{Skipped: 1}, c[tSleep])
	assert.Equal(t, TableCount{Skipped: 2}, c[tPhases])
	assert.Equal(t, TableCount{Skipped: 1}, c[tQuantity])
	assert.Equal(t, TableCount{Skipped: 1}, c[tWorkouts])
	assert.Equal(t, TableCount{Skipped: 2}, c[tRoutes])
	for tbl, n := range e.recordCounts(e.tenant, e.user) {
		assert.Zero(t, n, tbl)
	}
}

func TestDeletion_BeforeTheRecordStillWins(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	c := e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1")))
	assert.Equal(t, TableCount{Written: 1}, c[tDeleted])

	e.process(e.upload(e.tenant, e.user, "prov", "records", rec("sleep", "s-1", deletionBase), rec("sleep", "s-2", deletionBase.Add(24*time.Hour))))
	got := e.recordCounts(e.tenant, e.user)
	assert.Equal(t, 1, got["sleep_sessions"])
	assert.Equal(t, 2, got["sleep_phases"])
	assert.Equal(t, 1, got["quantity_samples"])

	// A record and its deletion in one object: the deletion wins too.
	c = e.process(e.upload(e.tenant, e.user, "prov", "records", rec("workout", "w-9", deletionBase), del("w-9")))
	assert.Equal(t, TableCount{Skipped: 1}, c[tWorkouts])
	assert.Equal(t, 0, e.count("workouts"))
}

func TestDeletion_OtherUsersAndProvidersUntouched(t *testing.T) {
	e := newEnv(t)
	e.register("prov", recordsMap)
	e.register("other", recordsMap)
	sameTenant := e.newUser(e.tenant)
	otherTenant := e.newTenant()
	otherUser := e.newUser(otherTenant)
	lines := []string{rec("sleep", "s-1", deletionBase), rec("workout", "w-1", deletionBase.Add(-6*time.Hour))}
	for _, o := range []struct{ tenant, user uuid.UUID }{{e.tenant, e.user}, {e.tenant, sameTenant}, {otherTenant, otherUser}} {
		e.process(e.upload(o.tenant, o.user, "prov", "records", lines...))
	}
	// The same ids under another provider for the deleting user.
	e.process(e.upload(e.tenant, e.user, "other", "records", lines...))
	full := map[string]int{
		"sleep_sessions": 1, "sleep_phases": 2, "workouts": 1, "route_points": 2, "quantity_samples": 1, "category_samples": 0,
	}

	e.process(e.upload(e.tenant, e.user, "prov", "records", del("s-1"), del("w-1")))

	var remaining int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ohs_data.sleep_sessions
		WHERE tenant_id = $1 AND user_id = $2 AND provider = 'other'`, e.tenant, e.user).Scan(&remaining))
	assert.Equal(t, 1, remaining, "the other provider's record of the same id stays")
	assert.Equal(t, 1, e.count("sleep_sessions"))
	assert.Equal(t, full, e.recordCounts(e.tenant, sameTenant))
	assert.Equal(t, full, e.recordCounts(otherTenant, otherUser))
	assert.Zero(t, e.tombstones(e.tenant, sameTenant))
	assert.Zero(t, e.tombstones(otherTenant, otherUser))

	// Their copies still come in after the deletion.
	e.exec(`DELETE FROM ohs_data.sleep_sessions WHERE tenant_id = $1 AND user_id = $2`, otherTenant, otherUser)
	e.process(e.upload(otherTenant, otherUser, "prov", "records", lines[0]))
	assert.Equal(t, 1, e.countFor("sleep_sessions", otherTenant, otherUser))
}
