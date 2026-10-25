// SPDX-License-Identifier: AGPL-3.0-only

// Package bench is the backtest of the quality score on public reference
// datasets (Walch et al. 2019, GalaxyPPG, PPG-DaLiA) and on synthetic data
// with known error. Everything but this comment is behind the bench build
// tag, and the data are read from OHS_QUALITY_DATA, never committed:
//
//	go test -tags bench ./internal/quality/bench -run TestBench -v
//
// With OHS_QUALITY_WRITE=1 the run also writes the published report
// (docs/quality/report.md) and the trained model and calibration embedded in
// internal/quality. See docs/quality.md.
package bench
