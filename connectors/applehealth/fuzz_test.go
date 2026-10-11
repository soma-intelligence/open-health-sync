// SPDX-License-Identifier: AGPL-3.0-only

package applehealth

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// FuzzParseUpload feeds arbitrary bytes down the path an upload really takes:
// gzip decompression, then ParseUpload, then Map on every line it accepted.
// Nothing here may panic, and no ParseUpload error may quote the data: its
// message is handed straight to the uploading client and the request log by
// internal/collect/upload.go's validate(), and an upload body is health data.
//
// The corpus is seeded with the NDJSON fixtures (gzipped, like the SDK sends)
// so the fuzzer starts from input the parser accepts and mutates outward,
// rather than spending its budget rediscovering "{".
func FuzzParseUpload(f *testing.F) {
	for _, seed := range seedCorpus(f) {
		f.Add(seed)
	}
	// A few shapes the fixtures do not cover: not gzip at all, empty,
	// truncated gzip, and gzip holding bad NDJSON.
	f.Add([]byte{})
	f.Add([]byte("{\"quantityType\":\"HKQuantityTypeIdentifierStepCount\"}\n"))
	f.Add(gzipBytes(f, []byte("not json\n")))
	f.Add(gzipBytes(f, []byte("\n\n\n")))
	f.Add(gzipBytes(f, []byte("[1,2,3]\n")))
	f.Add(gzipBytes(f, []byte("{\"a\":1}\n{\"b\":\n")))
	if truncated := gzipBytes(f, []byte("{\"a\":1}\n")); len(truncated) > 4 {
		f.Add(truncated[:len(truncated)-4])
	}

	c := New()
	f.Fuzz(func(t *testing.T, body []byte) {
		var r io.Reader = bytes.NewReader(body)
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			defer func() { _ = zr.Close() }()
			r = zr
		}

		// Tee the decompressed bytes so Map can be fuzzed on the same
		// lines ParseUpload just accepted.
		var seen bytes.Buffer
		err := c.ParseUpload(connector.UploadMeta{}, io.TeeReader(r, &seen), nil)
		if err != nil {
			requireNoDataInMessage(t, err.Error(), seen.Bytes())
			return
		}

		for _, line := range bytes.Split(seen.Bytes(), []byte("\n")) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var fields map[string]any
			if jsonErr := decodeLine(line, &fields); jsonErr != nil {
				continue
			}
			// Map errors are not checked for quoting: they end up in a
			// stored FailedRecord payload (internal/etl/processor.go's
			// fail()), not in a log line or an HTTP response, and
			// extractFloat deliberately names the offending value there.
			res, mapErr := c.Map(connector.RawRecord{UserID: "fuzz-user", Fields: fields})
			if mapErr != nil {
				continue
			}
			requireNoNaNQuantities(t, res)
		}
	})
}

// requireNoDataInMessage checks an error names the line but never quotes its
// data. HealthKit type identifiers are allowed: errors like
// "quantity HKQuantityTypeIdentifierHeartRate: missing startDate" name the
// schema, not the reading. Everything else from the body is health data, and a
// message carrying it would put it in the operator's logs.
func requireNoDataInMessage(t *testing.T, msg string, data []byte) {
	t.Helper()
	const window = 12
	for i := 0; i+window <= len(data); i++ {
		chunk := data[i : i+window]
		if !isPrintableASCII(chunk) || isTypeIdentifierFragment(data, i, window) {
			continue
		}
		if strings.Contains(msg, string(chunk)) {
			t.Fatalf("error message quotes upload data: %q appears in %q", chunk, msg)
		}
	}
}

// isTypeIdentifierFragment reports whether data[i:i+window] lies inside an
// "HK..." identifier, which the parser is allowed to name.
func isTypeIdentifierFragment(data []byte, i, window int) bool {
	start := i
	for start > 0 && isIdentifierByte(data[start-1]) {
		start--
	}
	if !bytes.HasPrefix(data[start:], []byte("HK")) {
		return false
	}
	end := i + window
	for end < len(data) && isIdentifierByte(data[end]) {
		end++
	}
	return end-start >= window
}

func isIdentifierByte(c byte) bool {
	return c == '_' ||
		(c >= '0' && c <= '9') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z')
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// requireNoNaNQuantities guards the other half of "never panics": a mapped
// record that carries NaN or Inf cannot be marshalled to JSON downstream.
func requireNoNaNQuantities(t *testing.T, res model.MapResult) {
	t.Helper()
	for _, s := range res.QuantitySamples {
		if isNotFinite(s.Value) {
			t.Fatalf("mapped quantity sample %q has a non-finite value", s.QuantityType)
		}
	}
}

func isNotFinite(v float64) bool {
	return math.IsNaN(v) || math.IsInf(v, 0)
}

func decodeLine(line []byte, out *map[string]any) error {
	if err := json.Unmarshal(line, out); err != nil {
		return err
	}
	if *out == nil {
		return errors.New("null line")
	}
	return nil
}

// seedCorpus returns every NDJSON fixture, gzipped, plus the JSON fixtures
// turned into one-line uploads.
func seedCorpus(f *testing.F) [][]byte {
	f.Helper()
	entries, err := os.ReadDir("testdata")
	require.NoError(f, err)

	var seeds [][]byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".ndjson") && !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(f, readErr)
		if strings.HasSuffix(name, ".json") {
			raw = append(bytes.Join(bytes.Fields(raw), nil), '\n')
		}
		seeds = append(seeds, gzipBytes(f, raw), raw)
	}
	return seeds
}

func gzipBytes(tb testing.TB, in []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(in)
	require.NoError(tb, err)
	require.NoError(tb, zw.Close())
	return buf.Bytes()
}
