// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/whoop/whoopfake"
)

// Contract tests: the fixtures the fake serves must have the shape of WHOOP's
// documented v2 responses, or the connector is tested against answers WHOOP
// never gives.
//
// The response schemas below are transcribed from WHOOP's OpenAPI document
// (https://api.prod.whoop.com/developer/doc/openapi.json, components.schemas)
// as field names, types and required lists only. The document itself carries
// no licence, so it is not copied into the repository. To check this table
// against the published document, download it and run:
//
//	OHS_WHOOP_OPENAPI=/path/to/openapi.json go test -run Contract ./connectors/whoop/
//
// which also validates every fixture against the document's own schemas.

// apiSchema is the subset of a JSON schema the WHOOP responses use.
type apiSchema struct {
	Type     string // object, array, string, integer, number, boolean
	Format   string // date-time, uuid, int32, int64, float
	Enum     []string
	Required []string
	Props    map[string]*apiSchema
	Items    *apiSchema
	Nullable bool
}

func obj(required []string, props map[string]*apiSchema) *apiSchema {
	return &apiSchema{Type: "object", Required: required, Props: props}
}

var (
	sInt64    = &apiSchema{Type: "integer", Format: "int64"}
	sInt32    = &apiSchema{Type: "integer", Format: "int32"}
	sFloat    = &apiSchema{Type: "number", Format: "float"}
	sString   = &apiSchema{Type: "string"}
	sBool     = &apiSchema{Type: "boolean"}
	sDateTime = &apiSchema{Type: "string", Format: "date-time"}
	sUUID     = &apiSchema{Type: "string", Format: "uuid"}
	sState    = &apiSchema{Type: "string", Enum: []string{"SCORED", "PENDING_SCORE", "UNSCORABLE"}}
)

func page(item *apiSchema) *apiSchema {
	return obj(nil, map[string]*apiSchema{"records": {Type: "array", Items: item}, "next_token": sString})
}

// whoopSchemas are the v2 response schemas, by their name in the document.
var whoopSchemas = func() map[string]*apiSchema {
	m := map[string]*apiSchema{}
	m["CycleScore"] = obj([]string{"average_heart_rate", "kilojoule", "max_heart_rate", "strain"}, map[string]*apiSchema{
		"strain": sFloat, "kilojoule": sFloat, "average_heart_rate": sInt32, "max_heart_rate": sInt32,
	})
	m["Cycle"] = obj([]string{"created_at", "id", "score_state", "start", "timezone_offset", "updated_at", "user_id"}, map[string]*apiSchema{
		"id": sInt64, "user_id": sInt64, "created_at": sDateTime, "updated_at": sDateTime,
		"start": sDateTime, "end": sDateTime, "timezone_offset": sString, "score_state": sState,
		"score": m["CycleScore"], "step_count": {Type: "integer", Format: "int32", Nullable: true},
	})
	m["RecoveryScore"] = obj([]string{"hrv_rmssd_milli", "recovery_score", "resting_heart_rate", "user_calibrating"}, map[string]*apiSchema{
		"user_calibrating": sBool, "recovery_score": sFloat, "resting_heart_rate": sFloat,
		"hrv_rmssd_milli": sFloat, "spo2_percentage": sFloat, "skin_temp_celsius": sFloat,
	})
	m["Recovery"] = obj([]string{"created_at", "cycle_id", "score_state", "sleep_id", "updated_at", "user_id"}, map[string]*apiSchema{
		"cycle_id": sInt64, "sleep_id": sUUID, "user_id": sInt64, "created_at": sDateTime,
		"updated_at": sDateTime, "score_state": sState, "score": m["RecoveryScore"],
	})
	m["SleepStageSummary"] = obj([]string{"disturbance_count", "sleep_cycle_count", "total_awake_time_milli", "total_in_bed_time_milli",
		"total_light_sleep_time_milli", "total_no_data_time_milli", "total_rem_sleep_time_milli", "total_slow_wave_sleep_time_milli"},
		map[string]*apiSchema{
			"total_in_bed_time_milli": sInt32, "total_awake_time_milli": sInt32, "total_no_data_time_milli": sInt32,
			"total_light_sleep_time_milli": sInt32, "total_slow_wave_sleep_time_milli": sInt32,
			"total_rem_sleep_time_milli": sInt32, "sleep_cycle_count": sInt32, "disturbance_count": sInt32,
		})
	m["SleepNeeded"] = obj([]string{"baseline_milli", "need_from_recent_nap_milli", "need_from_recent_strain_milli", "need_from_sleep_debt_milli"},
		map[string]*apiSchema{
			"baseline_milli": sInt64, "need_from_sleep_debt_milli": sInt64,
			"need_from_recent_strain_milli": sInt64, "need_from_recent_nap_milli": sInt64,
		})
	m["SleepScore"] = obj([]string{"sleep_needed", "stage_summary"}, map[string]*apiSchema{
		"stage_summary": m["SleepStageSummary"], "sleep_needed": m["SleepNeeded"], "respiratory_rate": sFloat,
		"sleep_performance_percentage": sFloat, "sleep_consistency_percentage": sFloat, "sleep_efficiency_percentage": sFloat,
	})
	m["Sleep"] = obj([]string{"created_at", "cycle_id", "end", "id", "nap", "score_state", "start", "timezone_offset", "updated_at", "user_id"},
		map[string]*apiSchema{
			"id": sUUID, "cycle_id": sInt64, "v1_id": sInt64, "user_id": sInt64, "created_at": sDateTime,
			"updated_at": sDateTime, "start": sDateTime, "end": sDateTime, "timezone_offset": sString,
			"nap": sBool, "score_state": sState, "score": m["SleepScore"],
		})
	m["ZoneDurations"] = obj([]string{"zone_five_milli", "zone_four_milli", "zone_one_milli", "zone_three_milli", "zone_two_milli", "zone_zero_milli"},
		map[string]*apiSchema{
			"zone_zero_milli": sInt64, "zone_one_milli": sInt64, "zone_two_milli": sInt64,
			"zone_three_milli": sInt64, "zone_four_milli": sInt64, "zone_five_milli": sInt64,
		})
	m["WorkoutScore"] = obj([]string{"average_heart_rate", "kilojoule", "max_heart_rate", "percent_recorded", "strain", "zone_durations"},
		map[string]*apiSchema{
			"strain": sFloat, "average_heart_rate": sInt32, "max_heart_rate": sInt32, "kilojoule": sFloat,
			"percent_recorded": sFloat, "distance_meter": sFloat, "altitude_gain_meter": sFloat,
			"altitude_change_meter": sFloat, "zone_durations": m["ZoneDurations"],
		})
	m["WorkoutV2"] = obj([]string{"created_at", "end", "id", "score_state", "sport_name", "start", "timezone_offset", "updated_at", "user_id"},
		map[string]*apiSchema{
			"id": sUUID, "v1_id": sInt64, "user_id": sInt64, "created_at": sDateTime, "updated_at": sDateTime,
			"start": sDateTime, "end": sDateTime, "timezone_offset": sString, "sport_name": sString,
			"score_state": sState, "score": m["WorkoutScore"], "sport_id": sInt32,
		})
	m["PaginatedCycleResponse"] = page(m["Cycle"])
	m["RecoveryCollection"] = page(m["Recovery"])
	m["PaginatedSleepResponse"] = page(m["Sleep"])
	m["WorkoutCollection"] = page(m["WorkoutV2"])
	m["UserBodyMeasurement"] = obj([]string{"height_meter", "max_heart_rate", "weight_kilogram"}, map[string]*apiSchema{
		"height_meter": sFloat, "weight_kilogram": sFloat, "max_heart_rate": sInt32,
	})
	m["UserBasicProfile"] = obj([]string{"email", "first_name", "last_name", "user_id"}, map[string]*apiSchema{
		"user_id": sInt64, "email": sString, "first_name": sString, "last_name": sString,
	})
	return m
}()

// fixtureSchemas names the schema each fixture answers with.
var fixtureSchemas = map[string]string{
	"cycles.json":     "PaginatedCycleResponse",
	"recoveries.json": "RecoveryCollection",
	"sleeps.json":     "PaginatedSleepResponse",
	"workouts.json":   "WorkoutCollection",
	"body.json":       "UserBodyMeasurement",
	"profile.json":    "UserBasicProfile",
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validate checks v (decoded with UseNumber) against s and returns every
// mismatch, each prefixed with its path. A property the schema does not name
// is a mismatch too: a fixture must not invent fields.
func validate(v any, s *apiSchema, path string) []string {
	if v == nil {
		if s.Nullable {
			return nil
		}
		return []string{path + ": null where the schema does not allow it"}
	}
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, args...)) }
	switch s.Type {
	case "object":
		o, ok := v.(map[string]any)
		if !ok {
			bad("want an object, got %T", v)
			return errs
		}
		for _, r := range s.Required {
			if _, has := o[r]; !has {
				bad("required %q missing", r)
			}
		}
		keys := make([]string, 0, len(o))
		for k := range o {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, ok := s.Props[k]
			if !ok {
				bad("%q is not in the schema", k)
				continue
			}
			errs = append(errs, validate(o[k], p, path+"."+k)...)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			bad("want an array, got %T", v)
			return errs
		}
		for i, e := range a {
			errs = append(errs, validate(e, s.Items, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			bad("want a string, got %T", v)
			return errs
		}
		switch s.Format {
		case "date-time":
			if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
				bad("not a date-time: %q", str)
			}
		case "uuid":
			if !uuidPattern.MatchString(str) {
				bad("not a uuid: %q", str)
			}
		}
		if len(s.Enum) > 0 {
			found := false
			for _, e := range s.Enum {
				found = found || e == str
			}
			if !found {
				bad("%q not in %v", str, s.Enum)
			}
		}
	case "integer", "number":
		n, ok := v.(json.Number)
		if !ok {
			bad("want a %s, got %T", s.Type, v)
			return errs
		}
		f, err := n.Float64()
		if err != nil {
			bad("not a number: %s", n)
			return errs
		}
		if s.Type == "integer" {
			if _, err := n.Int64(); err != nil || f != math.Trunc(f) {
				bad("not an integer: %s", n)
			}
			if s.Format == "int32" && (f > math.MaxInt32 || f < math.MinInt32) {
				bad("out of int32: %s", n)
			}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			bad("want a boolean, got %T", v)
		}
	default:
		bad("schema type %q not handled", s.Type)
	}
	return errs
}

func decodeNumbers(t *testing.T, b []byte) any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	var v any
	require.NoError(t, d.Decode(&v))
	return v
}

// Every fixture has the shape of the documented response.
func TestContract_FixturesMatchTheResponseSchemas(t *testing.T) {
	for file, name := range fixtureSchemas {
		errs := validate(decodeNumbers(t, whoopfake.File(file)), whoopSchemas[name], file)
		assert.Empty(t, errs, "%s against %s", file, name)
	}
}

// The validator itself catches what it must.
func TestContract_ValidatorCatchesMismatches(t *testing.T) {
	cases := map[string]string{
		`{"records":[{"id":"x"}]}`: "required",
		`{"records":[{"id":1,"user_id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"DONE"}]}`:                           "not in",
		`{"records":[{"id":1.5,"user_id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"SCORED"}]}`:                       "not an integer",
		`{"records":[{"id":1,"user_id":1,"created_at":"yesterday","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"SCORED"}]}`:                                    "date-time",
		`{"records":[{"id":1,"user_id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"SCORED","strain":3}]}`:              "not in the schema",
		`{"records":[{"id":1,"user_id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"SCORED","score":null}]}`:            "null",
		`{"records":[{"id":1,"user_id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"SCORED","step_count":null}]}`:       "",
		`{"records":[{"id":1,"user_id":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","start":"2026-01-01T00:00:00Z","timezone_offset":"Z","score_state":"SCORED","step_count":3000000000}]}`: "int32",
	}
	for doc, want := range cases {
		errs := validate(decodeNumbers(t, []byte(doc)), whoopSchemas["PaginatedCycleResponse"], "doc")
		if want == "" {
			assert.Empty(t, errs, doc)
			continue
		}
		require.NotEmpty(t, errs, doc)
		assert.Contains(t, strings.Join(errs, "\n"), want, doc)
	}
	assert.NotEmpty(t, validate(decodeNumbers(t, []byte(`{"records":[{"sleep_id":"not-a-uuid"}]}`)), whoopSchemas["RecoveryCollection"], "doc"))
}

// With OHS_WHOOP_OPENAPI pointing at WHOOP's published OpenAPI document, the
// table above is compared with it and every fixture is validated against the
// document's own schemas. Skipped otherwise (no test downloads anything).
func TestContract_AgainstPublishedOpenAPI(t *testing.T) {
	p := os.Getenv("OHS_WHOOP_OPENAPI")
	if p == "" {
		t.Skip("set OHS_WHOOP_OPENAPI to WHOOP's openapi.json to run")
	}
	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	conv := &specConverter{raw: doc.Components.Schemas, done: map[string]*apiSchema{}}

	for name, ours := range whoopSchemas {
		theirs, err := conv.named(name)
		if !assert.NoError(t, err, name) {
			continue
		}
		assert.Empty(t, diffSchemas(ours, theirs, name), "the table differs from the published %s", name)
	}
	for file, name := range fixtureSchemas {
		theirs, err := conv.named(name)
		if !assert.NoError(t, err, name) {
			continue
		}
		assert.Empty(t, validate(decodeNumbers(t, whoopfake.File(file)), theirs, file), "%s against the published %s", file, name)
	}
}

// specConverter turns OpenAPI 3 schemas (with $ref) into apiSchema.
type specConverter struct {
	raw  map[string]json.RawMessage
	done map[string]*apiSchema
}

type openAPISchema struct {
	Ref        string                    `json:"$ref"`
	Type       string                    `json:"type"`
	Format     string                    `json:"format"`
	Enum       []string                  `json:"enum"`
	Required   []string                  `json:"required"`
	Properties map[string]*openAPISchema `json:"properties"`
	Items      *openAPISchema            `json:"items"`
	Nullable   bool                      `json:"nullable"`
	AllOf      []*openAPISchema          `json:"allOf"`
}

func (c *specConverter) named(name string) (*apiSchema, error) {
	if s, ok := c.done[name]; ok {
		return s, nil
	}
	r, ok := c.raw[name]
	if !ok {
		return nil, fmt.Errorf("schema %s not in the document", name)
	}
	var o openAPISchema
	if err := json.Unmarshal(r, &o); err != nil {
		return nil, err
	}
	out := &apiSchema{}
	c.done[name] = out
	s, err := c.convert(&o)
	if err != nil {
		return nil, err
	}
	*out = *s
	return out, nil
}

func (c *specConverter) convert(o *openAPISchema) (*apiSchema, error) {
	if o.Ref != "" {
		return c.named(strings.TrimPrefix(o.Ref, "#/components/schemas/"))
	}
	if len(o.AllOf) == 1 {
		s, err := c.convert(o.AllOf[0])
		if err != nil {
			return nil, err
		}
		cp := *s
		cp.Nullable = cp.Nullable || o.Nullable
		return &cp, nil
	}
	s := &apiSchema{Type: o.Type, Format: o.Format, Enum: o.Enum, Required: o.Required, Nullable: o.Nullable}
	if s.Type == "" && len(o.Properties) > 0 {
		s.Type = "object"
	}
	if len(o.Properties) > 0 {
		s.Props = map[string]*apiSchema{}
		for k, p := range o.Properties {
			ps, err := c.convert(p)
			if err != nil {
				return nil, err
			}
			s.Props[k] = ps
		}
	}
	if o.Items != nil {
		it, err := c.convert(o.Items)
		if err != nil {
			return nil, err
		}
		s.Items = it
	}
	return s, nil
}

// diffSchemas lists where two schemas disagree on types, formats, enums,
// required fields and properties.
func diffSchemas(a, b *apiSchema, path string) []string {
	var out []string
	if a.Type != b.Type || a.Format != b.Format || a.Nullable != b.Nullable {
		out = append(out, fmt.Sprintf("%s: %s/%s/null=%v vs %s/%s/null=%v", path, a.Type, a.Format, a.Nullable, b.Type, b.Format, b.Nullable))
	}
	if strings.Join(sorted(a.Enum), ",") != strings.Join(sorted(b.Enum), ",") {
		out = append(out, fmt.Sprintf("%s: enum %v vs %v", path, a.Enum, b.Enum))
	}
	if strings.Join(sorted(a.Required), ",") != strings.Join(sorted(b.Required), ",") {
		out = append(out, fmt.Sprintf("%s: required %v vs %v", path, a.Required, b.Required))
	}
	for k, pa := range a.Props {
		pb, ok := b.Props[k]
		if !ok {
			out = append(out, fmt.Sprintf("%s.%s: not in the published schema", path, k))
			continue
		}
		out = append(out, diffSchemas(pa, pb, path+"."+k)...)
	}
	for k := range b.Props {
		if _, ok := a.Props[k]; !ok {
			out = append(out, fmt.Sprintf("%s.%s: published, missing from the table", path, k))
		}
	}
	if a.Items != nil && b.Items != nil {
		out = append(out, diffSchemas(a.Items, b.Items, path+"[]")...)
	}
	return out
}

func sorted(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}
