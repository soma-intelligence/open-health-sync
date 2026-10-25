// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"testing"

	"github.com/soma-intelligence/open-health-sync/internal/etl"
)

type oneObject []byte

func (o oneObject) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o)), nil
}

// FuzzReader streams an arbitrary raw object, plain or gzipped, through the
// worker's NDJSON reader with small caps. No input may panic it; the record
// indexes it hands over count up from zero and never pass the record cap;
// the decompressed size cap holds; and when it read to the end, every
// non-empty line is either a record or a parse error.
func FuzzReader(f *testing.F) {
	f.Add(false, []byte("{\"a\":1}\n{\"b\":2}\n"))
	f.Add(true, []byte("{\"a\":1}\n\nnot json\n{\"c\":[1,2,3]}"))
	f.Add(false, []byte{0x1f, 0x8b, 0x08, 0x00})
	f.Add(true, bytes.Repeat([]byte("{}\n"), 300))
	f.Add(false, []byte("null\n[]\n\"x\"\n{\"a\":1e400}\n"))
	const (
		maxLine    = 4096
		maxDecomp  = 64 * 1024
		maxRecords = 200
	)
	f.Fuzz(func(t *testing.T, compress bool, data []byte) {
		obj := data
		if compress {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			_, _ = zw.Write(data)
			_ = zw.Close()
			obj = b.Bytes()
		}
		r := etl.NewReaderWithLimits(oneObject(obj), maxLine, maxDecomp, maxRecords)
		next := 0
		res, err := r.StreamNDJSON(context.Background(), "k", func(i int, _ map[string]any) error {
			if i != next {
				t.Fatalf("record index %d, want %d", i, next)
			}
			next++
			return nil
		})
		if err != nil {
			return
		}
		if res.RecordCount != next || res.RecordCount > maxRecords {
			t.Fatalf("RecordCount %d, callbacks %d, cap %d", res.RecordCount, next, maxRecords)
		}
		if res.ScanTruncated || !compress {
			return
		}
		if len(data) > maxDecomp {
			t.Fatalf("read %d decompressed bytes to the end past the %d cap", len(data), maxDecomp)
		}
		lines := 0
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSuffix(l, []byte("\r"))) > 0 {
				lines++
			}
		}
		if got := res.RecordCount + len(res.ParseErrors) + res.ParseErrorsDropped; got != lines {
			t.Fatalf("%d non-empty lines, %d records + %d parse errors + %d dropped",
				lines, res.RecordCount, len(res.ParseErrors), res.ParseErrorsDropped)
		}
	})
}
