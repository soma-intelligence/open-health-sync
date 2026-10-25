// SPDX-License-Identifier: AGPL-3.0-only

package quality

// Device names the hardware behind a figure at three levels of detail, so a
// prior can be found for a model nobody has validated: its family, then its
// kind of sensor.
type Device struct {
	// Model is the exact model, in snake case: "oura_gen3", "apple_watch_s8".
	Model string `json:"model,omitempty"`
	// Family groups models that share an algorithm: "oura", "apple_watch".
	Family string `json:"family,omitempty"`
	// Sensor is the kind of measurement, one of the Sensor constants.
	Sensor string `json:"sensor,omitempty"`
}

// Sensor kinds, the third level of the prior fallback chain.
const (
	SensorWristPPG = "wrist_ppg" // a watch or band
	SensorRingPPG  = "ring_ppg"
	SensorChestECG = "chest_ecg" // a chest strap or ECG patch
	SensorCuff     = "cuff"
	SensorScale    = "scale"
	SensorCGM      = "cgm"
	SensorPhone    = "phone"
)

// Group returns the calibration family of the device: its family, or its
// sensor kind, or "other".
func (d Device) Group() string {
	switch {
	case d.Family != "":
		return d.Family
	case d.Sensor != "":
		return d.Sensor
	}
	return "other"
}
