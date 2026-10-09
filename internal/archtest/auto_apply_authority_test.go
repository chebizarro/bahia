package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// The SQL-backed manual lifecycle must not satisfy the reconciler's auto-apply
// interface. Only a separate executor with canonical authorization, an effect
// fence, and durable replay may replace the startup suspension.
func TestSQLRuntimeLifecycleCannotAutoRemediate(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), "internal/service/runtime_lifecycle.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		method, ok := decl.(*ast.FuncDecl)
		if !ok || method.Name.Name != "AutoRemediateDesiredState" || method.Recv == nil {
			continue
		}
		for _, field := range method.Recv.List {
			if receiver, ok := field.Type.(*ast.StarExpr); ok {
				if name, ok := receiver.X.(*ast.Ident); ok && name.Name == "RuntimeLifecycleService" {
					t.Fatal("SQL-backed RuntimeLifecycleService must not implement auto-remediation")
				}
			}
		}
	}
}

func TestStartupKeepsAutoRemediationSuspended(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), "internal/app/app.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "WithAutoRemediationDeployer" {
			return true
		}
		count++
		if len(call.Args) != 1 {
			t.Error("auto-remediation startup admission must have exactly one argument")
			return true
		}
		arg, ok := call.Args[0].(*ast.Ident)
		if !ok || arg.Name != "nil" {
			t.Error("auto-remediation must remain suspended until the canonical executor is verified")
		}
		return true
	})
	if count != 1 {
		t.Fatalf("startup auto-remediation wiring count = %d, want 1", count)
	}
}
