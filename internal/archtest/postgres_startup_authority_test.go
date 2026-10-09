package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// These methods read legacy SQL state and can enqueue or publish service-signed
// events. They belong to explicit, governed migration commands, never daemon
// construction, warm-start hooks, or database-recovery runners.
var sqlPromotionCalls = map[string]bool{
	"BootstrapF74aCanonical":     true,
	"BootstrapLocalDNS":          true,
	"BootstrapLocalML":           true,
	"MigratePendingPostgresRows": true,
	"BackfillFromIndex":          true,
	"BackfillCanonicalPolicies":  true,
	"seedHiveCIPipelinePolicies": true,
}

// A constructor remains permitted when its inputs are canonical relay/local
// views. These argument names identify the current PostgreSQL-backed assembly,
// rather than banning the reconciler or coordinator abstraction itself.
var sqlSourcedStartupCalls = map[string]string{
	"NewReconciler":                  "stateRepo",
	"NewDNSProjector":                "stateRepo",
	"NewStaleRunDetector":            "nostrEventRepo",
	"NewLLMProvisioningCoordinator":  "llmRunRepo",
	"NewBackupRunCoordinator":        "backupRegistry",
	"NewBackupRestoreCoordinator":    "backupRegistry",
	"NewBackupRetentionCoordinator":  "backupRegistry",
	"NewBackupSchedulerRunner":       "backupScheduler",
	"NewToolProvisioningCoordinator": "toolProvisionRepo",
}

func TestNoAutomaticSQLToCanonicalPromotion(t *testing.T) {
	for _, entry := range []struct {
		path     string
		function string
	}{
		{"internal/app/app.go", "New"},
		{"internal/app/db_recovery.go", "Run"},
		{"internal/app/db_recovery.go", "tryRecover"},
		{"internal/service/managed_instance_supervisor.go", "Run"},
	} {
		t.Run(entry.path+"/"+entry.function, func(t *testing.T) {
			set := token.NewFileSet()
			file, err := parser.ParseFile(set, filepath.Join(repoRoot(t), entry.path), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != entry.function || fn.Body == nil {
					continue
				}
				if entry.path == "internal/app/app.go" && fn.Recv != nil {
					continue
				}
				found = true
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if entry.path == "internal/app/app.go" && wiresSQLVirtualizationPublisher(n) {
						t.Errorf("%s: virtualization publisher is wired to PostgreSQL journal on daemon startup", set.Position(n.Pos()))
					}
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					name := startupCallName(call.Fun)
					if sqlPromotionCalls[name] || (entry.path == "internal/app/db_recovery.go" && strings.HasPrefix(name, "Publish")) {
						t.Errorf("%s: %s is forbidden in normal daemon startup/recovery", set.Position(call.Pos()), name)
					}
					if entry.path == "internal/app/app.go" {
						if source := sqlSourcedStartupCalls[name]; source != "" && callUsesIdentifier(call, source) {
							t.Errorf("%s: %s is wired to PostgreSQL-backed %s on daemon startup", set.Position(call.Pos()), name, source)
						}
					}
					return true
				})
			}
			if !found {
				t.Fatalf("startup function %s not found in %s", entry.function, entry.path)
			}
		})
	}
}

func wiresSQLVirtualizationPublisher(node ast.Node) bool {
	lit, ok := node.(*ast.CompositeLit)
	if !ok {
		return false
	}
	typeName, ok := lit.Type.(*ast.Ident)
	if !ok || typeName.Name != "VirtualizationDependencies" {
		return false
	}
	var sqlJournal, publisher bool
	for _, element := range lit.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, keyOK := field.Key.(*ast.Ident)
		value, valueOK := field.Value.(*ast.Ident)
		if !keyOK || !valueOK {
			continue
		}
		sqlJournal = sqlJournal || key.Name == "Repository" && value.Name == "virtualizationRepo"
		publisher = publisher || key.Name == "Publisher" && value.Name == "virtualizationPublisher"
	}
	return sqlJournal && publisher
}

func callUsesIdentifier(call *ast.CallExpr, identifier string) bool {
	for _, arg := range call.Args {
		used := false
		ast.Inspect(arg, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == identifier {
				used = true
			}
			return true
		})
		if used {
			return true
		}
	}
	return false
}

func startupCallName(expr ast.Expr) string {
	switch call := expr.(type) {
	case *ast.Ident:
		return call.Name
	case *ast.SelectorExpr:
		return call.Sel.Name
	default:
		return ""
	}
}
