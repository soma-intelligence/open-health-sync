// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration && race

package collect_test

// raceSlowdown scales the wall time bounds of the tests: the race detector
// makes decompression and parsing several times slower.
const raceSlowdown = 10
