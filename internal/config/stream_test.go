// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

func TestLoadStream(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if c.StreamMax != 1000 || c.StreamMaxPerTenant != 10 {
		t.Fatalf("defaults: %d %d", c.StreamMax, c.StreamMaxPerTenant)
	}

	for _, tc := range []struct {
		max, perTenant   string
		wantMax, wantPer int
	}{
		{"50", "", 50, 10},
		{"4", "", 4, 4},
		{"50", "50", 50, 50},
		{"0", "", -1, 10},
		{"0", "3", -1, 3},
		{"", "7", 1000, 7},
	} {
		m := base()
		if tc.max != "" {
			m["OHS_STREAM_MAX"] = tc.max
		}
		if tc.perTenant != "" {
			m["OHS_STREAM_MAX_PER_TENANT"] = tc.perTenant
		}
		c, err := load(t, m)
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if c.StreamMax != tc.wantMax || c.StreamMaxPerTenant != tc.wantPer {
			t.Errorf("%+v: got %d %d", tc, c.StreamMax, c.StreamMaxPerTenant)
		}
		if len(c.Providers) != 0 {
			t.Errorf("%+v: settings leaked as providers: %v", tc, c.Providers)
		}
	}

	for _, tc := range []struct{ name, value, want string }{
		{"OHS_STREAM_MAX", "-1", "OHS_STREAM_MAX"},
		{"OHS_STREAM_MAX", "100001", "OHS_STREAM_MAX"},
		{"OHS_STREAM_MAX", "many", "OHS_STREAM_MAX"},
		{"OHS_STREAM_MAX_PER_TENANT", "0", "OHS_STREAM_MAX_PER_TENANT"},
		{"OHS_STREAM_MAX_PER_TENANT", "1001", "OHS_STREAM_MAX (1000)"},
	} {
		m := base()
		m[tc.name] = tc.value
		_, err := load(t, m)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s=%s: got %v, want an error naming %s", tc.name, tc.value, err, tc.want)
		}
	}
}
