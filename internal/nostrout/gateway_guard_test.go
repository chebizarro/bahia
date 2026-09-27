package nostrout

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const bahiaModule = "github.com/openagentsinc/bahia"

// rawPublisherPackages define EVENT publication outside Bahia's admission
// layer. Any use of a Publish* function or method from these packages — a
// call, a method value, or a method expression, under any import alias — is a
// raw relay publication.
var rawPublisherPackages = []string{
	"fiatjaf.com/nostr",
	"git.sharegap.net/cascadia/cascadia-go",
}

// nip46Package's client builds and publishes kind 24133 requests internally
// with no publish hook (and may issue unsolicited switch_relays requests), so
// only Bahia's own admission-gated nostrout.Bunker may use the package, and
// only for URI parsing and wire types.
const nip46Package = "fiatjaf.com/nostr/nip46"

// guardRules maps a restricted symbol to the only declarations allowed to use
// it. The map is exact: a new use anywhere else fails, and so does an entry
// that no longer matches a real use.
//
// Raw library publishers may appear only inside the gateway that admits the
// frame immediately before sending it. Bahia's own raw-send callables (the
// RelayPool send hook and the SoulFactory endpoint interface) are equally
// restricted to their single admitted caller, and the isolated-controller
// constructors to the process default and its adapter alias.
var guardRules = map[string][]string{
	"(*fiatjaf.com/nostr.Relay).Publish": {
		bahiaModule + "/internal/adapters/nostr|var publishOnRelay",
		bahiaModule + "/internal/soulfactory|(*goNostrRelayEndpoint).Publish",
		bahiaModule + "/internal/nostrout|(*Bunker).sendFrame",
	},
	"(*fiatjaf.com/nostr.Pool).PublishMany": {
		bahiaModule + "/internal/adapters/signet|(*Client).callManagement",
	},
	"var " + bahiaModule + "/internal/adapters/nostr.publishOnRelay": {
		bahiaModule + "/internal/adapters/nostr|(*RelayPool).publishToRelayWithResult",
	},
	"(" + bahiaModule + "/internal/soulfactory.relayBusEndpoint).Publish": {
		bahiaModule + "/internal/soulfactory|(*SoulFactoryRelayBus).sendAdmitted",
	},
	"(*" + bahiaModule + "/internal/soulfactory.goNostrRelayEndpoint).Publish": {},
	bahiaModule + "/internal/nostrout.New": {
		bahiaModule + "/internal/nostrout|Default",
		bahiaModule + "/internal/adapters/nostr|NewOutboundAdmission",
	},
	bahiaModule + "/internal/adapters/nostr.NewOutboundAdmission": {},
}

// nip46Sites are the only declarations that may use the NIP-46 client: the
// admission-gated Bunker wrapper.
func nip46SiteAllowed(site string) bool {
	const pkg = bahiaModule + "/internal/nostrout|"
	return site == pkg+"ConnectBunker" || strings.HasPrefix(site, pkg+"(*Bunker).")
}

type guardUse struct {
	site   string
	symbol string
	pos    token.Position
}

func loadForGuard(t *testing.T, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  filepath.Join("..", ".."),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	require.NoError(t, err)
	for _, pkg := range pkgs {
		require.Empty(t, pkg.Errors, "package %s failed to load", pkg.PkgPath)
	}
	return pkgs
}

// findGuardedUses reports every identifier use of a guarded symbol, keyed by
// the enclosing top-level declaration. Identifier uses cover qualified and
// unqualified calls, method values, and method expressions alike.
func findGuardedUses(pkgs []*packages.Package) []guardUse {
	var found []guardUse
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				site := pkg.PkgPath + "|" + declarationSite(decl)
				ast.Inspect(decl, func(node ast.Node) bool {
					ident, ok := node.(*ast.Ident)
					if !ok {
						return true
					}
					symbol, guarded := guardedSymbol(pkg.TypesInfo.Uses[ident])
					if guarded {
						found = append(found, guardUse{site: site, symbol: symbol, pos: pkg.Fset.Position(ident.Pos())})
					}
					return true
				})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].pos.String() < found[j].pos.String() })
	return found
}

func guardedSymbol(obj types.Object) (string, bool) {
	if obj == nil || obj.Pkg() == nil {
		return "", false
	}
	switch o := obj.(type) {
	case *types.Func:
		name := o.FullName()
		if _, ok := guardRules[name]; ok {
			return name, true
		}
		if isRawPublisher(o) {
			return name, true
		}
	case *types.Var:
		if o.Parent() == o.Pkg().Scope() {
			name := "var " + o.Pkg().Path() + "." + o.Name()
			if _, ok := guardRules[name]; ok {
				return name, true
			}
		}
	}
	return "", false
}

func isRawPublisher(fn *types.Func) bool {
	path := fn.Pkg().Path()
	if path == nip46Package || strings.HasPrefix(path, nip46Package+"/") {
		return true
	}
	if !strings.HasPrefix(fn.Name(), "Publish") {
		return false
	}
	for _, prefix := range rawPublisherPackages {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func useAllowed(use guardUse) bool {
	if strings.HasPrefix(use.symbol, nip46Package+".") || strings.Contains(use.symbol, "("+nip46Package+".") || strings.Contains(use.symbol, "(*"+nip46Package+".") {
		return nip46SiteAllowed(use.site)
	}
	for _, site := range guardRules[use.symbol] {
		if site == use.site {
			return true
		}
	}
	return false
}

func declarationSite(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil || len(d.Recv.List) == 0 {
			return d.Name.Name
		}
		return "(" + receiverName(d.Recv.List[0].Type) + ")." + d.Name.Name
	case *ast.GenDecl:
		names := make([]string, 0)
		for _, spec := range d.Specs {
			if value, ok := spec.(*ast.ValueSpec); ok {
				for _, name := range value.Names {
					names = append(names, name.Name)
				}
			}
		}
		return d.Tok.String() + " " + strings.Join(names, ",")
	}
	return "?"
}

func receiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + receiverName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	}
	return "?"
}

// TestEveryRawRelayPublicationIsAnApprovedGateway enforces that no Bahia
// production package publishes to relays around the outbound admission layer
// or splits the process budget with an isolated controller.
func TestEveryRawRelayPublicationIsAnApprovedGateway(t *testing.T) {
	found := findGuardedUses(loadForGuard(t, "./..."))
	seen := map[string]bool{}
	var violations []string
	for _, use := range found {
		if useAllowed(use) {
			seen[use.symbol+" @ "+use.site] = true
			continue
		}
		violations = append(violations, use.pos.String()+": "+use.site+" uses "+use.symbol)
	}
	require.Empty(t, violations,
		"raw Nostr publication or isolated admission bypasses the process-wide controller; route it through RelayPool, the SoulFactory relay bus, or a nostrout permit instead of widening guardRules")
	for symbol, sites := range guardRules {
		for _, site := range sites {
			require.True(t, seen[symbol+" @ "+site], "guard rule %s @ %s no longer matches a real use; remove it", symbol, site)
		}
	}
	require.True(t, seen[nip46Package+".ParseBunkerInput @ "+bahiaModule+"/internal/nostrout|ConnectBunker"], "the admission-gated Bunker must remain the NIP-46 entry point")
}

// TestGuardDetectsDisguisedBypasses proves the detector is not a string
// match: aliased imports, method values, method expressions,
// connect-then-publish, pool fan-out, Cascadia helpers, and raw NIP-46 are all
// caught.
func TestGuardDetectsDisguisedBypasses(t *testing.T) {
	found := findGuardedUses(loadForGuard(t, "./internal/nostrout/testdata/bypass"))
	sites := map[string][]string{}
	for _, use := range found {
		require.False(t, useAllowed(use), "fixture use %s must be rejected", use.symbol)
		name := strings.TrimPrefix(use.site, bahiaModule+"/internal/nostrout/testdata/bypass|")
		sites[name] = append(sites[name], use.symbol)
	}
	for _, name := range []string{
		"AliasedRelayPublish", "MethodValue", "MethodExpression", "ConnectThenPublish",
		"PoolFanOut", "CascadiaHelper", "RawBunker",
	} {
		require.NotEmpty(t, sites[name], "guard missed the %s bypass", name)
	}
	require.Contains(t, sites["RawBunker"], "fiatjaf.com/nostr/nip46.ConnectBunker")
	require.Contains(t, sites["RawBunker"], "(*fiatjaf.com/nostr/nip46.BunkerClient).GetPublicKey")
	require.Contains(t, sites["CascadiaHelper"], "git.sharegap.net/cascadia/cascadia-go/nostr.Publish")
}
