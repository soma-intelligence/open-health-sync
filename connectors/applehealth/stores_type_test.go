// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import "testing"

// TestStoresType checks the exported list of stored HealthKit identifiers
// against the mapper's own dispatch: a client that skips what StoresType
// rejects must never drop a type the mapper would have stored.
func TestStoresType(t *testing.T) {
	for _, id := range []string{
		"HKQuantityTypeIdentifierHeartRate",
		"HKQuantityTypeIdentifierDietaryProtein",
		"HKCategoryTypeIdentifierSleepAnalysis",
		"HKCategoryTypeIdentifierMindfulSession",
	} {
		if !StoresType(id) {
			t.Errorf("StoresType(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"HKQuantityTypeIdentifierAppleWalkingSteadiness",
		"HKDataTypeSleepDurationGoal",
		"HKQuantityTypeIdentifierDietaryNotANutrient",
		"",
	} {
		if StoresType(id) {
			t.Errorf("StoresType(%q) = true, want false", id)
		}
	}
	m := newMapper()
	for _, id := range m.supportedTypes() {
		if len(id) > 2 && id[:2] == "HK" && id != "HKCorrelationTypeIdentifierFood" && !StoresType(id) {
			t.Errorf("mapper supports %q but StoresType says no", id)
		}
	}
}
