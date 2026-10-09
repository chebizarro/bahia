package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// An ordinary daemon startup must not wire SQL-origin F74a projection or
// migrate historical SQL outbox rows into the canonical local outbox.
func TestNormalStartupHasNoSQLToRelayF74aImport(t *testing.T) {
	path := filepath.Join("app.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"NewF74aBackfillRunner":      true,
		"NewF74aCanonicalPublisher":  true,
		"NewPgF74aBackfillSource":    true,
		"MigratePendingPostgresRows": true,
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && forbidden[sel.Sel.Name] {
			t.Errorf("normal app startup references %s", sel.Sel.Name)
		}
		return true
	})
}
