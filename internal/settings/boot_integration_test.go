// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package settings_test

import (
	"context"
	"encoding/base64"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/crypto"
	"github.com/soma-intelligence/open-health-sync/internal/db"
	"github.com/soma-intelligence/open-health-sync/internal/logutil/logtest"
	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

func bootEnv(t *testing.T, url string) (config.Config, func(string) (string, bool), []string) {
	t.Helper()
	kv := map[string]string{"OHS_DATABASE_URL": url, "OHS_ENCRYPTION_KEY": goodKey}
	environ := []string{"OHS_DATABASE_URL=" + url, "OHS_ENCRYPTION_KEY=" + goodKey}
	env := func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	cfg, err := config.Load(env, environ)
	require.NoError(t, err)
	return cfg, env, environ
}

func seed(t *testing.T, url string, put map[string]string) int64 {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	key, err := base64.StdEncoding.DecodeString(goodKey)
	require.NoError(t, err)
	box, err := crypto.NewBox(key)
	require.NoError(t, err)
	rev, err := settings.NewPG(pool, box).Apply(ctx, 0, settings.Change{By: uuid.New(), Put: put})
	require.NoError(t, err)
	return rev
}

func TestBootAppliesStoredSettings(t *testing.T) {
	url := db.TestDB(t)
	rev := seed(t, url, map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "9", "OHS_METRICS_TOKEN": "0123456789abcdefXYZ"})
	envCfg, env, environ := bootEnv(t, url)
	require.Equal(t, 5, envCfg.WebhookPauseAfter)

	cfg, start, err := settings.Boot(context.Background(), envCfg, env, environ)
	require.NoError(t, err)
	require.Equal(t, 9, cfg.WebhookPauseAfter)
	require.Equal(t, "0123456789abcdefXYZ", cfg.MetricsToken)
	require.Equal(t, rev, start.Revision)
	require.Empty(t, start.Invalid)
	require.Empty(t, start.Unknown)
}

func TestBootSkipsInvalidAndUnknown(t *testing.T) {
	url := db.TestDB(t)
	seed(t, url, map[string]string{
		"OHS_WEBHOOK_PAUSE_AFTER": "abc", "OHS_METRICS_TOKEN": "tooshortsecret", "OHS_GONE": "1"})
	envCfg, env, environ := bootEnv(t, url)

	buf := logtest.Capture(t, zerolog.DebugLevel)

	cfg, start, err := settings.Boot(context.Background(), envCfg, env, environ)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.WebhookPauseAfter)
	require.Equal(t, []string{"OHS_METRICS_TOKEN", "OHS_WEBHOOK_PAUSE_AFTER"}, names(sortInvalid(start.Invalid)))
	require.Equal(t, []string{"OHS_GONE"}, start.Unknown)

	logs := buf.String()
	require.Equal(t, 3, strings.Count(logs, `"level":"warn"`), logs)
	for _, n := range []string{"OHS_WEBHOOK_PAUSE_AFTER", "OHS_METRICS_TOKEN", "OHS_GONE"} {
		require.Contains(t, logs, n)
	}
	require.NotContains(t, logs, "tooshortsecret")
}

func sortInvalid(in []settings.Invalid) []settings.Invalid {
	out := append([]settings.Invalid(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
