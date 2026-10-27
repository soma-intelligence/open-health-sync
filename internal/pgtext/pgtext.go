// SPDX-License-Identifier: AGPL-3.0-only

// Package pgtext checks text before it reaches Postgres. A text or jsonb
// value refuses a NUL character (SQLSTATE 22021 or 22P05) and bytes that are
// not UTF-8: a request carrying one used to fail with a 500 deep in a query.
// The API refuses such input at the edge with Valid, and code that stores
// text it does not control (the audit log) cleans it with Clean.
package pgtext

import (
	"reflect"
	"strings"
	"unicode/utf8"
)

// Valid reports whether Postgres stores s as text: valid UTF-8 with no NUL.
func Valid(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// Clean returns s as Postgres stores it: invalid UTF-8 replaced by U+FFFD
// and every NUL removed.
func Clean(s string) string {
	if Valid(s) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
}

// FirstInvalid walks v (strings, pointers, interfaces, slices, arrays, maps
// and the exported fields of structs) and returns the JSON name of the first
// string that is not Valid, with ok false. The name is the field's json tag
// (or its Go name), "" for v itself or an element of a list or map.
func FirstInvalid(v any) (name string, ok bool) {
	return walk(reflect.ValueOf(v), "")
}

func walk(v reflect.Value, name string) (string, bool) {
	switch v.Kind() {
	case reflect.String:
		if !Valid(v.String()) {
			return name, false
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return walk(v.Elem(), name)
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return "", true // bytes are not text
		}
		for i := range v.Len() {
			if n, ok := walk(v.Index(i), name); !ok {
				return n, false
			}
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if n, ok := walk(it.Key(), name); !ok {
				return n, false
			}
			if n, ok := walk(it.Value(), name); !ok {
				return n, false
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			// An embedded struct of an unexported type still promotes its
			// exported fields.
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			fname := name
			if !f.Anonymous {
				fname = jsonName(f)
			}
			if n, ok := walk(v.Field(i), fname); !ok {
				return n, false
			}
		}
	}
	return "", true
}

func jsonName(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if tag != "" && tag != "-" {
		return tag
	}
	return f.Name
}
