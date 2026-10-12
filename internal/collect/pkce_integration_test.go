// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package collect_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/collect"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// pkceFake is the fake provider with a PKCE exchange: it records the state
// the callback hands it, and fails if Exchange is called instead.
type pkceFake struct {
	fakeConnector
	mu     *sync.Mutex
	states *[]string
}

func (c pkceFake) Exchange(context.Context, string, string) (connector.Tokens, error) {
	panic("Exchange must not be called for a StateExchanger")
}

func (c pkceFake) ExchangeState(ctx context.Context, code, state, redirect string) (connector.Tokens, error) {
	c.mu.Lock()
	*c.states = append(*c.states, state)
	c.mu.Unlock()
	return c.fakeConnector.Exchange(ctx, code, redirect)
}

func TestCallbackHandsStateToStateExchanger(t *testing.T) {
	var mu sync.Mutex
	var states []string
	e := newConnEnvWrapped(t, collect.SchedulerConfig{Concurrency: 8}, func(c fakeConnector) connector.Connector {
		return pkceFake{fakeConnector: c, mu: &mu, states: &states}
	})
	state := e.start(t, e.keyA, e.userA, "https://app.example.test/done")
	e.fake.grant("pkce-code", "pu-1")
	rec := e.callback(t, state, "pkce-code")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{state}, states)
	assert.Equal(t, "active", e.connStatus(t, e.userA))
}
