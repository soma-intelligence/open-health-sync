// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package etl

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
)

// The UUIDs of the samples of these tests, as HealthKit writes them.
const (
	hkWeightOld = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B01"
	hkWeightNew = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B02"
	hkWeightRe  = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B03"
	hkSteps     = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B04"
	hkPressure  = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B05"
	hkWorkout   = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B06"
	hkMindful   = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B07"
	hkProtein   = "0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B08"
)

func hkWeight(uuid string, kg float64) string {
	return fmt.Sprintf(`{"quantityType":"HKQuantityTypeIdentifierBodyMass","value":%v,"unit":"kg","startDate":"2026-03-01T07:00:00Z","endDate":"2026-03-01T07:00:00Z","uuid":%q,"metadata":{"HKTimeZone":"Europe/Paris"}}`, kg, uuid)
}

func hkDeleted(uuid string) string { return fmt.Sprintf(`{"deletedUuid":%q}`, uuid) }

func (e *env) weights() []float64 {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT value FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2 AND quantity_type = 'body_mass' ORDER BY value`, e.tenant, e.user)
	require.NoError(e.t, err)
	var out []float64
	for rows.Next() {
		var v float64
		require.NoError(e.t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(e.t, rows.Err())
	return out
}

// A HealthKit edit is the deletion of the old sample and a new one: sent in
// the same part, the stored value is replaced, not duplicated.
func TestAppleHealthEditReplacesTheStoredSample(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierBodyMass", hkWeight(hkWeightOld, 78.4)))
	require.Equal(t, []float64{78.4}, e.weights())
	var id string
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT external_id FROM ohs_data.quantity_samples
		WHERE tenant_id = $1 AND user_id = $2`, e.tenant, e.user).Scan(&id))
	assert.Equal(t, hkWeightOld, id, "the HealthKit UUID is the sample's external id")

	// The new sample comes first in the part: deletions are applied first anyway.
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierBodyMass",
		hkWeight(hkWeightNew, 78.0), hkDeleted(hkWeightOld)))
	assert.Equal(t, []float64{78.0}, e.weights())

	// An edit that keeps value and time (an app that rewrites its samples)
	// has the content hash of the deleted copy; it still lands.
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierBodyMass",
		hkWeight(hkWeightRe, 78.0), hkDeleted(hkWeightNew)))
	assert.Equal(t, []float64{78.0}, e.weights())

	// A replay of the deleted sample's old part stores nothing: the tombstone wins.
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierBodyMass", hkWeight(hkWeightOld, 78.4)))
	assert.Equal(t, []float64{78.0}, e.weights())
	assert.Equal(t, 2, e.count("deleted_records"))
	assert.Equal(t, 0, e.count("failed_records"))
}

// A deletion in a part of its own removes the sample and the steps it added
// to its day's derived summary.
func TestAppleHealthDeletionRecomputesTheDay(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	steps := func(uuid string, n int, at string) string {
		return fmt.Sprintf(`{"quantityType":"HKQuantityTypeIdentifierStepCount","value":%d,"unit":"count","startDate":%q,"endDate":%q,"uuid":%q,"metadata":{"HKTimeZone":"Europe/Paris"}}`, n, at, at, uuid)
	}
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierStepCount",
		steps(hkSteps, 1000, "2026-03-01T09:00:00Z"),
		steps("0B7E5C1A-7A4D-4F2B-9C11-2E6A1D3F4B99", 200, "2026-03-02T09:00:00Z")))
	require.Equal(t, []daySteps{{"2026-03-01", 1000}, {"2026-03-02", 200}}, e.dailySteps(applehealth.Slug))

	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierStepCount", hkDeleted(hkSteps)))
	assert.Equal(t, []daySteps{{"2026-03-02", 200}}, e.dailySteps(applehealth.Slug),
		"the day left without samples loses its derived row")
	assert.Equal(t, 1, e.count("quantity_samples"))
}

// Blood pressure, workouts, category samples and nutrition logs are removed
// by the deletion of their UUID too.
func TestAppleHealthDeletionOfOtherKinds(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	lines := []string{
		fmt.Sprintf(`{"correlationType":"HKCorrelationTypeIdentifierBloodPressure","startDate":"2026-03-01T08:00:00Z","endDate":"2026-03-01T08:00:00Z","uuid":%q,"objects":[{"sampleType":"HKQuantityTypeIdentifierBloodPressureSystolic","value":121,"unit":"mmHg"},{"sampleType":"HKQuantityTypeIdentifierBloodPressureDiastolic","value":79,"unit":"mmHg"}]}`, hkPressure),
		fmt.Sprintf(`{"workoutActivityType":37,"startDate":"2026-03-01T10:00:00Z","endDate":"2026-03-01T10:30:00Z","duration":1800,"uuid":%q}`, hkWorkout),
		fmt.Sprintf(`{"categoryType":"HKCategoryTypeIdentifierMindfulSession","value":0,"startDate":"2026-03-01T12:00:00Z","endDate":"2026-03-01T12:10:00Z","uuid":%q}`, hkMindful),
		fmt.Sprintf(`{"quantityType":"HKQuantityTypeIdentifierDietaryProtein","value":30,"unit":"g","startDate":"2026-03-01T13:00:00Z","endDate":"2026-03-01T13:00:00Z","uuid":%q}`, hkProtein),
	}
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed", lines...))
	require.Equal(t, 2, e.count("quantity_samples"))
	require.Equal(t, 1, e.count("workouts"))
	require.Equal(t, 1, e.count("category_samples"))
	require.Equal(t, 1, e.count("nutrition_logs"))

	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "mixed",
		hkDeleted(hkPressure), hkDeleted(hkWorkout), hkDeleted(hkMindful), hkDeleted(hkProtein)))
	assert.Equal(t, 0, e.count("quantity_samples"), "both members of the pressure reading")
	assert.Equal(t, 0, e.count("workouts"))
	assert.Equal(t, 0, e.count("category_samples"))
	assert.Equal(t, 0, e.count("nutrition_logs"))
	assert.Equal(t, 4, e.count("deleted_records"))
}

// A deletion line without a UUID is rejected and recorded; the lines beside
// it land.
func TestAppleHealthDeletionWithoutUUIDFails(t *testing.T) {
	e := newEnv(t)
	e.reg.Register(applehealth.New())
	e.process(e.upload(e.tenant, e.user, applehealth.Slug, "HKQuantityTypeIdentifierBodyMass",
		`{"deletedUuid":""}`, hkWeight(hkWeightOld, 78.4)))
	assert.Equal(t, []float64{78.4}, e.weights())
	assert.Equal(t, 1, e.count("failed_records"))
	assert.Equal(t, 0, e.count("deleted_records"))
}
