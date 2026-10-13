package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TrustSet is the authority for org-scoped intents. A derived SQL index must
// never enter its resolution path, including through daemon wiring.
func TestTrustSetNoPostgresAuthority(t *testing.T) {
	for _, path := range []string{
		"internal/controlplane/trust_set.go",
		"internal/app/app.go",
	} {
		t.Run(path, func(t *testing.T) {
			set := token.NewFileSet()
			file, err := parser.ParseFile(set, filepath.Join(repoRoot(t), path), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range file.Imports {
				name, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if path == "internal/controlplane/trust_set.go" && strings.HasSuffix(name, "/internal/auth") {
					t.Errorf("%s: TrustSet imports database-backed RBAC", set.Position(imp.Pos()))
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				if ident.Name == "WithPostgresRBAC" || ident.Name == "HasPostgres" ||
					path == "internal/controlplane/trust_set.go" &&
						(ident.Name == "postgres" || ident.Name == "rbac" ||
							ident.Name == "CheckPermission" || ident.Name == "LoadAuthzContext") {
					t.Errorf("%s: PostgreSQL authority is forbidden in TrustSet", set.Position(ident.Pos()))
				}
				return true
			})
		})
	}
}
