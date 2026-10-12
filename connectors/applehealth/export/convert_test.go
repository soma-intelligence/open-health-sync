// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// fixtureDir is an invented export, laid out as the Health app writes it.
const fixtureDir = "testdata/apple_health_export"

// fixtureZip builds export.zip from fixtureDir, as the Health app names its
// entries (apple_health_export/...).
func fixtureZip(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "export.zip")
	f, err := os.Create(name)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(fixtureDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(fixtureDir), p)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return name
}

func convertFile(t *testing.T, name string, opts Options) ([]Part, Stats) {
	t.Helper()
	e, err := Open(name)
	require.NoError(t, err)
	defer e.Close()
	var parts []Part
	stats, err := Convert(context.Background(), e, opts, func(p Part) error {
		parts = append(parts, p)
		return nil
	})
	require.NoError(t, err)
	return parts, stats
}

// lines splits a part into its decoded lines, decoded the way the ETL
// decodes them.
func lines(t *testing.T, p Part) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(p.NDJSON))
	sc.Buffer(nil, applehealth.MaxLineBytes)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), "part %d", p.Number)
		out = append(out, m)
	}
	require.NoError(t, sc.Err())
	return out
}

// mapPart runs a part through the real connector, line by line, as the ETL
// does for one raw object.
func mapPart(t *testing.T, p Part) model.MapResult {
	t.Helper()
	c := applehealth.New()
	require.NoError(t, c.ParseUpload(connector.UploadMeta{}, bytes.NewReader(p.NDJSON), nil), "part %d must pass the upload check", p.Number)
	var acc model.MapResult
	for _, l := range lines(t, p) {
		mr, err := c.Map(connector.RawRecord{UserID: "user-1", Provider: applehealth.Slug, Fields: l})
		require.NoError(t, err, "part %d line %v", p.Number, l)
		acc.QuantitySamples = append(acc.QuantitySamples, mr.QuantitySamples...)
		acc.CategorySamples = append(acc.CategorySamples, mr.CategorySamples...)
		acc.SleepSessions = append(acc.SleepSessions, mr.SleepSessions...)
		acc.SleepPhases = append(acc.SleepPhases, mr.SleepPhases...)
		acc.Workouts = append(acc.Workouts, mr.Workouts...)
		acc.RoutePoints = append(acc.RoutePoints, mr.RoutePoints...)
		acc.NutritionLogs = append(acc.NutritionLogs, mr.NutritionLogs...)
		acc.ECGRecordings = append(acc.ECGRecordings, mr.ECGRecordings...)
		acc.ECGSamples = append(acc.ECGSamples, mr.ECGSamples...)
		acc.HeartbeatSamples = append(acc.HeartbeatSamples, mr.HeartbeatSamples...)
	}
	return acc
}

func mapAll(t *testing.T, parts []Part) model.MapResult {
	t.Helper()
	var acc model.MapResult
	for _, p := range parts {
		mr := mapPart(t, p)
		acc.QuantitySamples = append(acc.QuantitySamples, mr.QuantitySamples...)
		acc.CategorySamples = append(acc.CategorySamples, mr.CategorySamples...)
		acc.SleepSessions = append(acc.SleepSessions, mr.SleepSessions...)
		acc.SleepPhases = append(acc.SleepPhases, mr.SleepPhases...)
		acc.Workouts = append(acc.Workouts, mr.Workouts...)
		acc.RoutePoints = append(acc.RoutePoints, mr.RoutePoints...)
		acc.NutritionLogs = append(acc.NutritionLogs, mr.NutritionLogs...)
		acc.ECGRecordings = append(acc.ECGRecordings, mr.ECGRecordings...)
		acc.ECGSamples = append(acc.ECGSamples, mr.ECGSamples...)
		acc.HeartbeatSamples = append(acc.HeartbeatSamples, mr.HeartbeatSamples...)
	}
	return acc
}

func samplesOf(mr model.MapResult, qt string) []model.QuantitySample {
	var out []model.QuantitySample
	for _, s := range mr.QuantitySamples {
		if s.QuantityType == qt {
			out = append(out, s)
		}
	}
	return out
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestFixtureExportMapsToCanonicalRecords is the shape proof: the lines made
// from the fixture export, passed through the real connector, give the
// canonical records the export describes.
func TestFixtureExportMapsToCanonicalRecords(t *testing.T) {
	parts, stats := convertFile(t, fixtureZip(t), Options{})
	mr := mapAll(t, parts)

	hr := samplesOf(mr, "heart_rate")
	require.Len(t, hr, 3)
	first := hr[0]
	assert.Equal(t, utc("2026-01-10T07:00:00Z"), first.StartDate)
	assert.Equal(t, 61.0, first.Value)
	assert.Equal(t, "bpm", first.Unit)
	assert.Equal(t, "Test Watch", first.SourceName)
	assert.Equal(t, "Test Watch", first.DeviceID)
	assert.Equal(t, "Watch", first.DeviceModel)
	assert.Equal(t, "Watch7,1", first.ProductType)
	assert.Equal(t, "1", first.Metadata["HKMetadataKeyHeartRateMotionContext"])
	assert.Nil(t, first.WasUserEntered, "no HKWasUserEntered: unknown, not false")

	mass := samplesOf(mr, "body_mass")
	require.Len(t, mass, 1)
	require.NotNil(t, mass[0].WasUserEntered)
	assert.True(t, *mass[0].WasUserEntered)
	assert.Equal(t, 70.5, mass[0].Value)

	assert.Len(t, samplesOf(mr, "step_count"), 2)
	assert.Empty(t, samplesOf(mr, "apple_walking_steadiness"))

	// Blood pressure correlation: both members, with the parent's zone.
	sys, dia := samplesOf(mr, "blood_pressure_systolic"), samplesOf(mr, "blood_pressure_diastolic")
	require.Len(t, sys, 1)
	require.Len(t, dia, 1)
	assert.Equal(t, 112.0, sys[0].Value)
	assert.Equal(t, 71.0, dia[0].Value)
	assert.Equal(t, utc("2026-01-10T07:05:00Z"), sys[0].StartDate)
	assert.Equal(t, "Europe/Paris", sys[0].Metadata["timezone"])
	assert.Equal(t, "HKCorrelationTypeIdentifierBloodPressure", sys[0].Metadata["correlation_type"])

	// Nutrition: one dietary record, one food correlation.
	require.Len(t, mr.NutritionLogs, 2)
	var food *model.NutritionLog
	for i := range mr.NutritionLogs {
		if mr.NutritionLogs[i].EnergyKcal != nil {
			food = &mr.NutritionLogs[i]
		}
	}
	require.NotNil(t, food)
	assert.Equal(t, 410.0, *food.EnergyKcal)
	require.NotNil(t, food.CarbsG)
	assert.Equal(t, 52.0, *food.CarbsG)

	// Category and sleep: HKCategoryValue names became HealthKit integers.
	require.Len(t, mr.CategorySamples, 1)
	assert.Equal(t, "mindful_session", mr.CategorySamples[0].CategoryType)
	require.Len(t, mr.SleepSessions, 1)
	assert.Equal(t, utc("2026-01-09T22:00:00Z"), mr.SleepSessions[0].BedtimeStart)
	require.Len(t, mr.SleepPhases, 2)
	assert.Equal(t, "in_bed", mr.SleepPhases[0].Phase)
	assert.Equal(t, model.CanonicalSleepStage("asleep_core"), mr.SleepPhases[1].Phase)

	// Workouts, statistics converted to metres and kilocalories.
	require.Len(t, mr.Workouts, 2)
	run, walk := mr.Workouts[0], mr.Workouts[1]
	if run.ActivityType != "running" {
		run, walk = walk, run
	}
	assert.Equal(t, "running", run.ActivityType)
	assert.Equal(t, utc("2026-01-10T07:00:00Z"), run.StartDate)
	require.NotNil(t, run.DurationS)
	assert.Equal(t, 1800, *run.DurationS)
	assert.InDelta(t, 5200, *run.DistanceM, 1e-9)
	assert.InDelta(t, 300, *run.ActiveEnergyKcal, 1e-9)
	assert.Equal(t, 150.0, *run.AvgHeartRate)
	assert.Equal(t, 171.0, *run.MaxHeartRate)
	assert.InDelta(t, 12.5, *run.ElevationGainM, 1e-9)
	assert.Equal(t, "Europe/Paris", run.Metadata["timezone"])
	assert.Equal(t, "walking", walk.ActivityType)
	assert.InDelta(t, 1609.344, *walk.DistanceM, 1e-9)
	assert.InDelta(t, 100, *walk.ActiveEnergyKcal, 1e-9)
	assert.Equal(t, 1800, *walk.DurationS)

	// Route: three points, in one part with the running workout only, so
	// the server attaches them to it.
	require.Len(t, mr.RoutePoints, 3)
	assert.Equal(t, 45.0, mr.RoutePoints[0].Latitude)
	assert.Equal(t, utc("2026-01-10T07:00:00Z"), mr.RoutePoints[0].Timestamp)
	require.NotNil(t, mr.RoutePoints[0].SpeedMps)
	assert.Equal(t, 2.9, *mr.RoutePoints[0].SpeedMps)
	routePart := partWith(t, parts, func(m model.MapResult) bool { return len(m.RoutePoints) > 0 })
	inPart := mapPart(t, routePart)
	assert.Len(t, inPart.Workouts, 1)
	assert.Len(t, inPart.RoutePoints, 3)
	assert.Equal(t, TypeWorkout, routePart.Type)

	// Heartbeat series across midnight: beats at 0.5 s, 1.25 s, 2 s.
	require.Len(t, mr.HeartbeatSamples, 2)
	assert.Equal(t, 750.0, mr.HeartbeatSamples[0].RRMS)
	assert.Equal(t, 750.0, mr.HeartbeatSamples[1].RRMS)
	assert.Equal(t, utc("2026-01-10T22:59:59Z").Add(1250*time.Millisecond), mr.HeartbeatSamples[0].Timestamp)

	// Audiogram.
	aud := samplesOf(mr, "audiogram")
	require.Len(t, aud, 1)
	assert.Equal(t, 2.0, aud[0].Value)

	// ECG: the recording and its voltages in one part, microvolts kept.
	require.Len(t, mr.ECGRecordings, 1)
	ecg := mr.ECGRecordings[0]
	assert.Equal(t, 1, ecg.Classification)
	assert.Equal(t, 4.0, ecg.SamplingFrequency)
	assert.Equal(t, 4, ecg.NumberOfMeasurements)
	assert.Equal(t, utc("2026-01-10T13:00:00Z"), ecg.StartDate)
	assert.Equal(t, utc("2026-01-10T13:00:01Z"), ecg.EndDate)
	require.Len(t, mr.ECGSamples, 4)
	assert.InDelta(t, 12.5, mr.ECGSamples[0].VoltageUV, 1e-9)
	assert.InDelta(t, -3.25, mr.ECGSamples[1].VoltageUV, 1e-9)
	assert.Equal(t, 0.25, mr.ECGSamples[1].TimeSinceSampleStart)
	ecgPart := partWith(t, parts, func(m model.MapResult) bool { return len(m.ECGSamples) > 0 })
	assert.Equal(t, TypeECG, ecgPart.Type)
	assert.Len(t, mapPart(t, ecgPart).ECGRecordings, 1)

	// What was read, produced and left out.
	assert.Equal(t, 19, stats.Records)
	assert.Equal(t, map[string]int{"HKQuantityTypeIdentifierAppleWalkingSteadiness": 1, "ActivitySummary": 1}, stats.Skipped)
	assert.Empty(t, stats.Invalid)
	assert.Equal(t, 3, stats.RoutePoints)
	assert.Equal(t, 0, stats.NoRoute)
	assert.Equal(t, len(parts), stats.Parts)
	total := 0
	for _, p := range parts {
		total += p.Lines
	}
	assert.Equal(t, stats.Lines, total)
}

func partWith(t *testing.T, parts []Part, match func(model.MapResult) bool) Part {
	t.Helper()
	for _, p := range parts {
		if match(mapPart(t, p)) {
			return p
		}
	}
	t.Fatal("no part matches")
	return Part{}
}

// findLine returns the part and line whose field key equals value.
func findLine(t *testing.T, parts []Part, pred func(map[string]any) bool) (Part, map[string]any) {
	t.Helper()
	for _, p := range parts {
		for _, l := range lines(t, p) {
			if pred(l) {
				return p, l
			}
		}
	}
	t.Fatal("no line matches")
	return Part{}, nil
}

func hrValue(v float64) func(map[string]any) bool {
	return func(l map[string]any) bool {
		return l["quantityType"] == "HKQuantityTypeIdentifierHeartRate" && l["value"] == v
	}
}

// TestTimeZones: export.xml dates carry the export's offset; the instant is
// kept exactly, and the local day follows HKTimeZone when HealthKit kept it.
func TestTimeZones(t *testing.T) {
	parts, _ := convertFile(t, fixtureZip(t), Options{})

	// No HKTimeZone: the instant in UTC, the day as the export wrote it, no
	// zone on the line (the server then uses UTC, as for any such upload).
	p, l := findLine(t, parts, hrValue(61))
	assert.Equal(t, "2026-01-10T07:00:00Z", l["startDate"])
	assert.Equal(t, "2026-01-10", p.Day)
	assert.Nil(t, l["metadata"].(map[string]any)["HKTimeZone"])

	// Summer time: +0200.
	_, l = findLine(t, parts, hrValue(64.5))
	assert.Equal(t, "2026-07-01T06:00:00Z", l["startDate"])

	// Recorded in New York, exported in Paris: 05:30 +0100 is 23:30 the day
	// before in New York, and that is the day it is filed under.
	p, l = findLine(t, parts, hrValue(58))
	assert.Equal(t, "2026-01-10T04:30:00Z", l["startDate"])
	assert.Equal(t, "2026-01-09", p.Day)
	mr := mapPart(t, p)
	assert.Equal(t, "America/New_York", samplesOf(mr, "heart_rate")[0].Metadata["timezone"])

	// Across the spring change: 01:30 +0100 to 03:30 +0200 is one hour.
	p, l = findLine(t, parts, func(l map[string]any) bool { return l["value"] == 120.0 })
	assert.Equal(t, "2026-03-29T00:30:00Z", l["startDate"])
	assert.Equal(t, "2026-03-29T01:30:00Z", l["endDate"])
	assert.Equal(t, "2026-03-29", p.Day)

	// --tz fills HKTimeZone where HealthKit gave none, and only there.
	parts, _ = convertFile(t, fixtureZip(t), Options{Zone: "Asia/Tokyo"})
	p, l = findLine(t, parts, hrValue(61))
	assert.Equal(t, "Asia/Tokyo", l["metadata"].(map[string]any)["HKTimeZone"])
	assert.Equal(t, "2026-01-10", p.Day) // 16:00 in Tokyo
	assert.Equal(t, "Asia/Tokyo", samplesOf(mapPart(t, p), "heart_rate")[0].Metadata["timezone"])
	_, l = findLine(t, parts, hrValue(58))
	assert.Equal(t, "America/New_York", l["metadata"].(map[string]any)["HKTimeZone"])
}

func TestSince(t *testing.T) {
	parts, stats := convertFile(t, fixtureZip(t), Options{Since: "2026-01-10"})
	for _, p := range parts {
		assert.GreaterOrEqual(t, p.Day, "2026-01-10")
	}
	// The 2025 steps, the New York reading (2026-01-09 there) and the two
	// sleep records starting on the evening of the 9th.
	assert.Equal(t, 4, stats.BeforeSince)
	mr := mapAll(t, parts)
	assert.Empty(t, mr.SleepSessions)
	assert.Len(t, samplesOf(mr, "heart_rate"), 2)

	_, err := Convert(context.Background(), nil, Options{Since: "10/01/2026"}, nil)
	assert.ErrorContains(t, err, "YYYY-MM-DD")
	_, err = Convert(context.Background(), nil, Options{Zone: "Mars/Olympus"}, nil)
	assert.ErrorContains(t, err, "IANA")
}

// TestDeterministic: the same export gives the same parts byte for byte, from
// the zip or from the extracted folder, so a re-run is all duplicates.
func TestDeterministic(t *testing.T) {
	zipName := fixtureZip(t)
	a, _ := convertFile(t, zipName, Options{})
	b, _ := convertFile(t, zipName, Options{})
	c, _ := convertFile(t, filepath.Join(fixtureDir, "export.xml"), Options{})
	require.Equal(t, a, b)
	require.Equal(t, a, c)
	for i := range a {
		ga, err := gzipBytes(a[i].NDJSON)
		require.NoError(t, err)
		gb, err := gzipBytes(b[i].NDJSON)
		require.NoError(t, err)
		require.Equal(t, ga, gb)
	}
}

func TestBatchID(t *testing.T) {
	e, err := Open(fixtureZip(t))
	require.NoError(t, err)
	defer e.Close()
	date, err := e.ExportDate()
	require.NoError(t, err)
	assert.Equal(t, "2026-07-15 09:00:00 +0200", date)
	size, err := e.XMLSize()
	require.NoError(t, err)
	st, err := os.Stat(filepath.Join(fixtureDir, "export.xml"))
	require.NoError(t, err)
	assert.Equal(t, st.Size(), size)

	id := BatchID(date, size, Options{})
	assert.Regexp(t, `^ahx-20260715T070000Z-[0-9a-f]{12}$`, id)
	assert.LessOrEqual(t, len(id), 128)
	assert.Equal(t, id, BatchID(date, size, Options{MaxPartBytes: DefaultMaxPartBytes}), "defaults are explicit or not")
	for _, other := range []string{
		BatchID(date, size+1, Options{}),
		BatchID("2026-07-16 09:00:00 +0200", size, Options{}),
		BatchID(date, size, Options{Since: "2026-01-01"}),
		BatchID(date, size, Options{Zone: "Europe/Paris"}),
		BatchID(date, size, Options{MaxPartBytes: 1 << 20}),
	} {
		assert.NotEqual(t, id, other)
	}
}

// TestPartLimits: small limits split parts without losing or reordering a
// line, every part holds one type and one day, and part numbers are unique.
func TestPartLimits(t *testing.T) {
	all, stats := convertFile(t, fixtureZip(t), Options{})
	small, smallStats := convertFile(t, fixtureZip(t), Options{MaxPartBytes: 700, MaxBufferedBytes: 1500})
	assert.Greater(t, len(small), len(all))
	assert.Equal(t, stats.Lines, smallStats.Lines)

	seen := map[int]bool{}
	for i, p := range small {
		assert.Equal(t, i, p.Number)
		assert.False(t, seen[p.Number])
		seen[p.Number] = true
		if p.Lines > 1 {
			assert.LessOrEqual(t, len(p.NDJSON), 700, "part %d", p.Number)
		}
		assert.Equal(t, p.Lines, strings.Count(string(p.NDJSON), "\n"))
		assert.Regexp(t, `^\d{4}-\d{2}-\d{2}$`, p.Day)
		assert.Regexp(t, `^[A-Za-z0-9_-]{1,128}$`, p.Type)
	}
	// The same records come out, whatever the split.
	assert.ElementsMatch(t, hashes(mapAll(t, all)), hashes(mapAll(t, small)))
}

func hashes(mr model.MapResult) []string {
	var out []string
	for _, s := range mr.QuantitySamples {
		out = append(out, s.ContentHash)
	}
	for _, s := range mr.CategorySamples {
		out = append(out, s.ContentHash)
	}
	for _, s := range mr.Workouts {
		out = append(out, s.ContentHash)
	}
	for _, s := range mr.NutritionLogs {
		out = append(out, s.ContentHash)
	}
	return out
}

func TestMissingRouteFileKeepsTheWorkout(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join(fixtureDir, "export.xml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export.xml"), b, 0o600))
	parts, stats := convertFile(t, filepath.Join(dir, "export.xml"), Options{})
	assert.Equal(t, 1, stats.NoRoute)
	assert.Equal(t, 0, stats.RoutePoints)
	mr := mapAll(t, parts)
	assert.Len(t, mr.Workouts, 2)
	assert.Empty(t, mr.ECGRecordings, "no electrocardiograms folder next to this export.xml")
}

func TestOpenRejectsAZipWithoutExport(t *testing.T) {
	name := filepath.Join(t.TempDir(), "other.zip")
	f, err := os.Create(name)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	w, err := zw.Create("notes/export_cda.xml")
	require.NoError(t, err)
	_, _ = w.Write([]byte("<x/>"))
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	_, err = Open(name)
	assert.ErrorIs(t, err, ErrNoExportXML)
}

func TestInvalidRecordsAreCounted(t *testing.T) {
	dir := t.TempDir()
	xmlDoc := `<?xml version="1.0" encoding="UTF-8"?>
<HealthData locale="en_US">
 <ExportDate value="2026-07-15 09:00:00 +0200"/>
 <Record type="HKQuantityTypeIdentifierHeartRate" unit="count/min" startDate="yesterday" endDate="today" value="60"/>
 <Record type="HKQuantityTypeIdentifierHeartRate" unit="count/min" startDate="2026-01-10 08:00:00 +0100" endDate="2026-01-10 08:00:00 +0100" value="n/a"/>
 <Record type="HKCategoryTypeIdentifierSleepAnalysis" startDate="2026-01-10 08:00:00 +0100" endDate="2026-01-10 08:00:00 +0100" value="HKCategoryValueSleepAnalysisSomethingNew"/>
 <Record type="HKDataTypeIdentifierHeartbeatSeries" startDate="2026-01-10 08:00:00 +0100" endDate="2026-01-10 08:00:00 +0100"><HeartbeatSeries><InstantaneousBeatsPerMinute bpm="60" time="noon"/></HeartbeatSeries></Record>
 <Record type="HKQuantityTypeIdentifierHeartRate" unit="count/min" startDate="2026-01-10 08:00:00 +0100" endDate="2026-01-10 08:00:00 +0100" value="60"/>
</HealthData>`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export.xml"), []byte(xmlDoc), 0o600))
	parts, stats := convertFile(t, filepath.Join(dir, "export.xml"), Options{})
	assert.Equal(t, 5, stats.Records)
	assert.Equal(t, map[string]int{
		"HKQuantityTypeIdentifierHeartRate":     2,
		"HKCategoryTypeIdentifierSleepAnalysis": 1,
		"HKDataTypeIdentifierHeartbeatSeries":   1,
	}, stats.Invalid)
	require.Len(t, parts, 1)
	assert.Equal(t, 1, parts[0].Lines)
}

func TestValueTables(t *testing.T) {
	assert.Equal(t, 37, workoutActivityType("HKWorkoutActivityTypeRunning"))
	assert.Equal(t, 3000, workoutActivityType("HKWorkoutActivityTypeSomethingNew"))
	assert.Equal(t, 13, workoutActivityType("13"))

	for in, want := range map[string]int{
		"HKCategoryValueSleepAnalysisAsleepREM": 5,
		"HKCategoryValueSleepAnalysisAsleep":    1,
		"HKCategoryValueMenstrualFlowHeavy":     4,
		"HKCategoryValueSeverityModerate":       3,
		"2":                                     2,
	} {
		got, ok := categoryValue(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	_, ok := categoryValue("HKCategoryValueUnknown")
	assert.False(t, ok)

	d := deviceFields("<<HKDevice: 0x6000>, name:My Watch, Ltd, manufacturer:Apple Inc., model:Watch, hardware:Watch7,1, software:10.0>")
	assert.Equal(t, "My Watch, Ltd", d["name"])
	assert.Equal(t, "Watch7,1", d["hardware"])
	assert.Equal(t, "10.0", d["software"])
	assert.Nil(t, deviceFields(""))

	for in, want := range map[string]time.Duration{
		"11:59:59.50 PM": 23*time.Hour + 59*time.Minute + 59500*time.Millisecond,
		"12:00:00.25 AM": 250 * time.Millisecond,
		"12:30:00 PM":    12*time.Hour + 30*time.Minute,
		"21:05:03,75":    21*time.Hour + 5*time.Minute + 3750*time.Millisecond,
	} {
		got, ok := parseClock(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	_, ok = parseClock("noon")
	assert.False(t, ok)
}
