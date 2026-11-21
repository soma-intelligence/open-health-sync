// SPDX-License-Identifier: AGPL-3.0-only

package settings

import (
	"sort"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

// Invalid is a stored value the process did not use. Message never holds the
// value of a secret.
type Invalid struct{ Name, Message string }

// Resolve runs config.Load on env, then stored, then defaults. Stored names
// not in the registry go to unknown; bootstrap names and names the
// environment sets are left out; a value failing Validate, or named first by
// a Load error (the variable the error starts with when stored, else the
// first stored name the message contains), is dropped into invalid and Load
// runs again. err is a Load error no stored value explains.
func Resolve(env func(string) (string, bool), environ []string, stored map[string]string) (cfg config.Config, invalid []Invalid, unknown []string, err error) {
	use := map[string]string{}
	names := make([]string, 0, len(stored))
	for n := range stored {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s, ok := config.LookupSetting(n)
		switch {
		case !ok:
			unknown = append(unknown, n)
		case s.Apply == config.ApplyBootstrap, config.EnvSet(env, n):
		default:
			if verr := s.Validate(stored[n]); verr != nil {
				invalid = append(invalid, Invalid{Name: n, Message: SafeMessage(s, verr)})
				continue
			}
			use[n] = stored[n]
		}
	}
	for {
		cfg, err = config.Load(config.Stacked(env, use), environ)
		if err == nil {
			return cfg, invalid, unknown, nil
		}
		name := blame(err.Error(), use)
		if name == "" {
			return config.Config{}, invalid, unknown, err
		}
		s, _ := config.LookupSetting(name)
		invalid = append(invalid, Invalid{Name: name, Message: SafeMessage(s, err)})
		delete(use, name)
	}
}

// blame names the stored setting a Load error is about: the one the message
// starts with, else the first one it contains.
func blame(msg string, use map[string]string) string {
	best, at := "", -1
	for n := range use {
		if strings.HasPrefix(msg, n+":") {
			return n
		}
		for from := 0; ; {
			i := strings.Index(msg[from:], n)
			if i < 0 {
				break
			}
			i += from
			end := i + len(n)
			if end < len(msg) && isNameChar(msg[end]) {
				from = i + 1
				continue
			}
			if at < 0 || i < at || (i == at && len(n) > len(best)) {
				best, at = n, i
			}
			break
		}
	}
	return best
}

func isNameChar(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
