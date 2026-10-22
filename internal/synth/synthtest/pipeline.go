// SPDX-License-Identifier: AGPL-3.0-only

// Package synthtest runs synthetic lines through the same steps the ETL worker
// takes for a raw object, without a database: NDJSON read, connector Map,
// source attribution, unit canonicalization and validation. It records what
// became of every line and checks the invariants every connector owes.
package synthtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/etl"
	"github.com/soma-intelligence/open-health-sync/internal/model"
	"github.com/soma-intelligence/open-health-sync/internal/synth"
)

// Outcome is what the pipeline did with one line.
type Outcome string

// Outcomes, in the order the worker can reach them.
const (
	ParseError Outcome = "parse_error" // not a JSON object: never reaches Map
	Panic      Outcome = "panic"       // Map panicked: always a bug
	MapError   Outcome = "map_error"   // Map refused the line
	Empty      Outcome = "empty"       // Map skipped the line on purpose
	UnitError  Outcome = "unit_error"  // a sample's unit has no conversion to storage
	Rejected   Outcome = "rejected"    // validation parked at least one record
	Stored     Outcome = "stored"      // every record would be written
)

// Result is the fate of one line.
type Result struct {
	Line    synth.Line
	Outcome Outcome
	Detail  string
	// Broken lists the invariants the mapped records break, for Stored and
	// Rejected lines.
	Broken   []string
	Suspects int
}

// UserID and TenantID are what the run stamps on every raw record.
const (
	TenantID = "00000000-0000-4000-8000-000000000001"
	UserID   = "00000000-0000-4000-8000-000000000002"
)

type memStore map[string][]byte

func (m memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("no object %s", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// Run maps lines with c, one raw object per data type, as the worker would.
// now is the validation clock.
func Run(c connector.Connector, lines []synth.Line, now time.Time) []Result {
	var order []string
	groups := map[string][]synth.Line{}
	for _, l := range lines {
		if _, ok := groups[l.DataType]; !ok {
			order = append(order, l.DataType)
		}
		groups[l.DataType] = append(groups[l.DataType], l)
	}
	var out []Result
	for _, dt := range order {
		out = append(out, runObject(c, dt, groups[dt], now)...)
	}
	return out
}

func runObject(c connector.Connector, dataType string, lines []synth.Line, now time.Time) []Result {
	var body bytes.Buffer
	for _, l := range lines {
		body.Write(l.JSON)
		body.WriteByte('\n')
	}
	key := "synth/" + dataType
	reader := etl.NewReader(memStore{key: body.Bytes()})

	results := make([]Result, len(lines))
	for i, l := range lines {
		results[i].Line = l
	}
	// The reader skips blank lines and keeps only its first parse errors, so
	// each line is parsed here the same way (a scanner line, then
	// json.Unmarshal into a map) to tell which ones it hands over; each
	// record is then mapped as it streams, so no run holds every record.
	var parsed []int
	for i, l := range lines {
		line := bytes.TrimSuffix(l.JSON, []byte("\r"))
		if len(line) == 0 {
			results[i].Outcome, results[i].Detail = ParseError, "blank line"
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			results[i].Outcome, results[i].Detail = ParseError, err.Error()
			continue
		}
		parsed = append(parsed, i)
	}
	k := 0
	_, err := reader.StreamNDJSON(context.Background(), key, func(_ int, rec map[string]any) error {
		if k < len(parsed) {
			mapOne(c, dataType, key, rec, now, &results[parsed[k]])
		}
		k++
		return nil
	})
	if err != nil {
		for i := range results {
			results[i].Outcome, results[i].Detail = ParseError, err.Error()
		}
		return results
	}
	if k != len(parsed) {
		for _, i := range parsed {
			results[i].Outcome = ParseError
			results[i].Detail = fmt.Sprintf("reader returned %d records for %d lines", k, len(parsed))
		}
	}
	return results
}

func mapOne(c connector.Connector, dataType, key string, rec map[string]any, now time.Time, r *Result) {
	mr, err := safeMap(c, connector.RawRecord{
		TenantID: TenantID, UserID: UserID, Provider: c.Slug(),
		DataType: dataType, SourceKey: key, Fields: rec,
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "connector panic:") {
			r.Outcome = Panic
		} else {
			r.Outcome = MapError
		}
		r.Detail = err.Error()
		return
	}
	if mr.IsEmpty() {
		r.Outcome = Empty
		return
	}
	model.CanonicalizeSourceAttribution(mr, rec, c.Slug())
	if errs := model.CanonicalizeUnits(mr); len(errs) > 0 {
		r.Outcome, r.Detail = UnitError, fmt.Sprint(errs)
		return
	}
	verrs := etl.ValidateMapResultAt(mr, now)
	r.Broken = Invariants(mr, now)
	if rej := etl.Rejections(verrs); len(rej) > 0 {
		r.Outcome, r.Detail = Rejected, fmt.Sprint(rej)
		return
	}
	r.Outcome = Stored
	r.Suspects = len(etl.Suspicions(verrs))
}

func safeMap(c connector.Connector, raw connector.RawRecord) (mr *model.MapResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			mr, err = nil, fmt.Errorf("connector panic: %v", p)
		}
	}()
	res, err := c.Map(raw)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// T is the part of testing.TB the checks use.
type T interface {
	Helper()
	Errorf(format string, args ...any)
}

// Check reports every broken invariant:
//   - no line panics the mapper;
//   - a line that follows the vendor's documentation is never refused:
//     no map error, no unit error, no rejection;
//   - a malformed line never reaches storage;
//   - every record that would be stored has finite numbers, times no more
//     than two days past now, and an end not before its start. The zone of
//     a time is free: hashes and the database work in UTC.
//
// At most max failures are reported, so one systematic bug does not flood the
// log.
func Check(t T, provider string, results []Result, now time.Time, max int) {
	t.Helper()
	n := 0
	report := func(r Result, format string, args ...any) {
		n++
		if n > max {
			return
		}
		t.Errorf("%s %s [%s %s]: %s\n  line: %s", provider, r.Line.DataType, r.Line.Fault, r.Line.Tag,
			fmt.Sprintf(format, args...), clip(r.Line.JSON, 400))
	}
	for _, r := range results {
		switch {
		case r.Outcome == Panic:
			report(r, "mapper panicked: %s", r.Detail)
		case r.Line.Valid() && (r.Outcome == MapError || r.Outcome == UnitError || r.Outcome == Rejected || r.Outcome == ParseError):
			report(r, "documented line refused (%s): %s", r.Outcome, r.Detail)
		case r.Line.Fault == synth.FaultMalformed && r.Outcome == Stored:
			report(r, "malformed line stored")
		}
		if r.Outcome == Stored {
			for _, msg := range r.Broken {
				report(r, "%s", msg)
			}
		}
	}
	if n > max {
		t.Errorf("%s: %d more failures not shown", provider, n-max)
	}
}

func clip(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// Invariants lists what is wrong with mr, whatever the line it came from.
func Invariants(mr *model.MapResult, now time.Time) []string {
	var out []string
	v := reflect.ValueOf(mr).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.Slice || f.Type().Elem().Kind() != reflect.Struct {
			continue
		}
		name := v.Type().Field(i).Name
		for j := 0; j < f.Len(); j++ {
			out = append(out, recordInvariants(fmt.Sprintf("%s[%d]", name, j), f.Index(j), now)...)
		}
	}
	return out
}

var timeType = reflect.TypeOf(time.Time{})

// pairs are the start and end fields of the canonical records.
var pairs = [][2]string{{"StartDate", "EndDate"}, {"BedtimeStart", "BedtimeEnd"}, {"TS", "EndTS"}}

func recordInvariants(where string, rec reflect.Value, now time.Time) []string {
	var out []string
	times := map[string]time.Time{}
	t := rec.Type()
	for i := 0; i < rec.NumField(); i++ {
		f := rec.Field(i)
		name := t.Field(i).Name
		if f.Kind() == reflect.Pointer {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		switch {
		case f.Type() == timeType:
			tm := f.Interface().(time.Time)
			if tm.IsZero() {
				continue
			}
			times[name] = tm
			if tm.After(now.Add(48 * time.Hour)) {
				out = append(out, fmt.Sprintf("%s.%s is in the future: %s", where, name, tm.Format(time.RFC3339)))
			}
		case f.Kind() == reflect.Float64 || f.Kind() == reflect.Float32:
			if x := f.Float(); math.IsNaN(x) || math.IsInf(x, 0) {
				out = append(out, fmt.Sprintf("%s.%s is not finite", where, name))
			}
		}
	}
	for _, p := range pairs {
		s, ok1 := times[p[0]]
		e, ok2 := times[p[1]]
		if ok1 && ok2 && e.Before(s) {
			out = append(out, fmt.Sprintf("%s ends before it starts: %s < %s", where, e.Format(time.RFC3339), s.Format(time.RFC3339)))
		}
	}
	return out
}

// Summary counts outcomes by fault and outcome, for a golden file. It holds
// no value, only counts, so it stays stable across value changes that keep
// every line's fate.
func Summary(results []Result) map[string]map[Outcome]int {
	out := map[string]map[Outcome]int{}
	for _, r := range results {
		k := string(r.Line.Fault)
		if k == "" {
			k = "valid"
		}
		if out[k] == nil {
			out[k] = map[Outcome]int{}
		}
		out[k][r.Outcome]++
	}
	return out
}

// Accepted lists the faulty lines that were stored anyway, sorted by fault
// and tag: what a reviewer reads to decide whether a connector is too lenient.
func Accepted(results []Result) []string {
	seen := map[string]int{}
	for _, r := range results {
		if r.Line.Valid() || r.Outcome != Stored {
			continue
		}
		seen[fmt.Sprintf("%s %s %s", r.Line.DataType, r.Line.Fault, r.Line.Tag)]++
	}
	out := make([]string, 0, len(seen))
	for k, n := range seen {
		out = append(out, fmt.Sprintf("%s x%d", k, n))
	}
	sort.Strings(out)
	return out
}

// Full reports whether the full run was asked for: OHS_SYNTH_FULL set and
// not -short. By default (go test ./..., the CI run) the suites keep a few
// days and skip Huge and the document scenarios one by one, since All holds
// every one of them; the nightly workflow sets OHS_SYNTH_FULL=1 for
// fourteen days, every scenario on its own and Huge.
func Full() bool { return !testing.Short() && os.Getenv("OHS_SYNTH_FULL") != "" }

// documentOnly are the scenarios All covers in the light run.
var documentOnly = map[synth.Scenario]bool{synth.Duplicates: true, synth.Missing: true, synth.OutOfRange: true,
	synth.UnitVariants: true, synth.Malformed: true, synth.Unknown: true, synth.Timestamps: true}

// Scenarios lists the scenarios a suite runs.
func Scenarios() []synth.Scenario {
	var out []synth.Scenario
	for _, s := range synth.Scenarios {
		if !Full() && (s == synth.Huge || documentOnly[s]) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Days is the number of days a suite generates for s.
func Days(s synth.Scenario) int {
	switch {
	case s == synth.Huge:
		return 3
	case Full():
		return 14
	}
	return 5
}

// PersonaDays is the number of days of each persona run.
func PersonaDays() int {
	if Full() {
		return 14
	}
	return 4
}

// Render turns a timeline into a provider's lines.
type Render func(tl *synth.Timeline) []synth.Line

// Suite runs render through c for the scenarios of Scenarios and every
// persona, and checks each run. It is the whole test of a
// renderer and its connector.
func Suite(t *testing.T, c func() connector.Connector, render Render) {
	t.Helper()
	for _, s := range Scenarios() {
		t.Run(string(s), func(t *testing.T) {
			t.Parallel()
			tl := synth.Build(synth.Options{Seed: 7, Scenarios: []synth.Scenario{s}, Days: Days(s)})
			lines := render(tl)
			if len(lines) == 0 {
				t.Fatal("no lines")
			}
			conn := c()
			res := Run(conn, lines, tl.Opt.Now)
			Check(t, conn.Slug(), res, tl.Opt.Now, 20)
			if s == synth.Baseline {
				stored := 0
				for _, r := range res {
					if r.Outcome == Stored {
						stored++
					}
				}
				if stored == 0 {
					t.Error("baseline stored nothing")
				}
			}
			t.Logf("%d lines, outcomes %v", len(lines), Summary(res))
		})
	}
	for _, p := range synth.Personas {
		t.Run("persona/"+string(p), func(t *testing.T) {
			t.Parallel()
			tl := synth.Build(synth.Options{Seed: 3, Persona: p, Days: PersonaDays(), Scenarios: []synth.Scenario{synth.DST, synth.Travel, synth.MultiDevice}})
			conn := c()
			Check(t, conn.Slug(), Run(conn, render(tl), tl.Opt.Now), tl.Opt.Now, 20)
		})
	}
	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()
		gen := func(seed uint64) string {
			tl := synth.Build(synth.Options{Seed: seed, Days: 2, Scenarios: []synth.Scenario{synth.All}})
			h := sha256.New()
			for _, l := range render(tl) {
				h.Write(l.JSON)
				h.Write([]byte{'\n'})
			}
			return hex.EncodeToString(h.Sum(nil))
		}
		if gen(1) != gen(1) {
			t.Error("same seed, different lines")
		}
		if gen(1) == gen(2) {
			t.Error("different seeds, same lines")
		}
	})
}
