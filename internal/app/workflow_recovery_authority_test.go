package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"go.uber.org/zap"
)

func TestSQLWorkflowRecoveryIsNotRegisteredAtStartup(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"backupCoordinator":          true,
		"backupRestoreCoordinator":   true,
		"backupRetentionCoordinator": true,
		"backupSchedulerRunner":      true,
		"llmCoordinator":             true,
		"llmReconciler":              true,
		"toolCoordinator":            true,
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "RegisterWithOptions" {
			return true
		}
		if name, ok := call.Args[0].(*ast.Ident); ok && forbidden[name.Name] {
			t.Errorf("SQL-backed %s must not be registered for startup/recovery", name.Name)
		}
		return true
	})
}

func TestSQLWorkflowRecoveryDegradedHealth(t *testing.T) {
	health := NewHealthProvider(nil, NewBackgroundManager(zap.NewNop()))
	registerSQLWorkflowRecoveryDegraded(health, zap.NewNop(), "backup_scheduler", "canonical schedule provenance required")
	check := requireCheckStatus(t, health.Readiness().Checks, "backup_scheduler", HealthStatusWarn)
	if check.Details["recovery_source"] != "canonical_signed_intent_unavailable" {
		t.Fatalf("recovery source = %q", check.Details["recovery_source"])
	}
}
