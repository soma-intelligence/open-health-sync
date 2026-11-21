// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// On an instance with no tenant key yet there is nothing to unwrap, so a
// provider that does not work (a KMS key the credentials cannot use, a KMS
// that is down) would only show at the first tenant. Ready makes one round
// trip through it instead, once: a process refuses to start on a provider
// that cannot wrap.
func TestReadyProbesTheProviderBeforeTheFirstKey(t *testing.T) {
	e := newStoreEnv(t)
	ctx := context.Background()
	p := newCountingProvider(t)
	p.down.Store(true)
	v := e.vault(p, Options{CanOpen: true})
	require.ErrorIs(t, v.Ready(ctx), ErrProviderUnavailable)

	p.down.Store(false)
	require.NoError(t, v.Ready(ctx))
	calls := p.calls()
	require.EqualValues(t, 1, p.unwraps.Load(), "one wrap and one unwrap")
	require.NoError(t, v.Ready(ctx))
	require.Equal(t, calls, p.calls(), "the probe is not repeated once it passed")

	// A process that cannot open keys never calls the provider.
	closed := newCountingProvider(t)
	require.NoError(t, e.vault(closed, Options{}).Ready(ctx))
	require.Zero(t, closed.calls())
}
