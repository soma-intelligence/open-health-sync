// SPDX-License-Identifier: AGPL-3.0-only

package all_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/soma-intelligence/open-health-sync/connectors/all"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// Every OAuth provider takes a tenant's own keys: NewInstance makes a
// separate connector, without keys, that its keys configure while the
// registered one keeps its own.
func TestEveryOAuthConnectorTakesTenantKeys(t *testing.T) {
	n := 0
	for _, c := range connector.All() {
		if _, ok := connector.AsOAuth(c); !ok {
			continue
		}
		k, keyed := c.(connector.KeyedConnector)
		if !keyed {
			continue
		}
		n++
		inst, ok := c.(connector.Instancer)
		require.True(t, ok, "%s: an OAuth connector implements connector.Instancer", c.Slug())
		fresh := inst.NewInstance()
		require.NotSame(t, c, fresh, c.Slug())
		assert.Equal(t, c.Slug(), fresh.Slug())
		_, ok = connector.AsOAuth(fresh)
		assert.True(t, ok, c.Slug())
		keys := connector.Config{}
		for _, f := range k.RequiredKeys() {
			keys[f] = "tenant-" + f
		}
		require.NoError(t, fresh.(connector.Configurable).Configure(keys), c.Slug())
		if _, pw := connector.AsPassword(c); pw {
			// No consent page to name the application: the user signs in
			// on ours, with the instance it was configured into.
			continue
		}
		url, err := fresh.(connector.OAuthConnector).AuthorizeURL("state", "https://ohs.example.test/oauth/cb")
		require.NoError(t, err, c.Slug())
		assert.Contains(t, url, "tenant-CLIENT_ID", "%s: the consent page names the tenant's application", c.Slug())
		// The registered connector is not configured by the instance's
		// keys here: it must not have picked up the tenant's.
		if u, err := c.(connector.OAuthConnector).AuthorizeURL("state", "https://ohs.example.test/oauth/cb"); err == nil {
			assert.NotContains(t, u, "tenant-CLIENT_ID", c.Slug())
		}
	}
	assert.Positive(t, n)
}
