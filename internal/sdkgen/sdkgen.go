// SPDX-License-Identifier: AGPL-3.0-only

// Package sdkgen generates the server-side SDKs (TypeScript and Python) and the
// Postman collection from api/openapi.yaml, so they cannot drift from the API:
// a test fails when the committed files differ from what Generate writes.
//
// Each SDK has one method per operation (operationId), typed from the
// document's schemas, plus an iterator for every cursor list. The webhook
// verification helpers are written by hand next to the generated files and
// tested against vectors this package signs with internal/webhook.
//
// Regenerate from the repository root with `make sdk`, or:
//
//	go run ./internal/sdkgen/cmd/sdkgen
package sdkgen

import (
	"path/filepath"
)

// Generated files, relative to the repository root.
const (
	TSClientPath = "sdks/server/typescript/src/client.ts"
	TSTypesPath  = "sdks/server/typescript/src/types.ts"
	PyClientPath = "sdks/server/python/open_health_sync/client.py"
	PyModelsPath = "sdks/server/python/open_health_sync/models.py"
	PostmanPath  = "api/collections/postman.json"
	VectorsPath  = "sdks/server/testdata/webhook-vectors.json"
	SpecPath     = "api/openapi.yaml"
)

// Generate reads the OpenAPI document under root and returns every generated
// file by its path relative to root.
func Generate(root string) (map[string][]byte, error) {
	api, err := Load(filepath.Join(root, SpecPath))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{
		TSClientPath: TypeScriptClient(api),
		TSTypesPath:  TypeScriptTypes(api),
	}
	if out[PyClientPath], err = PythonClient(api); err != nil {
		return nil, err
	}
	if out[PyModelsPath], err = PythonModels(api); err != nil {
		return nil, err
	}
	if out[PostmanPath], err = Postman(api); err != nil {
		return nil, err
	}
	if out[VectorsPath], err = WebhookVectors(); err != nil {
		return nil, err
	}
	return out, nil
}
