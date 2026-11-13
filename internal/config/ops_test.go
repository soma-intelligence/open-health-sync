// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

func TestLoadOpsListen(t *testing.T) {
	// With the api role the API listener already serves the ops routes.
	c, err := load(t, base())
	if err != nil || c.OpsListen != "" || c.OpsListenDefault {
		t.Fatalf("api role, unset: %q %v %v", c.OpsListen, c.OpsListenDefault, err)
	}
	// A worker has no other HTTP: on by default.
	for _, roles := range []string{"collect", "etl", "collect,etl"} {
		m := base()
		m["OHS_ROLES"] = roles
		c, err = load(t, m)
		if err != nil || c.OpsListen != DefaultOpsListen || !c.OpsListenDefault {
			t.Fatalf("roles %s, unset: %q %v %v", roles, c.OpsListen, c.OpsListenDefault, err)
		}
		m["OHS_OPS_LISTEN"] = "off"
		c, err = load(t, m)
		if err != nil || c.OpsListen != "" || c.OpsListenDefault {
			t.Fatalf("roles %s, off: %q %v %v", roles, c.OpsListen, c.OpsListenDefault, err)
		}
		m["OHS_OPS_LISTEN"] = ":9090"
		c, err = load(t, m)
		if err != nil || c.OpsListen != ":9090" || c.OpsListenDefault {
			t.Fatalf("roles %s, set: %q %v %v", roles, c.OpsListen, c.OpsListenDefault, err)
		}
		if len(c.Providers) != 0 {
			t.Fatalf("OHS_OPS_LISTEN read as a provider: %v", c.Providers)
		}
	}
	// Set with the api role: served next to the API.
	m := base()
	m["OHS_OPS_LISTEN"] = "127.0.0.1:9090"
	c, err = load(t, m)
	if err != nil || c.OpsListen != "127.0.0.1:9090" || c.OpsListenDefault {
		t.Fatalf("api role, set: %q %v %v", c.OpsListen, c.OpsListenDefault, err)
	}
	m["OHS_OPS_LISTEN"] = ":8080"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_OPS_LISTEN") {
		t.Fatalf("same address as OHS_LISTEN: %v", err)
	}
	m["OHS_OPS_LISTEN"] = "127.0.0.1:9464"
	m["OHS_METRICS_LISTEN"] = "127.0.0.1:9464"
	if _, err := load(t, m); err == nil || !strings.Contains(err.Error(), "OHS_OPS_LISTEN") {
		t.Fatalf("same address as OHS_METRICS_LISTEN: %v", err)
	}
}
