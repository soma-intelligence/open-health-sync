// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/soma-intelligence/open-health-sync/connectors/applehealth"
)

// Part type identifiers (X-OHS-Type) for the parts that are not named after
// a quantity, category or correlation type.
const (
	TypeWorkout   = "HKWorkoutTypeIdentifier"
	TypeHeartbeat = "HKDataTypeIdentifierHeartbeatSeries"
	TypeAudiogram = "HKDataTypeIdentifierAudiogram"
	TypeECG       = "HKElectrocardiogramType"
)

// Defaults of Options.
const (
	DefaultMaxPartBytes     = 8 << 20
	DefaultMaxBufferedBytes = 32 << 20
)

// Options tune a conversion. Every field takes part in BatchID: changing one
// changes the parts, so it must change the batch too.
type Options struct {
	// Since keeps only samples whose local day is on or after it
	// (YYYY-MM-DD). Empty keeps everything.
	Since string
	// Zone is the IANA time zone assumed for a sample export.xml gives no
	// HKTimeZone for. It is written as the line's HKTimeZone, so the server
	// files the sample under its local day in that zone. Empty leaves those
	// samples without a zone: the server then uses UTC days, as for any
	// upload without HKTimeZone.
	Zone string
	// MaxPartBytes caps the uncompressed NDJSON of one part. The gzip of a
	// part stays far below the contract's 64 MiB.
	MaxPartBytes int
	// MaxBufferedBytes caps the lines held in memory across all the
	// (type, day) groups still open. Past it, the group written to least
	// recently is sent as a part.
	MaxBufferedBytes int
}

func (o *Options) withDefaults() {
	if o.MaxPartBytes <= 0 {
		o.MaxPartBytes = DefaultMaxPartBytes
	}
	if o.MaxBufferedBytes <= 0 {
		o.MaxBufferedBytes = DefaultMaxBufferedBytes
	}
}

// Validate checks Since and Zone.
func (o Options) Validate() error {
	if o.Since != "" {
		if _, err := time.Parse(time.DateOnly, o.Since); err != nil {
			return fmt.Errorf("since %q is not a date as YYYY-MM-DD", o.Since)
		}
	}
	if o.Zone != "" {
		if _, err := time.LoadLocation(o.Zone); err != nil || o.Zone == "Local" {
			return fmt.Errorf("time zone %q is not an IANA zone name such as Europe/Paris", o.Zone)
		}
	}
	return nil
}

// Part is one upload: NDJSON lines of one data type and one local day.
type Part struct {
	Number int    // 0, 1, 2... in the order parts are produced
	Type   string // X-OHS-Type
	Day    string // X-OHS-Day, YYYY-MM-DD
	Lines  int
	NDJSON []byte // uncompressed
	// RecordsRead is how many export records had been read when the part
	// was produced, for progress reports.
	RecordsRead int
}

// Stats counts what a conversion read and produced. It holds counts and
// type identifiers only, never a health value.
type Stats struct {
	Records     int            // records read: export.xml elements and ECG files
	Lines       int            // NDJSON lines produced
	Parts       int            // parts produced
	BeforeSince int            // records left out by Options.Since
	Skipped     map[string]int // records of a type the server does not store, by type
	Invalid     map[string]int // records that could not be converted, by type
	RoutePoints int            // workout route points, included in Lines
	NoRoute     int            // workouts whose route file is missing from the export
}

// Convert reads the export and calls emit with each part, in a deterministic
// order. It stops at the first error emit returns.
func Convert(ctx context.Context, e *Export, opts Options, emit func(Part) error) (Stats, error) {
	opts.withDefaults()
	if err := opts.Validate(); err != nil {
		return Stats{}, err
	}
	c := &converter{
		ctx: ctx, exp: e, opts: opts, emit: emit,
		groups: map[groupKey]*list.Element{}, lru: list.New(),
		stats: Stats{Skipped: map[string]int{}, Invalid: map[string]int{}},
	}
	if opts.Zone != "" {
		c.zone, _ = time.LoadLocation(opts.Zone)
	}
	err := c.run()
	return c.stats, err
}

type groupKey struct{ typ, day string }

type group struct {
	key   groupKey
	buf   bytes.Buffer
	lines int
}

type converter struct {
	ctx  context.Context
	exp  *Export
	opts Options
	emit func(Part) error
	zone *time.Location // Options.Zone, resolved

	next     int
	groups   map[groupKey]*list.Element
	lru      *list.List // of *group, least recently written first
	buffered int
	stats    Stats
}

func (c *converter) run() error {
	r, err := c.exp.openXML()
	if err != nil {
		return err
	}
	defer r.Close()
	if err := c.readXML(r); err != nil {
		return err
	}
	if err := c.readECGs(); err != nil {
		return err
	}
	return c.flushAll()
}

func (c *converter) readXML(r io.Reader) error {
	dec := xml.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read export.xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if c.stats.Records%1000 == 0 {
			if err := c.ctx.Err(); err != nil {
				return err
			}
		}
		switch se.Name.Local {
		case "HealthData":
			continue // descend into the records
		case "ExportDate", "Me":
			err = dec.Skip()
		case "Record":
			var rec xmlRecord
			if err = dec.DecodeElement(&rec, &se); err == nil {
				err = c.record(rec)
			}
		case "Correlation":
			var corr xmlCorrelation
			if err = dec.DecodeElement(&corr, &se); err == nil {
				err = c.correlation(corr)
			}
		case "Workout":
			var w xmlWorkout
			if err = dec.DecodeElement(&w, &se); err == nil {
				err = c.workout(w)
			}
		case "Audiogram":
			var a xmlAudiogram
			if err = dec.DecodeElement(&a, &se); err == nil {
				err = c.audiogram(a)
			}
		default:
			// ActivitySummary, ClinicalRecord, VisionPrescription...: no
			// line format for them in the upload contract.
			c.stats.Records++
			c.stats.Skipped[se.Name.Local]++
			err = dec.Skip()
		}
		if err != nil {
			return fmt.Errorf("read export.xml: %w", err)
		}
	}
}

// sample is what every line shares: its dates, local day and provenance.
type sample struct {
	start, end time.Time
	day        string
	fields     map[string]any
}

// newSample parses the common attributes. ok is false when a date does not
// parse (the record is counted invalid) or the day is before Since.
func (c *converter) newSample(typ string, a commonAttrs, md []xmlMeta) (sample, bool) {
	start, err1 := parseExportTime(a.StartDate)
	end, err2 := parseExportTime(a.EndDate)
	if err1 != nil {
		c.stats.Invalid[typ]++
		return sample{}, false
	}
	if err2 != nil {
		end = start
	}
	meta := map[string]any{}
	for _, m := range md {
		meta[m.Key] = m.Value
	}
	// The local day: in the recorded zone when HealthKit kept it, else in
	// the assumed zone, else as the export wrote it.
	var loc *time.Location
	if tz, ok := meta["HKTimeZone"].(string); ok && tz != "" {
		loc, _ = time.LoadLocation(tz)
	} else if c.zone != nil {
		loc = c.zone
		meta["HKTimeZone"] = c.opts.Zone
	}
	local := start
	if loc != nil {
		local = start.In(loc)
	}
	day := local.Format(time.DateOnly)
	if c.opts.Since != "" && day < c.opts.Since {
		c.stats.BeforeSince++
		return sample{}, false
	}

	f := map[string]any{
		"startDate": wireTime(start),
		"endDate":   wireTime(end),
	}
	if a.SourceName != "" {
		f["sourceName"] = a.SourceName
	}
	if a.SourceVersion != "" {
		f["sourceVersion"] = a.SourceVersion
	}
	if d := deviceFields(a.Device); d != nil {
		for key, field := range map[string]string{
			"name": "device", "model": "deviceModel", "hardware": "productType",
			"manufacturer": "deviceManufacturer", "software": "deviceSoftwareVersion",
		} {
			if v := d[key]; v != "" {
				f[field] = v
			}
		}
	}
	if v, ok := meta["HKWasUserEntered"].(string); ok {
		f["wasUserEntered"] = v == "1" || strings.EqualFold(v, "true")
	}
	if len(meta) > 0 {
		f["metadata"] = meta
	}
	return sample{start: start, end: end, day: day, fields: f}, true
}

func (c *converter) record(r xmlRecord) error {
	c.stats.Records++
	if r.Type == TypeHeartbeat {
		return c.heartbeat(r)
	}
	if !applehealth.StoresType(r.Type) {
		c.stats.Skipped[r.Type]++
		return nil
	}
	s, ok := c.newSample(r.Type, r.commonAttrs, r.Metadata)
	if !ok {
		return nil
	}
	if strings.HasPrefix(r.Type, "HKCategoryTypeIdentifier") {
		v, ok := categoryValue(r.Value)
		if !ok {
			c.stats.Invalid[r.Type]++
			return nil
		}
		s.fields["categoryType"] = r.Type
		s.fields["value"] = v
	} else {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
		if err != nil {
			c.stats.Invalid[r.Type]++
			return nil
		}
		s.fields["quantityType"] = r.Type
		s.fields["value"] = v
		s.fields["unit"] = r.Unit
	}
	return c.add(r.Type, s.day, s.fields)
}

func (c *converter) correlation(x xmlCorrelation) error {
	c.stats.Records++
	if x.Type != "HKCorrelationTypeIdentifierBloodPressure" && x.Type != "HKCorrelationTypeIdentifierFood" {
		c.stats.Skipped[x.Type]++
		return nil
	}
	s, ok := c.newSample(x.Type, x.commonAttrs, x.Metadata)
	if !ok {
		return nil
	}
	objects := make([]any, 0, len(x.Records))
	for _, r := range x.Records {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
		if err != nil {
			continue
		}
		o := map[string]any{"sampleType": r.Type, "value": v, "unit": r.Unit}
		if t, err := parseExportTime(r.StartDate); err == nil {
			o["startDate"] = wireTime(t)
		}
		if t, err := parseExportTime(r.EndDate); err == nil {
			o["endDate"] = wireTime(t)
		}
		objects = append(objects, o)
	}
	if len(objects) == 0 {
		c.stats.Invalid[x.Type]++
		return nil
	}
	s.fields["correlationType"] = x.Type
	s.fields["objects"] = objects
	return c.add(x.Type, s.day, s.fields)
}

func (c *converter) workout(w xmlWorkout) error {
	c.stats.Records++
	s, ok := c.newSample(TypeWorkout, w.commonAttrs, w.Metadata)
	if !ok {
		return nil
	}
	f := s.fields
	f["workoutActivityType"] = workoutActivityType(w.ActivityType)
	if d, ok := convert(w.Duration, w.DurationUnit, secondsPer); ok {
		f["durationS"] = d
	} else {
		f["durationS"] = s.end.Sub(s.start).Seconds()
	}
	// iOS 16 and later write totals as WorkoutStatistics; older exports as
	// attributes. The statistics win when both are present.
	if v, ok := convert(w.TotalEnergyBurned, w.TotalEnergyBurnedUnit, kcalPer); ok {
		f["totalEnergyBurned"], f["totalEnergyBurnedUnit"] = v, "kcal"
	}
	if v, ok := convert(w.TotalDistance, w.TotalDistanceUnit, metresPer); ok {
		f["totalDistance"], f["totalDistanceUnit"] = v, "m"
	}
	for _, st := range w.Stats {
		switch {
		case st.Type == "HKQuantityTypeIdentifierActiveEnergyBurned":
			if v, ok := convert(st.Sum, st.Unit, kcalPer); ok {
				f["totalEnergyBurned"], f["totalEnergyBurnedUnit"] = v, "kcal"
			}
		case strings.HasPrefix(st.Type, "HKQuantityTypeIdentifierDistance"):
			if v, ok := convert(st.Sum, st.Unit, metresPer); ok {
				f["totalDistance"], f["totalDistanceUnit"] = v, "m"
			}
		case st.Type == "HKQuantityTypeIdentifierHeartRate" && st.Unit == "count/min":
			for field, raw := range map[string]string{"averageHeartRate": st.Average, "minHeartRate": st.Minimum, "maxHeartRate": st.Maximum} {
				if v, err := strconv.ParseFloat(raw, 64); err == nil {
					f[field] = v
				}
			}
		}
	}
	if len(w.Events) > 0 {
		events := make([]any, 0, len(w.Events))
		for _, ev := range w.Events {
			e := map[string]any{"type": ev.Type}
			if t, err := parseExportTime(ev.Date); err == nil {
				e["date"] = wireTime(t)
			}
			if d, ok := convert(ev.Duration, ev.DurationUnit, secondsPer); ok {
				e["durationS"] = d
			}
			events = append(events, e)
		}
		f["workoutEvents"] = events
	}

	route := ""
	for _, r := range w.Routes {
		if r.File.Path != "" {
			route = r.File.Path
			break
		}
	}
	if route == "" {
		return c.add(TypeWorkout, s.day, f)
	}
	return c.workoutWithRoute(s, route)
}

// workoutWithRoute sends a workout and its route points as a part of their
// own: the server attaches route points to the single workout of their part.
func (c *converter) workoutWithRoute(s sample, route string) error {
	rc, err := c.exp.openFile(route)
	if err != nil {
		c.stats.NoRoute++
		return c.add(TypeWorkout, s.day, s.fields)
	}
	defer rc.Close()
	p := c.newSolo(TypeWorkout, s.day)
	if err := p.add(s.fields); err != nil {
		return err
	}
	dec := xml.NewDecoder(bufio.NewReader(rc))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A broken GPX keeps the points read so far.
			c.stats.Invalid["route"]++
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "trkpt" {
			continue
		}
		var pt gpxPoint
		if err := dec.DecodeElement(&pt, &se); err != nil {
			c.stats.Invalid["route"]++
			break
		}
		line, ok := pt.line()
		if !ok {
			c.stats.Invalid["route"]++
			continue
		}
		c.stats.RoutePoints++
		if err := p.add(line); err != nil {
			return err
		}
	}
	return p.flush()
}

func (c *converter) heartbeat(r xmlRecord) error {
	s, ok := c.newSample(r.Type, r.commonAttrs, r.Metadata)
	if !ok {
		return nil
	}
	if r.Heartbeats == nil || len(r.Heartbeats.Beats) == 0 {
		c.stats.Invalid[r.Type]++
		return nil
	}
	// Beats carry a time of day only, in the export's wall clock: anchor
	// them on the series start's date, rolling over midnight.
	start := s.start
	midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	beats := make([]any, 0, len(r.Heartbeats.Beats))
	for _, b := range r.Heartbeats.Beats {
		tod, ok := parseClock(b.Time)
		if !ok {
			c.stats.Invalid[r.Type]++
			return nil
		}
		at := midnight.Add(tod)
		if at.Before(start.Add(-time.Second)) {
			at = at.AddDate(0, 0, 1)
		}
		beats = append(beats, map[string]any{
			"timeSinceSeriesStart": at.Sub(start).Seconds(),
			"precededByGap":        false,
		})
	}
	s.fields["heartbeats"] = beats
	return c.add(r.Type, s.day, s.fields)
}

func (c *converter) audiogram(a xmlAudiogram) error {
	c.stats.Records++
	s, ok := c.newSample(TypeAudiogram, a.commonAttrs, a.Metadata)
	if !ok {
		return nil
	}
	points := make([]any, 0, len(a.Points))
	for _, p := range a.Points {
		pt := map[string]any{}
		for field, raw := range map[string]string{"frequency": p.Frequency, "leftEarSensitivity": p.Left, "rightEarSensitivity": p.Right} {
			if v, err := strconv.ParseFloat(raw, 64); err == nil {
				pt[field] = v
			}
		}
		points = append(points, pt)
	}
	s.fields["quantity_type"] = "HKDataTypeIdentifierAudiogramSample"
	s.fields["_raw"] = map[string]any{"sensitivityPoints": points}
	return c.add(TypeAudiogram, s.day, s.fields)
}

// encodeLine marshals one NDJSON line, newline included. Map keys are
// sorted, so a line is the same bytes on every run.
func encodeLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if buf.Len() > applehealth.MaxLineBytes {
		return nil, fmt.Errorf("a line is longer than %d bytes", applehealth.MaxLineBytes)
	}
	return buf.Bytes(), nil
}

// add appends a line to the open part of its (type, day).
func (c *converter) add(typ, day string, fields map[string]any) error {
	line, err := encodeLine(fields)
	if err != nil {
		return err
	}
	key := groupKey{typ, day}
	el, ok := c.groups[key]
	if ok && el.Value.(*group).buf.Len()+len(line) > c.opts.MaxPartBytes {
		if err := c.flush(el); err != nil {
			return err
		}
		ok = false
	}
	if !ok {
		el = c.lru.PushBack(&group{key: key})
		c.groups[key] = el
	} else {
		c.lru.MoveToBack(el)
	}
	g := el.Value.(*group)
	g.buf.Write(line)
	g.lines++
	c.buffered += len(line)
	c.stats.Lines++
	for c.buffered > c.opts.MaxBufferedBytes && c.lru.Len() > 0 {
		if err := c.flush(c.lru.Front()); err != nil {
			return err
		}
	}
	return nil
}

func (c *converter) flush(el *list.Element) error {
	g := el.Value.(*group)
	c.lru.Remove(el)
	delete(c.groups, g.key)
	c.buffered -= g.buf.Len()
	return c.send(g.key.typ, g.key.day, g.lines, g.buf.Bytes())
}

// flushAll sends the groups still open, least recently written first.
func (c *converter) flushAll() error {
	for c.lru.Len() > 0 {
		if err := c.flush(c.lru.Front()); err != nil {
			return err
		}
	}
	return nil
}

func (c *converter) send(typ, day string, lines int, ndjson []byte) error {
	p := Part{Number: c.next, Type: typ, Day: day, Lines: lines, NDJSON: ndjson, RecordsRead: c.stats.Records}
	c.next++
	c.stats.Parts++
	return c.emit(p)
}

// solo is a part built outside the (type, day) groups, for lines that must
// travel together: a workout and its route, an ECG and its voltages.
type solo struct {
	c        *converter
	typ, day string
	buf      bytes.Buffer
	lines    int
}

func (c *converter) newSolo(typ, day string) *solo { return &solo{c: c, typ: typ, day: day} }

func (p *solo) add(fields map[string]any) error {
	line, err := encodeLine(fields)
	if err != nil {
		return err
	}
	if p.lines > 0 && p.buf.Len()+len(line) > p.c.opts.MaxPartBytes {
		// Too long for one part: the rest follows in the next one. Route
		// points there are linked by time to the workout once it is stored.
		// (An ECG, 15,360 voltages at most, always fits.)
		if err := p.flush(); err != nil {
			return err
		}
	}
	p.buf.Write(line)
	p.lines++
	p.c.stats.Lines++
	return nil
}

func (p *solo) flush() error {
	if p.lines == 0 {
		return nil
	}
	body := bytes.Clone(p.buf.Bytes())
	p.buf.Reset()
	n := p.lines
	p.lines = 0
	return p.c.send(p.typ, p.day, n, body)
}
