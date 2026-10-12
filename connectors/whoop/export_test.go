// SPDX-License-Identifier: AGPL-3.0-only

package whoop

import "net/http"

// PointAt sends c (for example the connector registered in init) to a fake
// WHOOP serving both the OAuth server and the API at base, through client,
// without pacing. For tests in package whoop_test, which may import the app.
// Call it before the connector serves anything.
func PointAt(c *Connector, base string, client *http.Client) {
	c.authBase, c.apiBase, c.httpc, c.pageDelay = base, base, client, -1
}
