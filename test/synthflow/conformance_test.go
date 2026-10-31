// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package synthflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/omh"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// shapesKept is how many Observations of one shape are written out for the
// HL7 validator: the same shape validates the same way, and the validator
// takes about a second a resource.
const shapesKept = 2

// TestEveryOutputConforms sends a clean synthetic dataset of every provider
// through the server, then reads back everything the standard outputs serve:
// every Open mHealth data point is checked here against the official Open
// mHealth schemas (internal/omh/testdata/schemas), and the FHIR resources
// (Observations, one of each shape, the Patient, a $everything page, the
// Devices, the Provenance and the CapabilityStatement) are written to
// OHS_FHIR_OUT, when it is set, for the official HL7 validator
// (scripts/fhir-validate.sh <dir>, the conformance job of ci.yml). About a
// minute and a half: it runs with OHS_CONFORMANCE=1 (make conformance), not
// in make integration.
func TestEveryOutputConforms(t *testing.T) {
	if os.Getenv("OHS_CONFORMANCE") == "" {
		t.Skip("set OHS_CONFORMANCE=1 to check every output against the official validators")
	}
	e := start(t)
	day := e.now.AddDate(0, 0, -10)
	tl := synth.Build(synth.Options{
		Seed: 5, Days: 2, Now: e.now,
		// Blood pressure twice a day, so two days hold readings.
		Persona:   synth.PersonaHypertensive,
		Start:     time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC),
		Scenarios: []synth.Scenario{synth.Baseline, synth.MultiDevice, synth.Extremes},
	})
	out := os.Getenv("OHS_FHIR_OUT")
	if out != "" {
		require.NoError(t, os.MkdirAll(out, 0o755))
	}
	schemas := omhCompiler(t)

	users := map[string]string{}
	ids := map[string]uuid.UUID{}
	for _, p := range providers {
		c, ok := connector.Get(p.slug)
		require.True(t, ok, "connector %s is registered", p.slug)
		user, token := e.user(p.slug)
		e.send(p, c, user, token, p.render(tl))
		users[p.slug], ids[p.slug] = user.String(), user
	}
	for _, p := range providers {
		e.waitProcessed(ids[p.slug])
	}

	shapes := map[string]int{}
	omhSchemas := map[string]int{}
	files := 0
	write := func(name string, raw []byte) {
		if out == "" {
			return
		}
		var pretty bytes.Buffer
		require.NoError(t, json.Indent(&pretty, raw, "", "  "))
		require.NoError(t, os.WriteFile(filepath.Join(out, name+".json"), pretty.Bytes(), 0o644))
		files++
	}
	get := func(t *testing.T, path string) []byte {
		t.Helper()
		code, b := e.do("GET", path, e.key, nil, nil)
		require.Equal(t, http.StatusOK, code, "GET %s: %s", path, b)
		return b
	}

	// The CapabilityStatement is the same for every user.
	write("capabilitystatement", get(t, "/v1/users/"+users[providers[0].slug]+"/fhir/metadata"))

	for _, p := range providers {
		u := "/v1/users/" + users[p.slug]
		t.Run(p.slug, func(t *testing.T) {
			// Every Observation, one file per new shape.
			observations, kept, first := 0, 0, ""
			next := u + "/fhir/Observation?limit=1000"
			for next != "" {
				var b fhirBundle
				require.NoError(t, json.Unmarshal(get(t, next), &b))
				require.Equal(t, "Bundle", b.ResourceType)
				for _, en := range b.Entry {
					observations++
					if first == "" {
						var id struct {
							ID string `json:"id"`
						}
						require.NoError(t, json.Unmarshal(en.Resource, &id))
						first = id.ID
					}
					key := observationShape(t, en.Resource)
					shapes[key]++
					if shapes[key] <= shapesKept {
						kept++
						write(fmt.Sprintf("observation-%s-%03d", p.slug, kept), en.Resource)
					}
				}
				next = relativeNext(t, b, u)
			}
			require.NotZero(t, observations, "the clean dataset is served as FHIR")

			write("patient-"+p.slug, get(t, u+"/fhir/Patient/"+users[p.slug]))
			write("bundle-everything-"+p.slug, get(t, u+"/fhir/Patient/"+users[p.slug]+"/$everything?limit=25"))
			write("bundle-device-"+p.slug, get(t, u+"/fhir/Device"))
			write("bundle-provenance-"+p.slug, get(t, u+"/fhir/Provenance?target=Observation/"+first))

			// Every Open mHealth data point, against the official schemas.
			points := 0
			next = u + "/omh/data-points?limit=1000"
			for next != "" {
				var page struct {
					Data       []json.RawMessage `json:"data"`
					NextCursor *string           `json:"next_cursor"`
				}
				require.NoError(t, json.Unmarshal(get(t, next), &page))
				for _, raw := range page.Data {
					points++
					if name, err := validateDataPoint(schemas, raw); err != nil {
						t.Errorf("%s data point breaks the official schema: %v\n%s", name, err, raw)
					} else {
						omhSchemas[name]++
					}
				}
				next = ""
				if page.NextCursor != nil {
					next = u + "/omh/data-points?limit=1000&cursor=" + url.QueryEscape(*page.NextCursor)
				}
			}
			t.Logf("%d FHIR Observations, %d Open mHealth data points", observations, points)
		})
	}

	names := make([]string, 0, len(omhSchemas))
	for name, n := range omhSchemas {
		names = append(names, fmt.Sprintf("%s %d", name, n))
	}
	sort.Strings(names)
	t.Logf("Open mHealth data points valid, by schema: %s", strings.Join(names, ", "))
	t.Logf("%d FHIR Observation shapes; %d files written for the HL7 validator", len(shapes), files)
	// Every schema the mapping emits is exercised, so a schema nobody sends
	// cannot stay broken unseen.
	for _, sc := range omh.Schemas() {
		assert.NotZero(t, omhSchemas[sc.Name+" "+sc.Version], "no data point of %s %s in the run", sc.Name, sc.Version)
	}
}

type fhirBundle struct {
	ResourceType string `json:"resourceType"`
	Link         []struct {
		Relation string `json:"relation"`
		URL      string `json:"url"`
	} `json:"link"`
	Entry []struct {
		Resource json.RawMessage `json:"resource"`
	} `json:"entry"`
}

// relativeNext is the bundle's next link as a path under u, empty on the
// last page.
func relativeNext(t *testing.T, b fhirBundle, u string) string {
	t.Helper()
	for _, l := range b.Link {
		if l.Relation != "next" {
			continue
		}
		parsed, err := url.Parse(l.URL)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(parsed.Path, u), "next link %s", l.URL)
		return parsed.RequestURI()
	}
	return ""
}

// observationShape is what makes two Observations validate differently:
// their codes, categories, profiles, value type, unit, components and
// effective time kind. Two with the same shape differ only in values.
func observationShape(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var o map[string]any
	require.NoError(t, json.Unmarshal(raw, &o))
	require.Equal(t, "Observation", o["resourceType"])
	codes := func(cc any) string {
		m, _ := cc.(map[string]any)
		list, _ := m["coding"].([]any)
		var out []string
		for _, c := range list {
			cm, _ := c.(map[string]any)
			out = append(out, fmt.Sprint(cm["system"], "|", cm["code"]))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	var parts []string
	parts = append(parts, codes(o["code"]))
	cats, _ := o["category"].([]any)
	for _, c := range cats {
		parts = append(parts, "cat:"+codes(c))
	}
	if meta, ok := o["meta"].(map[string]any); ok {
		parts = append(parts, fmt.Sprint("profile:", meta["profile"]))
	}
	var keys []string
	for k, v := range o {
		switch {
		case strings.HasPrefix(k, "value"), strings.HasPrefix(k, "effective"):
			keys = append(keys, k)
			if q, ok := v.(map[string]any); ok && k == "valueQuantity" {
				keys = append(keys, fmt.Sprint("unit:", q["code"]))
			}
		case k == "component", k == "derivedFrom", k == "hasMember", k == "device", k == "dataAbsentReason", k == "note":
			keys = append(keys, k)
		}
	}
	comps, _ := o["component"].([]any)
	for _, c := range comps {
		cm, _ := c.(map[string]any)
		var vk []string
		for k := range cm {
			if strings.HasPrefix(k, "value") {
				vk = append(vk, k)
			}
		}
		sort.Strings(vk)
		keys = append(keys, "comp:"+codes(cm["code"])+":"+strings.Join(vk, ","))
	}
	sort.Strings(keys)
	return strings.Join(append(parts, keys...), " ")
}

// omhSchemaBase is the URI the Open mHealth repository publishes its schemas
// under, so their relative references resolve among themselves.
const omhSchemaBase = "https://w3id.org/openmhealth/schemas/omh/"

// omhCompiler loads the official Open mHealth schemas vendored in
// internal/omh/testdata/schemas, the way internal/omh's own tests do.
func omhCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft4)
	c.AssertFormat()
	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "omh", "testdata", "schemas", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		require.NoError(t, err, f)
		if m, ok := doc.(map[string]any); ok {
			// A "-N.x" copy keeps the $id of the version it is; the file
			// name is its address here.
			delete(m, "$id")
		}
		require.NoError(t, c.AddResource(omhSchemaBase+filepath.Base(f), doc), f)
	}
	return c
}

// validateDataPoint checks a data point against omh:data-point:1.0 (its
// header included) and its body against the schema its header names, and
// returns that schema as "name version".
func validateDataPoint(c *jsonschema.Compiler, raw []byte) (string, error) {
	var d omh.DataPoint
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", err
	}
	name := d.Header.SchemaID.Name + " " + d.Header.SchemaID.Version
	if d.Header.SchemaID.Namespace != omh.Namespace {
		return name, fmt.Errorf("namespace %q", d.Header.SchemaID.Namespace)
	}
	check := func(schema string, doc []byte) error {
		s, err := c.Compile(omhSchemaBase + schema + ".json")
		if err != nil {
			return err
		}
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
		if err != nil {
			return err
		}
		return s.Validate(v)
	}
	if err := check("data-point-1.0", raw); err != nil {
		return name, err
	}
	body, err := json.Marshal(d.Body)
	if err != nil {
		return name, err
	}
	return name, check(d.Header.SchemaID.Name+"-"+d.Header.SchemaID.Version, body)
}
