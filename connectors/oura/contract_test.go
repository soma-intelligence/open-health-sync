// SPDX-License-Identifier: AGPL-3.0-only

package oura

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/connectors/oura/ourafake"
)

// Contract tests: the fixtures, the fake and the connector's requests checked
// against the response schemas of Oura's OpenAPI document.
//
// testdata/responses.schema.json holds the structure (types, required
// fields, enums, nullability) of the responses this connector reads, the
// OAuth scopes and the webhook data types, derived from Oura's OpenAPI
// document (openapi-1.37). Oura publishes that document without a licence, so
// only this derived structure is kept here. To check against a newer one,
// download it (https://api.ouraring.com/v2/static/json/openapi-<version>.json)
// and run
//
//	go test ./connectors/oura/ -run TestContract -oura-openapi=/path/to/openapi.json -oura-openapi-version=openapi-1.38
//
// which rewrites the derived file before the checks run; review its diff.

var (
	openAPIPath    = flag.String("oura-openapi", "", "Oura OpenAPI document to derive testdata/responses.schema.json from")
	openAPIVersion = flag.String("oura-openapi-version", "", "name of that document's version, for the derived file's header")
)

const schemaFile = "testdata/responses.schema.json"

// schemaDoc is testdata/responses.schema.json.
type schemaDoc struct {
	DerivedFrom string `json:"x-derived-from"`
	OAuth       struct {
		AuthorizationURL string   `json:"authorizationUrl"`
		TokenURL         string   `json:"tokenUrl"`
		Scopes           []string `json:"scopes"`
	} `json:"x-oauth"`
	Endpoints map[string]struct {
		Path   string   `json:"path"`
		Schema string   `json:"schema"`
		Query  []string `json:"query"`
	} `json:"x-endpoints"`
	Components struct {
		Schemas map[string]any `json:"schemas"`
	} `json:"components"`
}

func loadSchemaDoc(t *testing.T) *schemaDoc {
	t.Helper()
	if *openAPIPath != "" {
		deriveSchemaDoc(t, *openAPIPath, *openAPIVersion)
	}
	b, err := os.ReadFile(schemaFile)
	require.NoError(t, err)
	var d schemaDoc
	require.NoError(t, json.Unmarshal(b, &d))
	require.NotEmpty(t, d.Components.Schemas)
	return &d
}

// --- the schema walker ---

// validator checks a decoded JSON value (json.Number for numbers) against an
// OpenAPI 3.1 schema: $ref, anyOf, oneOf, allOf, type, enum, required,
// properties, items, additionalProperties and the formats date, date-time and
// uuid4. Objects are strict: a property the schema does not declare is an
// error, so an invented field name in a fixture shows.
type validator struct {
	schemas map[string]any
	errs    []string
}

func (v *validator) errorf(path, format string, args ...any) {
	v.errs = append(v.errs, path+": "+fmt.Sprintf(format, args...))
}

func (v *validator) sub() *validator { return &validator{schemas: v.schemas} }

// named formats of Oura's string aliases.
var namedFormats = map[string]string{
	"ISODate":           "date",
	"LocalizedDateTime": "date-time",
	"UtcDateTime":       "utc-date-time",
}

func (v *validator) check(schema any, val any, path string) {
	s, ok := schema.(map[string]any)
	if !ok {
		v.errorf(path, "schema is not an object")
		return
	}
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		target, ok := v.schemas[name]
		if !ok {
			v.errorf(path, "unknown schema %s", name)
			return
		}
		if f, ok := namedFormats[name]; ok {
			v.checkFormat(f, val, path)
		}
		v.check(target, val, path)
		return
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if alts, ok := s[key].([]any); ok {
			matched := 0
			var firstErrs []string
			for _, alt := range alts {
				sv := v.sub()
				sv.check(alt, val, path)
				if len(sv.errs) == 0 {
					matched++
				} else if firstErrs == nil {
					firstErrs = sv.errs
				}
			}
			if matched == 0 {
				v.errorf(path, "matches none of %s (first: %s)", key, strings.Join(firstErrs, "; "))
			}
			return
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		for _, sub := range all {
			v.check(sub, val, path)
		}
	}
	if typ, ok := s["type"]; ok && !typeMatches(typ, val) {
		v.errorf(path, "%s is not of type %v", describe(val), typ)
		return
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(val) {
				found = true
			}
		}
		if !found {
			v.errorf(path, "%v is not one of %v", val, enum)
		}
	}
	if f, ok := s["format"].(string); ok {
		v.checkFormat(f, val, path)
	}
	switch x := val.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			if _, ok := x[r.(string)]; !ok {
				v.errorf(path, "missing required %s", r)
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k]; ok {
				v.check(ps, x[k], path+"."+k)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case map[string]any:
				v.check(ap, x[k], path+"."+k)
			case bool:
				if !ap {
					v.errorf(path, "unknown property %s", k)
				}
			default:
				if props != nil || s["type"] == "object" {
					v.errorf(path, "property %s is not in the schema", k)
				}
			}
		}
	case []any:
		if items, ok := s["items"]; ok {
			for i, it := range x {
				v.check(items, it, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
}

func (v *validator) checkFormat(format string, val any, path string) {
	str, ok := val.(string)
	if !ok {
		return
	}
	var err error
	switch format {
	case "date":
		_, err = time.Parse(time.DateOnly, str)
	case "date-time":
		_, err = time.Parse(time.RFC3339Nano, str)
	case "utc-date-time":
		var ts time.Time
		ts, err = time.Parse(time.RFC3339Nano, str)
		if err == nil {
			if _, off := ts.Zone(); off != 0 {
				err = fmt.Errorf("offset is not UTC")
			}
		}
	case "uuid4":
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(str) {
			err = fmt.Errorf("not a UUID v4")
		}
	}
	if err != nil {
		v.errorf(path, "%q is not a %s: %v", str, format, err)
	}
}

func typeMatches(typ any, val any) bool {
	if list, ok := typ.([]any); ok {
		for _, t := range list {
			if typeMatches(t, val) {
				return true
			}
		}
		return false
	}
	switch typ {
	case "null":
		return val == nil
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "number":
		_, ok := val.(json.Number)
		return ok
	case "integer":
		n, ok := val.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	}
	return false
}

func describe(val any) string {
	switch val.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	}
	return fmt.Sprintf("%v", val)
}

// decodeNumbers decodes b keeping numbers as json.Number.
func decodeNumbers(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return v
}

func validate(d *schemaDoc, schemaRef string, val any) []string {
	v := &validator{schemas: d.Components.Schemas}
	v.check(map[string]any{"$ref": schemaRef}, val, "$")
	return v.errs
}

// The walker itself: it must catch what it is for.
func TestContract_WalkerCatchesMismatches(t *testing.T) {
	d := loadSchemaDoc(t)
	ref := d.Endpoints[DataTypeHeartRate].Schema
	ok := `{"data":[{"bpm":60,"source":"awake","timestamp":"2026-03-01T10:00:00+00:00","timestamp_unix":1772359200000}],"next_token":null}`
	assert.Empty(t, validate(d, ref, decodeNumbers(t, []byte(ok))))
	for name, bad := range map[string]string{
		"missing required":   `{"data":[{"bpm":60,"source":"awake","timestamp":"2026-03-01T10:00:00+00:00"}]}`,
		"unknown property":   `{"data":[{"bpm":60,"beats":1,"source":"awake","timestamp":"2026-03-01T10:00:00+00:00","timestamp_unix":1}]}`,
		"enum":               `{"data":[{"bpm":60,"source":"jogging","timestamp":"2026-03-01T10:00:00+00:00","timestamp_unix":1}]}`,
		"integer":            `{"data":[{"bpm":60.5,"source":"awake","timestamp":"2026-03-01T10:00:00+00:00","timestamp_unix":1}]}`,
		"utc":                `{"data":[{"bpm":60,"source":"awake","timestamp":"2026-03-01T10:00:00+01:00","timestamp_unix":1}]}`,
		"not a date-time":    `{"data":[{"bpm":60,"source":"awake","timestamp":"yesterday","timestamp_unix":1}]}`,
		"null where not":     `{"data":[{"bpm":null,"source":"awake","timestamp":"2026-03-01T10:00:00+00:00","timestamp_unix":1}]}`,
		"data not an array":  `{"data":{}}`,
		"missing data":       `{"next_token":null}`,
		"next_token integer": `{"data":[],"next_token":2}`,
	} {
		assert.NotEmpty(t, validate(d, ref, decodeNumbers(t, []byte(bad))), name)
	}
	day := d.Endpoints[DataTypeDailyStress].Schema
	assert.NotEmpty(t, validate(d, day, decodeNumbers(t, []byte(`{"data":[{"id":"x","day":"2026-3-1"}],"next_token":null}`))), "ISODate")
}

// fixtureFor is the ourafake testdata file of an endpoint's data type.
func fixtureFor(dataType string) string { return dataType + ".json" }

// Every recorded answer the fake serves matches Oura's response schema.
func TestContract_FixturesMatchTheSpec(t *testing.T) {
	d := loadSchemaDoc(t)
	for _, dt := range append(append([]string{}, AllDataTypes...), "personal_info") {
		ep, ok := d.Endpoints[dt]
		require.True(t, ok, dt)
		val := decodeNumbers(t, ourafake.File(fixtureFor(dt)))
		assert.Empty(t, validate(d, ep.Schema, val), "ourafake/testdata/%s", fixtureFor(dt))
	}
	// The connector's own fixtures: one page per file, named <data type>.<case>.json.
	files, err := filepath.Glob("testdata/pages/*.json")
	require.NoError(t, err)
	for _, f := range files {
		dt := strings.SplitN(filepath.Base(f), ".", 2)[0]
		ep, ok := d.Endpoints[dt]
		require.True(t, ok, f)
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.Empty(t, validate(d, ep.Schema, decodeNumbers(t, b)), f)
	}
}

// What the fake answers on the wire (its pages, its next_token) matches the
// schema too, page after page.
func TestContract_FakeAnswersMatchTheSpec(t *testing.T) {
	d := loadSchemaDoc(t)
	f := newFakeOura(t)
	f.PageSize = 1
	c := f.Client()
	for _, dt := range AllDataTypes {
		ep := d.Endpoints[dt]
		q := "start_date=2026-02-01&end_date=2026-04-01"
		if dt == DataTypeHeartRate {
			q = "start_datetime=2026-02-01T00:00:00Z&end_datetime=2026-04-01T00:00:00Z"
		}
		next := ""
		for page := 0; ; page++ {
			require.Less(t, page, 20, dt)
			u := f.URL() + ep.Path + "?" + q
			if next != "" {
				u += "&next_token=" + next
			}
			req, _ := http.NewRequest(http.MethodGet, u, nil)
			req.Header.Set("Authorization", "Bearer x")
			res, err := c.Do(req)
			require.NoError(t, err)
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			require.Equal(t, http.StatusOK, res.StatusCode, dt)
			val := decodeNumbers(t, b)
			assert.Empty(t, validate(d, ep.Schema, val), "%s page %d", dt, page)
			nt, _ := val.(map[string]any)["next_token"].(string)
			if nt == "" {
				break
			}
			next = nt
		}
	}
}

// The connector asks for what the spec offers: its scopes, its endpoint
// paths and query parameters, and the enum values it acts on.
func TestContract_ConnectorMatchesTheSpec(t *testing.T) {
	d := loadSchemaDoc(t)
	for _, s := range Scopes {
		assert.Contains(t, d.OAuth.Scopes, s, "scope %s", s)
	}
	assert.Equal(t, DefaultAccountBase+"/oauth/authorize", d.OAuth.AuthorizationURL)
	assert.Equal(t, DefaultAPIBase+"/oauth/token", d.OAuth.TokenURL)

	for _, dt := range AllDataTypes {
		ep, ok := d.Endpoints[dt]
		require.True(t, ok, dt)
		assert.Equal(t, ep.Path, collectionPath+endpoint(dt), dt)
		for _, w := range windows(dt, fetchFrom, fetchTo) {
			for k := range w {
				assert.Contains(t, ep.Query, k, "%s query parameter %s", dt, k)
			}
		}
		assert.Contains(t, ep.Query, "next_token", dt)
	}

	enum := func(name string) []string {
		s, ok := d.Components.Schemas[name].(map[string]any)
		require.True(t, ok, name)
		var out []string
		for _, e := range s["enum"].([]any) {
			out = append(out, e.(string))
		}
		return out
	}
	webhookTypes := enum("ExtApiV2DataType")
	for dt := range deletableTypes {
		assert.Contains(t, webhookTypes, dt, "deletable webhook type")
	}
	for dt := range pulledTypes {
		if dt == DataTypeHeartRate {
			assert.NotContains(t, webhookTypes, dt, "heart rate has no webhook")
			continue
		}
		assert.Contains(t, webhookTypes, dt, "webhook data type")
	}
	ops := enum("WebhookOperation")
	assert.ElementsMatch(t, []string{EventCreate, EventUpdate, EventDelete}, ops)

	sleepTypes := enum("PublicSleepType")
	for _, typ := range []string{sleepTypeDeleted, sleepTypeRest, sleepTypeLongSleep} {
		assert.Contains(t, sleepTypes, typ)
	}
	moments := enum("PublicMomentType")
	for typ := range mindfulSessionTypes {
		assert.Contains(t, moments, typ)
	}
	sources := enum("PublicHeartRateSource")
	assert.ElementsMatch(t, []string{"awake", "rest", "sleep", "session", "live", "workout"}, sources)
}

// --- deriving testdata/responses.schema.json from the full document ---

// derivedKeys are the schema keywords kept: structure only.
var derivedKeys = map[string]bool{
	"type": true, "properties": true, "required": true, "items": true, "anyOf": true,
	"oneOf": true, "allOf": true, "enum": true, "$ref": true, "format": true, "additionalProperties": true,
}

func deriveSchemaDoc(t *testing.T, path, version string) {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var spec struct {
		Info       struct{ Version string }  `json:"info"`
		Paths      map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas         map[string]any `json:"schemas"`
			SecuritySchemes struct {
				OAuth2 struct {
					Flows struct {
						AuthorizationCode struct {
							AuthorizationURL string            `json:"authorizationUrl"`
							TokenURL         string            `json:"tokenUrl"`
							Scopes           map[string]string `json:"scopes"`
						} `json:"authorizationCode"`
					} `json:"flows"`
				} `json:"OAuth2"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(b, &spec))
	out := map[string]any{}
	var collect func(name string)
	var strip func(v any) any
	strip = func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		r := map[string]any{}
		for k, x := range m {
			if !derivedKeys[k] {
				continue
			}
			switch k {
			case "properties":
				ps := map[string]any{}
				for pk, pv := range x.(map[string]any) {
					ps[pk] = strip(pv)
				}
				r[k] = ps
			case "anyOf", "oneOf", "allOf":
				var alts []any
				for _, a := range x.([]any) {
					alts = append(alts, strip(a))
				}
				r[k] = alts
			case "items", "additionalProperties":
				r[k] = strip(x)
			case "$ref":
				collect(strings.TrimPrefix(x.(string), "#/components/schemas/"))
				r[k] = x
			default:
				r[k] = x
			}
		}
		return r
	}
	collect = func(name string) {
		if _, done := out[name]; done {
			return
		}
		src, ok := spec.Components.Schemas[name]
		require.True(t, ok, name)
		out[name] = nil
		out[name] = strip(src)
	}
	paths := map[string]string{"personal_info": "/v2/usercollection/personal_info"}
	for _, dt := range AllDataTypes {
		paths[dt] = collectionPath + endpoint(dt)
	}
	endpoints := map[string]any{}
	for dt, p := range paths {
		op, ok := spec.Paths[p]["get"].(map[string]any)
		require.True(t, ok, p)
		sch := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if alts, ok := sch["anyOf"].([]any); ok {
			sch = alts[0].(map[string]any) // the typed model; the other is the "fields" answer
		}
		ref := sch["$ref"].(string)
		collect(strings.TrimPrefix(ref, "#/components/schemas/"))
		var query []string
		params, _ := op["parameters"].([]any)
		for _, prm := range params {
			query = append(query, prm.(map[string]any)["name"].(string))
		}
		sort.Strings(query)
		endpoints[dt] = map[string]any{"path": p, "schema": ref, "query": query}
	}
	collect("CreateWebhookSubscriptionRequest")
	collect("WebhookSubscriptionModel")
	ac := spec.Components.SecuritySchemes.OAuth2.Flows.AuthorizationCode
	var scopes []string
	for s := range ac.Scopes {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	doc := map[string]any{
		"x-derived-from": fmt.Sprintf("Oura API v2 OpenAPI document %s (info.version %s): response structure only", version, spec.Info.Version),
		"x-oauth":        map[string]any{"authorizationUrl": ac.AuthorizationURL, "tokenUrl": ac.TokenURL, "scopes": scopes},
		"x-endpoints":    endpoints,
		"components":     map[string]any{"schemas": out},
	}
	enc, err := json.MarshalIndent(doc, "", " ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(schemaFile, append(enc, '\n'), 0o644))
}
