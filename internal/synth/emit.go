// SPDX-License-Identifier: AGPL-3.0-only

package synth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Doc is one document in a provider's format, kept as a tree so the document
// scenarios can change it before it is written.
type Doc = map[string]any

// Shape tells the document scenarios what the fields of a document mean,
// from the vendor's documentation. Paths are dot separated keys into nested
// objects; a number is an index into an array.
type Shape struct {
	// ID is the path of the record's id, empty when the document has none.
	ID string
	// Required are the fields the documentation marks required, Optional
	// some it marks optional.
	Required, Optional []string
	// Numbers are measured values, each with the range a body or device
	// produces.
	Numbers map[string][2]float64
	// Counts are values that cannot be negative (steps, distance, energy).
	Counts []string
	// Times are timestamps; Start and End, when set, bound the record.
	Times      []string
	Start, End string
	// Enums are fields with a closed list of values.
	Enums []string
	// Units are fields holding a unit string.
	Units []string
	// Strings are free text fields (names, notes).
	Strings []string
	// TimeLayout writes a time for the Timestamps scenario's out of range
	// values: time.RFC3339 when empty. Epoch says the vendor writes seconds
	// since 1970 as a number, EpochMillis milliseconds.
	TimeLayout         string
	Epoch, EpochMillis bool
	// Closed says the line is written by this server, not a vendor: a field
	// it does not know is a fault, so the unknown field variant is skipped.
	Closed bool
}

// Emitter collects a renderer's documents and applies the document scenarios.
type Emitter struct {
	tl    *Timeline
	r     *Rand
	lines []Line
	// seen counts documents by data type, so every mutation lands on the
	// first document of each type and then on a spread of the others.
	seen map[string]int
	// cursor is where each data type is in its cycle of mutations.
	cursor  map[string]int
	pending []Line
	// docs holds the mutated document of each pending line, marshalled
	// when kept; nil for a line already in bytes.
	docs []Doc
	// UnknownType is a data type the provider's connector does not know,
	// for the Unknown scenario; empty to skip.
	UnknownType string
}

// NewEmitter returns an emitter for one provider.
func NewEmitter(tl *Timeline, provider string) *Emitter {
	return &Emitter{tl: tl, r: NewRand(tl.Opt.Seed, "emit/"+provider), seen: map[string]int{}, cursor: map[string]int{}}
}

// perDoc is how many mutations a later document of a type gets. The first
// document gets all of them, so every type sees every defect at least once
// while the volume stays proportional to the clean data.
const perDoc = 3

// Raw adds a line as is.
func (e *Emitter) Raw(l Line) { e.lines = append(e.lines, l) }

// Add writes a valid document and, depending on the scenarios, copies of it
// with the defects they ask for.
func (e *Emitter) Add(dataType string, day time.Time, d Doc, sh Shape) {
	day = day.UTC().Truncate(24 * time.Hour)
	n := e.seen[dataType]
	e.seen[dataType] = n + 1
	o := e.tl.Opt
	// The first document gets every mutation, then one in eight gets the
	// next few of the cycle. The others are written as they are: the
	// normalized tree marshals to the same bytes.
	if n%8 != 0 {
		e.lines = append(e.lines, Line{DataType: dataType, Day: day, JSON: marshal(d)})
		return
	}
	d = normalize(d)
	e.lines = append(e.lines, Line{DataType: dataType, Day: day, JSON: marshal(d)})
	e.pending = e.pending[:0]
	e.docs = e.docs[:0]
	defer e.flush(dataType, n == 0)
	// A mutated document is marshalled only if the cycle keeps it.
	add := func(doc Doc, f Fault, tag string) {
		e.pending = append(e.pending, Line{DataType: dataType, Day: day, Fault: f, Tag: tag})
		e.docs = append(e.docs, doc)
	}
	if o.Has(Duplicates) {
		add(d, FaultNone, "duplicate")
		if sh.ID != "" {
			if c, ok := e.revise(d, sh); ok {
				add(c, FaultNone, "revision")
			}
		}
	}
	if o.Has(Missing) {
		for _, p := range sh.Optional {
			if c, ok := without(d, p); ok {
				add(c, FaultNone, "optional_absent:"+p)
			}
		}
		for _, p := range sh.Required {
			if c, ok := without(d, p); ok {
				add(c, FaultMissingField, "required_absent:"+p)
			}
			if c, ok := set(d, p, nil); ok {
				add(c, FaultNullField, "null:"+p)
			}
		}
	}
	if o.Has(OutOfRange) {
		for p, rg := range sortedRanges(sh.Numbers) {
			for _, v := range []float64{-rg[1], rg[1] * 10, 0} {
				if v == 0 && rg[0] <= 0 {
					continue
				}
				if c, ok := set(d, p, v); ok {
					add(c, FaultOutOfRange, fmt.Sprintf("out_of_range:%s=%g", p, v))
				}
			}
		}
		for _, p := range sh.Counts {
			if c, ok := set(d, p, -1); ok {
				add(c, FaultOutOfRange, "negative:"+p)
			}
		}
		if sh.Start != "" && sh.End != "" {
			s, sok := get(d, sh.Start)
			en, eok := get(d, sh.End)
			if sok && eok {
				c := clone(d)
				setIn(c, sh.Start, en)
				setIn(c, sh.End, s)
				if fmt.Sprint(s) != fmt.Sprint(en) {
					add(c, FaultTimestamp, "end_before_start")
				}
			}
		}
	}
	if o.Has(Timestamps) {
		for _, p := range sh.Times {
			for _, t := range []time.Time{time.Unix(0, 0).UTC(), time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)} {
				if c, ok := set(d, p, e.timeValue(sh, t)); ok {
					add(c, FaultTimestamp, "time:"+p+"="+t.Format("2006"))
				}
			}
			if c, ok := set(d, p, "not a date"); ok {
				add(c, FaultTimestamp, "time:"+p+"=garbage")
			}
		}
	}
	if o.Has(Unknown) {
		if !sh.Closed {
			c := clone(d)
			c["x_future_field"] = map[string]any{"added_in": "a later API version", "values": []any{1, 2, 3}}
			add(c, FaultNone, "unknown_field")
		}
		for _, p := range sh.Enums {
			if c, ok := set(d, p, "SOMETHING_NEW"); ok {
				add(c, FaultUnknownEnum, "unknown_enum:"+p)
			}
		}
		if n == 0 && e.UnknownType != "" {
			e.pending = append(e.pending, Line{DataType: e.UnknownType, Day: day, JSON: marshal(d), Fault: FaultUnknownType, Tag: "unknown_type"})
			e.docs = append(e.docs, nil)
		}
	}
	if o.Has(UnitVariants) {
		for _, p := range sh.Units {
			if c, ok := set(d, p, "furlong/fortnight"); ok {
				add(c, FaultUnit, "unit:"+p+"=unknown")
			}
		}
	}
	if o.Has(Malformed) {
		b := marshal(d)
		e.malformed(dataType, day, b)
		for p := range sortedRanges(sh.Numbers) {
			if v, ok := get(d, p); ok {
				if c, ok := set(d, p, fmt.Sprint(v)); ok {
					add(c, FaultWrongType, "number_as_string:"+p)
				}
				if c, ok := set(d, p, map[string]any{"value": v}); ok {
					add(c, FaultWrongType, "number_as_object:"+p)
				}
			}
			break
		}
		for _, p := range sh.Strings {
			if c, ok := set(d, p, "Name \x00 with NUL"); ok {
				add(c, FaultControlChar, "nul:"+p)
			}
			if c, ok := set(d, p, "Rêve 😴 שינה"+strings.Repeat("·", 3)); ok {
				add(c, FaultNone, "unicode:"+p)
			}
			if c, ok := set(d, p, strings.Repeat("x", 70000)); ok {
				add(c, FaultNone, "long_string:"+p)
			}
		}
	}
}

// flush keeps all the pending mutations, or the next perDoc of the type's
// cycle.
func (e *Emitter) flush(dataType string, all bool) {
	keep := func(i int) {
		l := e.pending[i]
		if l.JSON == nil && i < len(e.docs) && e.docs[i] != nil {
			l.JSON = marshal(e.docs[i])
		}
		e.lines = append(e.lines, l)
	}
	if all || len(e.pending) <= perDoc {
		for i := range e.pending {
			keep(i)
		}
		return
	}
	c := e.cursor[dataType]
	for k := 0; k < perDoc; k++ {
		keep((c + k) % len(e.pending))
	}
	e.cursor[dataType] = c + perDoc
}

// malformed adds lines that are not a valid JSON object.
func (e *Emitter) malformed(dataType string, day time.Time, b []byte) {
	add := func(j []byte, tag string) {
		e.pending = append(e.pending, Line{DataType: dataType, Day: day, JSON: j, Fault: FaultMalformed, Tag: tag})
		e.docs = append(e.docs, nil)
	}
	add(b[:len(b)/2], "truncated")
	add(append([]byte("\xef\xbb\xbf"), b...), "bom")
	add([]byte("[]"), "array")
	add([]byte("null"), "null")
	add([]byte(`"a string"`), "string")
	add([]byte("{}"), "empty_object")
	add(bytes.Replace(b, []byte(`"`), []byte("\xff\xfe"), 1), "invalid_utf8")
	add([]byte(`{"value":NaN}`), "nan")
	add([]byte(`{"value":1e400}`), "huge_number")
	add([]byte(strings.Repeat("[", 10000)+strings.Repeat("]", 10000)), "deep_nesting")
}

func (e *Emitter) timeValue(sh Shape, t time.Time) any {
	switch {
	case sh.Epoch:
		return t.Unix()
	case sh.EpochMillis:
		return t.UnixMilli()
	case sh.TimeLayout != "":
		return t.Format(sh.TimeLayout)
	}
	return t.Format(time.RFC3339)
}

// revise returns the document with its numbers changed and its id kept: the
// vendor re-scored or the user edited the record.
func (e *Emitter) revise(d Doc, sh Shape) (Doc, bool) {
	c := clone(d)
	changed := false
	for p := range sortedRanges(sh.Numbers) {
		if v, ok := get(c, p); ok {
			if n, ok := v.(json.Number); ok {
				// Two percent lower: the corrected value stays inside
				// any range the original was in, SpO2 1.0 included.
				if f, err := n.Float64(); err == nil {
					v := round(f*0.98, 3)
					if _, err := n.Int64(); err == nil {
						// An integer stays one: a scale or a count has no
						// fractions.
						v = math.Round(v)
					}
					setIn(c, p, v)
				}
				changed = true
			}
		}
	}
	return c, changed
}

// Lines returns everything emitted, sorted.
func (e *Emitter) Lines() []Line { return Sorted(e.lines) }

// sortedRanges iterates a range map in key order.
func sortedRanges(m map[string][2]float64) func(func(string, [2]float64) bool) {
	return func(yield func(string, [2]float64) bool) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

// marshal writes a document as one line; HTML escaping off, as vendors do.
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("synth: marshal: %v", err))
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Marshal is marshal for renderers.
func Marshal(v any) []byte { return marshal(v) }

func clone(v Doc) Doc { return cloneAny(v).(Doc) }

func cloneAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(x))
		for k, vv := range x {
			c[k] = cloneAny(vv)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, vv := range x {
			c[i] = cloneAny(vv)
		}
		return c
	case []map[string]any:
		c := make([]any, len(x))
		for i, vv := range x {
			c[i] = cloneAny(vv)
		}
		return c
	}
	return v
}

func get(d Doc, path string) (any, bool) {
	var cur any = d
	for _, k := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[k]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// setIn sets path in d; the parent must exist.
func setIn(d Doc, path string, v any) bool {
	keys := strings.Split(path, ".")
	var cur any = d
	for i, k := range keys {
		last := i == len(keys)-1
		switch x := cur.(type) {
		case map[string]any:
			if last {
				if _, ok := x[k]; !ok {
					return false
				}
				x[k] = v
				return true
			}
			cur = x[k]
		case []any:
			j, err := strconv.Atoi(k)
			if err != nil || j < 0 || j >= len(x) {
				return false
			}
			if last {
				x[j] = v
				return true
			}
			cur = x[j]
		default:
			return false
		}
	}
	return false
}

func set(d Doc, path string, v any) (Doc, bool) {
	c := clone(d)
	return c, setIn(c, path, v)
}

// normalize turns a document built with Go types into the tree the JSON
// decoder gives (objects, arrays, json.Number), so paths work the same on
// every document.
func normalize(d Doc) Doc {
	dec := json.NewDecoder(bytes.NewReader(marshal(d)))
	dec.UseNumber()
	var out Doc
	if err := dec.Decode(&out); err != nil {
		panic(fmt.Sprintf("synth: normalize: %v", err))
	}
	return out
}

func without(d Doc, path string) (Doc, bool) {
	c := clone(d)
	keys := strings.Split(path, ".")
	parent := c
	if len(keys) > 1 {
		p, ok := get(c, strings.Join(keys[:len(keys)-1], "."))
		if !ok {
			return nil, false
		}
		m, ok := p.(map[string]any)
		if !ok {
			return nil, false
		}
		parent = m
	}
	k := keys[len(keys)-1]
	if _, ok := parent[k]; !ok {
		return nil, false
	}
	delete(parent, k)
	return c, true
}
