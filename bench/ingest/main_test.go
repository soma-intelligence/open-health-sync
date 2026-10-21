// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b} {
		if err := gen([]string{"-out", dir, "-days", "2", "-hr-interval", "600"}); err != nil {
			t.Fatal(err)
		}
	}
	var m manifest
	mb, err := os.ReadFile(filepath.Join(a, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	// Per day: 144 heart rate, 288 steps, 288 energy, 24 oxygen.
	if len(m.Requests) != 8 || m.Records != 2*(144+288+288+24) {
		t.Fatalf("got %d requests, %d records", len(m.Requests), m.Records)
	}
	for _, r := range m.Requests {
		x, _ := os.ReadFile(filepath.Join(a, r.Name+".ndjson.gz"))
		y, _ := os.ReadFile(filepath.Join(b, r.Name+".ndjson.gz"))
		if !bytes.Equal(x, y) {
			t.Fatalf("%s differs between two runs with the same seed", r.Name)
		}
		zr, err := gzip.NewReader(bytes.NewReader(x))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(zr)
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		if len(lines) != r.Records {
			t.Fatalf("%s: %d lines, manifest says %d", r.Name, len(lines), r.Records)
		}
		var l line
		if err := json.Unmarshal([]byte(lines[0]), &l); err != nil || l.QuantityType != r.Type || l.Metadata["HKTimeZone"] != tzName {
			t.Fatalf("%s: bad first line %s (%v)", r.Name, lines[0], err)
		}
	}
}

func TestReportOnCommittedResults(t *testing.T) {
	var out bytes.Buffer
	if err := report([]string{"results/2026-10-11"}, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"### baseline: 28,560 records in 56 requests",
		"### dense: 250,320 records in 56 requests",
		"| Idle memory, RSS (MB) | 22.1 |",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "### baseline") > strings.Index(s, "### dense") {
		t.Error("baseline should come first")
	}
}
