// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/api/read"
)

func ptr[T any](v T) *T { return &v }

var (
	t0       = time.Date(2026, 3, 1, 8, 0, 0, 0, time.FixedZone("CET", 3600))
	ingested = time.Date(2026, 3, 1, 9, 15, 30, 250000000, time.UTC)
)

// samples are invented: one with every member, one with only the required.
func samples() []read.Sample {
	return []read.Sample{
		{ID: 1, Type: "heart_rate", Value: 61.5, Unit: ptr("bpm"), StartDate: t0, EndDate: ptr(t0.Add(time.Minute)),
			SourceName: ptr("=HYPERLINK(\"x\")"), ExternalID: ptr("ext-1"), IsPrimarySource: ptr(true),
			WasUserEntered: ptr(false), Metadata: json.RawMessage(`{ "zone": "Europe/Paris" }`),
			Provenance: read.Provenance{Provider: "apple_health", Source: ptr("apple"), Device: ptr("apple_watch"), IngestedAt: ingested}},
		{ID: 2, Type: "step_count", Value: 1200, StartDate: t0, Metadata: json.RawMessage(`null`),
			Provenance: read.Provenance{Provider: "oura", IngestedAt: ingested}},
	}
}

func write[T any](t *testing.T, f Format, pages ...[]T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter[T](&buf, f)
	require.NoError(t, err)
	for _, p := range pages {
		require.NoError(t, w.Write(p))
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestCSV(t *testing.T) {
	s := samples()
	got := string(write(t, CSV, s[:1], s[1:]))
	want := "id,type,value,unit,start_date,end_date,source_name,source_bundle_id,device_model,product_type," +
		"external_id,is_primary_source,was_user_entered,metadata,provider,source,device,ingested_at\n" +
		`1,heart_rate,61.5,bpm,2026-03-01T07:00:00Z,2026-03-01T07:01:00Z,"'=HYPERLINK(""x"")",,,,ext-1,true,false,` +
		`"{""zone"":""Europe/Paris""}",apple_health,apple,apple_watch,2026-03-01T09:15:30.25Z` + "\n" +
		"2,step_count,1200,,2026-03-01T07:00:00Z,,,,,,,,,,oura,,,2026-03-01T09:15:30.25Z\n"
	assert.Equal(t, want, got)
}

func TestCSVEmpty(t *testing.T) {
	got := string(write[read.DailySummary](t, CSV))
	assert.Equal(t, "id,date,readiness_score,activity_score,sleep_score,recovery_score,strain_score,step_count,"+
		"active_kcal,total_kcal,distance_m,floors_climbed,resting_hr,hrv_avg_ms,metadata,sources,provider,source,device,ingested_at\n", got)
}

func TestCSVSourcesMap(t *testing.T) {
	d := read.DailySummary{ID: 1, Date: "2026-03-01", StepCount: ptr(9000),
		Sources:    map[string]string{"step_count": "apple_health"},
		Provenance: read.Provenance{Provider: "apple_health", IngestedAt: ingested}}
	got := string(write(t, CSV, []read.DailySummary{d}))
	assert.Contains(t, got, `,"{""step_count"":""apple_health""}",apple_health,`)
}

func TestParquet(t *testing.T) {
	raw := write(t, Parquet, samples())
	f, err := parquet.OpenFile(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	assert.EqualValues(t, 2, f.NumRows())

	fields := map[string]parquet.Field{}
	var names []string
	for _, fl := range f.Schema().Fields() {
		fields[fl.Name()] = fl
		names = append(names, fl.Name())
	}
	assert.Equal(t, []string{"id", "type", "value", "unit", "start_date", "end_date", "source_name",
		"source_bundle_id", "device_model", "product_type", "external_id", "is_primary_source", "was_user_entered",
		"metadata", "provider", "source", "device", "ingested_at"}, names, "the REST member order")
	assert.True(t, fields["id"].Required())
	assert.True(t, fields["unit"].Optional())
	assert.Equal(t, "TIMESTAMP(isAdjustedToUTC=true,unit=MICROS)", fields["start_date"].Type().LogicalType().String())
	assert.Equal(t, "JSON", fields["metadata"].Type().LogicalType().String())

	r := parquet.NewReader(bytes.NewReader(raw))
	var rows []map[string]any
	for range 2 {
		row := map[string]any{}
		require.NoError(t, r.Read(&row))
		rows = append(rows, row)
	}
	assert.EqualValues(t, 1, rows[0]["id"])
	assert.Equal(t, 61.5, rows[0]["value"])
	assert.Equal(t, t0.UnixMicro(), rows[0]["start_date"])
	assert.Equal(t, `=HYPERLINK("x")`, rows[0]["source_name"], "only CSV guards against formulas")
	assert.Equal(t, true, rows[0]["is_primary_source"])
	assert.Nil(t, rows[1]["unit"])
	assert.Nil(t, rows[1]["end_date"])
	assert.Nil(t, rows[1]["metadata"], "JSON null is no value")
	assert.Equal(t, "oura", rows[1]["provider"])
}

// TestParquetRowGroups: a large file is cut into row groups as it is
// written, and keeps every row.
func TestParquetRowGroups(t *testing.T) {
	recs := make([]read.Sample, rowGroupRows+10)
	for i := range recs {
		recs[i] = read.Sample{ID: int64(i + 1), Type: "heart_rate", Value: 60, StartDate: t0, Provenance: read.Provenance{Provider: "x", IngestedAt: ingested}}
	}
	raw := write(t, Parquet, recs[:1000], recs[1000:])
	f, err := parquet.OpenFile(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	assert.EqualValues(t, len(recs), f.NumRows())
	assert.Len(t, f.RowGroups(), 2)
}

// listColumns checks that T has a column for each of its JSON members (an
// omitempty member, absent from the zero value, included) and that a Parquet
// schema can be made of it.
func listColumns[T any](t *testing.T) {
	t.Helper()
	cols, err := columns(reflect.TypeFor[T](), nil)
	require.NoError(t, err)
	_, err = NewWriter[T](&bytes.Buffer{}, Parquet)
	require.NoError(t, err)
	var zero T
	b, err := json.Marshal(zero)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	names := map[string]bool{}
	for _, c := range cols {
		names[c.name] = true
		if _, ok := m[c.name]; !ok {
			tag := reflect.TypeFor[T]().FieldByIndex(c.index).Tag.Get("json")
			assert.Contains(t, tag, ",omitempty", c.name)
		}
	}
	for k := range m {
		assert.True(t, names[k], k)
	}
}

// TestEveryListType: every list an export reads has a column for each JSON
// member.
func TestEveryListType(t *testing.T) {
	listColumns[read.Sample](t)
	listColumns[read.CategorySample](t)
	listColumns[read.SleepSession](t)
	listColumns[read.Workout](t)
	listColumns[read.NutritionLog](t)
	listColumns[read.DailySummary](t)
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"csv": CSV, "CSV": CSV, "parquet": Parquet} {
		got, ok := ParseFormat(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"", "json", "xlsx"} {
		_, ok := ParseFormat(in)
		assert.False(t, ok, in)
	}
	assert.Equal(t, "text/csv; charset=utf-8", CSV.ContentType())
	assert.Equal(t, "application/vnd.apache.parquet", Parquet.ContentType())
}

func TestUnsupportedType(t *testing.T) {
	_, err := NewWriter[struct{ A []int }](&bytes.Buffer{}, CSV)
	assert.Error(t, err)
	_, err = NewWriter[int](&bytes.Buffer{}, CSV)
	assert.Error(t, err)
}

func TestParquetEmpty(t *testing.T) {
	raw := write[read.SleepSession](t, Parquet)
	f, err := parquet.OpenFile(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	assert.EqualValues(t, 0, f.NumRows())
	assert.Len(t, f.Schema().Fields(), 19)
}
