package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestArchitectureContextVMMethodConstants keeps the operator-facing
// ContextVM surface closed. Mutations must be added as signed intents, not
// reintroduced by declaring another ContextVMMethod constant.
func TestArchitectureContextVMMethodConstants(t *testing.T) {
	allowed := map[string]string{
		"ContextVMMethodServiceSecretsReveal": "services/secrets-reveal",
		"ContextVMMethodDeploymentRunLogsGet": "deployments/run-logs-get",
	}
	registrationOwners := map[string]bool{
		"internal/controlplane/assistant_handlers.go":       true,
		"internal/controlplane/encrypted_route_handlers.go": true,
		"internal/controlplane/encrypted_transport.go":      true,
		"internal/dnsagent/agent/agent.go":                  true, // daemon-to-agent RPC fallback
	}
	root := filepath.Join(repoRoot(t), "internal")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, spec := range group.Specs {
				values := spec.(*ast.ValueSpec)
				for i, name := range values.Names {
					if !strings.HasPrefix(name.Name, "ContextVMMethod") {
						continue
					}
					want, retained := allowed[name.Name]
					if !retained || i >= len(values.Values) {
						t.Errorf("%s: ContextVM mutation method constant %s is forbidden; add a signed intent instead", relPath(path), name.Name)
						continue
					}
					literal, ok := values.Values[i].(*ast.BasicLit)
					if !ok || literal.Value != "\""+want+"\"" {
						t.Errorf("%s: retained ContextVM method %s must remain %q", relPath(path), name.Name, want)
					}
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "RegisterContextVMHandler" && selector.Sel.Name != "RegisterOperatorContextVMHandler") {
				return true
			}
			if !registrationOwners[relPath(path)] {
				t.Errorf("%s: ContextVM registration is forbidden outside retained assistant/reveal/log and DNS-agent RPC", relPath(path))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
