// SPDX-License-Identifier: AGPL-3.0-only

// Package memlimit gives the Go runtime a soft memory limit when the operator
// set none: three quarters of the container's memory limit, read from the
// cgroup. Without it the heap grows to twice its live size before a
// collection (GOGC=100), and concurrent large uploads can take a process past
// its container limit, or the host's memory, and have it killed.
package memlimit

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// Fraction of the cgroup limit set as GOMEMLIMIT. The rest is for what the
// limit does not count well: goroutine stacks, the runtime's own overhead,
// and memory the runtime has not returned to the OS yet.
const Fraction = 0.75

// DefaultCgroupRoot is where a container sees its own cgroup.
const DefaultCgroupRoot = "/sys/fs/cgroup"

// unlimited is a cgroup v1 limit that means none (the page-rounded max int64).
const unlimited = 1 << 62

// Source tells where the limit Apply leaves in force came from.
type Source string

// Sources.
const (
	// None: no GOMEMLIMIT and no cgroup limit; the runtime has no limit.
	None Source = ""
	// Env: the operator's GOMEMLIMIT, which the runtime read at start.
	Env Source = "GOMEMLIMIT"
	// Cgroup: Fraction of the cgroup memory limit.
	Cgroup Source = "cgroup"
)

// Apply sets the soft memory limit to Fraction of the cgroup limit under
// root when GOMEMLIMIT is unset (lookup reads the environment), and reports
// the limit in force in bytes and where it came from.
func Apply(lookup func(string) (string, bool), root string) (int64, Source) {
	if v, ok := lookup("GOMEMLIMIT"); ok && strings.TrimSpace(v) != "" {
		return debug.SetMemoryLimit(-1), Env
	}
	limit, ok := CgroupLimit(root)
	if !ok {
		return 0, None
	}
	n := int64(float64(limit) * Fraction)
	debug.SetMemoryLimit(n)
	return n, Cgroup
}

// CgroupLimit reads the memory limit of the cgroup at root: cgroup v2's
// memory.max, else cgroup v1's memory/memory.limit_in_bytes. It reports
// false when neither file is there or the limit is "max" (none).
func CgroupLimit(root string) (int64, bool) {
	for _, name := range []string{"memory.max", filepath.Join("memory", "memory.limit_in_bytes")} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "max" {
			return 0, false
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 || n >= unlimited {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
