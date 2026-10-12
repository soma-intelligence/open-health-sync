// SPDX-License-Identifier: AGPL-3.0-only

// Package api embeds the OpenAPI description of the HTTP API, openapi.yaml,
// so the server can serve the exact document of its own version at
// GET /openapi.yaml.
package api

import _ "embed"

// OpenAPI is the content of api/openapi.yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
