// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/json"
	"os"
	"testing"
)

// The console's inspector names settings per page kind; each name must be a
// registry entry, or the console would link to a setting that does not exist.
func TestConsoleRelatedSettingsAreRegistered(t *testing.T) {
	raw, err := os.ReadFile("../../web/console/src/inspector/related-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var related map[string][]string
	if err := json.Unmarshal(raw, &related); err != nil {
		t.Fatal(err)
	}
	if len(related) == 0 {
		t.Fatal("related-settings.json names no kind")
	}
	for kind, names := range related {
		for _, name := range names {
			if _, ok := LookupSetting(name); !ok {
				t.Errorf("kind %q names %q, which is not a registry setting", kind, name)
			}
		}
	}
}
