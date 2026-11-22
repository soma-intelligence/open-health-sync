// SPDX-License-Identifier: AGPL-3.0-only

package settings_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/settings"
)

var goodKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func envOf(extra map[string]string) (func(string) (string, bool), []string) {
	kv := map[string]string{"OHS_DATABASE_URL": "postgres://x", "OHS_ENCRYPTION_KEY": goodKey}
	for k, v := range extra {
		kv[k] = v
	}
	var environ []string
	for k, v := range kv {
		environ = append(environ, k+"="+v)
	}
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }, environ
}

func names(inv []settings.Invalid) []string {
	var out []string
	for _, i := range inv {
		out = append(out, i.Name)
	}
	return out
}

func TestResolveStoredApplies(t *testing.T) {
	env, environ := envOf(nil)
	cfg, invalid, unknown, err := settings.Resolve(env, environ, map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "9"})
	require.NoError(t, err)
	require.Equal(t, 9, cfg.WebhookPauseAfter)
	require.Empty(t, invalid)
	require.Empty(t, unknown)
}

func TestResolveEnvWins(t *testing.T) {
	stored := map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "9"}
	env, environ := envOf(map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "3"})
	cfg, invalid, _, err := settings.Resolve(env, environ, stored)
	require.NoError(t, err)
	require.Equal(t, 3, cfg.WebhookPauseAfter)
	require.Empty(t, invalid)

	env, environ = envOf(map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": ""})
	cfg, _, _, err = settings.Resolve(env, environ, stored)
	require.NoError(t, err)
	require.Equal(t, 9, cfg.WebhookPauseAfter)
}

func TestResolveSkipsInvalid(t *testing.T) {
	env, environ := envOf(nil)
	cfg, invalid, _, err := settings.Resolve(env, environ, map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "abc"})
	require.NoError(t, err)
	require.Equal(t, 5, cfg.WebhookPauseAfter)
	require.Equal(t, []string{"OHS_WEBHOOK_PAUSE_AFTER"}, names(invalid))
	require.NotEmpty(t, invalid[0].Message)
}

func TestResolveCrossField(t *testing.T) {
	env, environ := envOf(nil)
	cfg, invalid, _, err := settings.Resolve(env, environ, map[string]string{
		"OHS_WEBHOOK_CONCURRENCY": "2", "OHS_WEBHOOK_PER_TENANT": "4"})
	require.NoError(t, err)
	require.Equal(t, 2, cfg.WebhookConcurrency)
	require.Equal(t, 1, cfg.WebhookPerTenant)
	require.Equal(t, []string{"OHS_WEBHOOK_PER_TENANT"}, names(invalid))

	env, environ = envOf(map[string]string{"OHS_WEBHOOK_PER_TENANT": "4"})
	cfg, invalid, _, err = settings.Resolve(env, environ, map[string]string{"OHS_WEBHOOK_CONCURRENCY": "2"})
	require.NoError(t, err)
	require.Equal(t, 4, cfg.WebhookPerTenant)
	require.Equal(t, []string{"OHS_WEBHOOK_CONCURRENCY"}, names(invalid))
}

func TestResolveIgnoresBootstrapAndUnknown(t *testing.T) {
	env, environ := envOf(map[string]string{"OHS_LISTEN": ":7"})
	cfg, invalid, unknown, err := settings.Resolve(env, environ, map[string]string{
		"OHS_LISTEN": ":9", "OHS_GONE": "1"})
	require.NoError(t, err)
	require.Equal(t, ":7", cfg.Listen)
	require.Empty(t, invalid)
	require.Equal(t, []string{"OHS_GONE"}, unknown)
}

func TestResolveSecretMessage(t *testing.T) {
	env, environ := envOf(nil)
	_, invalid, _, err := settings.Resolve(env, environ, map[string]string{"OHS_METRICS_TOKEN": "short"})
	require.NoError(t, err)
	require.Len(t, invalid, 1)
	require.Equal(t, "OHS_METRICS_TOKEN", invalid[0].Name)
	require.False(t, strings.Contains(invalid[0].Message, "short"), invalid[0].Message)
}

func TestResolveUnexplainedErrorReturned(t *testing.T) {
	env, environ := envOf(map[string]string{"OHS_ENCRYPTION_KEY": "nope"})
	_, _, _, err := settings.Resolve(env, environ, map[string]string{"OHS_WEBHOOK_PAUSE_AFTER": "9"})
	require.Error(t, err)
}
