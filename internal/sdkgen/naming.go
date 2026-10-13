// SPDX-License-Identifier: AGPL-3.0-only

package sdkgen

import (
	"strings"
	"unicode"
)

// words splits an identifier in any style into lower-case words:
// "searchFHIRObservations" and "X-OHS-Type" alike.
func words(s string) []string {
	var out []string
	var cur []rune
	rs := []rune(s)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && len(cur) > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// snake is snake_case: list_fhir_observations.
func snake(s string) string { return strings.Join(words(s), "_") }

// camel is camelCase: userId.
func camel(s string) string {
	w := words(s)
	for i := 1; i < len(w); i++ {
		w[i] = upperFirst(w[i])
	}
	return strings.Join(w, "")
}

// pascal upper-cases the first letter of each word and keeps the rest, so
// FHIR and OMH stay as written: listFHIRBundle is ListFHIRBundle, event_types
// is EventTypes.
func pascal(s string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == ' ' }) {
		b.WriteString(upperFirst(part))
	}
	return b.String()
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// singular drops a plural ending, for the name of an element: days is day,
// entries is entry. Words ending in s that are not plurals stay.
func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "ss"), strings.HasSuffix(s, "us"), !strings.HasSuffix(s, "s"):
		return s
	}
	return strings.TrimSuffix(s, "s")
}
