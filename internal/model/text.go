// SPDX-License-Identifier: AGPL-3.0-only

package model

import (
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"
)

// ScrubText makes every string of mr storable: PostgreSQL text and jsonb
// cannot hold the NUL character, and a single one in a device name or a
// metadata value fails the whole write, so the job retries until it is
// parked and every record of the object is lost. NUL is removed and invalid
// UTF-8 replaced with U+FFFD, in fields, metadata values and metadata keys.
func ScrubText(mr *MapResult) {
	if mr == nil {
		return
	}
	scrubValue(reflect.ValueOf(mr).Elem())
}

// ScrubJSON returns v (a decoded JSON value) with every string scrubbed as
// ScrubText does, for raw payloads stored as jsonb. v itself is not changed.
func ScrubJSON(v any) any {
	switch x := v.(type) {
	case string:
		return scrubString(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[scrubString(k)] = ScrubJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ScrubJSON(e)
		}
		return out
	default:
		return v
	}
}

func scrubString(s string) string {
	if strings.IndexByte(s, 0) < 0 && utf8.ValidString(s) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "")
}

// textTypes caches whether a type can hold a string, so the series of
// numbers (ECG voltage, heartbeats, route points) are not walked.
var textTypes sync.Map // reflect.Type -> bool

func holdsText(t reflect.Type) bool {
	if v, ok := textTypes.Load(t); ok {
		return v.(bool)
	}
	textTypes.Store(t, false) // a recursive type stops here
	var has bool
	switch t.Kind() {
	case reflect.String, reflect.Interface, reflect.Map:
		has = true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		has = holdsText(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField() && !has; i++ {
			has = t.Field(i).IsExported() && holdsText(t.Field(i).Type)
		}
	}
	textTypes.Store(t, has)
	return has
}

func scrubValue(v reflect.Value) {
	if !holdsText(v.Type()) {
		return
	}
	switch v.Kind() {
	case reflect.String:
		if s := v.String(); v.CanSet() {
			if c := scrubString(s); c != s {
				v.SetString(c)
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			scrubValue(v.Elem())
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			scrubValue(v.Index(i))
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).IsExported() {
				scrubValue(v.Field(i))
			}
		}
	case reflect.Map:
		if v.IsNil() || v.Type().Key().Kind() != reflect.String {
			return
		}
		if m, ok := v.Interface().(map[string]any); ok {
			for k, e := range m {
				c := scrubString(k)
				if c != k {
					delete(m, k)
				}
				m[c] = ScrubJSON(e)
			}
			return
		}
		for _, k := range v.MapKeys() {
			e := v.MapIndex(k)
			ck, ce := scrubString(k.String()), e
			if e.Kind() == reflect.String {
				ce = reflect.ValueOf(scrubString(e.String())).Convert(e.Type())
			}
			if ck != k.String() || (e.Kind() == reflect.String && ce.String() != e.String()) {
				v.SetMapIndex(k, reflect.Value{})
				v.SetMapIndex(reflect.ValueOf(ck).Convert(v.Type().Key()), ce)
			}
		}
	case reflect.Interface:
		if !v.IsNil() && v.CanSet() {
			if c := ScrubJSON(v.Elem().Interface()); c != nil {
				v.Set(reflect.ValueOf(c))
			}
		}
	}
}
