package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// serviceKeyDerivationAllowlist is every non-test call of
// nostrutil.RequireServiceKeyMaterial, as "<file>|<enclosing func>": the
// features that still derive key material from the raw service key and so
// fail closed with ErrServiceKeyMaterialRequired under a remote signer
// (docs/architecture/service-identity.md, classification (b)). The list is
// exact and may only shrink: secret values live in the secret store
// (docs/architecture/secret-store.md), never under a key derived from the
// service nsec.
var serviceKeyDerivationAllowlist = map[string]string{
	"internal/app/service_keyer.go|legacyO1OrgStateDecryptor": "(b) legacy O1 org-state decryption",
}

func TestServiceKeyMaterialDerivationsConfined(t *testing.T) {
	pkgs := loadModule(t)
	found := map[string][]string{}
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		for _, decl := range file.Decls {
			name := declarationSite(decl)
			ast.Inspect(decl, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || !callsServiceKeyMaterial(pkg.TypesInfo, call) {
					return true
				}
				pos := pkg.Fset.Position(call.Pos())
				key := path + "|" + name
				found[key] = append(found[key], fmt.Sprintf("%s:%d", relPath(pos.Filename), pos.Line))
				return true
			})
		}
	})
	keys := make([]string, 0, len(found))
	for key := range found {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := serviceKeyDerivationAllowlist[key]; !ok {
			t.Errorf("new raw service key derivation at %s: sign and encrypt through the injected nostr.Keyer, and keep secret values in the secret store", strings.Join(found[key], ", "))
		}
	}
	for key := range serviceKeyDerivationAllowlist {
		if _, ok := found[key]; !ok {
			t.Errorf("stale derivation allowlist entry %q: it no longer derives from the raw service key; remove it", key)
		}
	}
}

func callsServiceKeyMaterial(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RequireServiceKeyMaterial" {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == modulePath+"/internal/nostrutil"
}
