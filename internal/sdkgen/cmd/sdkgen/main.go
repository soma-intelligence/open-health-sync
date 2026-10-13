// SPDX-License-Identifier: AGPL-3.0-only

// Command sdkgen writes the server-side SDKs (sdks/server/typescript and
// sdks/server/python), the Postman collection (api/collections/postman.json)
// and the webhook test vectors from api/openapi.yaml. The files are generated
// so they cannot drift; a test fails when the committed copies differ.
//
// Run it from the repository root:
//
//	go run ./internal/sdkgen/cmd/sdkgen
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/soma-intelligence/open-health-sync/internal/sdkgen"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	files, err := sdkgen.Generate(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkgen:", err)
		os.Exit(1)
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(*root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "sdkgen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(full, files[p], 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sdkgen:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", p)
	}
}
