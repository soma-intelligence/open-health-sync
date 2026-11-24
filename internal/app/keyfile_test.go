// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soma-intelligence/open-health-sync/internal/config"
)

func TestWriteKeyFileCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "key")
	got, err := writeKeyFile(config.Config{BootstrapKeyFile: path}, "ohs_sk_secret")
	require.NoError(t, err)
	assert.Equal(t, path, got)
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	b, _ := os.ReadFile(path)
	assert.Equal(t, "ohs_sk_secret\n", string(b))
}

func TestWriteKeyFileNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o600))
	_, err := writeKeyFile(config.Config{BootstrapKeyFile: path}, "ohs_sk_new")
	require.Error(t, err)
	assert.ErrorContains(t, err, path)
	assert.ErrorContains(t, err, "OHS_BOOTSTRAP_KEY_FILE")
	b, _ := os.ReadFile(path)
	assert.Equal(t, "old\n", string(b))
}

func TestWriteKeyFileDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	link := filepath.Join(dir, "key")
	require.NoError(t, os.Symlink(target, link))
	_, err := writeKeyFile(config.Config{BootstrapKeyFile: link}, "ohs_sk_new")
	require.Error(t, err)
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "the symlink target must not be created")
}

func TestWriteKeyFileTempFallbackIsUnique(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg := config.Config{RawStore: "s3://bucket/raw"}
	a, err := writeKeyFile(cfg, "k1")
	require.NoError(t, err)
	b, err := writeKeyFile(cfg, "k2")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	st, _ := os.Stat(a)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}
