// SPDX-License-Identifier: AGPL-3.0-only

// Package export writes lists of records as CSV or Parquet files. The
// columns are the record's JSON members, in the order of its struct (an
// embedded struct's members where it is embedded), so a file has the same
// names and values as the REST list it comes from: times are UTC, a missing
// value is empty (CSV) or null (Parquet), and a JSON member (metadata) is
// one column holding its JSON text.
package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
)

// Format is a file format.
type Format string

// Formats.
const (
	CSV     Format = "csv"
	Parquet Format = "parquet"
)

// ParseFormat reads a format name.
func ParseFormat(s string) (Format, bool) {
	switch f := Format(strings.ToLower(s)); f {
	case CSV, Parquet:
		return f, true
	}
	return "", false
}

// ContentType is the media type of a file in f.
func (f Format) ContentType() string {
	if f == Parquet {
		return "application/vnd.apache.parquet"
	}
	return "text/csv; charset=utf-8"
}

// rowGroupRows is the number of rows a Parquet row group holds before the
// writer flushes it.
const rowGroupRows = 64 * 1024

type kind int

const (
	kInt kind = iota
	kFloat
	kString
	kBool
	kTime
	kJSON
)

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
)

// column is one JSON member of a record.
type column struct {
	name     string
	index    []int
	kind     kind
	optional bool
}

// columns lists the columns of t, a struct type.
func columns(t reflect.Type, prefix []int) ([]column, error) {
	var out []column
	for i := range t.NumField() {
		f := t.Field(i)
		index := append(append([]int{}, prefix...), i)
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			sub, err := columns(f.Type, index)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		c := column{name: name, index: index}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			c.optional, ft = true, ft.Elem()
		}
		switch {
		case ft == rawType, ft.Kind() == reflect.Map && ft.Key().Kind() == reflect.String:
			c.kind, c.optional = kJSON, true
		case ft == timeType:
			c.kind = kTime
		case ft.Kind() == reflect.Int || ft.Kind() == reflect.Int64:
			c.kind = kInt
		case ft.Kind() == reflect.Float64:
			c.kind = kFloat
		case ft.Kind() == reflect.String:
			c.kind = kString
		case ft.Kind() == reflect.Bool:
			c.kind = kBool
		default:
			return nil, fmt.Errorf("export: %s.%s: unsupported type %s", t.Name(), f.Name, f.Type)
		}
		out = append(out, c)
	}
	return out, nil
}

// value is the column's value in rec (a struct value), nil when it has none:
// an int64, float64, string, bool, time.Time or, for JSON, its compact text.
func (c column) value(rec reflect.Value) any {
	v := rec.FieldByIndex(c.index)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch c.kind {
	case kInt:
		return v.Int()
	case kFloat:
		return v.Float()
	case kString:
		return v.String()
	case kBool:
		return v.Bool()
	case kTime:
		return v.Interface().(time.Time).UTC()
	}
	if v.Kind() == reflect.Map {
		if v.Len() == 0 {
			return nil
		}
		b, err := json.Marshal(v.Interface())
		if err != nil {
			return nil
		}
		return string(b)
	}
	raw := v.Interface().(json.RawMessage)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// Writer writes records of type T to one file. Close finishes the file
// (Parquet writes its footer then); it does not close the underlying writer.
type Writer[T any] struct {
	cols []column
	csv  *csv.Writer
	pq   *parquet.Writer
	row  reflect.Type
	rows int
}

// NewWriter starts a file of records of type T, a struct, in format f. A
// CSV file starts with its header line.
func NewWriter[T any](w io.Writer, f Format) (*Writer[T], error) {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("export: %s is not a struct", t)
	}
	cols, err := columns(t, nil)
	if err != nil {
		return nil, err
	}
	out := &Writer[T]{cols: cols}
	switch f {
	case CSV:
		out.csv = csv.NewWriter(w)
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
		}
		if err := out.csv.Write(names); err != nil {
			return nil, err
		}
	case Parquet:
		out.row = parquetRow(cols)
		schema := parquet.SchemaOf(reflect.New(out.row).Interface())
		out.pq = parquet.NewWriter(w, schema, parquet.Compression(&parquet.Snappy))
	default:
		return nil, fmt.Errorf("export: unknown format %q", f)
	}
	return out, nil
}

// parquetRow is a struct type with one field per column, tagged with the
// column's name and logical type: times are UTC timestamps in microseconds,
// JSON is a string of logical type JSON, and every column the record may
// leave empty is optional.
func parquetRow(cols []column) reflect.Type {
	fields := make([]reflect.StructField, len(cols))
	for i, c := range cols {
		var t reflect.Type
		tag := c.name
		switch c.kind {
		case kInt:
			t = reflect.TypeFor[int64]()
		case kFloat:
			t = reflect.TypeFor[float64]()
		case kString:
			t = reflect.TypeFor[string]()
		case kBool:
			t = reflect.TypeFor[bool]()
		case kTime:
			t, tag = timeType, tag+",timestamp(microsecond)"
		case kJSON:
			t, tag = reflect.TypeFor[string](), tag+",json"
		}
		if c.optional {
			t, tag = reflect.PointerTo(t), tag+",optional"
		}
		fields[i] = reflect.StructField{Name: "F" + strconv.Itoa(i), Type: t, Tag: reflect.StructTag(`parquet:"` + tag + `"`)}
	}
	return reflect.StructOf(fields)
}

// Write adds recs to the file.
func (w *Writer[T]) Write(recs []T) error {
	for i := range recs {
		rec := reflect.ValueOf(&recs[i]).Elem()
		if w.csv != nil {
			if err := w.csv.Write(w.csvRecord(rec)); err != nil {
				return err
			}
			continue
		}
		row := reflect.New(w.row)
		for j, c := range w.cols {
			v := c.value(rec)
			if v == nil {
				continue
			}
			f := row.Elem().Field(j)
			rv := reflect.ValueOf(v)
			if c.optional {
				p := reflect.New(f.Type().Elem())
				p.Elem().Set(rv)
				rv = p
			}
			f.Set(rv)
		}
		if err := w.pq.Write(row.Interface()); err != nil {
			return err
		}
		if w.rows++; w.rows%rowGroupRows == 0 {
			if err := w.pq.Flush(); err != nil {
				return err
			}
		}
	}
	if w.csv != nil {
		w.csv.Flush()
		return w.csv.Error()
	}
	return nil
}

// csvRecord formats one record: numbers in the shortest form that reads
// back the same, times in RFC 3339 UTC, an empty field for no value. A text
// field a spreadsheet would read as a formula (it starts with =, +, -, @, a
// tab or a carriage return) gets a leading apostrophe.
func (w *Writer[T]) csvRecord(rec reflect.Value) []string {
	out := make([]string, len(w.cols))
	for i, c := range w.cols {
		switch v := c.value(rec).(type) {
		case nil:
		case int64:
			out[i] = strconv.FormatInt(v, 10)
		case float64:
			out[i] = strconv.FormatFloat(v, 'g', -1, 64)
		case bool:
			out[i] = strconv.FormatBool(v)
		case time.Time:
			out[i] = v.Format(time.RFC3339Nano)
		case string:
			if c.kind == kString && v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
				v = "'" + v
			}
			out[i] = v
		}
	}
	return out
}

// Close finishes the file.
func (w *Writer[T]) Close() error {
	if w.csv != nil {
		w.csv.Flush()
		return w.csv.Error()
	}
	return w.pq.Close()
}
