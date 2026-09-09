package compat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

var foundationPackages = []string{
	"internal/classifier",
	"internal/deliverysnapshot",
	"internal/domain",
	"internal/idempotency",
	"internal/migration",
	"internal/policy",
	"internal/runtimeconfig",
	"internal/security",
}

// TestFoundationPackagesHaveNoRuntimeSideEffects keeps Phase 0 contracts
// disconnected from Legacy startup. This is intentionally a static check:
// importing a foundation package must not register hooks, start goroutines,
// open SQLite, mutate config, or pull in a blank-import Legacy adapter.
func TestFoundationPackagesHaveNoRuntimeSideEffects(t *testing.T) {
	for _, packagePath := range foundationPackages {
		dir := filepath.FromSlash(packagePath)
		entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", packagePath, err)
		}
		for _, filename := range entries {
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly|parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", filename, err)
			}
			for _, spec := range file.Imports {
				if spec.Name != nil && spec.Name.Name == "_" {
					t.Errorf("%s has a blank import %q", filename, strings.Trim(spec.Path.Value, `"`))
				}
			}

			fullFile, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
			if err != nil {
				t.Fatalf("parse declarations %s: %v", filename, err)
			}
			ast.Inspect(fullFile, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.FuncDecl:
					if node.Recv == nil && node.Name.Name == "init" {
						t.Errorf("%s defines init(), which is forbidden in Phase 0 foundations", filename)
					}
				case *ast.GoStmt:
					t.Errorf("%s starts a goroutine from a Phase 0 foundation package", filename)
				}
				return true
			})
		}
	}
}
