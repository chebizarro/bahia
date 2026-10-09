package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Normal daemon startup may rebuild a SQL index from canonical local records,
// but must not import SQL rows into canonical state or publish from them.
func TestNormalStartupDoesNotPromotePostgresRows(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"BootstrapLocalDNS":          true,
		"BootstrapLocalML":           true,
		"BackfillFromIndex":          true,
		"BackfillCanonicalPolicies":  true,
		"seedHiveCIPipelinePolicies": true,
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if forbidden[fun.Sel.Name] {
				t.Errorf("normal startup calls SQL-to-canonical promotion %s", fun.Sel.Name)
			}
		case *ast.Ident:
			if forbidden[fun.Name] {
				t.Errorf("normal startup calls SQL-to-canonical promotion %s", fun.Name)
			}
		}
		return true
	})
}
