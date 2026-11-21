// SPDX-License-Identifier: AGPL-3.0-only

package config

import "fmt"

// The event stream settings when unset, and the largest OHS_STREAM_MAX.
const (
	DefaultStreamMax          = 1000
	DefaultStreamMaxPerTenant = 10
	MaxStreamMax              = 100_000
)

// loadStream reads the caps of the event stream (GET /v1/events/stream):
// OHS_STREAM_MAX, 0 to turn it off, and OHS_STREAM_MAX_PER_TENANT, from 1 to
// OHS_STREAM_MAX.
func loadStream(get func(name, def string) string, c *Config) error {
	c.StreamMax = DefaultStreamMax
	if v := get("OHS_STREAM_MAX", ""); v != "" {
		n, err := intIn("OHS_STREAM_MAX", 0, MaxStreamMax)(v)
		if err != nil {
			return err
		}
		c.StreamMax = n
		if n == 0 {
			c.StreamMax = -1 // off: the zero of a Config not built by Load is the default
		}
	}
	c.StreamMaxPerTenant = DefaultStreamMaxPerTenant
	if c.StreamMax > 0 {
		c.StreamMaxPerTenant = min(DefaultStreamMaxPerTenant, c.StreamMax)
	}
	if v := get("OHS_STREAM_MAX_PER_TENANT", ""); v != "" {
		// With the stream off, any positive value is accepted and unused.
		n, err := intIn("OHS_STREAM_MAX_PER_TENANT", 1, MaxStreamMax)(v)
		if err == nil && c.StreamMax > 0 && n > c.StreamMax {
			err = fmt.Errorf("OHS_STREAM_MAX_PER_TENANT: %q is not an integer from 1 to OHS_STREAM_MAX (%d)", v, c.StreamMax)
		}
		if err != nil {
			return err
		}
		c.StreamMaxPerTenant = n
	}
	return nil
}
