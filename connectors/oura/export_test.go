// SPDX-License-Identifier: AGPL-3.0-only

package oura

import "net/http"

// PointAt aims a connector (the registered one, in the end-to-end test) at a
// fake of the Oura API, without pacing between pages. Call it before the
// connector is used.
func PointAt(c *Connector, base string, client *http.Client) {
	c.accountBase, c.apiBase, c.httpc = base, base, client
	c.pageDelay = -1
}
