// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration && !race

package collect_test

// raceSlowdown scales the wall time bounds of the tests (see the race build).
const raceSlowdown = 1
