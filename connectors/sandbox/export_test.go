// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import "time"

// SetAccessTTL makes the access tokens the connector's server issues last d,
// from its next Configure on (the end-to-end test shortens it so every pull
// refreshes first).
func SetAccessTTL(c *Connector, d time.Duration) {
	c.mu.Lock()
	c.opts.AccessTTL = d
	c.mu.Unlock()
}

// Refreshes is how many refresh grants the connector's current server
// honoured.
func Refreshes(c *Connector) int64 { return c.Server().refreshes.Load() }

// Exchanges is how many codes the connector's current server traded.
func Exchanges(c *Connector) int64 { return c.Server().exchanges.Load() }
