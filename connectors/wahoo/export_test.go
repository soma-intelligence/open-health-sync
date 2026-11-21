// SPDX-License-Identifier: AGPL-3.0-only

package wahoo

import (
	"context"
	"net/http"
	"time"
)

// PointAt aims a connector (the registered one, in the end-to-end test) at a
// fake of the Wahoo API, without retry waits. Call it before the connector
// is used.
func PointAt(c *Connector, base string, client *http.Client) {
	c.base, c.httpc = base, client
	c.sleep = func(context.Context, time.Duration) error { return nil }
}
