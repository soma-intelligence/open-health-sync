// SPDX-License-Identifier: AGPL-3.0-only

package rawstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFS(t *testing.T) {
	s, err := Open("file://" + t.TempDir())
	require.NoError(t, err)
	RunConformance(t, s)
}

func TestFSNoLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open("file://" + dir)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "raw/t/k", strings.NewReader("v")))
	require.Error(t, s.Put(ctx, "raw/t/k", strings.NewReader("w")))
	ents, err := os.ReadDir(filepath.Join(dir, "raw", "t"))
	require.NoError(t, err)
	require.Len(t, ents, 1)
}

func TestFSDeletePrunesEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open("file://" + dir)
	require.NoError(t, s.Put(context.Background(), "raw/t/p/q/k", strings.NewReader("v")))
	require.NoError(t, s.DeletePrefix(context.Background(), "raw/t/p/"))
	_, err := os.Stat(filepath.Join(dir, "raw", "t", "p"))
	require.True(t, os.IsNotExist(err))
}

func TestOpenErrors(t *testing.T) {
	for _, u := range []string{"ftp://x", "file://host/x", "file:relative", "s3://", "::bad"} {
		_, err := Open(u)
		require.Error(t, err, u)
	}
}

func TestFSTempLeftoversRemovedByDeletePrefix(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open("file://" + dir)
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "raw/t/u/k", strings.NewReader("v")))
	leftover := filepath.Join(dir, "raw", "t", "u", ".ohs-tmp-crash")
	require.NoError(t, os.WriteFile(leftover, []byte("x"), 0o600))
	keys, _ := s.List(ctx, "raw/t/u/")
	require.Equal(t, []string{"raw/t/u/k"}, keys) // hidden from List
	require.NoError(t, s.DeletePrefix(ctx, "raw/t/u/"))
	_, err := os.Stat(leftover)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "raw", "t", "u"))
	require.True(t, os.IsNotExist(err))
}

func TestPingFS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "root")
	s, err := Open("file://" + dir)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, Ping(ctx, s))

	// A vanished root (unmounted volume) is not ready, although listing a
	// missing prefix would still succeed.
	require.NoError(t, os.RemoveAll(dir))
	_, err = s.List(ctx, "raw/x/")
	require.NoError(t, err)
	require.Error(t, Ping(ctx, s))

	// A file where the root should be is not ready either.
	require.NoError(t, os.WriteFile(dir, []byte("x"), 0o600))
	require.Error(t, Ping(ctx, s))
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open("file://" + dir)
	require.NoError(t, err)
	require.NoError(t, CheckWritable(context.Background(), s))
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, ents, "the probe must leave nothing behind")
	keys, err := s.List(context.Background(), "")
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestCheckWritableReadOnlyRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	s, err := Open("file://" + dir)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err = CheckWritable(context.Background(), s)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not writable")
}
