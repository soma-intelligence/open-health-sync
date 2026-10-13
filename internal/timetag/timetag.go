// SPDX-License-Identifier: AGPL-3.0-only

// Package timetag stores time tags: intervals that give context to a user's
// data (an illness, a trip, jet lag, a vaccination), recorded through the API
// and read by the layers built on top of it. A tag is context, never a
// measurement: the server computes nothing from it.
//
// The REST API and the MCP server both call this package, and every query
// filters on the caller's tenant and user. A user of another tenant, or one
// being deleted, is ErrUserNotFound. Labels and notes are text a person
// wrote: they are health data, never logged and never put in an error.
package timetag

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Limits on a tag's fields, in characters.
const (
	MaxLabel    = 100
	MaxNote     = 2000
	MaxTimezone = 64
	MaxSource   = 64
)

// DefaultSource is the source of a tag created without one.
const DefaultSource = "user"

// Kinds is the controlled vocabulary of tag kinds, in documentation order.
var Kinds = []string{"illness", "injury", "travel", "jet_lag", "vaccination", "medication", "alcohol", "other"}

// KnownKind reports whether k is one of Kinds.
func KnownKind(k string) bool { return slices.Contains(Kinds, k) }

// The range a tag's times must fall in: wide enough for a childhood
// vaccination, narrow enough to catch a unit mistake.
var (
	minTime = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	maxTime = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

var (
	// ErrUserNotFound means the user is not an active user of the tenant.
	ErrUserNotFound = errors.New("timetag: user not found")
	// ErrNotFound means the tag is not one of the user's.
	ErrNotFound = errors.New("timetag: tag not found")
	// ErrInvalidCursor means a list cursor could not be decoded.
	ErrInvalidCursor = errors.New("timetag: invalid cursor")
)

// InvalidError is a caller mistake (the API answers 400 with Msg). Msg names
// the field, never its value.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &InvalidError{Msg: fmt.Sprintf(format, args...)}
}

// Tag is one interval of one user. EndAt nil is a tag still ongoing.
type Tag struct {
	ID        uuid.UUID  `json:"id"`
	Kind      string     `json:"kind"`
	Label     *string    `json:"label"`
	StartAt   time.Time  `json:"start_at"`
	EndAt     *time.Time `json:"end_at"`
	Timezone  *string    `json:"timezone"`
	Note      *string    `json:"note"`
	Source    string     `json:"source"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Fields are what a caller sets on a tag.
type Fields struct {
	Kind     string
	Label    *string
	StartAt  time.Time
	EndAt    *time.Time
	Timezone *string
	Note     *string
	// Source is who recorded the tag: empty means DefaultSource.
	Source string
}

// Check validates f and normalizes it: times in UTC to the microsecond (what
// the database keeps), an empty label, note or timezone as none, an empty
// source as DefaultSource.
func (f *Fields) Check() error {
	switch {
	case f.Kind == "":
		return invalid("kind is required")
	case !KnownKind(f.Kind):
		return invalid("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	if f.StartAt.IsZero() {
		return invalid("start_at is required")
	}
	f.StartAt = f.StartAt.UTC().Truncate(time.Microsecond)
	if !inRange(f.StartAt) {
		return invalid("start_at must be between 1900 and 2200")
	}
	if f.EndAt != nil {
		end := f.EndAt.UTC().Truncate(time.Microsecond)
		f.EndAt = &end
		if !inRange(end) {
			return invalid("end_at must be between 1900 and 2200")
		}
		if !end.After(f.StartAt) {
			return invalid("end_at must be after start_at (null for a tag still ongoing)")
		}
	}
	f.Label, f.Note, f.Timezone = orNil(f.Label), orNil(f.Note), orNil(f.Timezone)
	if f.Label != nil && utf8.RuneCountInString(*f.Label) > MaxLabel {
		return invalid("label is at most %d characters", MaxLabel)
	}
	if f.Note != nil && utf8.RuneCountInString(*f.Note) > MaxNote {
		return invalid("note is at most %d characters", MaxNote)
	}
	if f.Timezone != nil && !ValidTimezone(*f.Timezone) {
		return invalid("timezone must be an IANA time zone name, for example Europe/Paris")
	}
	if f.Source == "" {
		f.Source = DefaultSource
	}
	if !validSource(f.Source) {
		return invalid("source must be 1 to %d letters, digits, '.', '_', ':' or '-'", MaxSource)
	}
	return nil
}

func inRange(t time.Time) bool { return !t.Before(minTime) && t.Before(maxTime) }

func orNil(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// ValidTimezone reports whether name is an IANA time zone the server knows.
// "Local" (the server's own zone) is not one.
func ValidTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > MaxTimezone {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

func validSource(s string) bool {
	if s == "" || len(s) > MaxSource {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == ':', r == '-':
		default:
			return false
		}
	}
	return true
}

// Change is one field of a Patch. Set false leaves the field as it is; Set
// with a nil Value clears it.
type Change[T any] struct {
	Set   bool
	Value *T
}

// Patch changes some fields of a tag. The source is kept: it says who
// recorded the tag.
type Patch struct {
	Kind     Change[string]
	Label    Change[string]
	StartAt  Change[time.Time]
	EndAt    Change[time.Time]
	Timezone Change[string]
	Note     Change[string]
}

// check refuses clearing a field every tag has.
func (p Patch) check() error {
	if p.Kind.Set && p.Kind.Value == nil {
		return invalid("kind cannot be null")
	}
	if p.StartAt.Set && p.StartAt.Value == nil {
		return invalid("start_at cannot be null")
	}
	return nil
}

// apply returns the fields of t with p applied, and whether p changes any.
func (p Patch) apply(t Tag) (Fields, bool) {
	f := Fields{Kind: t.Kind, Label: t.Label, StartAt: t.StartAt, EndAt: t.EndAt,
		Timezone: t.Timezone, Note: t.Note, Source: t.Source}
	changed := false
	if p.Kind.Set {
		f.Kind, changed = *p.Kind.Value, true
	}
	if p.StartAt.Set {
		f.StartAt, changed = *p.StartAt.Value, true
	}
	for _, c := range []struct {
		ch  Change[string]
		dst **string
	}{{p.Label, &f.Label}, {p.Timezone, &f.Timezone}, {p.Note, &f.Note}} {
		if c.ch.Set {
			*c.dst, changed = c.ch.Value, true
		}
	}
	if p.EndAt.Set {
		f.EndAt, changed = p.EndAt.Value, true
	}
	return f, changed
}
