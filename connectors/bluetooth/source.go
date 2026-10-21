// SPDX-License-Identifier: AGPL-3.0-only

package bluetooth

import (
	"strings"
)

// source is who made a reading: the vendor (source slug and display name),
// the device's own name and its model.
type source struct {
	slug        string // source slug; Slug when the vendor is unknown
	vendor      string // display name of the vendor, "" when unknown
	name        string // advertised name, serial-like suffix removed
	deviceModel string // Device Information model number
	deviceID    string // vendor and model ("Polar H10")
}

// vendors are searched for, in order, in the manufacturer name, then in the
// advertised name.
var vendors = []struct{ word, slug, display string }{
	{"polar", "polar", "Polar"},
	{"garmin", "garmin", "Garmin"},
	{"wahoo", "wahoo", "Wahoo"},
	{"tickr", "wahoo", "Wahoo"},
	{"omron", "omron", "OMRON"},
	{"withings", "withings", "Withings"},
	{"beurer", "beurer", "Beurer"},
	{"a&d", "and", "A&D"},
	{"ihealth", "ihealth", "iHealth"},
	{"nonin", "nonin", "Nonin"},
	{"masimo", "masimo", "Masimo"},
	{"roche", "roche", "Roche"},
	{"accu-chek", "roche", "Roche"},
	{"ascensia", "ascensia", "Ascensia"},
	{"contour", "ascensia", "Ascensia"},
	{"coros", "coros", "COROS"},
	{"suunto", "suunto", "Suunto"},
	{"scosche", "scosche", "Scosche"},
	{"decathlon", "decathlon", "Decathlon"},
}

// sourceOf names the vendor and the device of a line.
func sourceOf(d device) source {
	s := source{slug: Slug, name: cleanName(d.Name), deviceModel: strings.TrimSpace(d.Model)}
	for _, text := range []string{d.Manufacturer, d.Name} {
		low := strings.ToLower(text)
		if low == "" {
			continue
		}
		for _, v := range vendors {
			if strings.Contains(low, v.word) {
				s.slug, s.vendor = v.slug, v.display
				break
			}
		}
		if s.vendor != "" {
			break
		}
	}
	switch {
	case s.deviceModel == "":
	case s.vendor == "", strings.HasPrefix(strings.ToLower(s.deviceModel), strings.ToLower(s.vendor)):
		s.deviceID = s.deviceModel
	default:
		s.deviceID = s.vendor + " " + s.deviceModel
	}
	return s
}

// cleanName drops the last word of an advertised name when it looks like a
// serial number: hex digits after a space or an underscore, at least six, or
// at least four with a letter among them. "Polar H10 0A1B2C3D" is "Polar
// H10"; "Edge 1040", a model number, is kept. The serial identifies one
// device and is not kept. The clients apply the same rule before sending.
func cleanName(name string) string {
	name = strings.TrimSpace(name)
	i := strings.LastIndexAny(name, " _")
	if i <= 0 {
		return name
	}
	tail := name[i+1:]
	letter := false
	for _, r := range tail {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F':
			letter = true
		default:
			return name
		}
	}
	if len(tail) < 4 || len(tail) < 6 && !letter {
		return name
	}
	return strings.TrimSpace(name[:i])
}
