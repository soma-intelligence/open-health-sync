// SPDX-License-Identifier: AGPL-3.0-only

package healthconnect

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// record is one uploaded line. Every value field of every supported record
// type is listed; a type reads only its own. Field names follow the androidx
// Health Connect record classes, with the unit in the name.
type record struct {
	RecordType string   `json:"recordType"`
	Metadata   metadata `json:"metadata"`

	// Instant records.
	Time       string `json:"time"`
	ZoneOffset string `json:"zoneOffset"`
	// Interval records.
	StartTime       string `json:"startTime"`
	EndTime         string `json:"endTime"`
	StartZoneOffset string `json:"startZoneOffset"`

	Count               *float64 `json:"count"`
	BeatsPerMinute      *float64 `json:"beatsPerMinute"`
	HRVMillis           *float64 `json:"heartRateVariabilityMillis"`
	Percentage          *float64 `json:"percentage"`
	Rate                *float64 `json:"rate"`
	TemperatureCelsius  *float64 `json:"temperatureCelsius"`
	Systolic            *float64 `json:"systolicMillimetersOfMercury"`
	Diastolic           *float64 `json:"diastolicMillimetersOfMercury"`
	GlucoseMmolPerL     *float64 `json:"levelMillimolesPerLiter"`
	WeightKilograms     *float64 `json:"weightKilograms"`
	HeightMeters        *float64 `json:"heightMeters"`
	EnergyKilocalories  *float64 `json:"energyKilocalories"`
	DistanceMeters      *float64 `json:"distanceMeters"`
	VolumeLiters        *float64 `json:"volumeLiters"`
	BasalMetabolicRate  *float64 `json:"basalMetabolicRateKilocaloriesPerDay"`
	EnergyFromFat       *float64 `json:"energyFromFatKilocalories"`
	MeasurementLocation string   `json:"measurementLocation"`
	BodyPosition        string   `json:"bodyPosition"`
	SpecimenSource      string   `json:"specimenSource"`
	MealType            string   `json:"mealType"`
	RelationToMeal      string   `json:"relationToMeal"`
	Flow                string   `json:"flow"`

	Samples []struct {
		Time           string   `json:"time"`
		BeatsPerMinute *float64 `json:"beatsPerMinute"`
	} `json:"samples"`

	Title        string `json:"title"`
	Name         string `json:"name"`
	ExerciseType string `json:"exerciseType"`
	Stages       []struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
		Stage     string `json:"stage"`
	} `json:"stages"`

	// Deletion.
	RecordID string `json:"recordId"`
}

type metadata struct {
	ID               string `json:"id"`
	DataOrigin       string `json:"dataOrigin"`
	LastModifiedTime string `json:"lastModifiedTime"`
	ClientRecordID   string `json:"clientRecordId"`
	RecordingMethod  string `json:"recordingMethod"`
	Device           *struct {
		Type         string `json:"type"`
		Manufacturer string `json:"manufacturer"`
		Model        string `json:"model"`
	} `json:"device"`
}

// instant is a quantity type read from one field of an instant record.
type instant struct {
	qt, unit string
	value    func(r *record) *float64
}

// instants are the instant record types that give one quantity sample.
var instants = map[string]instant{
	"RestingHeartRate":          {model.QTRestingHeartRate, "bpm", func(r *record) *float64 { return r.BeatsPerMinute }},
	"HeartRateVariabilityRmssd": {model.QTHRVRMSSD, "ms", func(r *record) *float64 { return r.HRVMillis }},
	"OxygenSaturation":          {model.QTOxygenSaturation, "percent", func(r *record) *float64 { return r.Percentage }},
	"RespiratoryRate":           {model.QTRespiratoryRate, "bpm", func(r *record) *float64 { return r.Rate }},
	"BodyTemperature":           {model.QTBodyTemperature, "celsius", func(r *record) *float64 { return r.TemperatureCelsius }},
	"BloodGlucose":              {"blood_glucose", "mmol/L", func(r *record) *float64 { return r.GlucoseMmolPerL }},
	"Weight":                    {"body_mass", "kg", func(r *record) *float64 { return r.WeightKilograms }},
	"Height":                    {"height", "m", func(r *record) *float64 { return r.HeightMeters }},
	"BodyFat":                   {"body_fat_percentage", "percent", func(r *record) *float64 { return r.Percentage }},
	"BasalMetabolicRate":        {model.QTBasalMetabolicRate, "kcal/d", func(r *record) *float64 { return r.BasalMetabolicRate }},
}

// intervals are the interval record types that give one quantity sample.
var intervals = map[string]instant{
	"Steps":                {model.QTStepCount, "count", func(r *record) *float64 { return r.Count }},
	"ActiveCaloriesBurned": {model.QTActiveEnergyBurned, "kcal", func(r *record) *float64 { return r.EnergyKilocalories }},
	"Distance":             {model.QTDistanceWalkingRunning, "m", func(r *record) *float64 { return r.DistanceMeters }},
	"TotalCaloriesBurned":  {model.QTTotalEnergyBurned, "kcal", func(r *record) *float64 { return r.EnergyKilocalories }},
}

// nutritionMasses maps the mass fields of a Nutrition line, each in grams
// (Mass.inGrams), to the canonical nutrient names. The value is converted to
// the nutrient's unit (model.Nutrients).
var nutritionMasses = map[string]string{
	"biotinGrams":             "biotin",
	"caffeineGrams":           "caffeine",
	"calciumGrams":            "calcium",
	"chlorideGrams":           "chloride",
	"cholesterolGrams":        "cholesterol",
	"chromiumGrams":           "chromium",
	"copperGrams":             "copper",
	"dietaryFiberGrams":       "fiber",
	"folateGrams":             "folate",
	"folicAcidGrams":          "folic_acid",
	"iodineGrams":             "iodine",
	"ironGrams":               "iron",
	"magnesiumGrams":          "magnesium",
	"manganeseGrams":          "manganese",
	"molybdenumGrams":         "molybdenum",
	"monounsaturatedFatGrams": "fat_monounsaturated",
	"niacinGrams":             "niacin",
	"pantothenicAcidGrams":    "pantothenic_acid",
	"phosphorusGrams":         "phosphorus",
	"polyunsaturatedFatGrams": "fat_polyunsaturated",
	"potassiumGrams":          "potassium",
	"proteinGrams":            "protein",
	"riboflavinGrams":         "riboflavin",
	"saturatedFatGrams":       "fat_saturated",
	"seleniumGrams":           "selenium",
	"sodiumGrams":             "sodium",
	"sugarGrams":              "sugar",
	"thiaminGrams":            "thiamin",
	"totalCarbohydrateGrams":  "carbohydrates",
	"totalFatGrams":           "fat_total",
	"transFatGrams":           "fat_trans",
	"unsaturatedFatGrams":     "fat_unsaturated",
	"vitaminAGrams":           "vitamin_a",
	"vitaminB12Grams":         "vitamin_b12",
	"vitaminB6Grams":          "vitamin_b6",
	"vitaminCGrams":           "vitamin_c",
	"vitaminDGrams":           "vitamin_d",
	"vitaminEGrams":           "vitamin_e",
	"vitaminKGrams":           "vitamin_k",
	"zincGrams":               "zinc",
}

// MetadataSpecimen is the metadata key naming what a glucose value was
// measured in (the FHIR output codes interstitial fluid with its own LOINC).
const MetadataSpecimen = "specimen"

// mapRecord maps one line. An unknown record type gives nil, nil.
func mapRecord(fields map[string]any, userID string) (*model.MapResult, error) {
	line, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("health_connect: re-encode line: %w", err)
	}
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("health_connect: %w", err)
	}
	if r.RecordType == "Deletion" {
		if r.RecordID == "" {
			return nil, fmt.Errorf("health_connect: Deletion without recordId")
		}
		return &model.MapResult{Deletions: []model.Deletion{{ExternalID: r.RecordID}}}, nil
	}
	_, isInstant := instants[r.RecordType]
	_, isInterval := intervals[r.RecordType]
	known := isInstant || isInterval || r.RecordType == "BloodPressure" || r.RecordType == "HeartRate" ||
		r.RecordType == "SleepSession" || r.RecordType == "ExerciseSession" ||
		r.RecordType == "MenstruationFlow" || r.RecordType == "IntermenstrualBleeding" ||
		r.RecordType == "Nutrition" || r.RecordType == "Hydration"
	if !known {
		noteUnknown(r.RecordType)
		return nil, nil
	}
	if r.Metadata.ID == "" {
		return nil, fmt.Errorf("health_connect: %s without metadata.id", r.RecordType)
	}
	b := newBuilder(&r, userID)
	switch {
	case isInstant:
		return b.instant()
	case isInterval:
		return b.interval()
	}
	switch r.RecordType {
	case "BloodPressure":
		return b.bloodPressure()
	case "HeartRate":
		return b.heartRate()
	case "SleepSession":
		return b.sleep()
	case "MenstruationFlow", "IntermenstrualBleeding":
		return b.menstruation()
	case "Nutrition":
		return b.nutrition(fields)
	case "Hydration":
		return b.hydration()
	default:
		return b.exercise()
	}
}

// builder holds what every record of one line shares.
type builder struct {
	r      *record
	userID string
	source string // source slug: the app that wrote the record
	md     map[string]any
}

func newBuilder(r *record, userID string) *builder {
	md := map[string]any{"record_type": r.RecordType}
	set := func(k, v string) {
		if v != "" {
			md[k] = v
		}
	}
	set("data_origin", r.Metadata.DataOrigin)
	set("last_modified_time", r.Metadata.LastModifiedTime)
	set("client_record_id", r.Metadata.ClientRecordID)
	set("recording_method", r.Metadata.RecordingMethod)
	if d := r.Metadata.Device; d != nil {
		set("device_type", d.Type)
		set("device_manufacturer", d.Manufacturer)
	}
	off := r.ZoneOffset
	if off == "" {
		off = r.StartZoneOffset
	}
	if m, ok := parseOffset(off); ok {
		md["utc_offset_minutes"] = m
		if z := offsetZone(m); z != "" {
			md["timezone"] = z
		}
	}
	return &builder{r: r, userID: userID, source: SourceSlug(r.Metadata.DataOrigin), md: md}
}

// sample builds one quantity sample. Health Connect records can be edited
// in place (same id, new value or time), so samples are Replaceable, hashed
// on their id and start rather than their value, and supersede a copy whose
// start moved.
func (b *builder) sample(externalID, qt, unit string, v float64, start, end time.Time, md map[string]any) model.QuantitySample {
	s := model.QuantitySample{
		UserID:          b.userID,
		Provider:        Slug,
		SourceSlug:      b.source,
		SourceName:      b.r.Metadata.DataOrigin,
		SourceBundleID:  b.r.Metadata.DataOrigin,
		ExternalID:      externalID,
		QuantityType:    qt,
		Value:           v,
		Unit:            unit,
		IsPrimarySource: true,
		StartDate:       start,
		EndDate:         end,
		Metadata:        maps.Clone(md),
		Replaceable:     true,
		SupersedeMoved:  true,
	}
	if d := b.r.Metadata.Device; d != nil {
		s.DeviceID = strings.TrimSpace(d.Manufacturer + " " + d.Model)
		s.DeviceModel = d.Model
	}
	if b.r.Metadata.RecordingMethod == "manual" {
		t := true
		s.WasUserEntered = &t
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id":       b.userID,
		"provider":      Slug,
		"quantity_type": qt,
		"external_id":   externalID,
		"start_date":    start.UTC().Format(time.RFC3339Nano),
	})
	return s
}

func (b *builder) instant() (*model.MapResult, error) {
	spec := instants[b.r.RecordType]
	v := spec.value(b.r)
	if v == nil {
		return nil, fmt.Errorf("health_connect: %s %s: no value", b.r.RecordType, b.r.Metadata.ID)
	}
	at, err := parseTime(b.r.Time)
	if err != nil {
		return nil, fmt.Errorf("health_connect: %s %s: %w", b.r.RecordType, b.r.Metadata.ID, err)
	}
	md := b.md
	if b.r.RecordType == "BloodGlucose" {
		md = maps.Clone(md)
		for k, val := range map[string]string{MetadataSpecimen: b.r.SpecimenSource, "meal_type": b.r.MealType, "relation_to_meal": b.r.RelationToMeal} {
			if val != "" && val != "unknown" {
				md[k] = val
			}
		}
	}
	if b.r.MeasurementLocation != "" {
		md = maps.Clone(md)
		md["measurement_location"] = b.r.MeasurementLocation
	}
	s := b.sample(b.r.Metadata.ID, spec.qt, spec.unit, *v, at, at, md)
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

func (b *builder) interval() (*model.MapResult, error) {
	spec := intervals[b.r.RecordType]
	v := spec.value(b.r)
	if v == nil {
		return nil, fmt.Errorf("health_connect: %s %s: no value", b.r.RecordType, b.r.Metadata.ID)
	}
	start, end, err := b.span()
	if err != nil {
		return nil, err
	}
	s := b.sample(b.r.Metadata.ID, spec.qt, spec.unit, *v, start, end, b.md)
	return &model.MapResult{QuantitySamples: []model.QuantitySample{s}}, nil
}

// bloodPressure gives a systolic and a diastolic sample with the same time,
// source and device, which the read API and FHIR pair into one panel.
func (b *builder) bloodPressure() (*model.MapResult, error) {
	if b.r.Systolic == nil || b.r.Diastolic == nil {
		return nil, fmt.Errorf("health_connect: BloodPressure %s: systolic and diastolic are both required", b.r.Metadata.ID)
	}
	at, err := parseTime(b.r.Time)
	if err != nil {
		return nil, fmt.Errorf("health_connect: BloodPressure %s: %w", b.r.Metadata.ID, err)
	}
	md := maps.Clone(b.md)
	if b.r.BodyPosition != "" {
		md["body_position"] = b.r.BodyPosition
	}
	if b.r.MeasurementLocation != "" {
		md["measurement_location"] = b.r.MeasurementLocation
	}
	id := b.r.Metadata.ID
	return &model.MapResult{QuantitySamples: []model.QuantitySample{
		b.sample(id+"_systolic", "blood_pressure_systolic", "mmHg", *b.r.Systolic, at, at, md),
		b.sample(id+"_diastolic", "blood_pressure_diastolic", "mmHg", *b.r.Diastolic, at, at, md),
	}}, nil
}

// heartRate gives one heart_rate sample per series sample, "<id>_<index>".
func (b *builder) heartRate() (*model.MapResult, error) {
	res := &model.MapResult{HighFrequency: len(b.r.Samples) > 100}
	for i, p := range b.r.Samples {
		if p.BeatsPerMinute == nil {
			continue
		}
		at, err := parseTime(p.Time)
		if err != nil {
			return nil, fmt.Errorf("health_connect: HeartRate %s sample %d: %w", b.r.Metadata.ID, i, err)
		}
		s := b.sample(b.r.Metadata.ID+"_"+strconv.Itoa(i), model.QTHeartRate, "bpm", *p.BeatsPerMinute, at, at, b.md)
		// A series is re-sent whole when edited; its points keep their own
		// identity, so plain first-write-wins on time and value is right.
		s.Replaceable, s.SupersedeMoved = false, false
		s.ContentHash = model.ComputeContentHash(map[string]string{
			"user_id": b.userID, "provider": Slug, "quantity_type": model.QTHeartRate,
			"start_date": at.UTC().Format(time.RFC3339Nano), "value": model.CanonicalFloat(*p.BeatsPerMinute),
			"source": b.source,
		})
		res.QuantitySamples = append(res.QuantitySamples, s)
	}
	if len(res.QuantitySamples) == 0 {
		return nil, nil
	}
	return res, nil
}

// menstrualFlows maps the Health Connect flow (FLOW_ dropped, lowercased) to
// the canonical menstrual_flow label and value, numbered as Apple Health
// numbers them. unknown, or no flow, is flow of unknown amount.
var menstrualFlows = map[string]struct {
	label string
	value int
}{
	"light":  {"light", 2},
	"medium": {"medium", 3},
	"heavy":  {"heavy", 4},
}

// menstruation gives one menstrual_flow or intermenstrual_bleeding category
// sample. Category samples are written first-wins, so the sample is hashed
// on the record id and time: a re-sent record is a no-op, and an edit of the
// flow keeps the stored value until the record is deleted.
func (b *builder) menstruation() (*model.MapResult, error) {
	at, err := parseTime(b.r.Time)
	if err != nil {
		return nil, fmt.Errorf("health_connect: %s %s: %w", b.r.RecordType, b.r.Metadata.ID, err)
	}
	ct, label, value := "intermenstrual_bleeding", "", 0
	if b.r.RecordType == "MenstruationFlow" {
		ct, label, value = "menstrual_flow", "unspecified", 1
		if f, ok := menstrualFlows[strings.ToLower(strings.TrimSpace(b.r.Flow))]; ok {
			label, value = f.label, f.value
		}
	}
	s := model.CategorySample{
		UserID:       b.userID,
		Provider:     Slug,
		ExternalID:   b.r.Metadata.ID,
		CategoryType: ct,
		ValueInt:     value,
		ValueLabel:   label,
		StartDate:    at,
		EndDate:      at,
		SourceName:   b.r.Metadata.DataOrigin,
		Metadata:     maps.Clone(b.md),
	}
	s.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id": b.userID, "provider": Slug, "category_type": ct, "external_id": b.r.Metadata.ID,
		"start_date": at.UTC().Format(time.RFC3339Nano),
	})
	return &model.MapResult{CategorySamples: []model.CategorySample{s}}, nil
}

// mealTypes are the Health Connect meal types (MEAL_TYPE_ dropped,
// lowercased) a nutrition log keeps; unknown, or none, is no meal type.
var mealTypes = map[string]bool{"breakfast": true, "lunch": true, "dinner": true, "snack": true}

// nutritionLog starts the nutrition log of a Nutrition or Hydration record.
// Like the samples, it is identified by the record id: a re-sent record
// replaces the stored figures, and a record whose start moved replaces its
// older copy.
func (b *builder) nutritionLog() (model.NutritionLog, error) {
	start, end, err := b.span()
	if err != nil {
		return model.NutritionLog{}, err
	}
	nl := model.NutritionLog{
		UserID:         b.userID,
		Provider:       Slug,
		ExternalID:     b.r.Metadata.ID,
		StartDate:      start,
		EndDate:        &end,
		Metadata:       maps.Clone(b.md),
		Replaceable:    true,
		SupersedeMoved: true,
	}
	nl.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id": b.userID, "provider": Slug, "data_type": b.r.RecordType, "external_id": b.r.Metadata.ID,
	})
	return nl, nil
}

// setNutrient converts a value to its nutrient's unit and sets it.
func (b *builder) setNutrient(nl *model.NutritionLog, name string, v float64, unit string) error {
	v, err := model.ToNutrientUnit(name, v, unit)
	if err != nil {
		return fmt.Errorf("health_connect: %s %s: %w", b.r.RecordType, b.r.Metadata.ID, err)
	}
	nl.SetNutrient(name, v)
	return nil
}

// nutrition gives one nutrition log: the energies in kilocalories, every
// mass field in grams, the food name in metadata.food_name. A record with no
// figure is still a meal the person logged, so it is kept.
func (b *builder) nutrition(fields map[string]any) (*model.MapResult, error) {
	nl, err := b.nutritionLog()
	if err != nil {
		return nil, err
	}
	if mt := strings.ToLower(strings.TrimSpace(b.r.MealType)); mealTypes[mt] {
		nl.MealType = mt
	}
	if n := strings.TrimSpace(b.r.Name); n != "" {
		nl.Metadata["food_name"] = n
	}
	if v := b.r.EnergyKilocalories; v != nil {
		if err := b.setNutrient(&nl, "energy_consumed", *v, "kcal"); err != nil {
			return nil, err
		}
	}
	if v := b.r.EnergyFromFat; v != nil {
		if err := b.setNutrient(&nl, "energy_from_fat", *v, "kcal"); err != nil {
			return nil, err
		}
	}
	for field, name := range nutritionMasses {
		v, ok := number(fields[field])
		if !ok {
			continue
		}
		if err := b.setNutrient(&nl, name, v, "g"); err != nil {
			return nil, err
		}
	}
	return &model.MapResult{NutritionLogs: []model.NutritionLog{nl}}, nil
}

// number reads a JSON number of a decoded line.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// hydration gives one nutrition log of water, in millilitres.
func (b *builder) hydration() (*model.MapResult, error) {
	if b.r.VolumeLiters == nil {
		return nil, fmt.Errorf("health_connect: Hydration %s: no value", b.r.Metadata.ID)
	}
	nl, err := b.nutritionLog()
	if err != nil {
		return nil, err
	}
	if err := b.setNutrient(&nl, "water", *b.r.VolumeLiters, "L"); err != nil {
		return nil, err
	}
	return &model.MapResult{NutritionLogs: []model.NutritionLog{nl}}, nil
}

// sleepStages maps Health Connect sleep stages to the canonical phases.
// "sleeping" (asleep, stage unknown) counts as light, like Apple's
// asleep_unspecified; out of bed and unknown are not phases.
var sleepStages = map[string]string{
	"awake":        model.SleepStageAwake,
	"awake_in_bed": model.SleepStageAwake,
	"sleeping":     model.SleepStageLight,
	"light":        model.SleepStageLight,
	"deep":         model.SleepStageDeep,
	"rem":          model.SleepStageREM,
}

func (b *builder) sleep() (*model.MapResult, error) {
	start, end, err := b.span()
	if err != nil {
		return nil, err
	}
	md := maps.Clone(b.md)
	if b.r.Title != "" {
		md["title"] = b.r.Title
	}
	session := model.SleepSession{
		UserID:       b.userID,
		Provider:     Slug,
		ExternalID:   b.r.Metadata.ID,
		BedtimeStart: start,
		BedtimeEnd:   end,
		Metadata:     md,
		Replaceable:  true,
	}
	session.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id": b.userID, "provider": Slug, "data_type": "SleepSession", "external_id": b.r.Metadata.ID,
	})
	totals := map[string]int{}
	res := &model.MapResult{}
	for i, st := range b.r.Stages {
		phase, ok := sleepStages[st.Stage]
		if !ok {
			continue
		}
		ps, err1 := parseTime(st.StartTime)
		pe, err2 := parseTime(st.EndTime)
		if err1 != nil || err2 != nil || !pe.After(ps) {
			return nil, fmt.Errorf("health_connect: SleepSession %s stage %d: bad times", b.r.Metadata.ID, i)
		}
		d := int(pe.Sub(ps) / time.Second)
		totals[phase] += d
		res.SleepPhases = append(res.SleepPhases, model.SleepPhase{
			UserID: b.userID, Phase: phase, StartDate: ps, EndDate: pe, DurationS: d,
			ParentContentHash: session.ContentHash,
		})
	}
	if len(res.SleepPhases) > 0 {
		ptr := func(v int) *int { return &v }
		session.DeepSleepSeconds = ptr(totals[model.SleepStageDeep])
		session.REMSleepSeconds = ptr(totals[model.SleepStageREM])
		session.LightSleepSeconds = ptr(totals[model.SleepStageLight])
		session.AwakeSeconds = ptr(totals[model.SleepStageAwake])
		session.TotalSleepSeconds = ptr(totals[model.SleepStageDeep] + totals[model.SleepStageREM] + totals[model.SleepStageLight])
	}
	res.SleepSessions = []model.SleepSession{session}
	return res, nil
}

// exerciseTypes maps Health Connect exercise types (EXERCISE_TYPE_ dropped,
// lowercased) whose canonical activity has another name. The others are kept
// as they are: they already read like the canonical vocabulary.
var exerciseTypes = map[string]string{
	"biking":            "cycling",
	"biking_stationary": "cycling",
	"running_treadmill": "running",
	"swimming_pool":     "swimming",
	"dancing":           "dance",
	"strength_training": "traditional_strength_training",
	"weightlifting":     "traditional_strength_training",
	"other_workout":     "other",
	"":                  "other",
}

func (b *builder) exercise() (*model.MapResult, error) {
	start, end, err := b.span()
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(b.r.ExerciseType))
	activity, ok := exerciseTypes[kind]
	if !ok {
		activity = kind
	}
	md := maps.Clone(b.md)
	md["exercise_type"] = b.r.ExerciseType
	if b.r.Title != "" {
		md["title"] = b.r.Title
	}
	dur := int(end.Sub(start) / time.Second)
	w := model.Workout{
		UserID:       b.userID,
		Provider:     Slug,
		ExternalID:   b.r.Metadata.ID,
		ActivityType: activity,
		StartDate:    start,
		EndDate:      end,
		DurationS:    &dur,
		Metadata:     md,
		Replaceable:  true,
	}
	w.ContentHash = model.ComputeContentHash(map[string]string{
		"user_id": b.userID, "provider": Slug, "data_type": "ExerciseSession", "external_id": b.r.Metadata.ID,
	})
	return &model.MapResult{Workouts: []model.Workout{w}}, nil
}

// span reads startTime and endTime; end must not be before start.
func (b *builder) span() (time.Time, time.Time, error) {
	start, err1 := parseTime(b.r.StartTime)
	end, err2 := parseTime(b.r.EndTime)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("health_connect: %s %s: startTime and endTime are required RFC 3339 instants", b.r.RecordType, b.r.Metadata.ID)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("health_connect: %s %s: endTime is before startTime", b.r.RecordType, b.r.Metadata.ID)
	}
	return start, end, nil
}

// parseTime reads an RFC 3339 instant (Instant.toString() on Android).
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("unreadable time %q", s)
	}
	return t.UTC(), nil
}

// parseOffset reads a ZoneOffset ("+02:00", "Z", "-05:30") in minutes.
func parseOffset(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	if s == "Z" {
		return 0, true
	}
	t, err := time.Parse("-07:00", s)
	if err != nil {
		return 0, false
	}
	_, sec := t.Zone()
	m := sec / 60
	if m < -12*60 || m > 14*60 {
		return 0, false
	}
	return m, true
}

// offsetZone is the Etc/GMT zone of a whole hour offset ("Etc/GMT-2" is
// UTC+2), "Etc/UTC" for 0, and "" otherwise.
func offsetZone(minutes int) string {
	if minutes == 0 {
		return "Etc/UTC"
	}
	if minutes%60 != 0 {
		return ""
	}
	h := minutes / 60
	if h > 0 {
		return "Etc/GMT-" + strconv.Itoa(h)
	}
	return "Etc/GMT+" + strconv.Itoa(-h)
}

// knownOrigins are the source slugs of apps that write to Health Connect and
// have their own name in Open Health Sync, by package name prefix. An app
// not listed gets its package name, slugified, so its data is still told
// apart from other apps'.
var knownOrigins = []struct{ prefix, slug string }{
	{"com.dexcom.", "dexcom"},
	{"com.freestylelibre", "freestyle_libre"},
	{"com.omronhealthcare.", "omron"},
	{"jp.co.omron.", "omron"},
	{"com.ouraring.", "oura"},
	{"com.whoop.", "whoop"},
	{"com.garmin.", "garmin"},
	{"com.fitbit.", "fitbit"},
	{"fi.polar.", "polar"},
	{"com.withings.", "withings"},
	{"com.strava", "strava"},
	{"com.sec.android.app.shealth", "samsung_health"},
	{"com.google.android.apps.fitness", "google_fit"},
	{"com.xiaomi.wearable", "xiaomi"},          // Mi Fitness
	{"com.mi.health", "xiaomi"},                // Mi Health
	{"com.xiaomi.hm.health", "xiaomi"},         // Zepp Life (Mi Band 7 and earlier)
	{"com.huami.watch.hmwatchmanager", "zepp"}, // Zepp (Amazfit)
	{"com.gdjztech.ringconn", "ringconn"},
}

// SourceSlug is the source slug of the app that wrote a record (its package
// name, metadata.dataOrigin). An empty origin gives "health_connect".
func SourceSlug(dataOrigin string) string {
	o := strings.ToLower(strings.TrimSpace(dataOrigin))
	for _, k := range knownOrigins {
		if strings.HasPrefix(o, k.prefix) {
			return k.slug
		}
	}
	return model.ResolveSourceSlug(Slug, o)
}

// unknownTypes counts record types seen without a mapping, so each is logged
// once rather than once per line.
var (
	unknownMu    sync.Mutex
	unknownTypes = map[string]bool{}
)

func noteUnknown(t string) {
	unknownMu.Lock()
	defer unknownMu.Unlock()
	if unknownTypes[t] || len(unknownTypes) >= 64 {
		return
	}
	unknownTypes[t] = true
	slog.Info("health_connect: record type not stored", "provider", Slug, "record_type", t)
}
