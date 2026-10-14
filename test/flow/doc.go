// SPDX-License-Identifier: AGPL-3.0-only

// Package flow runs the whole server on a fresh database and follows one
// small, hand-written dataset from the ingest routes to every output (REST,
// FHIR R4, Open mHealth, CSV export, webhooks) and through the user's
// deletion. Run it with `make integration` (build tag integration, Docker).
package flow
