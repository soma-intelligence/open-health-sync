// SPDX-License-Identifier: AGPL-3.0-only

// Package recorded replays, in CI, what real provider accounts answered each
// connector.
//
// A recording (a cassette, see internal/connector/cassette) is made once on a
// contributor's machine against a real account, anonymized there, and
// committed under testdata/<slug>/. Every `go test ./...` then replays each
// cassette: the connector must make exactly the recorded requests, and every
// line it emits must map, to the record counts held in the cassette's
// .summary.json golden file. docs/connectors/recording.md explains how to
// record.
package recorded

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
	"github.com/soma-intelligence/open-health-sync/connectors/dexcom"
	"github.com/soma-intelligence/open-health-sync/connectors/fitbit"
	"github.com/soma-intelligence/open-health-sync/connectors/healthconnect"
	"github.com/soma-intelligence/open-health-sync/connectors/intervalsicu"
	"github.com/soma-intelligence/open-health-sync/connectors/omron"
	"github.com/soma-intelligence/open-health-sync/connectors/oura"
	"github.com/soma-intelligence/open-health-sync/connectors/polar"
	"github.com/soma-intelligence/open-health-sync/connectors/strava"
	"github.com/soma-intelligence/open-health-sync/connectors/suunto"
	"github.com/soma-intelligence/open-health-sync/connectors/ultrahuman"
	"github.com/soma-intelligence/open-health-sync/connectors/whoop"
	"github.com/soma-intelligence/open-health-sync/connectors/withings"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/connector/cassette"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

// Build is what a connector is built with for a recording or a replay.
type Build struct {
	HTTPClient *http.Client
	Now        func() time.Time
	Sleep      func(context.Context, time.Duration) error
	// Fast turns off the pauses between requests (replay). A recording
	// keeps them, to respect the provider's rate limits.
	Fast bool
	// Config holds the non-secret keys a cassette was recorded with.
	Config map[string]string
}

// Provider is one connector the harness knows.
type Provider struct {
	Slug, Name string
	// APIVersion is the provider API version the connector calls today. A
	// cassette records the version it was made with, which tells a failed
	// replay from an API the provider has since changed.
	APIVersion string
	Kind       string // cassette.KindHTTP or cassette.KindUpload
	// ConfigKeys are the non-secret operator keys a cassette keeps (a
	// region, an environment).
	ConfigKeys []string
	// Keep names fields whose values anonymization must keep as they are,
	// because the mapping depends on them.
	Keep []string
	// Doc is the connector's guide under docs/connectors.
	Doc string
	New func(b Build) connector.Connector
}

func delay(fast bool) time.Duration {
	if fast {
		return -1
	}
	return 0
}

// Providers lists every connector.
var Providers = []Provider{
	{Slug: applehealth.Slug, Name: "Apple Health", APIVersion: "upload contract v1 (HealthKit)", Kind: cassette.KindUpload, Doc: "apple-health.md",
		New: func(Build) connector.Connector { return applehealth.New() }},
	{Slug: healthconnect.Slug, Name: "Health Connect", APIVersion: "upload contract v1 (Health Connect)", Kind: cassette.KindUpload, Doc: "health-connect.md",
		New: func(Build) connector.Connector { return healthconnect.New() }},
	{Slug: withings.Slug, Name: "Withings", APIVersion: "Public API (measure, v2)", Kind: cassette.KindHTTP, Doc: "withings.md",
		New: func(b Build) connector.Connector {
			return withings.New(withings.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, ChunkDelay: delay(b.Fast), PageDelay: delay(b.Fast)})
		}},
	{Slug: oura.Slug, Name: "Oura", APIVersion: "API v2", Kind: cassette.KindHTTP, Doc: "oura.md",
		New: func(b Build) connector.Connector {
			return oura.New(oura.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, PageDelay: delay(b.Fast)})
		}},
	{Slug: whoop.Slug, Name: "WHOOP", APIVersion: "API v2", Kind: cassette.KindHTTP, Doc: "whoop.md",
		New: func(b Build) connector.Connector {
			return whoop.New(whoop.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, PageDelay: delay(b.Fast)})
		}},
	{Slug: strava.Slug, Name: "Strava", APIVersion: "API v3", Kind: cassette.KindHTTP, Doc: "strava.md",
		New: func(b Build) connector.Connector {
			return strava.New(strava.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, PageDelay: delay(b.Fast)})
		}},
	{Slug: polar.Slug, Name: "Polar", APIVersion: "AccessLink v3", Kind: cassette.KindHTTP, Doc: "polar.md",
		New: func(b Build) connector.Connector {
			return polar.New(polar.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, ChunkDelay: delay(b.Fast)})
		}},
	{Slug: fitbit.Slug, Name: "Fitbit", APIVersion: "Google Health API v4", Kind: cassette.KindHTTP, Doc: "fitbit.md",
		New: func(b Build) connector.Connector {
			return fitbit.New(fitbit.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, PageDelay: delay(b.Fast)})
		}},
	{Slug: suunto.Slug, Name: "Suunto", APIVersion: "Cloud API (workouts v3)", Kind: cassette.KindHTTP, Doc: "suunto.md",
		New: func(b Build) connector.Connector {
			return suunto.New(suunto.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, PageDelay: delay(b.Fast)})
		}},
	{Slug: ultrahuman.Slug, Name: "Ultrahuman", APIVersion: "Partnership API", Kind: cassette.KindHTTP, Doc: "ultrahuman.md",
		New: func(b Build) connector.Connector {
			return ultrahuman.New(ultrahuman.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, DayDelay: delay(b.Fast)})
		}},
	{Slug: intervalsicu.Slug, Name: "intervals.icu", APIVersion: "API v1", Kind: cassette.KindHTTP, Doc: "intervals-icu.md",
		New: func(b Build) connector.Connector {
			return intervalsicu.New(intervalsicu.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep})
		}},
	{Slug: dexcom.Slug, Name: "Dexcom", APIVersion: "API v3", Kind: cassette.KindHTTP, Doc: "dexcom.md", ConfigKeys: []string{dexcom.KeyRegion},
		New: func(b Build) connector.Connector {
			return dexcom.New(dexcom.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, ChunkDelay: delay(b.Fast), Region: b.Config[dexcom.KeyRegion]})
		}},
	{Slug: omron.Slug, Name: "OMRON", APIVersion: "Wellness API", Kind: cassette.KindHTTP, Doc: "omron.md", ConfigKeys: []string{omron.KeyEnvironment},
		New: func(b Build) connector.Connector {
			return omron.New(omron.Options{HTTPClient: b.HTTPClient, Now: b.Now, Sleep: b.Sleep, Environment: b.Config[omron.KeyEnvironment]})
		}},
}

// Lookup returns the provider of a slug.
func Lookup(slug string) (Provider, bool) {
	for _, p := range Providers {
		if p.Slug == slug {
			return p, true
		}
	}
	return Provider{}, false
}

// Configure hands a connector its operator keys: every required key set to
// a placeholder, then cfg on top.
func Configure(c connector.Connector, cfg map[string]string) error {
	keys := connector.Config{}
	if k, ok := c.(connector.KeyedConnector); ok {
		for _, name := range k.RequiredKeys() {
			keys[name] = "recorded"
		}
	}
	for k, v := range cfg {
		keys[k] = v
	}
	if cf, ok := c.(connector.Configurable); ok {
		return cf.Configure(keys)
	}
	return nil
}

// Summary is what a replay produced, by data type. It holds counts only.
type Summary map[string]TypeSummary

// TypeSummary counts the lines of one data type and the records they mapped
// to, by kind ("quantity:heart_rate", "sleep_session", "workout").
type TypeSummary struct {
	Lines   int            `json:"lines"`
	Records map[string]int `json:"records"`
}

// Report is what went wrong in a replay.
type Report struct {
	Misses    []string // requests the cassette had no answer for
	Unused    []string // recorded requests the connector no longer makes
	MapErrors []string // lines Map refused: data type, line number, error kind
}

// OK reports a clean replay.
func (r Report) OK() bool { return len(r.Misses)+len(r.Unused)+len(r.MapErrors) == 0 }

func (r Report) String() string {
	var b strings.Builder
	for _, m := range r.Misses {
		fmt.Fprintf(&b, "\n  not recorded: %s", m)
	}
	for _, m := range r.Unused {
		fmt.Fprintf(&b, "\n  recorded but not requested: %s", m)
	}
	for _, m := range r.MapErrors {
		fmt.Fprintf(&b, "\n  does not map: %s", m)
	}
	return b.String()
}

type sink struct {
	mu    sync.Mutex
	order []string
	lines map[string][][]byte
}

func (s *sink) Emit(_ context.Context, dataType string, _ time.Time, lines [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lines[dataType]; !ok {
		s.order = append(s.order, dataType)
	}
	for _, l := range lines {
		s.lines[dataType] = append(s.lines[dataType], append([]byte(nil), l...))
	}
	return nil
}

// Replay runs a cassette through its connector: Fetch over the recorded
// window against the recorded answers (or ParseUpload over the recorded
// parts), then Map over every line.
func Replay(ctx context.Context, p Provider, c *cassette.Cassette) (Summary, Report, error) {
	if c.Provider != p.Slug {
		return nil, Report{}, fmt.Errorf("cassette of %q replayed as %q", c.Provider, p.Slug)
	}
	rp := cassette.NewReplayer(c)
	now := c.Window.Now
	conn := p.New(Build{
		HTTPClient: rp.Client(), Now: func() time.Time { return now },
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		Fast:  true, Config: c.Config,
	})
	if err := Configure(conn, c.Config); err != nil {
		return nil, Report{}, fmt.Errorf("configure: %w", err)
	}
	s := &sink{lines: map[string][][]byte{}}
	var rep Report
	switch c.Kind {
	case cassette.KindHTTP:
		o, ok := connector.AsOAuth(conn)
		if !ok {
			return nil, rep, fmt.Errorf("%s is not a cloud connector", p.Slug)
		}
		cn := connector.Connection{Provider: p.Slug, Tokens: connector.Tokens{
			Access: "recorded", Refresh: "recorded", Expiry: now.Add(time.Hour), ProviderUserID: c.ProviderUserID,
		}}
		err := o.Fetch(ctx, cn, c.Window.From, c.Window.To, s)
		rep.Misses, rep.Unused = rp.Misses(), rp.Unused()
		if err != nil && len(rep.Misses) == 0 {
			return nil, rep, fmt.Errorf("fetch: %w", err)
		}
	case cassette.KindUpload:
		d, ok := connector.AsDevice(conn)
		if !ok {
			return nil, rep, fmt.Errorf("%s is not a device connector", p.Slug)
		}
		for _, u := range c.Uploads {
			var body bytes.Buffer
			lines := make([][]byte, len(u.Lines))
			for i, l := range u.Lines {
				// A loaded cassette holds indented lines; an upload line is
				// one line.
				var c bytes.Buffer
				if err := json.Compact(&c, l); err != nil {
					return nil, rep, fmt.Errorf("upload %s %s line %d: %w", u.Type, u.Day, i+1, err)
				}
				lines[i] = c.Bytes()
				body.Write(lines[i])
				body.WriteByte('\n')
			}
			day, err := time.Parse(time.DateOnly, u.Day)
			if err != nil {
				return nil, rep, fmt.Errorf("upload day %q: %w", u.Day, err)
			}
			if err := d.ParseUpload(connector.UploadMeta{Type: u.Type, Day: day}, io.NopCloser(&body), s); err != nil {
				rep.MapErrors = append(rep.MapErrors, fmt.Sprintf("%s %s: upload refused: %v", u.Type, u.Day, errKind(err)))
				continue
			}
			// The core stores an upload's lines under its type; the ETL
			// maps each one.
			for _, l := range lines {
				_ = s.Emit(ctx, u.Type, day, [][]byte{l})
			}
		}
	default:
		return nil, rep, fmt.Errorf("unknown cassette kind %q", c.Kind)
	}

	sum := Summary{}
	for _, dt := range s.order {
		ts := TypeSummary{Records: map[string]int{}}
		for i, line := range s.lines[dt] {
			ts.Lines++
			var fields map[string]any
			if err := json.Unmarshal(line, &fields); err != nil {
				rep.MapErrors = append(rep.MapErrors, fmt.Sprintf("%s line %d: not a JSON object", dt, i+1))
				continue
			}
			res, err := conn.Map(connector.RawRecord{UserID: "recorded", Provider: p.Slug, DataType: dt, Fields: fields})
			if err != nil {
				rep.MapErrors = append(rep.MapErrors, fmt.Sprintf("%s line %d: %s", dt, i+1, errKind(err)))
				continue
			}
			// The ETL converts every sample to its storage unit before storing
			// it, and parks the record when it cannot.
			if ue := model.CanonicalizeUnits(&res); len(ue) > 0 {
				rep.MapErrors = append(rep.MapErrors, fmt.Sprintf("%s line %d: no storage unit for %q (%s)", dt, i+1, ue[0].Unit, ue[0].QuantityType))
				continue
			}
			count(ts.Records, res)
		}
		sum[dt] = ts
	}
	return sum, rep, nil
}

// errKind keeps an error's text up to the first quote, so no value from a
// line ends up in a log.
func errKind(err error) string {
	s := err.Error()
	if i := strings.IndexAny(s, "\"'"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func count(m map[string]int, r model.MapResult) {
	for _, q := range r.QuantitySamples {
		m["quantity:"+q.QuantityType]++
	}
	for _, c := range r.CategorySamples {
		m["category:"+c.CategoryType]++
	}
	add := func(k string, n int) {
		if n > 0 {
			m[k] += n
		}
	}
	add("sleep_session", len(r.SleepSessions))
	add("sleep_phase", len(r.SleepPhases))
	add("workout", len(r.Workouts))
	add("route_point", len(r.RoutePoints))
	add("nutrition_log", len(r.NutritionLogs))
	add("ecg_recording", len(r.ECGRecordings))
	add("ecg_sample", len(r.ECGSamples))
	add("heartbeat_sample", len(r.HeartbeatSamples))
	add("daily_summary", len(r.DailySummaries))
	add("observation", len(r.Observations))
	add("deletion", len(r.Deletions))
}

// MarshalSummary encodes a summary the way the golden files hold it.
func MarshalSummary(s Summary) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// RecordFetch runs one Fetch of c through rec and returns the anonymized
// cassette. c must have been built on an *http.Client using rec. Recording
// is on during the Fetch only. Tokens of cn and secrets in cfg join the
// values that must not survive.
func RecordFetch(ctx context.Context, p Provider, c connector.OAuthConnector, rec *cassette.Recorder,
	cn connector.Connection, from, to, now time.Time, cfg map[string]string, note string, o cassette.Options) (*cassette.Cassette, error) {
	rec.Start()
	err := c.Fetch(ctx, cn, from, to, &sink{lines: map[string][][]byte{}})
	rec.Stop()
	if err != nil {
		return nil, fmt.Errorf("fetch: %s", errKind(err))
	}
	kept := map[string]string{}
	for _, k := range p.ConfigKeys {
		if v := cfg[k]; v != "" {
			kept[k] = v
		}
	}
	for k, v := range cfg {
		if _, ok := kept[k]; !ok {
			o.Secrets = append(o.Secrets, v)
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	o.Secrets = append(o.Secrets, cn.Tokens.Access, cn.Tokens.Refresh)
	o.Keep = append(o.Keep, p.Keep...)
	o.ProviderUserID = cn.Tokens.ProviderUserID
	return cassette.AnonymizeHTTP(rec.Exchanges(), cassette.Meta{
		Provider: p.Slug, APIVersion: p.APIVersion, RecordedAt: now, Days: int(to.Sub(from).Hours()/24 + 0.5),
		From: from, To: to, Now: now, Config: kept, Note: note,
	}, o)
}
