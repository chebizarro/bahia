package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Export may query SQL and subscribe for EOSE, but must never acquire a
// signer, service key, publisher or outbox dependency.
func TestLegacyExportHasNoSignerOrPublisherDependency(t *testing.T) {
	path := filepath.Join(repoRoot(t), "cmd", "bahia-migrate", "export.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		for _, forbidden := range []string{"nostrutil", "servicesigner", "pkg/client", "localstore", "nostrout"} {
			if strings.Contains(imp.Path.Value, forbidden) {
				t.Errorf("export imports %s", forbidden)
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		for _, forbidden := range []string{"PrivateKey", "SignEvent", "Publish", "PublishAndWait", "NewPublisher", "NewLocalKeyer", "OpenOutbox"} {
			if sel.Sel.Name == forbidden {
				t.Errorf("export calls or reads forbidden %s", forbidden)
			}
		}
		return true
	})
}
