// SPDX-License-Identifier: AGPL-3.0-only

// Package fuzz holds native Go fuzz targets that cross connector boundaries:
// every registered connector's mapper behind the ETL reader, every device
// upload parser and every push notification handler. The seeds come from the
// synthetic data generator (internal/synth), so the fuzzer starts from what
// each vendor documents and mutates outward.
//
// `go test ./test/fuzz/` runs the seeds as ordinary tests; explore with, for
// example, `go test -run '^$' -fuzz '^FuzzConnectorPipeline$' -fuzztime 2m ./test/fuzz/`.
package fuzz
