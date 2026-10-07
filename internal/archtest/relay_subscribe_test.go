package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const libraryNostrPath = "fiatjaf.com/nostr"

// relaySubscribeOwners are the only files allowed to open REQs on library
// relay objects directly: the relay pool. Everything else, SoulFactory
// included since its bus was retired, goes through RelayPool.Subscribe*, so
// fixes to EOSE, CLOSED, AUTH and reconnect handling land once.
var relaySubscribeOwners = []string{
	"internal/adapters/nostr/relay_pool",
}

func isLibraryRelaySubscribe(method *types.Func) bool {
	sig, ok := method.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != libraryNostrPath {
		return false
	}
	switch named.Obj().Name() {
	case "Relay":
		return method.Name() == "Subscribe" || method.Name() == "PrepareSubscription"
	case "Pool":
		name := method.Name()
		return strings.HasPrefix(name, "Subscribe") || strings.HasPrefix(name, "FetchMany") || name == "QuerySingle"
	}
	return false
}

// TestNoNewDirectLibraryRelaySubscriptions bans new Subscribe/Fetch calls on
// fiatjaf.com/nostr Relay and Pool values outside the pool and bus.
func TestNoNewDirectLibraryRelaySubscriptions(t *testing.T) {
	found := newViolations()
	collectDirectRelaySubscriptions(loadModule(t), found)
	ratchet(t, "relay_subscribe", found)
}

func collectDirectRelaySubscriptions(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		for _, owner := range relaySubscribeOwners {
			if strings.HasPrefix(path, owner) {
				return
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection := pkg.TypesInfo.Selections[sel]
			if selection == nil {
				return true
			}
			method, ok := selection.Obj().(*types.Func)
			if ok && isLibraryRelaySubscribe(method) {
				found.add(path+" nostr."+recvName(method)+"."+method.Name(), pkg.Fset.Position(sel.Sel.Pos()))
			}
			return true
		})
	})
}

func recvName(method *types.Func) string {
	recv := method.Type().(*types.Signature).Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	if named, ok := recv.(*types.Named); ok {
		return named.Obj().Name()
	}
	return recv.String()
}
