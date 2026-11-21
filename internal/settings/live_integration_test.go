// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package settings_test

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

func TestLiveSecondProcessWithinOneInterval(t *testing.T) {
	url := db.TestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	key, err := base64.StdEncoding.DecodeString(goodKey)
	require.NoError(t, err)
	box, err := crypto.NewBox(key)
	require.NoError(t, err)
	store := settings.NewPG(pool, box)

	cfg, env, environ := bootEnv(t, url)
	mk := func() *settings.Live {
		return settings.NewLive(settings.LiveOptions{Store: store, Env: env, Environ: environ, Config: cfg})
	}
	first, second := mk(), mk()
	go func() { _ = first.Run(ctx, 100*time.Millisecond) }()
	go func() { _ = second.Run(ctx, 100*time.Millisecond) }()
	require.Equal(t, 5, second.WebhookPauseAfter())

	rev, err := store.Apply(ctx, 0, settings.Change{By: uuid.New(), Put: map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "9"}})
	require.NoError(t, err)
	require.NoError(t, first.Reload(ctx))
	require.Equal(t, 9, first.WebhookPauseAfter())
	require.Eventually(t, func() bool { return second.WebhookPauseAfter() == 9 }, time.Second, 20*time.Millisecond)
	require.Equal(t, rev, second.Status().LiveRevision)
}
