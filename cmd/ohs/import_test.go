// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fixtureXML is the invented export of the converter's tests.
var fixtureXML = filepath.Join("..", "..", "connectors", "applehealth", "export", "testdata", "apple_health_export", "export.xml")

// ingestStub answers 200 to every part and remembers the request headers.
type ingestStub struct {
	mu      sync.Mutex
	batches map[string]bool
	auth    map[string]bool
	seen    map[string]bool
}

func newIngestStub(t *testing.T) (*ingestStub, *httptest.Server) {
	s := &ingestStub{batches: map[string]bool{}, auth: map[string]bool{}, seen: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path != "/v1/ingest/apple-health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ohs_ut_cli_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		key := r.Header.Get("X-OHS-Batch") + "/" + r.Header.Get("X-OHS-Part")
		dup := s.seen[key]
		s.seen[key] = true
		s.batches[r.Header.Get("X-OHS-Batch")] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "duplicate": dup})
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func TestImportAppleHealth(t *testing.T) {
	stub, srv := newIngestStub(t)
	var out bytes.Buffer
	err := run([]string{"import", "apple-health", fixtureXML, "--url", srv.URL, "--token", "ohs_ut_cli_test"}, &out)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"batch ahx-", "records read", "parts", "already stored    0", "ActivitySummary", "HKQuantityTypeIdentifierAppleWalkingSteadiness"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ohs_ut_cli_test") {
		t.Error("the token must never be printed")
	}
	// Health values never reach the output: the fixture's heart rate of 61
	// would show as "61" in a line. The server URL is left out first, since
	// its random port can contain one of these digits (127.0.0.1:41127).
	values := strings.ReplaceAll(text, srv.URL, "")
	for _, value := range []string{"64.5", "70.5", "112"} {
		if strings.Contains(values, value) {
			t.Errorf("output shows %q, a health value of the fixture:\n%s", value, text)
		}
	}
	if len(stub.batches) != 1 {
		t.Fatalf("batches %v, want one", stub.batches)
	}

	// A second run is the same batch, so all duplicates.
	out.Reset()
	if err := run([]string{"import", "apple-health", "--token", "ohs_ut_cli_test", "--url", srv.URL, fixtureXML}, &out); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(stub.batches) != 1 || strings.Contains(out.String(), "already stored    0") {
		t.Errorf("second run should reuse the batch and find every part stored:\n%s", out.String())
	}
}

func TestImportTokenFromEnvironment(t *testing.T) {
	_, srv := newIngestStub(t)
	t.Setenv("OHS_URL", srv.URL)
	t.Setenv("OHS_TOKEN", "ohs_ut_cli_test")
	if err := run([]string{"import", "apple-health", fixtureXML, "--batch", "my-batch"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestImportDryRunNeedsNoServer(t *testing.T) {
	t.Setenv("OHS_URL", "")
	t.Setenv("OHS_TOKEN", "")
	var out bytes.Buffer
	if err := run([]string{"import", "apple-health", fixtureXML, "--dry-run", "--since", "2026-01-01", "--tz", "Europe/Paris"}, &out); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), "before --since    1") {
		t.Errorf("dry run output:\n%s", out.String())
	}
}

func TestImportRefusedToken(t *testing.T) {
	_, srv := newIngestStub(t)
	err := run([]string{"import", "apple-health", fixtureXML, "--url", srv.URL, "--token", "ohs_ut_wrong"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "ohs_ut_wrong") {
		t.Fatalf("err = %v, want a 401 that does not show the token", err)
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatal("a refused token is not a usage error")
	}
}

func TestImportUsage(t *testing.T) {
	t.Setenv("OHS_URL", "")
	t.Setenv("OHS_TOKEN", "")
	cases := map[string][]string{
		"usage: ohs import":     {"import"},
		"unknown source":        {"import", "fitbit", "x.zip"},
		"one export":            {"import", "apple-health"},
		"--url":                 {"import", "apple-health", fixtureXML, "--token", "ohs_ut_x"},
		"--token":               {"import", "apple-health", fixtureXML, "--url", "https://ohs.example.org"},
		"in clear":              {"import", "apple-health", fixtureXML, "--url", "http://ohs.example.org", "--token", "ohs_ut_x"},
		"YYYY-MM-DD":            {"import", "apple-health", fixtureXML, "--dry-run", "--since", "yesterday"},
		"IANA":                  {"import", "apple-health", fixtureXML, "--dry-run", "--tz", "Nowhere"},
		"--concurrency":         {"import", "apple-health", fixtureXML, "--dry-run", "--concurrency", "0"},
		"letters, digits":       {"import", "apple-health", fixtureXML, "--dry-run", "--batch", "a b"},
		"flag provided but not": {"import", "apple-health", fixtureXML, "--nope"},
	}
	for want, args := range cases {
		err := run(args, &bytes.Buffer{})
		var ue usageError
		if err == nil || !errors.As(err, &ue) || !strings.Contains(err.Error(), want) {
			t.Errorf("run %v = %v, want a usage error containing %q", args, err, want)
		}
	}
}

func TestImportHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"import", "--help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"usage: ohs import apple-health", "--dry-run", "--since", "--tz", "--batch", "OHS_TOKEN"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help lacks %q:\n%s", want, out.String())
		}
	}
}
