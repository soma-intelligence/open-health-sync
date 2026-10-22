// SPDX-License-Identifier: AGPL-3.0-only

package applehealth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	ahc "github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/applehealth/export"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/applehealth"
	"github.com/soma-intelligence/open-health-sync/internal/synth/synthtest"
)

func scenarios(t *testing.T) []synth.Scenario {
	var out []synth.Scenario
	for _, s := range synth.Scenarios {
		if s == synth.Huge && testing.Short() {
			continue
		}
		out = append(out, s)
	}
	return out
}

func days(s synth.Scenario) int {
	if s == synth.Huge {
		return 2
	}
	return 14
}

// TestUploadLines runs the SDK upload lines of every scenario through the
// worker's steps.
func TestUploadLines(t *testing.T) {
	for _, s := range scenarios(t) {
		t.Run(string(s), func(t *testing.T) {
			t.Parallel()
			tl := synth.Build(synth.Options{Seed: 7, Scenarios: []synth.Scenario{s}, Days: days(s)})
			lines := applehealth.Lines(tl)
			if len(lines) == 0 {
				t.Fatal("no lines")
			}
			res := synthtest.Run(ahc.New(), lines, tl.Opt.Now)
			synthtest.Check(t, applehealth.Slug, res, tl.Opt.Now, 20)
			t.Logf("%d lines, outcomes %v", len(lines), synthtest.Summary(res))
		})
	}
}

// TestPersonas covers every invented person on a clean dataset.
func TestPersonas(t *testing.T) {
	for _, p := range synth.Personas {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			tl := synth.Build(synth.Options{Seed: 3, Persona: p, Scenarios: []synth.Scenario{synth.DST, synth.Travel, synth.MultiDevice}})
			res := synthtest.Run(ahc.New(), applehealth.Lines(tl), tl.Opt.Now)
			synthtest.Check(t, applehealth.Slug, res, tl.Opt.Now, 20)
		})
	}
}

// TestParseUpload checks the upload gate: a part of documented lines passes,
// a part holding one malformed line is refused whole, and neither panics.
func TestParseUpload(t *testing.T) {
	tl := synth.Build(synth.Options{Seed: 7, Scenarios: []synth.Scenario{synth.All}})
	parts := map[string][2]*bytes.Buffer{}
	for _, l := range applehealth.Lines(tl) {
		p, ok := parts[l.DataType]
		if !ok {
			p = [2]*bytes.Buffer{{}, {}}
			parts[l.DataType] = p
		}
		i := 0
		if l.Fault == synth.FaultMalformed {
			i = 1
		}
		p[i].Write(l.JSON)
		p[i].WriteByte('\n')
	}
	c := ahc.New()
	for typ, p := range parts {
		if p[0].Len() > 0 {
			if err := c.ParseUpload(connector.UploadMeta{}, bytes.NewReader(p[0].Bytes()), nil); err != nil {
				t.Errorf("%s: documented part refused: %v", typ, err)
			}
		}
		if p[1].Len() > 0 {
			body := append(append([]byte(nil), p[0].Bytes()...), p[1].Bytes()...)
			if err := c.ParseUpload(connector.UploadMeta{}, bytes.NewReader(body), nil); err == nil {
				t.Errorf("%s: part with malformed lines accepted", typ)
			}
		}
	}
}

func writeExport(t *testing.T, tl *synth.Timeline, o applehealth.ExportOptions) (string, applehealth.ExportStats) {
	t.Helper()
	var buf bytes.Buffer
	st, err := applehealth.WriteExport(&buf, tl, o)
	if err != nil {
		t.Fatalf("write export: %v", err)
	}
	name := filepath.Join(t.TempDir(), "export.zip")
	if err := os.WriteFile(name, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return name, st
}

// convert opens and converts an archive and returns the part lines, each
// marked with fault so a faulty archive's lines may be refused.
func convert(t *testing.T, name string, fault synth.Fault) ([]synth.Line, export.Stats, error) {
	t.Helper()
	e, err := export.Open(name)
	if err != nil {
		return nil, export.Stats{}, err
	}
	defer e.Close()
	var lines []synth.Line
	st, err := export.Convert(context.Background(), e, export.Options{}, func(p export.Part) error {
		for _, l := range bytes.Split(bytes.TrimRight(p.NDJSON, "\n"), []byte("\n")) {
			lines = append(lines, synth.Line{DataType: p.Type, JSON: append([]byte(nil), l...), Fault: fault, Tag: p.Day})
		}
		return nil
	})
	return lines, st, err
}

// TestExportArchive writes a full export.zip, converts it with the CLI's
// converter and runs every part through the worker's steps.
func TestExportArchive(t *testing.T) {
	for _, units := range []string{"metric", "imperial"} {
		t.Run(units, func(t *testing.T) {
			t.Parallel()
			tl := synth.Build(synth.Options{Seed: 11, Scenarios: []synth.Scenario{synth.DST, synth.Travel, synth.MultiDevice, synth.Extremes, synth.Gaps}})
			o := applehealth.ExportOptions{}
			if units == "imperial" {
				o.Units = applehealth.ImperialUnits
			}
			name, ws := writeExport(t, tl, o)
			lines, cs, err := convert(t, name, synth.FaultNone)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if len(cs.Invalid) > 0 {
				t.Errorf("converter found invalid records in a documented export: %v", cs.Invalid)
			}
			if ws.Records == 0 || len(lines) == 0 {
				t.Fatalf("empty export: wrote %+v, converted %d lines", ws, len(lines))
			}
			res := synthtest.Run(ahc.New(), lines, tl.Opt.Now)
			synthtest.Check(t, applehealth.Slug, res, tl.Opt.Now, 20)
			t.Logf("wrote %d records, %d workouts, %d ECGs; %d parts, %d lines, skipped %v; outcomes %v",
				ws.Records, ws.Workouts, ws.ECGs, cs.Parts, len(lines), cs.Skipped, synthtest.Summary(res))
		})
	}
}

// TestExportFaults puts each archive defect in and checks the converter
// either refuses the archive with an error or yields lines that break no
// invariant.
func TestExportFaults(t *testing.T) {
	for _, f := range applehealth.ExportFaults {
		t.Run(string(f), func(t *testing.T) {
			t.Parallel()
			tl := synth.Build(synth.Options{Seed: 5, Days: 4})
			name, _ := writeExport(t, tl, applehealth.ExportOptions{Faults: []applehealth.ExportFault{f}})
			lines, cs, err := func() (l []synth.Line, s export.Stats, err error) {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("converter panicked: %v", p)
					}
				}()
				return convert(t, name, synth.Fault("export:"+string(f)))
			}()
			if f == applehealth.ExportNoXML {
				if !errors.Is(err, export.ErrNoExportXML) {
					t.Errorf("archive without export.xml: got %v, want ErrNoExportXML", err)
				}
				return
			}
			res := synthtest.Run(ahc.New(), lines, tl.Opt.Now)
			synthtest.Check(t, applehealth.Slug, res, tl.Opt.Now, 20)
			t.Logf("err %v; %d parts, invalid %v; outcomes %v", err, cs.Parts, cs.Invalid, synthtest.Summary(res))
		})
	}
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestDeterministic checks a seed gives the same bytes twice and another seed
// different ones.
func TestDeterministic(t *testing.T) {
	gen := func(seed uint64) (string, string) {
		tl := synth.Build(synth.Options{Seed: seed, Days: 5, Scenarios: []synth.Scenario{synth.All}})
		h := sha256.New()
		for _, l := range applehealth.Lines(tl) {
			h.Write(l.JSON)
			h.Write([]byte{'\n'})
		}
		var buf bytes.Buffer
		if _, err := applehealth.WriteExport(&buf, tl, applehealth.ExportOptions{}); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(h.Sum(nil)), digest(buf.Bytes())
	}
	a1, z1 := gen(1)
	a2, z2 := gen(1)
	b1, y1 := gen(2)
	if a1 != a2 || z1 != z2 {
		t.Errorf("same seed, different output: lines %s/%s, zip %s/%s", a1, a2, z1, z2)
	}
	if a1 == b1 || z1 == y1 {
		t.Error("different seeds, same output")
	}
}

// TestMain silences the connector's skip warnings: the unknown types are
// counted in the outcomes instead.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
