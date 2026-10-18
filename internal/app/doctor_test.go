// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/connector"
	"github.com/soma-intelligence/open-health-sync/internal/model"
)

type fakeOAuth struct {
	connector.OAuthConnector
	slug string
}

func (f fakeOAuth) Slug() string                     { return f.slug }
func (f fakeOAuth) Enabled(config.ProviderKeys) bool { return true }
func (f fakeOAuth) RequiredKeys() []string           { return []string{"CLIENT_ID", "CLIENT_SECRET"} }
func (f fakeOAuth) Map(connector.RawRecord) (model.MapResult, error) {
	return model.MapResult{}, nil
}

func env(m map[string]string) (func(string) (string, bool), []string) {
	var list []string
	for k, v := range m {
		list = append(list, k+"="+v)
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }, list
}

func goodKey() string { return base64.StdEncoding.EncodeToString(make([]byte, 32)) }

func runDoctorT(t *testing.T, m map[string]string, quick bool, enabled []connector.Connector) (string, error) {
	t.Helper()
	lookup, environ := env(m)
	var buf bytes.Buffer
	err := Doctor(context.Background(), &buf, DoctorOptions{
		Quick: quick, Lookup: lookup, Environ: environ,
		Enabled: func(map[string]config.ProviderKeys) []connector.Connector { return enabled },
	})
	return buf.String(), err
}

func TestDoctorDBUnreachableFails(t *testing.T) {
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   "postgres://ohs:secretpw@127.0.0.1:1/ohs?sslmode=disable",
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, true, nil)
	require.ErrorIs(t, err, ErrDoctorFailed)
	require.Contains(t, out, "fail  database")
	require.Contains(t, out, "ok    raw store")
	require.NotContains(t, out, "secretpw")
}

func TestDoctorQuickSkipsConnectors(t *testing.T) {
	out, _ := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	}, true, nil)
	require.NotContains(t, out, "public url")
	require.NotContains(t, out, "connectors")
}

func TestDoctorPrintsCallbackURL(t *testing.T) {
	out, _ := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":           "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY":         goodKey(),
		"OHS_RAW_STORE":              "file://" + t.TempDir(),
		"OHS_PUBLIC_URL":             "https://ohs.example.com/",
		"OHS_WITHINGS_CLIENT_ID":     "id",
		"OHS_WITHINGS_CLIENT_SECRET": "s",
	}, false, []connector.Connector{fakeOAuth{slug: "withings"}})
	require.Contains(t, out, "ok    connector withings")
	require.Contains(t, out, "https://ohs.example.com/oauth/withings/callback")
}

func TestDoctorMissingKeyPrintsGeneratedKey(t *testing.T) {
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL": "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_RAW_STORE":    "file://" + t.TempDir(),
	}, true, nil)
	require.ErrorIs(t, err, ErrDoctorFailed)
	var key string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "OHS_ENCRYPTION_KEY=") {
			key = strings.TrimPrefix(l, "OHS_ENCRYPTION_KEY=")
		}
	}
	b, derr := base64.StdEncoding.DecodeString(key)
	require.NoError(t, derr)
	require.Len(t, b, 32)
}

func TestDoctorPartialConnector(t *testing.T) {
	lookup, environ := env(map[string]string{
		"OHS_DATABASE_URL":       "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY":     goodKey(),
		"OHS_RAW_STORE":          "file://" + t.TempDir(),
		"OHS_WITHINGS_CLIENT_ID": "id",
	})
	var buf bytes.Buffer
	err := Doctor(context.Background(), &buf, DoctorOptions{
		Lookup: lookup, Environ: environ,
		Enabled: func(map[string]config.ProviderKeys) []connector.Connector { return nil },
		All:     func() []connector.Connector { return []connector.Connector{fakeOAuth{slug: "withings"}} },
	})
	require.ErrorIs(t, err, ErrDoctorFailed)
	require.Contains(t, buf.String(), "fail  connector withings: partially configured, missing OHS_WITHINGS_CLIENT_SECRET")
}

// A keyed connector without any of its keys is listed as disabled, not
// silently left out, and is not a failure.
func TestDoctorListsDisabledConnector(t *testing.T) {
	lookup, environ := env(map[string]string{
		"OHS_DATABASE_URL":   "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
	})
	var buf bytes.Buffer
	_ = Doctor(context.Background(), &buf, DoctorOptions{
		Lookup: lookup, Environ: environ,
		Enabled: func(map[string]config.ProviderKeys) []connector.Connector { return nil },
		All:     func() []connector.Connector { return []connector.Connector{fakeOAuth{slug: "withings"}} },
	})
	require.Contains(t, buf.String(), "skip  connector withings: disabled (no keys)\n")
	require.NotContains(t, buf.String(), "fail  connector")
}

func TestDoctorBadPublicURL(t *testing.T) {
	out, err := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":   "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + t.TempDir(),
		"OHS_PUBLIC_URL":     "not a url",
	}, false, nil)
	require.True(t, errors.Is(err, ErrDoctorFailed))
	require.Contains(t, out, "fail  public url")
}

func TestDoctorRawStoreNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	m := map[string]string{
		"OHS_DATABASE_URL":   "postgres://ohs@127.0.0.1:1/ohs?connect_timeout=1",
		"OHS_ENCRYPTION_KEY": goodKey(),
		"OHS_RAW_STORE":      "file://" + dir,
	}
	out, err := runDoctorT(t, m, false, nil)
	require.ErrorIs(t, err, ErrDoctorFailed)
	require.Contains(t, out, "fail  raw store")
	require.Contains(t, out, "not writable")
	// --quick (the healthcheck, every 10 s) only lists: no write each time.
	out, _ = runDoctorT(t, m, true, nil)
	require.Contains(t, out, "ok    raw store")
}

func TestDoctorSaysWhereFileSecretsCameFrom(t *testing.T) {
	dir := t.TempDir()
	keyFile, tokenFile := dir+"/secrets/encryption-key", dir+"/secrets/setup-token"
	out, _ := runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":        "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY_FILE": keyFile,
		"OHS_SETUP_TOKEN_FILE":    tokenFile,
		"OHS_RAW_STORE":           "file://" + dir,
	}, true, nil)
	require.Contains(t, out, "ok    config")
	require.Contains(t, out, "OHS_ENCRYPTION_KEY generated into "+keyFile)
	require.Contains(t, out, "OHS_SETUP_TOKEN generated into "+tokenFile)
	key, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	require.NotContains(t, out, strings.TrimSpace(string(key)), "the doctor prints the key")
	require.NotContains(t, out, "Paste this generated key")

	out, _ = runDoctorT(t, map[string]string{
		"OHS_DATABASE_URL":        "postgres://ohs@127.0.0.1:1/ohs",
		"OHS_ENCRYPTION_KEY_FILE": keyFile,
		"OHS_RAW_STORE":           "file://" + dir,
	}, true, nil)
	require.Contains(t, out, "OHS_ENCRYPTION_KEY read from "+keyFile)
	require.NotContains(t, out, "OHS_SETUP_TOKEN")
}
