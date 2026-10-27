// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

var allKinds = Table{
	Name: "all_kinds",
	Sealed: []Column{
		{"f", 1, KindFloat64},
		{"i", 2, KindInt64},
		{"b", 3, KindBool},
		{"s", 4, KindText},
		{"j", 5, KindJSON},
		{"t", 6, KindTime},
		{"y", 7, KindBytes},
	},
}

func TestCodecRoundTripEveryKind(t *testing.T) {
	when := time.Date(2026, 10, 27, 13, 45, 12, 123456000, time.UTC)
	cases := map[string][]any{
		"plain":     {1.5, int64(-42), true, "héllo", json.RawMessage(`{"a":1}`), when, []byte{0, 1, 2}},
		"extremes":  {math.MaxFloat64, int64(math.MinInt64), false, "", json.RawMessage(`{}`), time.Time{}, []byte{}},
		"negzero":   {math.Copysign(0, -1), int64(math.MaxInt64), true, "x", json.RawMessage(`[]`), when, []byte{255}},
		"allnull":   {nil, nil, nil, nil, nil, nil, nil},
		"nullmixed": {nil, int64(0), nil, "", nil, time.Time{}, nil},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			enc, err := allKinds.Encode(in)
			if err != nil {
				t.Fatal(err)
			}
			out, err := allKinds.Decode(enc)
			if err != nil {
				t.Fatal(err)
			}
			for i := range in {
				if !same(in[i], out[i]) {
					t.Errorf("field %d: got %#v, want %#v", i, out[i], in[i])
				}
			}
		})
	}

	t.Run("nan bits", func(t *testing.T) {
		nan := math.Float64frombits(0x7ff8000000001234)
		enc, err := allKinds.Encode([]any{nan, nil, nil, nil, nil, nil, nil})
		if err != nil {
			t.Fatal(err)
		}
		out, err := allKinds.Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		if got := math.Float64bits(out[0].(float64)); got != 0x7ff8000000001234 {
			t.Errorf("NaN bits %#x", got)
		}
	})

	t.Run("empty is not null", func(t *testing.T) {
		a, _ := allKinds.Encode([]any{nil, nil, nil, "", nil, nil, nil})
		b, _ := allKinds.Encode([]any{nil, nil, nil, nil, nil, nil, nil})
		if slices.Equal(a, b) {
			t.Fatal("empty string and NULL encode the same")
		}
		out, _ := allKinds.Decode(a)
		if out[3] != "" {
			t.Errorf("got %#v", out[3])
		}
		out, _ = allKinds.Decode(b)
		if out[3] != nil {
			t.Errorf("got %#v", out[3])
		}
	})

	t.Run("zero time is not null", func(t *testing.T) {
		enc, _ := allKinds.Encode([]any{nil, nil, nil, nil, nil, time.Time{}, nil})
		out, _ := allKinds.Decode(enc)
		if tm, ok := out[5].(time.Time); !ok || !tm.IsZero() {
			t.Errorf("got %#v", out[5])
		}
	})

	t.Run("wrong type and arity", func(t *testing.T) {
		if _, err := allKinds.Encode([]any{"x", nil, nil, nil, nil, nil, nil}); err == nil {
			t.Error("string for a float column was accepted")
		}
		if _, err := allKinds.Encode([]any{1.0}); err == nil {
			t.Error("short value list was accepted")
		}
	})
}

// same compares exactly, floats by their bits.
func same(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	case []byte:
		y, ok := b.([]byte)
		return ok && slices.Equal(x, y)
	case json.RawMessage:
		y, ok := b.(json.RawMessage)
		return ok && slices.Equal(x, y)
	}
	return a == b
}

func TestCodecUnknownTag(t *testing.T) {
	enc, err := allKinds.Encode([]any{1.0, nil, nil, nil, nil, nil, nil})
	if err != nil {
		t.Fatal(err)
	}
	// Insert a field with a tag the table does not know, before the end marker.
	bad := append(slices.Clone(enc[:len(enc)-1]), 99, 1, 7, endTag)
	if _, err := allKinds.Decode(bad); err == nil || !strings.Contains(err.Error(), "unknown tag") {
		t.Fatalf("err = %v", err)
	}
	if _, err := allKinds.Decode([]byte{0x7f, endTag}); err == nil {
		t.Fatal("unknown format byte was accepted")
	}
}

func TestCodecTruncated(t *testing.T) {
	enc, err := allKinds.Encode([]any{1.5, int64(300), true, "hello", json.RawMessage(`{"k":"v"}`), time.Unix(1700000000, 0), []byte{1, 2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(enc); n++ {
		if _, err := allKinds.Decode(enc[:n]); err == nil {
			t.Errorf("prefix of %d bytes of %d decoded", n, len(enc))
		}
	}
	if _, err := allKinds.Decode(append(slices.Clone(enc), 0)); err == nil {
		t.Error("trailing byte was accepted")
	}
}

func TestSchemaTagsUnique(t *testing.T) {
	for name, tab := range Schema {
		if tab.Name != name {
			t.Errorf("%s: Name is %q", name, tab.Name)
		}
		tags := map[uint8]string{}
		names := map[string]bool{}
		for _, c := range tab.Sealed {
			if c.Tag == endTag {
				t.Errorf("%s.%s uses the reserved tag 0", name, c.Name)
			}
			if prev, dup := tags[c.Tag]; dup {
				t.Errorf("%s: tag %d on %s and %s", name, c.Tag, prev, c.Name)
			}
			tags[c.Tag] = c.Name
			if names[c.Name] {
				t.Errorf("%s: column %s twice", name, c.Name)
			}
			names[c.Name] = true
		}
		if len(tab.Identity) == 0 {
			t.Errorf("%s: no identity columns", name)
		}
		for _, id := range tab.Identity {
			if names[id] {
				t.Errorf("%s: identity column %s is also sealed", name, id)
			}
		}
	}
}

func TestCodecTypedNilIsNull(t *testing.T) {
	enc, err := allKinds.Encode([]any{nil, nil, nil, nil, json.RawMessage(nil), nil, []byte(nil)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := allKinds.Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if out[4] != nil || out[6] != nil {
		t.Errorf("typed nil came back as %#v and %#v", out[4], out[6])
	}
	// The other typed nil of each kind, and the empty non-nil value, which is not NULL.
	enc, _ = allKinds.Encode([]any{nil, nil, nil, nil, []byte(nil), nil, json.RawMessage(nil)})
	if out, _ = allKinds.Decode(enc); out[4] != nil || out[6] != nil {
		t.Errorf("typed nil came back as %#v and %#v", out[4], out[6])
	}
	enc, _ = allKinds.Encode([]any{nil, nil, nil, nil, nil, nil, []byte{}})
	if out, _ = allKinds.Decode(enc); out[6] == nil {
		t.Error("empty bytes came back as NULL")
	}
}

var update = flag.Bool("update", false, "rewrite testdata/schema_tags.golden")

// TestSchemaTagsGolden pins every (table, column, tag, kind) triple: a tag that
// changes meaning would decode one column as another.
func TestSchemaTagsGolden(t *testing.T) {
	names := make([]string, 0, len(Schema))
	for n := range Schema {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		for _, c := range Schema[n].Sealed {
			fmt.Fprintf(&b, "%s %s %d %d\n", n, c.Name, c.Tag, c.Kind)
		}
	}
	const path = "testdata/schema_tags.golden"
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != b.String() {
		t.Errorf("sealed tags differ from %s (run with -update only if the change is deliberate)\ngot:\n%s", path, b.String())
	}
}

var (
	createTable = regexp.MustCompile(`(?m)^CREATE TABLE ohs_data\.([a-z_]+) \(`)
	alterTable  = regexp.MustCompile(`(?is)ALTER TABLE (?:ONLY )?ohs_data\.([a-z_]+)\s+(.*?);`)
	addColumn   = regexp.MustCompile(`(?i)ADD COLUMN (?:IF NOT EXISTS )?"?([a-z_]+)"?`)
	dropColumn  = regexp.MustCompile(`(?i)DROP COLUMN (?:IF EXISTS )?"?([a-z_]+)"?`)
	colLine     = regexp.MustCompile(`^\s+"?([a-z0-9_]+)"?\s+[a-z]`)
)

// futureColumns are the columns the vault migration adds. A Clear entry may name
// a column no migration creates yet only if it is in this set.
var futureColumns = []string{"sealed", "content_mac", "source_key", "worn", "tz", "derived", "synthesized", "summary_date"}

// migrationColumns returns the columns of each ohs_data table after every
// migration (CREATE TABLE, then ADD COLUMN and DROP COLUMN in file order).
func migrationColumns(t *testing.T) map[string][]string {
	t.Helper()
	files, err := filepath.Glob("../db/migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	out := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, m := range createTable.FindAllStringSubmatchIndex(s, -1) {
			end := strings.Index(s[m[1]:], "\n);")
			if end < 0 {
				t.Fatalf("%s: unterminated CREATE TABLE", f)
			}
			var cs []string
			for _, line := range strings.Split(s[m[1]:m[1]+end], "\n") {
				c := colLine.FindStringSubmatch(line)
				if c == nil {
					continue
				}
				switch strings.ToUpper(c[1]) {
				case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK":
					continue
				}
				cs = append(cs, c[1])
			}
			out[s[m[2]:m[3]]] = cs
		}
		for _, m := range alterTable.FindAllStringSubmatch(s, -1) {
			tab := m[1]
			for _, a := range addColumn.FindAllStringSubmatch(m[2], -1) {
				out[tab] = append(out[tab], a[1])
			}
			for _, d := range dropColumn.FindAllStringSubmatch(m[2], -1) {
				out[tab] = slices.DeleteFunc(out[tab], func(c string) bool { return c == d[1] })
			}
		}
	}
	return out
}

func TestSchemaCoversEveryDataTable(t *testing.T) {
	tables := migrationColumns(t)
	if len(tables) < 10 {
		t.Fatalf("only %d tables found in the migrations", len(tables))
	}
	for name := range tables {
		_, sealed := Schema[name]
		unsealed := slices.Contains(Unsealed, name)
		switch {
		case sealed && unsealed:
			t.Errorf("%s is both sealed and unsealed", name)
		case !sealed && !unsealed:
			t.Errorf("table ohs_data.%s is neither in Schema nor in Unsealed", name)
		}
	}
	for _, name := range Unsealed {
		if _, ok := tables[name]; !ok {
			t.Errorf("Unsealed names %s, which no migration creates", name)
		}
	}
}

// TestSchemaCoversEveryColumn requires each column of each sealed table to be
// in exactly one of Sealed and Clear, so a new column never lands in clear by
// accident.
func TestSchemaCoversEveryColumn(t *testing.T) {
	tables := migrationColumns(t)
	for name, tab := range Schema {
		place := map[string]string{}
		for _, c := range tab.Sealed {
			place[c.Name] = "Sealed"
		}
		for _, c := range tab.Clear {
			if place[c] != "" {
				t.Errorf("%s.%s is in %s and in Clear", name, c, place[c])
			}
			place[c] = "Clear"
		}
		cols, inMigrations := tables[name]
		if !inMigrations {
			continue // quantity_hourly_sealed: no migration yet.
		}
		for _, c := range cols {
			if place[c] == "" {
				t.Errorf("%s.%s is in neither Sealed nor Clear", name, c)
			}
		}
		for c := range place {
			if !slices.Contains(cols, c) && !slices.Contains(futureColumns, c) {
				t.Errorf("%s.%s is listed but no migration creates it", name, c)
			}
		}
		for _, id := range tab.Identity {
			if place[id] != "Clear" {
				t.Errorf("%s: identity column %s is not in Clear", name, id)
			}
		}
	}
}
