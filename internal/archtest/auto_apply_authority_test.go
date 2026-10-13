package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The SQL-backed manual lifecycle must not satisfy the reconciler's auto-apply
// interface. Only a separate executor with canonical authorization, an effect
// fence, and durable replay may replace the startup suspension.
func TestSQLRuntimeLifecycleCannotAutoRemediate(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, filepath.Join(repoRoot(t), "internal/service"), func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	servicePackage, ok := packages["service"]
	if !ok {
		t.Fatal("internal/service package not found")
	}
	for _, file := range servicePackage.Files {
		for _, decl := range file.Decls {
			method, ok := decl.(*ast.FuncDecl)
			if !ok || method.Name.Name != "AutoRemediateDesiredState" || method.Recv == nil {
				continue
			}
			for _, field := range method.Recv.List {
				if runtimeLifecycleReceiver(field.Type) {
					t.Errorf("%s: SQL-backed RuntimeLifecycleService must not implement auto-remediation", set.Position(method.Pos()))
				}
			}
		}
	}
}

func runtimeLifecycleReceiver(expr ast.Expr) bool {
	switch receiver := expr.(type) {
	case *ast.Ident:
		return receiver.Name == "RuntimeLifecycleService"
	case *ast.StarExpr:
		return runtimeLifecycleReceiver(receiver.X)
	case *ast.ParenExpr:
		return runtimeLifecycleReceiver(receiver.X)
	default:
		return false
	}
}

func TestStartupWiresOnlyCanonicalAutoApplyExecutor(t *testing.T) {
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
		if !ok || arg.Name != "autoApplyExecutor" {
			t.Error("auto-remediation must use only the canonical executor, never the SQL-backed lifecycle")
		}
		return true
	})
	if count != 1 {
		t.Fatalf("startup auto-remediation wiring count = %d, want 1", count)
	}
}

// Runtime effect exclusion must not depend on PostgreSQL. Other repository
// advisory locks protect SQL-only projections and are outside this ratchet.
func TestCanonicalAutoApplyUsesNoPostgresAdvisoryLock(t *testing.T) {
	for _, path := range []string{
		"internal/reconcile/auto_apply_executor.go",
		"internal/reconcile/canonical_auto_apply_source.go",
		"internal/service/runtime_apply_lock.go",
	} {
		data, err := os.ReadFile(filepath.Join(repoRoot(t), path))
		if err != nil {
			t.Fatal(err)
		}
		for _, prohibited := range []string{"pg_advisory", "pgx.", "internal/repository"} {
			if strings.Contains(string(data), prohibited) {
				t.Errorf("%s contains %q: canonical effects must work without PostgreSQL", path, prohibited)
			}
		}
	}
}
