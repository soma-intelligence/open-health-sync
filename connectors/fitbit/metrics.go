// SPDX-License-Identifier: AGPL-3.0-only

package fitbit

import (
	"errors"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Activity intervals (one minute each). They land under the *_intraday keys
// (and flights_climbed): this connector pulls no daily totals, so a daily
// sum over the intraday key counts the day once.

func mapSteps(p point, userID string) (*model.MapResult, error) {
	return mapIntervalCount(p, userID, model.QTStepCountIntraday)
}

func mapFloors(p point, userID string) (*model.MapResult, error) {
	return mapIntervalCount(p, userID, model.QTFlightsClimbed)
}

func mapIntervalCount(p point, userID, qt string) (*model.MapResult, error) {
	var v intervalCount
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	n, ok := val(v.Count)
	if !ok || n < 0 {
		return nil, nil
	}
	md := p.metadata()
	start, end, err := spanOf(v.Interval, md)
	if err != nil {
		return nil, err
	}
	return one(quantity(p, userID, qt, "count", n, start, end, md, true)), nil
}

// mapDistance converts millimeters to meters.
func mapDistance(p point, userID string) (*model.MapResult, error) {
	var v distanceValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	mm, ok := val(v.Millimeters)
	if !ok || mm < 0 {
		return nil, nil
	}
	md := p.metadata()
	start, end, err := spanOf(v.Interval, md)
	if err != nil {
		return nil, err
	}
	return one(quantity(p, userID, model.QTDistanceWalkRunIntday, "m", mm/1000, start, end, md, true)), nil
}

func mapActiveEnergy(p point, userID string) (*model.MapResult, error) {
	var v energyValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	kcal, ok := val(v.Kcal)
	if !ok || kcal < 0 {
		return nil, nil
	}
	md := p.metadata()
	start, end, err := spanOf(v.Interval, md)
	if err != nil {
		return nil, err
	}
	return one(quantity(p, userID, model.QTActiveEnergyIntraday, "kcal", kcal, start, end, md, true)), nil
}

// Samples: one measurement at one instant.

// sampleAt maps a measurement at st, skipping a missing or non-positive
// value.
func sampleAt(p point, userID string, st sampleTime, qt, unit string, v *num, md map[string]any) (*model.MapResult, error) {
	x, ok := val(v)
	if !ok || x <= 0 {
		return nil, nil
	}
	at, err := instantOf(st, md)
	if err != nil {
		return nil, err
	}
	return one(quantity(p, userID, qt, unit, x, at, at, md, false)), nil
}

func mapHeartRate(p point, userID string) (*model.MapResult, error) {
	var v heartRateValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	if v.Metadata != nil {
		if v.Metadata.MotionContext != "" {
			md["motion_context"] = v.Metadata.MotionContext
		}
		if v.Metadata.SensorLocation != "" {
			md["sensor_location"] = v.Metadata.SensorLocation
		}
	}
	return sampleAt(p, userID, v.SampleTime, model.QTHeartRate, "bpm", v.BeatsPerMinute, md)
}

// mapHRV keeps the RMSSD, which is what Google reports as HRV. The API's
// standardDeviationMilliseconds is not documented as SDNN, so it stays in
// metadata and is never written as hrv_sdnn. A HEALTH_KIT point is the
// exception: HealthKit only measures SDNN, so its value is hrv_sdnn (the
// standard deviation when set, else the RMSSD field Google filled with it).
func mapHRV(p point, userID string) (*model.MapResult, error) {
	var v hrvValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	if p.fromHealthKit() {
		md["hrv_method"] = "sdnn"
		x := v.RootMeanSquareOfSuccessiveDifferencesMilliseconds
		if sd, ok := val(v.StandardDeviationMilliseconds); ok && sd > 0 {
			x = v.StandardDeviationMilliseconds
		}
		return sampleAt(p, userID, v.SampleTime, model.QTHRVSDNN, "ms", x, md)
	}
	md["hrv_method"] = "rmssd"
	if sd, ok := val(v.StandardDeviationMilliseconds); ok {
		md["standard_deviation_ms"] = sd
	}
	return sampleAt(p, userID, v.SampleTime, model.QTHRVRMSSD, "ms", v.RootMeanSquareOfSuccessiveDifferencesMilliseconds, md)
}

// percent reads an oxygen saturation: the API documents 0 to 100, but a value
// at or under 1 can only be a fraction and is scaled.
func percent(v *num) *num {
	x, ok := val(v)
	if !ok {
		return nil
	}
	if x > 0 && x <= 1 {
		x *= 100
	}
	if x > 100 {
		return nil
	}
	n := num(x)
	return &n
}

func mapOxygenSaturation(p point, userID string) (*model.MapResult, error) {
	var v percentageSample
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	return sampleAt(p, userID, v.SampleTime, model.QTOxygenSaturation, "percent", percent(v.Percentage), p.metadata())
}

// mapWeight converts grams to kilograms.
func mapWeight(p point, userID string) (*model.MapResult, error) {
	var v weightValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	g, ok := val(v.WeightGrams)
	if !ok {
		return nil, nil
	}
	kg := num(g / 1000)
	return sampleAt(p, userID, v.SampleTime, "body_mass", "kg", &kg, p.metadata())
}

func mapBodyFat(p point, userID string) (*model.MapResult, error) {
	var v percentageSample
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	if x, ok := val(v.Percentage); ok && x > 100 {
		return nil, errors.New("body fat percentage over 100")
	}
	return sampleAt(p, userID, v.SampleTime, "body_fat_percentage", "percent", v.Percentage, p.metadata())
}

// mapHeight converts millimeters to centimeters.
func mapHeight(p point, userID string) (*model.MapResult, error) {
	var v heightValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	mm, ok := val(v.HeightMillimeters)
	if !ok {
		return nil, nil
	}
	cm := num(mm / 10)
	return sampleAt(p, userID, v.SampleTime, "height", "cm", &cm, p.metadata())
}

func mapVO2Max(p point, userID string) (*model.MapResult, error) {
	var v vo2MaxValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	if v.MeasurementMethod != "" {
		md["measurement_method"] = v.MeasurementMethod
	}
	return sampleAt(p, userID, v.SampleTime, "vo2_max", "mL/kg/min", v.VO2Max, md)
}

func mapBloodGlucose(p point, userID string) (*model.MapResult, error) {
	var v bloodGlucoseValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	for k, s := range map[string]string{
		"specimen": v.Specimen, "meal_type": v.MealType,
		"measurement_source": v.MeasurementSource, "measurement_timing": v.MeasurementTiming,
	} {
		if s != "" {
			md[k] = s
		}
	}
	return sampleAt(p, userID, v.SampleTime, "blood_glucose", "mg/dL", v.BloodGlucoseMilligramsPerDeciliter, md)
}

func mapCoreBodyTemperature(p point, userID string) (*model.MapResult, error) {
	var v coreTemperatureValue
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	if v.MeasurementLocation != "" {
		md["measurement_location"] = v.MeasurementLocation
	}
	return sampleAt(p, userID, v.SampleTime, model.QTBodyTemperature, "celsius", v.TemperatureCelsius, md)
}

// Daily points: one figure per calendar day.

func dailyResult(p point, userID string, date civilDate, qt, unit string, v *num, md map[string]any) (*model.MapResult, error) {
	x, ok := val(v)
	if !ok || x <= 0 {
		return nil, nil
	}
	s, err := dailySample(p, userID, date, qt, unit, x, md)
	if err != nil {
		return nil, err
	}
	return one(s), nil
}

func mapDailyRestingHR(p point, userID string) (*model.MapResult, error) {
	var v dailyRestingHR
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	return dailyResult(p, userID, v.Date, model.QTRestingHeartRate, "bpm", v.BeatsPerMinute, p.metadata())
}

// mapDailyHRV keeps the day's average RMSSD (SDNN on a HEALTH_KIT point, see
// mapHRV); the deep sleep RMSSD, non-REM heart rate and entropy stay in
// metadata. A deep sleep RMSSD of 0, which HEALTH_KIT points carry, is no
// measurement and is left out.
func mapDailyHRV(p point, userID string) (*model.MapResult, error) {
	var v dailyHRV
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	qt := model.QTHRVRMSSD
	md["hrv_method"] = "rmssd"
	if p.fromHealthKit() {
		qt = model.QTHRVSDNN
		md["hrv_method"] = "sdnn"
	}
	for k, n := range map[string]*num{
		"deep_sleep_rmssd_ms":        v.DeepSleepRootMeanSquareOfSuccessiveDifferencesMilliseconds,
		"non_rem_heart_rate_bpm":     v.NonRemHeartRateBeatsPerMinute,
		"heartbeat_interval_entropy": v.Entropy,
	} {
		if x, ok := val(n); ok {
			if k == "deep_sleep_rmssd_ms" && x <= 0 {
				continue
			}
			md[k] = x
		}
	}
	return dailyResult(p, userID, v.Date, qt, "ms", v.AverageHeartRateVariabilityMilliseconds, md)
}

func mapDailyOxygen(p point, userID string) (*model.MapResult, error) {
	var v dailyOxygen
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	if x, ok := val(percent(v.LowerBoundPercentage)); ok {
		md["lower_bound_percent"] = x
	}
	if x, ok := val(percent(v.UpperBoundPercentage)); ok {
		md["upper_bound_percent"] = x
	}
	return dailyResult(p, userID, v.Date, model.QTOxygenSaturation, "percent", percent(v.AveragePercentage), md)
}

func mapDailyRespiratory(p point, userID string) (*model.MapResult, error) {
	var v dailyRespiratory
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	return dailyResult(p, userID, v.Date, model.QTRespiratoryRate, "bpm", v.BreathsPerMinute, p.metadata())
}

func mapDailyVO2Max(p point, userID string) (*model.MapResult, error) {
	var v dailyVO2Max
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	md := p.metadata()
	if v.CardioFitnessLevel != "" {
		md["cardio_fitness_level"] = v.CardioFitnessLevel
	}
	if v.Estimated != nil {
		md["estimated"] = *v.Estimated
	}
	return dailyResult(p, userID, v.Date, "vo2_max", "mL/kg/min", v.VO2Max, md)
}

// mapSleepTemperature emits the night's skin temperature (skin_temperature)
// and, with a baseline, its deviation from it (body_temperature_deviation).
func mapSleepTemperature(p point, userID string) (*model.MapResult, error) {
	var v sleepTemperature
	if err := p.decode(&v); err != nil {
		return nil, err
	}
	nightly, ok := val(v.NightlyTemperatureCelsius)
	if !ok || nightly <= 0 {
		return nil, nil
	}
	res := &model.MapResult{}
	md := p.metadata()
	s, err := dailySample(p, userID, v.Date, model.QTSkinTemperature, "celsius", nightly, md)
	if err != nil {
		return nil, err
	}
	res.QuantitySamples = append(res.QuantitySamples, s)
	if base, ok := val(v.BaselineTemperatureCelsius); ok && base > 0 {
		md2 := p.metadata()
		md2["baseline_celsius"] = base
		d, err := dailySample(p, userID, v.Date, model.QTBodyTemperatureDeviation, "celsius", nightly-base, md2)
		if err != nil {
			return nil, err
		}
		res.QuantitySamples = append(res.QuantitySamples, d)
	}
	return res, nil
}
