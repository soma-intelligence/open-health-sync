// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// Source says where the effective value of a setting comes from.
type Source string

// The three sources.
const (
	SourceEnv     Source = "env"
	SourceConsole Source = "console"
	SourceDefault Source = "default"
)

// maxValueBytes bounds a value a console may store.
const maxValueBytes = 4096

// secretSet is what a caller sees of a secret that is set.
const secretSet = "set"

// Entry is one setting as the console shows it.
type Entry struct {
	Setting config.Setting
	Source  Source
	// Value is the effective value; "set" or nil for a secret.
	Value *string
	// StoredValue is set when Source is env and a value is also stored
	// ("set" for a secret).
	StoredValue *string
	// Invalid is the message when the stored value was skipped, else "".
	Invalid string
}

// View is every registry setting at one revision, in registry order.
type View struct {
	Revision int64
	Entries  []Entry
}

// SettingError is a message about one setting.
type SettingError struct{ Name, Message string }

// LockedError is a Patch refused because settings are not the console's to
// change (answer 409).
type LockedError struct{ Settings []SettingError }

func (e *LockedError) Error() string { return "settings: locked: " + summary(e.Settings) }

// InvalidError is a Patch refused because of invalid values (answer 422).
type InvalidError struct{ Settings []SettingError }

func (e *InvalidError) Error() string { return "settings: invalid: " + summary(e.Settings) }

func summary(in []SettingError) string {
	names := make([]string, len(in))
	for i, s := range in {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// PatchResult is what a Patch changed.
type PatchResult struct {
	Revision int64
	// Changed and RestartRequired are sorted, never nil.
	Changed, RestartRequired []string
}

// envValue is the value the environment gives a non secret setting.
func (l *Live) envValue(s config.Setting) *string {
	if s.Secret {
		v := secretSet
		return &v
	}
	v, _ := l.env(s.Name)
	if v == "" {
		if path, ok := l.env(s.Name + "_FILE"); ok && path != "" {
			if b, err := os.ReadFile(path); err == nil {
				v = strings.TrimSpace(string(b))
			}
		}
	}
	return &v
}

// secretInvalid is the one message a secret's problem ever gets: it is never
// derived from the value.
func secretInvalid(s config.Setting) string { return s.Name + ": invalid value" }

// SafeMessage is err's text for a problem with a stored value of s, safe to
// log: a Secret always gets the fixed message, whatever err quotes.
func SafeMessage(s config.Setting, err error) string {
	if s.Secret {
		return secretInvalid(s)
	}
	return err.Error()
}

// storedMap is the rows a process can use: the ones that opened.
func storedMap(st State) map[string]string {
	m := make(map[string]string, len(st.Rows))
	for n, r := range st.Rows {
		if r.OpenErr == nil {
			m[n] = r.Value
		}
	}
	return m
}

// View reads the settings fresh from the store, not from the snapshot.
func (l *Live) View(ctx context.Context) (View, error) {
	st, err := l.store.Load(ctx)
	if err != nil {
		return View{}, err
	}
	_, invalid, _, err := Resolve(l.env, l.environ, storedMap(st))
	if err != nil {
		return View{}, err
	}
	bad := make(map[string]string, len(invalid))
	for _, inv := range invalid {
		bad[inv.Name] = inv.Message
	}
	reg := config.Registry()
	v := View{Revision: st.Revision, Entries: make([]Entry, 0, len(reg))}
	for _, s := range reg {
		e := Entry{Setting: s}
		row, stored := st.Rows[s.Name]
		switch {
		case config.EnvSet(l.env, s.Name):
			e.Source = SourceEnv
			e.Value = l.envValue(s)
			if stored && s.Apply != config.ApplyBootstrap {
				switch {
				case s.Secret:
					sv := secretSet
					e.StoredValue = &sv
				case row.OpenErr == nil:
					sv := row.Value
					e.StoredValue = &sv
				}
			}
		case s.Apply == config.ApplyBootstrap:
			e.Source = SourceDefault
		case stored && row.OpenErr != nil:
			e.Source = SourceDefault
			e.Invalid = openErrMessage
		case stored && bad[s.Name] == "":
			e.Source = SourceConsole
			val := row.Value
			if s.Secret {
				val = secretSet
			}
			e.Value = &val
		default:
			e.Source = SourceDefault
			if m := bad[s.Name]; m != "" {
				e.Invalid = m
				if s.Secret {
					e.Invalid = secretInvalid(s)
				}
			}
		}
		if e.Source == SourceDefault && e.Value == nil && !s.Secret {
			d := s.Default
			e.Value = &d
		}
		v.Entries = append(v.Entries, e)
	}
	return v, nil
}

func hasControl(v string) bool {
	for _, r := range v {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// Patch saves changes (a nil value goes back to the default) if the revision
// is still ifRevision, all or nothing, then reloads this process at once. A
// body that changes no row answers the current revision and writes nothing.
func (l *Live) Patch(ctx context.Context, ifRevision int64, changes map[string]*string, by uuid.UUID) (PatchResult, error) {
	st, err := l.store.Load(ctx)
	if err != nil {
		return PatchResult{}, err
	}
	if st.Revision != ifRevision {
		return PatchResult{}, ErrRevisionMoved
	}
	var skipped map[string]bool // filled when a re-sent value needs it
	names := make([]string, 0, len(changes))
	for n := range changes {
		names = append(names, n)
	}
	sort.Strings(names)

	var invalid, locked []SettingError
	bad := func(n, msg string) { invalid = append(invalid, SettingError{n, msg}) }
	put := map[string]string{}
	var del []string
	for _, n := range names {
		val := changes[n]
		s, known := config.LookupSetting(n)
		if !known {
			if _, stored := st.Rows[n]; val == nil && stored {
				del = append(del, n)
			} else {
				bad(n, "unknown setting")
			}
			continue
		}
		switch {
		case val != nil && *val == "":
			bad(n, "empty: send null to go back to the default")
			continue
		case val != nil && len(*val) > maxValueBytes:
			bad(n, fmt.Sprintf("%s: longer than %d bytes", n, maxValueBytes))
			continue
		case val != nil && hasControl(*val):
			bad(n, n+": holds a control character")
			continue
		}
		switch {
		case s.Apply == config.ApplyBootstrap:
			locked = append(locked, SettingError{n, "bootstrap: set it in the environment"})
			continue
		case config.EnvSet(l.env, n):
			locked = append(locked, SettingError{n, "env: set by the environment, change it there"})
			continue
		}
		row, stored := st.Rows[n]
		if val == nil {
			if stored {
				del = append(del, n)
			}
			continue
		}
		if stored && row.OpenErr == nil && row.Value == *val {
			// The store bumps the revision on a re-save, so drop it here, unless
			// the current rows skip it: then it is checked like a new value.
			if skipped == nil {
				if skipped, err = l.skippedNow(st); err != nil {
					return PatchResult{}, err
				}
			}
			if !skipped[n] {
				continue
			}
		}
		if err := s.Validate(*val); err != nil {
			bad(n, SafeMessage(s, err))
			continue
		}
		put[n] = *val
	}
	if len(invalid) > 0 {
		return PatchResult{}, &InvalidError{Settings: invalid}
	}
	if len(locked) > 0 {
		return PatchResult{}, &LockedError{Settings: locked}
	}

	if len(put)+len(del) > 0 {
		if err := l.dryRun(st, put, del); err != nil {
			return PatchResult{}, err
		}
	}

	res := PatchResult{Revision: st.Revision, Changed: []string{}, RestartRequired: []string{}}
	restart := false
	for _, n := range names {
		_, isPut := put[n]
		if !isPut && !slices.Contains(del, n) {
			continue
		}
		res.Changed = append(res.Changed, n)
		if s, ok := config.LookupSetting(n); ok && s.Apply == config.ApplyRestart {
			res.RestartRequired = append(res.RestartRequired, n)
			restart = true
		}
	}
	if len(res.Changed) == 0 {
		return res, nil
	}
	rev, err := l.store.Apply(ctx, ifRevision, Change{Put: put, Delete: del, Restart: restart, By: by})
	if err != nil {
		return PatchResult{}, err
	}
	res.Revision = rev
	if err := l.Reload(ctx); err != nil {
		// The save went through; the failure is in Status and the loop retries.
		slog.Warn("settings: reload after a save failed", "error", err)
	}
	return res, nil
}

// skippedNow is the names the current rows make a process skip.
func (l *Live) skippedNow(st State) (map[string]bool, error) {
	_, inv, _, err := Resolve(l.env, l.environ, storedMap(st))
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(inv))
	for _, i := range inv {
		m[i.Name] = true
	}
	return m, nil
}

// dryRun resolves the proposed state and refuses it when it makes a setting
// skipped that the current rows do not skip.
func (l *Live) dryRun(st State, put map[string]string, del []string) error {
	cur := storedMap(st)
	_, before, _, err := Resolve(l.env, l.environ, cur)
	if err != nil {
		return err
	}
	skipped := make(map[string]bool, len(before))
	for _, inv := range before {
		skipped[inv.Name] = true
	}
	next := make(map[string]string, len(cur)+len(put))
	for n, v := range cur {
		next[n] = v
	}
	for _, n := range del {
		delete(next, n)
	}
	for n, v := range put {
		next[n] = v
	}
	_, after, _, err := Resolve(l.env, l.environ, next)
	if err != nil {
		return err
	}
	var out []SettingError
	for _, inv := range after {
		if _, touched := put[inv.Name]; skipped[inv.Name] && !touched {
			continue
		}
		msg := inv.Message
		if s, ok := config.LookupSetting(inv.Name); ok && s.Secret {
			msg = secretInvalid(s)
		}
		out = append(out, SettingError{inv.Name, msg})
	}
	if len(out) > 0 {
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return &InvalidError{Settings: out}
	}
	return nil
}

// Export is the settings as an .env file. A secret is never written: a set
// one gets a comment and an empty assignment.
func (l *Live) Export(ctx context.Context) ([]byte, error) {
	v, err := l.View(ctx)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Open Health Sync settings, revision %d\n", v.Revision)
	for _, section := range config.Sections {
		first := true
		for _, e := range v.Entries {
			if e.Setting.Section != section {
				continue
			}
			if first {
				fmt.Fprintf(&b, "\n# %s\n", section)
				first = false
			}
			fmt.Fprintf(&b, "# %s (source %s, applies %s)\n", e.Setting.Label, e.Source, e.Setting.Apply)
			if e.Setting.Secret {
				if e.Value != nil {
					b.WriteString("# set, value not exported\n")
				}
				fmt.Fprintf(&b, "%s=\n", e.Setting.Name)
				continue
			}
			val := ""
			if e.Value != nil {
				val = *e.Value
			}
			fmt.Fprintf(&b, "%s=%s\n", e.Setting.Name, quoteEnv(val))
		}
	}
	return b.Bytes(), nil
}

func quoteEnv(v string) string {
	if !strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || r == '#' || r == '"' || r == '\'' }) {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}
