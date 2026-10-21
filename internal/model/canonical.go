// SPDX-License-Identifier: AGPL-3.0-only

// Package model defines the canonical health records every collector maps into
// and the pure helpers around them: content hashing, unit canonicalization,
// quantity vocabulary and source attribution. It has no I/O and no database
// access, so the ETL and the tests can use it freely.
package model

import "time"

// QuantitySample represents a single scalar health measurement.
type QuantitySample struct {
	TenantID string // owning tenant; set by the pipeline, not part of any content hash
	UserID   string
	Provider string // the integration that delivered the sample ("oura", "withings")
	// SourceSlug is the upstream that actually PRODUCED it. Equal to Provider for a
	// direct integration; for an aggregator it names the real sensor vendor, so
	// fusion can rank the hardware instead of the pipe. See ResolveSourceSlug.
	SourceSlug     string
	SourceName     string
	SourceBundleID string
	DeviceID       string

	// Provenance. DeviceID alone is
	// HKDevice.name ("Apple Watch"), which no per-model accuracy row can match.
	//
	// ProductType is HKSourceRevision.productType ("Watch7,2"), the exact model,
	// and it describes what RAN the writing app. DeviceModel is HKDevice.model
	// and describes what TOOK the reading. They differ whenever a phone app
	// writes a paired sensor's output, so they are kept apart.
	ProductType string
	DeviceModel string

	// WasUserEntered is a pointer because absent is a third state, distinct from
	// false: every row written before this existed genuinely does not know, and
	// storing that as "measured" would be the same fabrication this whole
	// provenance model is meant to prevent.
	WasUserEntered *bool

	ExternalID      string
	QuantityType    string // Canonical: "heart_rate", "step_count", etc.
	Value           float64
	Unit            string // Canonical: "bpm", "count", "kg", etc.
	IsPrimarySource bool
	StartDate       time.Time
	EndDate         time.Time
	ContentHash     string
	Metadata        map[string]any // Original fields + timezone

	// Replaceable is an instruction to the writer, not a column. It marks a figure
	// the upstream re-sends with a newer value while its window is still open (a
	// day's running step count), whose ContentHash therefore leaves the value out:
	// on conflict the stored value is replaced by this one instead of this one
	// being skipped as a duplicate. Everything else stays first-write-wins.
	Replaceable bool

	// SupersedeMoved is an instruction to the writer, not a column. It says
	// that ExternalID names this one sample of one provider record (a figure
	// of a sleep, a workout, a cycle), so a stored sample of the same
	// provider, ExternalID and QuantityType under another StartDate is an
	// older copy whose start moved (an edited night, a shifted cycle): the
	// writer deletes it before writing this one. Requires Replaceable and
	// ExternalID. Never set it on samples that share an ExternalID across
	// starts (a day's heart rate series under one id, per-day figures keyed
	// by a device): the writer would keep only one of them.
	SupersedeMoved bool
}

// CategorySample represents a categorical health event.
type CategorySample struct {
	TenantID     string // owning tenant; set by the pipeline, not part of any content hash
	UserID       string
	Provider     string
	ExternalID   string
	CategoryType string // Canonical: "menstrual_flow", "high_heart_rate_event", etc.
	ValueInt     int
	ValueLabel   string
	StartDate    time.Time
	EndDate      time.Time
	SourceName   string
	ContentHash  string
	Metadata     map[string]any
}

// SleepSession represents a structured sleep record with duration breakdowns.
type SleepSession struct {
	TenantID          string // owning tenant; set by the pipeline, not part of any content hash
	UserID            string
	Provider          string
	ExternalID        string
	BedtimeStart      time.Time
	BedtimeEnd        time.Time
	TotalSleepSeconds *int
	DeepSleepSeconds  *int
	REMSleepSeconds   *int
	LightSleepSeconds *int
	AwakeSeconds      *int
	SleepScore        *float64
	HRVAvgMs          *float64
	RestingHR         *float64
	RespiratoryRate   *float64
	SpO2Avg           *float64
	ContentHash       string
	Metadata          map[string]any

	// Replaceable: see QuantitySample.Replaceable. For a night the provider
	// re-scores or the user edits (an Oura, WHOOP or Polar sleep), hashed on its
	// provider record id (and, for some, its start), never on its figures: the
	// new copy's figures replace the stored ones, a figure it omits keeps its
	// stored value, and the phases it carries replace the stored phases. A
	// stored copy of the same ExternalID under another start (the night moved)
	// is superseded by this one. Requires ExternalID for that last step.
	Replaceable bool
}

// SleepPhase represents a single phase within a sleep session.
type SleepPhase struct {
	TenantID string // owning tenant; set by the pipeline, not part of any content hash
	// SessionID is set by the writer after the parent row is inserted.
	UserID            string
	Phase             string // "deep", "rem", "light", "awake", "in_bed", "asleep_core", etc.
	StartDate         time.Time
	EndDate           time.Time
	DurationS         int
	ParentContentHash string // Matches parent SleepSession.ContentHash; empty if orphan
	ContentHash       string // Set by the writer via SleepPhaseContentHash for dedup
}

// Workout represents a workout session with performance metrics.
type Workout struct {
	TenantID string // owning tenant; set by the pipeline, not part of any content hash
	UserID   string
	Provider string
	// SourceSlug is the upstream that recorded the workout, as for a
	// QuantitySample. Empty means the provider itself.
	SourceSlug string
	// DeviceID names the device that recorded it ("Garmin edge_530"). Empty
	// when the provider does not say.
	DeviceID         string
	ExternalID       string
	ActivityType     string // Canonical: "running", "cycling", etc.
	StartDate        time.Time
	EndDate          time.Time
	DurationS        *int
	ActiveEnergyKcal *float64
	DistanceM        *float64
	AvgHeartRate     *float64
	MaxHeartRate     *float64
	AvgSpeedMps      *float64
	ElevationGainM   *float64
	VO2Max           *float64
	ContentHash      string
	Metadata         map[string]any

	// Replaceable: see QuantitySample.Replaceable. For a workout the provider
	// edits or re-scores after the fact (a Strava activity whose type or
	// figures change), hashed on its provider id (and, for some, its start):
	// the new copy's figures replace the stored ones. A figure the re-send
	// omits keeps its stored value. A stored copy of the same ExternalID under
	// another start is superseded by this one, its route points moving over.
	Replaceable bool
}

// RoutePoint represents a GPS coordinate from a workout route.
type RoutePoint struct {
	TenantID string // owning tenant; set by the pipeline, not part of any content hash
	// WorkoutID is set by the writer after the parent row is inserted.
	UserID            string
	Timestamp         time.Time
	Latitude          float64
	Longitude         float64
	AltitudeM         *float64
	SpeedMps          *float64
	CourseDeg         *float64
	ParentContentHash string // Matches parent Workout.ContentHash; empty if orphan
	ContentHash       string // Set by the writer via RoutePointContentHash for dedup
}

// NutritionLog represents a nutrition/dietary record.
type NutritionLog struct {
	TenantID      string // owning tenant; set by the pipeline, not part of any content hash
	UserID        string
	Provider      string
	ExternalID    string
	StartDate     time.Time
	EndDate       *time.Time
	MealType      string
	EnergyKcal    *float64
	ProteinG      *float64
	CarbsG        *float64
	FatG          *float64
	FiberG        *float64
	SugarG        *float64
	SodiumMg      *float64
	NutrientsJSON map[string]any // Extended nutrients
	ContentHash   string
	Metadata      map[string]any

	// Replaceable: see QuantitySample.Replaceable. A figure the re-send omits
	// keeps its stored value rather than being cleared.
	Replaceable bool
	// SupersedeMoved: see QuantitySample.SupersedeMoved. The writer deletes
	// the stored copies of the log (same provider and external id) under
	// another start.
	SupersedeMoved bool
}

// ECGRecording represents ECG metadata (voltage data is in ECGVoltageSample).
type ECGRecording struct {
	TenantID             string // owning tenant; set by the pipeline, not part of any content hash
	UserID               string
	StartDate            time.Time
	EndDate              time.Time
	Classification       int
	SymptomsStatus       int
	AverageHeartRate     *float64
	SamplingFrequency    float64
	NumberOfMeasurements int
	VoltagePath          string
	ContentHash          string
	Metadata             map[string]any
}

// ECGVoltageSample represents a single voltage measurement from an ECG recording.
type ECGVoltageSample struct {
	TenantID             string // owning tenant; set by the pipeline, not part of any content hash
	ECGID                string
	UserID               string
	TimeSinceSampleStart float64
	VoltageUV            float64 // Microvolts (converted from volts)
	ParentContentHash    string  // Matches parent ECGRecording.ContentHash; empty if orphan
	ContentHash          string  // Set by the writer via ECGVoltageSampleContentHash for dedup
}

// DailySummary represents an aggregated daily score from a provider.
type DailySummary struct {
	TenantID       string // owning tenant; set by the pipeline, not part of any content hash
	UserID         string
	Provider       string
	SummaryDate    time.Time // Date only (time component is midnight UTC)
	ReadinessScore *float64
	ActivityScore  *float64
	SleepScore     *float64
	RecoveryScore  *float64
	StrainScore    *float64
	StepCount      *int
	ActiveKcal     *float64
	TotalKcal      *float64
	DistanceM      *float64
	FloorsClimbed  *int
	RestingHR      *float64
	HRVAvgMs       *float64
	ContentHash    string
	Metadata       map[string]any

	// Replaceable: see QuantitySample.Replaceable. A figure the re-send omits
	// keeps its stored value rather than being cleared.
	Replaceable bool

	// RecordID is an instruction to the writer, not a column: the provider
	// record whose figures this summary carries, when it carries the figures
	// of one record only (a WHOOP recovery fills the recovery figures of its
	// day). A summary whose record, or a record it was derived from (an id
	// prefix before "_", as for samples), is tombstoned is not written, so
	// reprocessing an old raw object does not bring a deleted record's figures
	// back. Empty for a summary of the whole day.
	RecordID string
}

// HeartbeatSample represents a single beat-to-beat RR interval measurement.
type HeartbeatSample struct {
	TenantID    string // owning tenant; set by the pipeline, not part of any content hash
	SeriesID    string
	UserID      string
	Timestamp   time.Time
	RRMS        float64 // RR interval in milliseconds
	ContentHash string  // Set by the writer via HeartbeatSampleContentHash for dedup
}

// Observation is a generic coded health fact (the long-tail catch-all). The "attribute"
// is a controlled canonical_code (not free EAV text); the value is a typed slot
// (num/code/text); the unit is UCUM. Any metric without a dedicated typed table lands
// here with zero schema change. Mirrors OMOP MEASUREMENT/OBSERVATION + FHIR Observation.
type Observation struct {
	TenantID        string // owning tenant; set by the pipeline, not part of any content hash
	UserID          string
	TS              time.Time
	EndTS           time.Time // zero value => NULL
	CanonicalCode   string
	ValueNum        *float64
	ValueCode       string // categorical, coded
	ValueText       string
	UnitUCUM        string
	ValueCanonical  *float64 // value normalized to the base UCUM unit (cross-source compare)
	Provider        string
	SourceName      string
	DeviceID        string
	IsPrimarySource bool
	TypeConcept     string // device | survey | ehr | derived (provenance)
	SourceValue     string // verbatim provider key (audit / re-mapping)
	S3Ref           string // pointer if a blob backs this observation
	ContentHash     string // Set via ObservationContentHash for dedup
	Metadata        map[string]any
}

// MapResult holds all canonical records produced from a single input record.
// Most records produce output for exactly one table, but some (e.g., sleep
// with phases) produce records for multiple tables.
type MapResult struct {
	QuantitySamples  []QuantitySample
	CategorySamples  []CategorySample
	SleepSessions    []SleepSession
	SleepPhases      []SleepPhase
	Workouts         []Workout
	RoutePoints      []RoutePoint
	NutritionLogs    []NutritionLog
	ECGRecordings    []ECGRecording
	ECGSamples       []ECGVoltageSample
	HeartbeatSamples []HeartbeatSample
	DailySummaries   []DailySummary
	Observations     []Observation

	// HighFrequency signals to the pipeline that this result contains
	// high-volume data that should use COPY protocol for writes.
	HighFrequency bool

	// SourceKey is the raw object this batch was mapped from. It is the only
	// identity a child record has when its parent arrived in a DIFFERENT object,
	// which is the normal case for Apple Health ECG voltage: the metadata record
	// uploads separately and stores this key in ecg_recordings.voltage_path.
	// Empty for callers that do not track an origin object; the writer must then
	// skip rather than guess a parent.
	SourceKey string

	// Deletions are provider records the provider removed. The writer deletes
	// the stored rows of each one and keeps a tombstone, so a record of that
	// id is never written again (see Deletion).
	Deletions []Deletion
}

// Deletion says that a provider removed one of its records: the sleep,
// workout or nutrition log stored with ExternalID, and the quantity and
// category samples of it. Those samples carry the record id as their
// ExternalID, or "<record id>_<suffix>" when one record gives several (the
// Oura and WHOOP convention), and are removed whenever they start, whether or
// not a sleep or workout of that id is stored. A Deletion is applied for the
// job's tenant and user only, and is final: a later copy of the record (a
// re-pull, an old raw object processed again) is skipped, whichever arrives
// first.
type Deletion struct {
	// Provider is the integration the record came through. Empty means the
	// provider of the job.
	Provider string
	// ExternalID is the provider's id of the record. Required.
	ExternalID string
	// ClearSummaryFields names the provider daily summary figures the record
	// contributed, as daily_summaries columns ("recovery_score",
	// "resting_hr"). They are set to NULL in the provider's summaries of the
	// days named by the MetadataSummaryDate of the quantity samples the
	// deletion removes; a summary left without any figure is deleted. Names
	// outside the summary figures are ignored.
	ClearSummaryFields []string
}

// MetadataSummaryDate is the metadata key of a quantity sample whose record
// also filled figures of a provider daily summary: the summary's day,
// "YYYY-MM-DD". A Deletion of the record clears its ClearSummaryFields on
// that day.
const MetadataSummaryDate = "summary_date"

// IsEmpty returns true if no canonical records and no deletions were produced.
func (r *MapResult) IsEmpty() bool {
	return len(r.QuantitySamples) == 0 &&
		len(r.CategorySamples) == 0 &&
		len(r.SleepSessions) == 0 &&
		len(r.SleepPhases) == 0 &&
		len(r.Workouts) == 0 &&
		len(r.RoutePoints) == 0 &&
		len(r.NutritionLogs) == 0 &&
		len(r.ECGRecordings) == 0 &&
		len(r.ECGSamples) == 0 &&
		len(r.HeartbeatSamples) == 0 &&
		len(r.DailySummaries) == 0 &&
		len(r.Observations) == 0 &&
		len(r.Deletions) == 0
}

// RecordCount returns the total number of canonical records and deletions in
// the result.
func (r *MapResult) RecordCount() int {
	return len(r.QuantitySamples) +
		len(r.CategorySamples) +
		len(r.SleepSessions) +
		len(r.SleepPhases) +
		len(r.Workouts) +
		len(r.RoutePoints) +
		len(r.NutritionLogs) +
		len(r.ECGRecordings) +
		len(r.ECGSamples) +
		len(r.HeartbeatSamples) +
		len(r.DailySummaries) +
		len(r.Observations) +
		len(r.Deletions)
}
