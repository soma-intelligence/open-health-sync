// SPDX-License-Identifier: AGPL-3.0-only

// Package synthflow runs the synthetic data of every provider
// (internal/synth) through the whole server on a fresh database: the upload
// routes for device providers, the raw store and ETL queue for the others,
// the worker, the database and the read API. Each line carrying a fault is
// sent as its own part, so what the server did with it can be read back from
// its own logs (sync_log, failed_records) and compared with what the offline
// harness (internal/synth/synthtest) predicts. It takes about twenty
// minutes, so it runs only with OHS_SYNTH_E2E=1 (build tag integration,
// Docker); the nightly synthetic-data workflow runs it. OHS_SYNTH_REPORT=path
// writes the anomaly report as Markdown.
package synthflow
