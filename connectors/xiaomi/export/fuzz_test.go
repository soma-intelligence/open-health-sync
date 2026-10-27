// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/soma-intelligence/open-health-sync/connectors/xiaomi"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// FuzzConvert feeds arbitrary CSV tables (fitness data, sport records, data
// sources) to Convert. It must not panic, and every line it emits is one
// JSON object on one line with a table, a key and a positive time, which
// the connector's Map takes without panicking. Run it for a while with
//
//	go test -run='^$' -fuzz=^FuzzConvert$ -fuzztime=60s ./connectors/xiaomi/export/
func FuzzConvert(f *testing.F) {
	read := func(suffix string) []byte {
		m, err := filepath.Glob(filepath.Join("testdata", "mi_fitness", "*"+suffix))
		if err != nil || len(m) != 1 {
			f.Fatalf("no %s in testdata: %v", suffix, err)
		}
		b, err := os.ReadFile(m[0])
		if err != nil {
			f.Fatal(err)
		}
		return b
	}
	fit, sport, ds := read(fileFitness), read(fileSport), read(fileDataSource)
	f.Add(fit, sport, ds)
	f.Add([]byte(fitnessHeader+`1,5,steps,9223372036854775807,"{""steps"":1}",0`+"\n"), []byte(nil), []byte(nil))
	f.Add([]byte(fitnessHeader+`1,5,steps,1772960400,"{""steps"":""NaN"",""time"":1e999}",-1`+"\n"), []byte("Sid\n"), []byte("\n\n"))
	f.Add([]byte("\xef\xbb\xbf\"Uid,Sid,Key,Time,Value\"\n\"1,5,k,1,\"{}\"\"\n"), []byte(`"""`), []byte("Sid,Model\n5,m\n5,n\n"))
	f.Add([]byte(fitnessHeader+`1,5,heart_rate,1772960400,"{""a"":[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]}"`+"\n"), []byte(nil), []byte(nil))

	c := xiaomi.New()
	f.Fuzz(func(t *testing.T, fitness, sports, sources []byte) {
		e := &Export{files: map[string]source{}}
		for suffix, b := range map[string][]byte{fileFitness: fitness, fileSport: sports, fileDataSource: sources} {
			if b == nil {
				continue
			}
			b := b
			e.files[suffix] = source{name: suffix, size: int64(len(b)), open: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(b)), nil
			}}
		}
		_, err := e.Convert(Options{MaxPartBytes: 512}, func(p Part) error {
			n := 0
			for _, l := range bytes.Split(bytes.TrimSuffix(p.NDJSON, []byte("\n")), []byte("\n")) {
				n++
				var fields map[string]any
				if err := json.Unmarshal(l, &fields); err != nil {
					t.Fatalf("emitted line is not a JSON object: %q", l)
				}
				var head struct {
					Table string `json:"table"`
					Key   string `json:"key"`
					Time  int64  `json:"time"`
				}
				if json.Unmarshal(l, &head) != nil || head.Table == "" || head.Key == "" || head.Time <= 0 {
					t.Fatalf("emitted line without table, key or time: %q", l)
				}
				_, _ = c.Map(connector.RawRecord{UserID: "u", Provider: xiaomi.Slug, Fields: fields})
			}
			if n != p.Lines {
				t.Fatalf("part says %d lines, holds %d", p.Lines, n)
			}
			return nil
		})
		_ = err
	})
}
