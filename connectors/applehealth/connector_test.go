// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
)

func TestRegisteredAsDeviceConnector(t *testing.T) {
	c, ok := connector.Get(Slug)
	require.True(t, ok, "apple_health registers itself in init")
	_, device := connector.AsDevice(c)
	assert.True(t, device)
	assert.Equal(t, "apple_health", c.Slug())
	assert.True(t, c.Enabled(nil), "a device connector needs no keys")
}

func TestMapUnknownLineIsEmpty(t *testing.T) {
	res, err := New().Map(connector.RawRecord{UserID: "u", Fields: loadTestFixture(t, "unknown_type.json")})
	require.NoError(t, err)
	assert.True(t, res.IsEmpty())
}

func TestMapClinicalLineIsDropped(t *testing.T) {
	res, err := New().Map(connector.RawRecord{UserID: "u", Fields: map[string]any{
		"startDate": "2025-01-15T09:00:00Z", "clinicalType": "AllergyRecord",
		"fhirResource": map[string]any{"resourceType": "AllergyIntolerance"},
	}})
	require.NoError(t, err)
	assert.True(t, res.IsEmpty(), "clinical records have no canonical table")
}

func TestMapErrorPropagates(t *testing.T) {
	_, err := New().Map(connector.RawRecord{UserID: "u", Fields: loadTestFixture(t, "bad_values.json")})
	assert.Error(t, err)
}

// R17: every sample the connector maps carries the line's IANA zone, which
// the ETL needs to find the local day.
func TestEveryMappedSampleCarriesTheTimezone(t *testing.T) {
	f, err := os.Open("testdata/ios_upload_lines.ndjson")
	require.NoError(t, err)
	defer f.Close()

	c := New()
	var mapped int
	check := func(kind string, md map[string]any) {
		mapped++
		assert.Equal(t, "Europe/Paris", md["timezone"], "%s lost the time zone", kind)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
		res, err := c.Map(connector.RawRecord{UserID: "u", Fields: rec})
		require.NoError(t, err)
		for _, s := range res.QuantitySamples {
			check("quantity "+s.QuantityType, s.Metadata)
		}
		for _, s := range res.CategorySamples {
			check("category "+s.CategoryType, s.Metadata)
		}
		for _, s := range res.SleepSessions {
			check("sleep session", s.Metadata)
		}
		for _, w := range res.Workouts {
			check("workout", w.Metadata)
		}
	}
	require.NoError(t, sc.Err())
	assert.Equal(t, 7, mapped, "heart rate, spo2, steps, in-bed session, workout, two blood pressure")

	// The other sample kinds, from lines that carry a zone.
	tz := map[string]any{"HKTimeZone": "Europe/Paris"}
	for name, rec := range map[string]map[string]any{
		"dietary": {"quantityType": "HKQuantityTypeIdentifierDietaryProtein", "value": 25.0, "unit": "g",
			"startDate": "2025-03-01T12:00:00Z", "metadata": tz},
		"food": {"correlationType": "HKCorrelationTypeIdentifierFood", "startDate": "2025-03-01T12:00:00Z",
			"objects":  []any{map[string]any{"sampleType": "HKQuantityTypeIdentifierDietaryProtein", "value": 25.0, "unit": "g"}},
			"metadata": tz},
		"ecg": {"classification": 1.0, "startDate": "2025-03-01T12:00:00Z", "metadata": tz},
		"category": {"categoryType": "HKCategoryTypeIdentifierMindfulSession", "value": 0.0,
			"startDate": "2025-03-01T12:00:00Z", "metadata": tz},
		"audiogram":     {"quantity_type": "HKDataTypeIdentifierAudiogramSample", "startDate": "2025-03-01T12:00:00Z", "metadata": tz},
		"state of mind": {"quantity_type": "HKStateOfMindType", "startDate": "2025-03-01T12:00:00Z", "metadata": tz},
	} {
		res, err := c.Map(connector.RawRecord{UserID: "u", Fields: rec})
		require.NoError(t, err, name)
		var mds []map[string]any
		for _, s := range res.QuantitySamples {
			mds = append(mds, s.Metadata)
		}
		for _, s := range res.CategorySamples {
			mds = append(mds, s.Metadata)
		}
		for _, s := range res.NutritionLogs {
			mds = append(mds, s.Metadata)
		}
		for _, s := range res.ECGRecordings {
			mds = append(mds, s.Metadata)
		}
		require.Len(t, mds, 1, name)
		assert.Equal(t, "Europe/Paris", mds[0]["timezone"], name)
	}
}

func parse(t *testing.T, body string) error {
	t.Helper()
	return New().ParseUpload(connector.UploadMeta{}, strings.NewReader(body), nil)
}

func TestParseUploadAcceptsTheSDKLines(t *testing.T) {
	raw, err := os.ReadFile("testdata/ios_upload_lines.ndjson")
	require.NoError(t, err)
	require.NoError(t, parse(t, string(raw)))
	require.NoError(t, parse(t, "{\"a\":1}\n\n  \n{\"b\":2}"), "blank lines and a missing final newline are fine")
	require.NoError(t, parse(t, "{\"a\":1}\r\n"), "CRLF is fine")
}

func TestParseUploadRejectsBadLines(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   "{\"a\":1}\nnot json\n",
		"array":      "[1,2]\n",
		"scalar":     "42\n",
		"truncated":  "{\"a\":\n",
		"two values": "{\"a\":1}{\"b\":2}\n",
	} {
		err := parse(t, body)
		assert.ErrorIs(t, err, ErrInvalidLine, name)
	}
	assert.ErrorIs(t, parse(t, ""), ErrNoLines)
	assert.ErrorIs(t, parse(t, "\n \n"), ErrNoLines)
}

func TestParseUploadNamesTheBadLine(t *testing.T) {
	err := parse(t, "{\"a\":1}\n{\"b\":2}\nnope\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 3")
}

func TestParseUploadRejectsAnOverlongLine(t *testing.T) {
	big := "{\"a\":\"" + strings.Repeat("x", MaxLineBytes) + "\"}\n"
	assert.ErrorIs(t, parse(t, big), ErrInvalidLine)
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestParseUploadReturnsTheReadError(t *testing.T) {
	cause := errors.New("unexpected EOF in gzip")
	err := New().ParseUpload(connector.UploadMeta{},
		io.MultiReader(strings.NewReader("{\"a\":1}\n"), failingReader{cause}), nil)
	assert.ErrorIs(t, err, cause)
	assert.NotErrorIs(t, err, ErrInvalidLine)
}

func TestUnknownTypesAreBounded(t *testing.T) {
	buf := logtest.Capture(t, zerolog.DebugLevel)

	m := newMapper()
	for i := 0; i < maxUnknownTypes+50; i++ {
		m.trackUnknown(fmt.Sprintf("HKQuantityTypeIdentifierFuture%d", i))
	}
	m.trackUnknown("HKQuantityTypeIdentifierFuture0")
	counts := m.unknownTypeCounts()
	assert.Len(t, counts, maxUnknownTypes)
	assert.Equal(t, 2, counts["HKQuantityTypeIdentifierFuture0"], "a tracked type keeps counting past the cap")
	assert.Equal(t, 50, m.unknownOverflow)
	assert.Equal(t, 1, strings.Count(buf.String(), `"message":"unknown_type_tracking_full"`), "one warning when the cap is first hit")
	assert.Equal(t, maxUnknownTypes, strings.Count(buf.String(), `"message":"unknown_type_skipped"`), "one log per tracked type")
}

// A line with no field the mapper recognizes is skipped and counted like an
// unknown type, under one label.
func TestUnrecognizedLineIsCounted(t *testing.T) {
	m := newMapper()
	res, err := m.mapRecord(map[string]any{"foo": 1.0}, "u")
	assert.NoError(t, err)
	assert.Nil(t, res)
	assert.Equal(t, 1, m.unknownTypeCounts()[unrecognizedLine])
}
