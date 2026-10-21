// SPDX-License-Identifier: AGPL-3.0-only

package main

// The source scan: what a connector package's mapper can write, read from
// its Go files without running it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/soma-intelligence/open-health-sync/internal/connector"
)

// kindOfField maps a model.MapResult field to the record kind it holds.
var kindOfField = map[string]string{
	"QuantitySamples":  connector.KindSample,
	"CategorySamples":  connector.KindCategory,
	"SleepSessions":    connector.KindSleep,
	"Workouts":         connector.KindWorkout,
	"RoutePoints":      connector.KindRoute,
	"NutritionLogs":    connector.KindNutrition,
	"ECGRecordings":    connector.KindECG,
	"HeartbeatSamples": connector.KindHeartbeat,
	"DailySummaries":   connector.KindDaily,
	"Observations":     connector.KindObservation,
}

// Result is what one package's source can write.
type Result struct {
	Kinds      []string // record kinds, sorted
	Quantities []string // canonical quantity names, sorted
}

// Dir scans the non-test Go files of dir (and of its subdirectories that
// are not testdata or test helpers). canonical is the quantity vocabulary,
// qt the value of each model.QT constant.
func Dir(dir string, canonical map[string]bool, qt map[string]string) (Result, error) {
	kinds, quantities := map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (d.Name() == "testdata" || strings.HasSuffix(d.Name(), "fake")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if id, ok := n.Key.(*ast.Ident); ok && kindOfField[id.Name] != "" {
					kinds[kindOfField[id.Name]] = true
				}
			case *ast.SelectorExpr:
				if k := kindOfField[n.Sel.Name]; k != "" {
					kinds[k] = true
				}
				if pkg, ok := n.X.(*ast.Ident); ok && pkg.Name == "model" {
					if v, ok := qt[n.Sel.Name]; ok {
						quantities[v] = true
					}
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil && canonical[s] {
						quantities[s] = true
					}
				}
			}
			return true
		})
		return nil
	})
	return Result{Kinds: sorted(kinds), Quantities: sorted(quantities)}, err
}

// QTConstants reads the QT constants of the model package in dir.
func QTConstants(dir string) (map[string]string, error) {
	out := map[string]string{}
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "QT") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						out[name.Name] = s
					}
				}
			}
			return true
		})
	}
	return out, nil
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
