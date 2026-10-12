// SPDX-License-Identifier: AGPL-3.0-only

package omh

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// schemaBase is where the vendored schemas are registered: the URI the Open
// mHealth repository publishes them under, so their relative references
// resolve among themselves.
const schemaBase = "https://w3id.org/openmhealth/schemas/omh/"

// compiler loads every schema under testdata/schemas: the official Open
// mHealth schemas (Apache-2.0, see testdata/schemas/README.md), each "-N.x"
// file being a copy of the version it resolves to.
func compiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft4)
	c.AssertFormat()
	files, err := filepath.Glob(filepath.Join("testdata", "schemas", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		require.NoError(t, err, f)
		if m, ok := doc.(map[string]any); ok {
			// A copy registered under its "-N.x" name keeps the $id of the
			// version it is; the file name is its address here.
			delete(m, "$id")
		}
		require.NoError(t, c.AddResource(schemaBase+filepath.Base(f), doc), f)
	}
	return c
}

func compile(t *testing.T, c *jsonschema.Compiler, name, version string) *jsonschema.Schema {
	t.Helper()
	s, err := c.Compile(schemaBase + name + "-" + version + ".json")
	require.NoError(t, err, "%s %s", name, version)
	return s
}

func instance(t *testing.T, raw []byte) any {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	return v
}

// validate checks a data point against omh:data-point:1.0 (header
// included) and its body against the schema its header names.
func validate(t *testing.T, c *jsonschema.Compiler, raw []byte) {
	t.Helper()
	require.NoError(t, compile(t, c, "data-point", "1.0").Validate(instance(t, raw)), "%s", raw)
	var d DataPoint
	require.NoError(t, json.Unmarshal(raw, &d))
	require.Equal(t, Namespace, d.Header.SchemaID.Namespace)
	body, err := json.Marshal(d.Body)
	require.NoError(t, err)
	s := compile(t, c, d.Header.SchemaID.Name, d.Header.SchemaID.Version)
	require.NoError(t, s.Validate(instance(t, body)), "%s %s: %s", d.Header.SchemaID.Name, d.Header.SchemaID.Version, body)
}

// TestOfficialExamples runs the repository's own test data (shouldPass and
// shouldFail) for every schema the mapping emits, so a mistake in how the
// schemas are loaded cannot pass every data point unnoticed.
func TestOfficialExamples(t *testing.T) {
	c := compiler(t)
	dirs, err := filepath.Glob(filepath.Join("testdata", "official", "*", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, dirs)
	seen := map[string]bool{}
	for _, dir := range dirs {
		name, version := filepath.Base(filepath.Dir(dir)), filepath.Base(dir)
		seen[name+":"+version] = true
		s := compile(t, c, name, version)
		for _, want := range []string{"shouldPass", "shouldFail"} {
			files, err := filepath.Glob(filepath.Join(dir, want, "*.json"))
			require.NoError(t, err)
			for _, f := range files {
				raw, err := os.ReadFile(f)
				require.NoError(t, err)
				err = s.Validate(instance(t, raw))
				if want == "shouldPass" {
					require.NoError(t, err, f)
				} else {
					require.Error(t, err, f)
				}
			}
		}
	}
	for _, sc := range append(Schemas(), Schema{"data-point", "1.0"}) {
		if !seen[sc.Name+":"+sc.Version] {
			t.Logf("no official test data for %s %s", sc.Name, sc.Version)
		}
	}
}

// TestEverySchemaCompiles: each schema the mapping names is vendored, with
// every definition it refers to.
func TestEverySchemaCompiles(t *testing.T) {
	c := compiler(t)
	for _, s := range Schemas() {
		compile(t, c, s.Name, s.Version)
	}
	compile(t, c, "header", "1.2")
}

// TestGoldenFilesValidate checks every golden data point against the
// official schemas.
func TestGoldenFilesValidate(t *testing.T) {
	c := compiler(t)
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	covered := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		validate(t, c, raw)
		var d DataPoint
		require.NoError(t, json.Unmarshal(raw, &d))
		covered[d.Header.SchemaID.Name] = true
	}
	for _, s := range Schemas() {
		require.True(t, covered[s.Name], "no golden data point for %s", s.Name)
	}
}
