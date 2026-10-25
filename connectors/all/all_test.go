// SPDX-License-Identifier: AGPL-3.0-only

package all_test

import (
	"testing"
	"time"

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

// A sync policy a connector declares must be valid as written: Normalize
// replaces a field it refuses with the default, which would silently change
// how the provider is pulled. The declared policy of a tenant instance is the
// registered one's.
func TestEveryDeclaredSyncPolicyIsValid(t *testing.T) {
	for _, c := range connector.All() {
		p, ok := c.(connector.SyncPolicied)
		if !ok {
			continue
		}
		declared := p.SyncPolicy()
		got := connector.SyncPolicyOf(c)
		d := connector.DefaultSyncPolicy()
		check := func(field string, v, norm, def any) {
			if v != norm && !(isZero(v) && norm == def) {
				t.Errorf("%s: %s %v is out of bounds (used: %v)", c.Slug(), field, v, norm)
			}
		}
		check("PullEvery", declared.PullEvery, got.PullEvery, d.PullEvery)
		check("PullEveryWithoutPush", declared.PullEveryWithoutPush, got.PullEveryWithoutPush, d.PullEveryWithoutPush)
		check("Lookback", declared.Lookback, got.Lookback, d.Lookback)
		check("MaxCatchUp", declared.MaxCatchUp, got.MaxCatchUp, d.MaxCatchUp)
		check("Backfill", declared.Backfill, got.Backfill, d.Backfill)
		check("BackfillChunk", declared.BackfillChunk, got.BackfillChunk, d.BackfillChunk)
		check("Reconcile", declared.Reconcile, got.Reconcile, d.Reconcile)
		if inst, ok := c.(connector.Instancer); ok {
			assert.Equal(t, got, connector.SyncPolicyOf(inst.NewInstance()), c.Slug())
		}
	}
}

func isZero(v any) bool { d, ok := v.(time.Duration); return ok && d == 0 }
