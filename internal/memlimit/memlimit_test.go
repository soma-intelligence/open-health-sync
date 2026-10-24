// SPDX-License-Identifier: AGPL-3.0-only

package memlimit

import (
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, root, name, v string) {
	t.Helper()
	p := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(v), 0o644))
}

func TestCgroupLimit(t *testing.T) {
	v2 := t.TempDir()
	write(t, v2, "memory.max", "1073741824\n")
	n, ok := CgroupLimit(v2)
	assert.True(t, ok)
	assert.Equal(t, int64(1<<30), n)

	v1 := t.TempDir()
	write(t, v1, "memory/memory.limit_in_bytes", "536870912\n")
	n, ok = CgroupLimit(v1)
	assert.True(t, ok)
	assert.Equal(t, int64(512<<20), n)

	for name, v := range map[string]string{
		"memory.max":                   "max\n",
		"memory/memory.limit_in_bytes": "9223372036854771712\n",
	} {
		root := t.TempDir()
		write(t, root, name, v)
		_, ok := CgroupLimit(root)
		assert.False(t, ok, "%s %s means no limit", name, v)
	}
	_, ok = CgroupLimit(t.TempDir())
	assert.False(t, ok, "no cgroup files")
}

func TestApply(t *testing.T) {
	old := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(old) })

	root := t.TempDir()
	write(t, root, "memory.max", "1073741824")
	none := func(string) (string, bool) { return "", false }
	n, src := Apply(none, root)
	assert.Equal(t, Cgroup, src)
	assert.Equal(t, int64(768<<20), n, "three quarters of 1 GiB")
	assert.Equal(t, int64(768<<20), debug.SetMemoryLimit(-1))

	debug.SetMemoryLimit(math.MaxInt64)
	set := func(k string) (string, bool) { return "300MiB", k == "GOMEMLIMIT" }
	_, src = Apply(set, root)
	assert.Equal(t, Env, src, "the operator's GOMEMLIMIT wins")
	assert.Equal(t, int64(math.MaxInt64), debug.SetMemoryLimit(-1), "and is left as the runtime read it")

	n, src = Apply(none, t.TempDir())
	assert.Equal(t, None, src)
	assert.Zero(t, n)
}
