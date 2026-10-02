package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheControlPlaneReadsTheEnvironmentOnlyThroughConfig holds `config.Config`'s
// own claim — "the whole of what the control plane reads from its environment" —
// to the code: this package must not read FOYER_POLICY_HOST, the two maintenance
// role URLs or GOMEMLIMIT itself, nor have store.NewStore read FOYER_KEK a second
// time. A variable read here is one
// `foyerctl doctor` and every reader of internal/config's documentation cannot see.
func TestTheControlPlaneReadsTheEnvironmentOnlyThroughConfig(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"os.Getenv": true, "os.LookupEnv": true, "os.Environ": true,
		"config.EnvOrFile": true, "store.KeyringFromEnv": true, "store.NewStore": true,
	}
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && forbidden[x.Name+"."+sel.Sel.Name] {
				t.Errorf("%s: %s.%s — read it in config.Load and carry it on config.Config",
					fset.Position(sel.Pos()), x.Name, sel.Sel.Name)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source file was checked, so this test has lost its subject")
	}
}
