// SPDX-License-Identifier: AGPL-3.0-only

// Package export converts the "Export All Health Data" archive of the iPhone
// Health app (export.zip, holding apple_health_export/export.xml, the GPX
// files of workout-routes/ and the CSV files of electrocardiograms/) into the
// NDJSON lines of the Apple Health upload contract, grouped into parts of one
// data type and one local day, and uploads them to POST /v1/ingest/apple-health.
//
// The server, the raw store and the connector's mapper are unchanged: an
// imported line is the same line an iOS client would send. The XML is read
// token by token straight out of the zip, so memory stays flat whatever the
// size of the export (bounded by Options.MaxBufferedBytes plus the parts in
// flight).
//
// Conversion is deterministic: the same export with the same options gives
// the same parts, byte for byte, under the same batch id (BatchID), so a
// re-run is answered "duplicate" by the server instead of storing anything
// twice.
package export
