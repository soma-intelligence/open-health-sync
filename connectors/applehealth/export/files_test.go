// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routeExportXML is an invented export.xml with one workout whose route file
// is workout-routes/route_a.gpx.
const routeExportXML = `<?xml version="1.0" encoding="UTF-8"?>
<HealthData locale="en_US">
 <ExportDate value="2026-07-15 09:00:00 +0200"/>
 <Workout workoutActivityType="HKWorkoutActivityTypeCycling" duration="20" durationUnit="min" sourceName="Test Watch" creationDate="2026-02-01 09:21:00 +0100" startDate="2026-02-01 09:00:00 +0100" endDate="2026-02-01 09:20:00 +0100">
  <MetadataEntry key="HKTimeZone" value="Europe/Paris"/>
  <WorkoutRoute sourceName="Test Watch" startDate="2026-02-01 09:00:00 +0100" endDate="2026-02-01 09:20:00 +0100">
   <FileReference path="/workout-routes/route_a.gpx"/>
  </WorkoutRoute>
 </Workout>
</HealthData>`

// writeExport lays out an export on disk: export.xml and the given files,
// by path relative to the export root.
func writeExport(t *testing.T, xmlDoc string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export.xml"), []byte(xmlDoc), 0o600))
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}
	return filepath.Join(dir, "export.xml")
}

func TestWorkoutRouteKeepsGoodPointsOfABrokenFile(t *testing.T) {
	gpx := `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1"><trk><trkseg>
 <trkpt lon="2.5" lat="45.5"><time>2026-02-01T08:00:00Z</time><ele>120</ele><extensions><speed>6.5</speed></extensions></trkpt>
 <trkpt lon="east" lat="45.6"><time>2026-02-01T08:00:01Z</time></trkpt>
 <trkpt lon="2.6" lat="45.6"><ele>121</ele></trkpt>
 <trkpt lon="2.7" lat="45.7"><time>2026-02-01T08:00:03Z</time></trkpt>
 <trkpt lon="2.8"`
	parts, stats := convertFile(t, writeExport(t, routeExportXML, map[string]string{"workout-routes/route_a.gpx": gpx}), Options{})
	assert.Equal(t, 2, stats.RoutePoints)
	assert.Equal(t, 0, stats.NoRoute)
	assert.Equal(t, 3, stats.Invalid["route"], "two points without a position or time, then the cut")
	require.Len(t, parts, 1, "a workout with a route is a part of its own")
	assert.Equal(t, TypeWorkout, parts[0].Type)
	assert.Equal(t, "2026-02-01", parts[0].Day)
	assert.Equal(t, 3, parts[0].Lines)

	mr := mapAll(t, parts)
	require.Len(t, mr.Workouts, 1)
	require.Len(t, mr.RoutePoints, 2)
	p := mr.RoutePoints[0]
	assert.Equal(t, 45.5, p.Latitude)
	assert.Equal(t, 2.5, p.Longitude)
	assert.Equal(t, utc("2026-02-01T08:00:00Z"), p.Timestamp)
	require.NotNil(t, p.AltitudeM)
	assert.Equal(t, 120.0, *p.AltitudeM)
	require.NotNil(t, p.SpeedMps)
	assert.Equal(t, 6.5, *p.SpeedMps)
	assert.Nil(t, mr.RoutePoints[1].AltitudeM)
}

func TestWorkoutRouteEmitErrorStops(t *testing.T) {
	e, err := Open(writeExport(t, routeExportXML, map[string]string{"workout-routes/route_a.gpx": `<gpx/>`}))
	require.NoError(t, err)
	defer e.Close()
	stop := errors.New("disk full")
	_, err = Convert(context.Background(), e, Options{}, func(Part) error { return stop })
	require.ErrorIs(t, err, stop)
}

const ecgAFib = "Name,Test Person\nRecorded Date,2026-02-02 07:30:00 +0100\nClassification,Atrial Fibrillation\nSymptoms,None\nSample Rate,2 hertz\n\n100\n-50.5\nnot a number\n0\n25\n"

func TestReadECGFiles(t *testing.T) {
	name := writeExport(t, `<HealthData locale="en_US"/>`, map[string]string{
		"electrocardiograms/ecg_a.csv": ecgAFib,
		// A translated header: the date is found by its format, an unknown
		// classification is 100, any other symptom text is 2.
		"electrocardiograms/ecg_b.CSV": "Nom,Test\nDate,2026-02-03 07:30:00 +0100\nClassification,Rythme sinusal\nSymptoms,Vertige\n12\n",
		"electrocardiograms/ecg_c.csv": "Name,Test Person\nClassification,Sinus Rhythm\n10\n", // no date
		"electrocardiograms/ecg_d.csv": "Recorded Date,2026-02-04 07:30:00 +0100\n",           // no voltage
		"electrocardiograms/notes.txt": "ignored",
	})
	parts, stats := convertFile(t, name, Options{})
	assert.Equal(t, 4, stats.Records)
	assert.Equal(t, 2, stats.Invalid[TypeECG])
	require.Len(t, parts, 2)

	l := lines(t, parts[0])
	require.Len(t, l, 5, "the recording, then its four voltages")
	assert.EqualValues(t, 2, l[0]["classification"])
	assert.EqualValues(t, 1, l[0]["symptomsStatus"])
	assert.EqualValues(t, 2, l[0]["samplingFrequency"])
	assert.EqualValues(t, 4, l[0]["numberOfVoltageMeasurements"])
	assert.InDelta(t, 0.5, l[2]["timeSinceSampleStart"], 1e-12)
	assert.InDelta(t, -50.5e-6, l[2]["value"], 1e-15, "microvolts are sent as volts")

	mr := mapAll(t, parts)
	require.Len(t, mr.ECGRecordings, 2)
	rec := mr.ECGRecordings[0]
	assert.Equal(t, utc("2026-02-02T06:30:00Z"), rec.StartDate)
	assert.Equal(t, rec.StartDate.Add(2*time.Second), rec.EndDate, "four samples at 2 Hz last 2 s")
	require.Len(t, mr.ECGSamples, 5)
	assert.InDelta(t, 100.0, mr.ECGSamples[0].VoltageUV, 1e-9)
	assert.InDelta(t, -50.5, mr.ECGSamples[1].VoltageUV, 1e-9)

	b := mr.ECGRecordings[1]
	assert.Equal(t, 100, b.Classification)
	assert.Equal(t, 2, b.SymptomsStatus)
	assert.Equal(t, defaultECGRate, b.SamplingFrequency)
	assert.Equal(t, utc("2026-02-03T06:30:00Z"), b.StartDate)
}

func TestReadECGsErrors(t *testing.T) {
	t.Run("electrocardiograms is a file", func(t *testing.T) {
		e, err := Open(writeExport(t, `<HealthData locale="en_US"/>`, map[string]string{"electrocardiograms": "x"}))
		require.NoError(t, err)
		defer e.Close()
		_, err = Convert(context.Background(), e, Options{}, func(Part) error { return nil })
		require.Error(t, err)
	})
	t.Run("cancelled before the ECG files", func(t *testing.T) {
		e, err := Open(writeExport(t, routeExportXML, map[string]string{
			"workout-routes/route_a.gpx":   `<gpx/>`,
			"electrocardiograms/ecg_a.csv": ecgAFib,
		}))
		require.NoError(t, err)
		defer e.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var parts []Part
		_, err = Convert(ctx, e, Options{}, func(p Part) error { parts = append(parts, p); cancel(); return nil })
		require.ErrorIs(t, err, context.Canceled)
		require.Len(t, parts, 1, "only the workout part was sent")
	})
	t.Run("emit error on an ECG part", func(t *testing.T) {
		e, err := Open(writeExport(t, `<HealthData locale="en_US"/>`, map[string]string{"electrocardiograms/ecg_a.csv": ecgAFib}))
		require.NoError(t, err)
		defer e.Close()
		stop := errors.New("disk full")
		_, err = Convert(context.Background(), e, Options{}, func(Part) error { return stop })
		require.ErrorIs(t, err, stop)
	})
}

func TestXMLSize(t *testing.T) {
	want, err := os.Stat(filepath.Join(fixtureDir, "export.xml"))
	require.NoError(t, err)

	z, err := Open(fixtureZip(t))
	require.NoError(t, err)
	defer z.Close()
	got, err := z.XMLSize()
	require.NoError(t, err)
	assert.Equal(t, want.Size(), got)

	name := writeExport(t, routeExportXML, nil)
	d, err := Open(name)
	require.NoError(t, err)
	got, err = d.XMLSize()
	require.NoError(t, err)
	assert.EqualValues(t, len(routeExportXML), got)

	require.NoError(t, os.Remove(name))
	_, err = d.XMLSize()
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestOpenErrors(t *testing.T) {
	_, err := Open(t.TempDir())
	require.ErrorContains(t, err, "is a folder: pass export.zip or export.xml")
	_, err = Open(filepath.Join(t.TempDir(), "absent.zip"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestUploaderDefaults(t *testing.T) {
	u := &Uploader{}
	assert.Equal(t, 5*time.Minute, u.client().Timeout)
	custom := &http.Client{Timeout: time.Second}
	u.Client = custom
	assert.Same(t, custom, u.client())

	for n, want := range map[int]time.Duration{
		1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 40: 30 * time.Second, 70: 30 * time.Second,
	} {
		assert.Equal(t, want, u.backoff(n), "retry %d", n)
	}
	u.Backoff = func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	assert.Equal(t, 3*time.Millisecond, u.backoff(3))
}

func TestGzipBytesIsDeterministic(t *testing.T) {
	in := []byte("{\"a\":1}\n{\"b\":2}\n")
	a, err := gzipBytes(in)
	require.NoError(t, err)
	b, err := gzipBytes(in)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	zr, err := gzip.NewReader(bytes.NewReader(a))
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, in, out)
	assert.Empty(t, zr.Name)
	assert.True(t, zr.ModTime.IsZero() || zr.ModTime.Unix() == 0)
}

// roundTripFunc is an http.RoundTripper made of one function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUploadNetworkErrorsAreRetriedAndUnwrapped(t *testing.T) {
	refused := errors.New("connection refused")
	calls := 0
	up := &Uploader{
		Endpoint: "http://ingest.invalid/v1/ingest/apple-health", Token: testToken, Batch: "ahx-net",
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return time.Millisecond },
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			assert.Equal(t, "application/gzip", r.Header.Get("Content-Type"))
			assert.Equal(t, "4", r.Header.Get("X-OHS-Part"))
			return nil, refused
		})},
	}
	_, err := up.Upload(context.Background(), Part{Number: 4, Type: TypeWorkout, Day: "2026-02-01", Lines: 1, NDJSON: []byte("{}\n")})
	require.Error(t, err)
	assert.Equal(t, 3, calls)
	assert.ErrorIs(t, err, refused, "the transport error stays reachable through the retry wrapper")
	assert.True(t, strings.Contains(err.Error(), "gave up after 3 attempts"))

	var re retryable
	require.ErrorAs(t, err, &re)
	assert.ErrorIs(t, re.Unwrap(), refused)
}
