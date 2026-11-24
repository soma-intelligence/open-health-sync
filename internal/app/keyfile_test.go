// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
	"github.com/soma-intelligence/open-health-sync/internal/tenant"
)

func writeKey(t *testing.T, cfg config.Config, key string) (string, error) {
	t.Helper()
	f, path, err := openKeyFile(cfg)
	if err != nil {
		return path, err
	}
	_, werr := f.WriteString(key + "\n")
	require.NoError(t, werr)
	require.NoError(t, f.Close())
	return path, nil
}

func TestOpenKeyFileCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "key")
	got, err := writeKey(t, config.Config{BootstrapKeyFile: path}, "ohs_sk_secret")
	require.NoError(t, err)
	assert.Equal(t, path, got)
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	b, _ := os.ReadFile(path)
	assert.Equal(t, "ohs_sk_secret\n", string(b))
}

func TestOpenKeyFileNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o600))
	_, err := writeKey(t, config.Config{BootstrapKeyFile: path}, "ohs_sk_new")
	require.Error(t, err)
	assert.ErrorContains(t, err, path)
	assert.ErrorContains(t, err, "OHS_BOOTSTRAP_KEY_FILE")
	b, _ := os.ReadFile(path)
	assert.Equal(t, "old\n", string(b))
}

func TestOpenKeyFileDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	link := filepath.Join(dir, "key")
	require.NoError(t, os.Symlink(target, link))
	_, err := writeKey(t, config.Config{BootstrapKeyFile: link}, "ohs_sk_new")
	require.Error(t, err)
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "the symlink target must not be created")
}

func TestOpenKeyFileDefaultsInsideTheRawStoreDir(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "raw")
	path, err := writeKey(t, config.Config{RawStore: "file://" + raw}, "k")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(raw, "bootstrap-api-key"), path)
}

func TestOpenKeyFileTempFallbackIsUnique(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg := config.Config{RawStore: "s3://bucket/raw"}
	a, err := writeKey(t, cfg, "k1")
	require.NoError(t, err)
	b, err := writeKey(t, cfg, "k2")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	st, _ := os.Stat(a)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

// fakeTenants records whether a tenant was created.
type fakeTenants struct {
	exists   bool
	created  int
	key      string
	adminErr error
}

func (f *fakeTenants) TenantNamed(context.Context, string) (bool, error) { return f.exists, nil }
func (f *fakeTenants) EnsureTenant(_ context.Context, name string) (tenant.Tenant, string, error) {
	if f.exists {
		return tenant.Tenant{ID: uuid.New(), Name: name, Admin: true}, "", nil
	}
	f.created++
	f.exists = true
	return tenant.Tenant{ID: uuid.New(), Name: name, Admin: f.adminErr == nil}, f.key, nil
}
func (f *fakeTenants) SetAdmin(context.Context, uuid.UUID, bool) error { return f.adminErr }

// A key file that cannot be opened stops the start with no tenant created, so the key is never
// lost with a tenant that already exists.
func TestBootstrapUnwritableKeyFileCreatesNoTenant(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(existing, []byte("old\n"), 0o600))
	for name, path := range map[string]string{
		"existing file":    existing,
		"parent is a file": filepath.Join(existing, "sub", "key"),
	} {
		t.Run(name, func(t *testing.T) {
			st := &fakeTenants{key: "ohs_sk_x"}
			err := bootstrap(context.Background(), config.Config{BootstrapTenant: "dev", BootstrapKeyFile: path}, st)
			require.Error(t, err)
			assert.Zero(t, st.created, "no tenant row is created")
			b, _ := os.ReadFile(existing)
			assert.Equal(t, "old\n", string(b))
		})
	}
}

func TestBootstrapWritesKeyOnceAndLeavesFileAloneAfterwards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	st := &fakeTenants{key: "ohs_sk_x"}
	cfg := config.Config{BootstrapTenant: "dev", BootstrapKeyFile: path}
	require.NoError(t, bootstrap(context.Background(), cfg, st))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "ohs_sk_x\n", string(b))

	// A restart: the tenant exists, the file is not touched and does not stop the start.
	require.NoError(t, bootstrap(context.Background(), cfg, st))
	b, _ = os.ReadFile(path)
	assert.Equal(t, "ohs_sk_x\n", string(b))
	assert.Equal(t, 1, st.created)
}

// Once the tenant is created the key exists nowhere else: a SetAdmin failure keeps the file and
// the error names it.
func TestBootstrapSetAdminFailureKeepsTheKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	st := &fakeTenants{key: "ohs_sk_x", adminErr: errors.New("db gone")}
	err := bootstrap(context.Background(), config.Config{BootstrapTenant: "dev", BootstrapKeyFile: path}, st)
	require.Error(t, err)
	assert.ErrorContains(t, err, path)
	b, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, "ohs_sk_x\n", string(b))
}
