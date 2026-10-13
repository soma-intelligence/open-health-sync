// SPDX-License-Identifier: AGPL-3.0-only

package recorded

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/connector/cassette"
)

var update = flag.Bool("update", false, "rewrite the .summary.json golden files")

// cassettes lists the committed cassettes: testdata/<slug>/<name>.json.
func cassettes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "*", "*.json"))
	require.NoError(t, err)
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".summary.json") {
			out = append(out, f)
		}
	}
	return out
}

// Every committed cassette is safe to publish, replays cleanly through its
// connector, and maps to the record counts of its golden summary. Run with
// -update after a deliberate mapping change, and review the diff.
func TestReplayCassettes(t *testing.T) {
	for _, f := range cassettes(t) {
		t.Run(filepath.ToSlash(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			require.NoError(t, err)
			c, err := cassette.Load(f)
			require.NoError(t, err)
			require.Empty(t, cassette.Check(c, raw), "the cassette is not safe to publish")
			require.Equal(t, filepath.Base(filepath.Dir(f)), c.Provider, "a cassette lives under testdata/<its provider>/")
			p, ok := Lookup(c.Provider)
			require.True(t, ok, "no connector %q", c.Provider)
			require.Equal(t, p.Kind, c.Kind)

			got, rep, err := Replay(context.Background(), p, c)
			require.NoError(t, err)
			require.True(t, rep.OK(), "the connector no longer handles what the real API answered; fix it, or record again if the API changed:%s", rep.String())
			b, err := MarshalSummary(got)
			require.NoError(t, err)

			golden := strings.TrimSuffix(f, ".json") + ".summary.json"
			if *update {
				require.NoError(t, os.WriteFile(golden, b, 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "run go test ./test/recorded -update to write the golden summary")
			require.Equal(t, string(want), string(b), "the record counts changed; if that is intended, run go test ./test/recorded -update")
		})
	}
}

func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(line))
	require.NoError(t, dec.Decode(&m))
	return m
}
