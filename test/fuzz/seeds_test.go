// SPDX-License-Identifier: AGPL-3.0-only

package fuzz

import (
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	_ "github.com/soma-intelligence/open-health-sync/connectors/all"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
	sactivity "github.com/soma-intelligence/open-health-sync/internal/synth/activityfiles"
	sapple "github.com/soma-intelligence/open-health-sync/internal/synth/applehealth"
	sble "github.com/soma-intelligence/open-health-sync/internal/synth/bluetooth"
	sdexcom "github.com/soma-intelligence/open-health-sync/internal/synth/dexcom"
	sfitbit "github.com/soma-intelligence/open-health-sync/internal/synth/fitbit"
	shc "github.com/soma-intelligence/open-health-sync/internal/synth/healthconnect"
	sicu "github.com/soma-intelligence/open-health-sync/internal/synth/intervalsicu"
	somron "github.com/soma-intelligence/open-health-sync/internal/synth/omron"
	soura "github.com/soma-intelligence/open-health-sync/internal/synth/oura"
	spolar "github.com/soma-intelligence/open-health-sync/internal/synth/polar"
	sself "github.com/soma-intelligence/open-health-sync/internal/synth/selfreport"
	sstrava "github.com/soma-intelligence/open-health-sync/internal/synth/strava"
	ssuunto "github.com/soma-intelligence/open-health-sync/internal/synth/suunto"
	swhoop "github.com/soma-intelligence/open-health-sync/internal/synth/whoop"
	swithings "github.com/soma-intelligence/open-health-sync/internal/synth/withings"
)

func TestMain(m *testing.M) {
	// Mappers log refused lines; a fuzz run would print millions of them.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// provider is one connector with the synthetic renderer of its wire format.
type provider struct {
	slug   string
	render func(*synth.Timeline) []synth.Line
	device bool // lines are upload lines of a /v1/ingest route
}

// providers is fixed and ordered: the fuzz targets pick one by index, so an
// index in a saved crasher must keep meaning the same provider.
var providers = []provider{
	{"apple_health", sapple.Lines, true},
	{"health_connect", shc.Lines, true},
	{"bluetooth", sble.Lines, true},
	{"activity_files", sactivity.Lines, true},
	{"dexcom", sdexcom.Lines, false},
	{"fitbit", sfitbit.Lines, false},
	{"intervals_icu", sicu.Lines, false},
	{"omron", somron.Lines, false},
	{"oura", soura.Lines, false},
	{"polar", spolar.Lines, false},
	{"self_report", sself.Lines, false},
	{"strava", sstrava.Lines, false},
	{"suunto", ssuunto.Lines, false},
	{"whoop", swhoop.Lines, false},
	{"withings", swithings.Lines, false},
}

// corpus is what the generator renders for each provider: its data types
// (sorted, so an index is stable) and a few lines of each.
type corpus struct {
	types []string
	lines map[string][]synth.Line
}

var (
	corpusOnce sync.Once
	corpora    []corpus
	// genNow is the validation clock of the generated timeline.
	genNow time.Time
)

// perType caps the seed lines kept for each data type: the first valid one
// and the first of each fault.
func loadCorpora() []corpus {
	corpusOnce.Do(func() {
		tl := synth.Build(synth.Options{Seed: 11, Days: 3, Scenarios: []synth.Scenario{synth.All}})
		genNow = tl.Opt.Now
		for _, p := range providers {
			c := corpus{lines: map[string][]synth.Line{}}
			seen := map[string]bool{}
			for _, l := range p.render(tl) {
				k := l.DataType + "\x00" + string(l.Fault)
				if seen[k] {
					continue
				}
				seen[k] = true
				if _, ok := c.lines[l.DataType]; !ok {
					c.types = append(c.types, l.DataType)
				}
				c.lines[l.DataType] = append(c.lines[l.DataType], l)
			}
			sort.Strings(c.types)
			corpora = append(corpora, c)
		}
	})
	return corpora
}

// pick returns the provider and data type the two index bytes name.
func pick(pi, ti uint8) (provider, corpus, string) {
	cs := loadCorpora()
	i := int(pi) % len(providers)
	c := cs[i]
	if len(c.types) == 0 {
		return providers[i], c, ""
	}
	return providers[i], c, c.types[int(ti)%len(c.types)]
}

func mustConnector(tb testing.TB, slug string) connector.Connector {
	tb.Helper()
	c, ok := connector.Get(slug)
	if !ok {
		tb.Fatalf("connector %s is not registered", slug)
	}
	return c
}
