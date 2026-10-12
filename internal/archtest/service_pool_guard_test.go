package archtest

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const nostrAdapterPath = modulePath + "/internal/adapters/nostr"

// servicePoolGuardScope holds the code that opens the Bahia service identity
// and builds relay pools with it: the daemon (internal/app) and the binaries.
var servicePoolGuardScope = []string{"internal/app/", "cmd/"}

// servicePoolGuardExemptions are pools in scope that answer NIP-42 AUTH with
// a key that is not the Bahia service identity, or never publish. Keys are
// "<file>|<function>"; an entry that no longer matches fails the test.
var servicePoolGuardExemptions = map[string]string{
	"cmd/bahia-dns-agent/main.go|run":             "signs with the DNS agent's own key, never the service identity",
	"cmd/bahia-leak-delete/main.go|newPool":       "signs with the rotated-out leaked key and publishes NIP-09 deletions to the public relays that hold leaked events, by design",
	"cmd/bahia-policy-census/main.go|run":         "read-only census: REQ/EOSE and NIP-42 AUTH only, never publishes an EVENT",
	"cmd/cli/config_fabric.go|openConfigCLIState": "signs with the operator's key, never the service identity",
}

// TestServiceSignerPoolsCarryTheRelayGuard: in the daemon and the binaries,
// every relay pool built with an auth signer (WithAuthSigner) also carries the
// control-plane relay guard (WithControlPlaneRelayGuard), passed directly or
// through an option slice built in the same function. A pool that can sign as
// the service and publish must never put cp-state on an uncontrolled relay.
func TestServiceSignerPoolsCarryTheRelayGuard(t *testing.T) {
	found := collectUnguardedSignerPools(loadModule(t))
	used := map[string]bool{}
	var failures []string
	for key, location := range found {
		if _, exempt := servicePoolGuardExemptions[key]; exempt {
			used[key] = true
			continue
		}
		failures = append(failures, key+" at "+location)
	}
	sort.Strings(failures)
	for _, failure := range failures {
		t.Errorf("relay pool with an auth signer and no WithControlPlaneRelayGuard: %s", failure)
	}
	for key := range servicePoolGuardExemptions {
		if !used[key] {
			t.Errorf("stale servicePoolGuardExemptions entry %q: remove it", key)
		}
	}
}

func collectUnguardedSignerPools(pkgs []*packages.Package) map[string]string {
	found := map[string]string{}
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		inScope := false
		for _, prefix := range servicePoolGuardScope {
			inScope = inScope || strings.HasPrefix(path, prefix)
		}
		if !inScope {
			return
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || nostrAdapterFunc(pkg.TypesInfo, call.Fun) != "NewRelayPool" || len(call.Args) < 3 {
					return true
				}
				options := map[string]bool{}
				for _, arg := range call.Args[2:] {
					collectPoolOptions(pkg.TypesInfo, fn.Body, arg, options, map[types.Object]bool{})
				}
				if options["WithAuthSigner"] && !options["WithControlPlaneRelayGuard"] {
					found[path+"|"+fn.Name.Name] = pkg.Fset.Position(call.Pos()).String()
				}
				return true
			})
		}
	})
	return found
}

// collectPoolOptions records the nostr adapter option constructors expr
// calls, following identifiers to their assignments in body.
func collectPoolOptions(info *types.Info, body *ast.BlockStmt, expr ast.Expr, options map[string]bool, seen map[types.Object]bool) {
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if name := nostrAdapterFunc(info, n.Fun); strings.HasPrefix(name, "With") {
				options[name] = true
			}
		case *ast.Ident:
			obj := info.Uses[n]
			if _, isVar := obj.(*types.Var); !isVar || seen[obj] {
				return true
			}
			seen[obj] = true
			for _, rhs := range assignmentsTo(info, body, obj) {
				collectPoolOptions(info, body, rhs, options, seen)
			}
		}
		return true
	})
}

func assignmentsTo(info *types.Info, body *ast.BlockStmt, obj types.Object) []ast.Expr {
	var values []ast.Expr
	assigned := func(ident *ast.Ident) bool {
		return info.Defs[ident] == obj || info.Uses[ident] == obj
	}
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && assigned(ident) && i < len(n.Rhs) {
					values = append(values, n.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if assigned(name) && i < len(n.Values) {
					values = append(values, n.Values[i])
				}
			}
		}
		return true
	})
	return values
}

// nostrAdapterFunc names the internal/adapters/nostr package function expr
// refers to, or "".
func nostrAdapterFunc(info *types.Info, expr ast.Expr) string {
	var ident *ast.Ident
	switch e := expr.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return ""
	}
	fn, ok := info.Uses[ident].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != nostrAdapterPath {
		return ""
	}
	return fn.Name()
}
