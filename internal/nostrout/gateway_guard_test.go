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

// nip46Package builds and publishes kind 24133 requests internally with no
// publish hook; only the admission-gated nostrout.Bunker may use it.
const nip46Package = "fiatjaf.com/nostr/nip46"

// approvedGateways is the complete set of raw publication sites. Each gateway
// is admitted by nostrout immediately before the frame it sends. The map is
// exact: a new raw site fails, and so does a stale entry.
var approvedGateways = map[string]string{
	bahiaModule + "/internal/adapters/nostr|var publishOnRelay":           "(*fiatjaf.com/nostr.Relay).Publish",
	bahiaModule + "/internal/soulfactory|(*goNostrRelayEndpoint).Publish": "(*fiatjaf.com/nostr.Relay).Publish",
	bahiaModule + "/internal/adapters/signet|(*Client).callManagement":    "(*fiatjaf.com/nostr.Pool).PublishMany",
}

type rawPublication struct {
	site   string
	callee string
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

// findRawPublications reports every use of a raw publisher outside the
// nostrout package itself, keyed by the enclosing top-level declaration.
func findRawPublications(pkgs []*packages.Package) []rawPublication {
	var found []rawPublication
	for _, pkg := range pkgs {
		if pkg.PkgPath == bahiaModule+"/internal/nostrout" {
			continue // the admission layer and its gated NIP-46 client
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				site := declarationSite(decl)
				ast.Inspect(decl, func(node ast.Node) bool {
					sel, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					fn := selectedFunc(pkg.TypesInfo, sel)
					if fn == nil || !isRawPublisher(fn) {
						return true
					}
					found = append(found, rawPublication{
						site:   pkg.PkgPath + "|" + site,
						callee: fn.FullName(),
						pos:    pkg.Fset.Position(sel.Pos()),
					})
					return true
				})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].pos.String() < found[j].pos.String() })
	return found
}

func selectedFunc(info *types.Info, sel *ast.SelectorExpr) *types.Func {
	if selection, ok := info.Selections[sel]; ok {
		fn, _ := selection.Obj().(*types.Func)
		return fn
	}
	fn, _ := info.Uses[sel.Sel].(*types.Func)
	return fn
}

func isRawPublisher(fn *types.Func) bool {
	if fn.Pkg() == nil {
		return false
	}
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
// production package publishes to relays around the outbound admission layer.
func TestEveryRawRelayPublicationIsAnApprovedGateway(t *testing.T) {
	found := findRawPublications(loadForGuard(t, "./..."))
	seen := make(map[string]bool, len(approvedGateways))
	var violations []string
	for _, raw := range found {
		if approvedGateways[raw.site] == raw.callee {
			seen[raw.site] = true
			continue
		}
		violations = append(violations, raw.pos.String()+": "+raw.site+" uses "+raw.callee)
	}
	require.Empty(t, violations,
		"raw Nostr publication bypasses outbound admission; route it through RelayPool, the SoulFactory relay bus, or a nostrout permit instead of widening approvedGateways")
	for site := range approvedGateways {
		require.True(t, seen[site], "approved gateway %s no longer exists; remove it from the allowlist", site)
	}
}

// isolatedConstructors create a controller with its own budget. Production
// code must share Default(); only the adapter alias may forward to New.
var isolatedConstructors = map[string]string{
	bahiaModule + "/internal/nostrout.New":                        "",
	bahiaModule + "/internal/adapters/nostr.NewOutboundAdmission": "",
}

var isolatedConstructorForwarders = map[string]bool{
	bahiaModule + "/internal/adapters/nostr|NewOutboundAdmission": true,
}

// TestProductionCodeSharesTheProcessController prevents a production package
// from silently splitting the process budget with an isolated controller.
func TestProductionCodeSharesTheProcessController(t *testing.T) {
	var violations []string
	for _, pkg := range loadForGuard(t, "./...") {
		if pkg.PkgPath == bahiaModule+"/internal/nostrout" {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				site := pkg.PkgPath + "|" + declarationSite(decl)
				ast.Inspect(decl, func(node ast.Node) bool {
					sel, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					fn := selectedFunc(pkg.TypesInfo, sel)
					if fn == nil {
						return true
					}
					if _, isolated := isolatedConstructors[fn.FullName()]; isolated && !isolatedConstructorForwarders[site] {
						violations = append(violations, pkg.Fset.Position(sel.Pos()).String()+": "+site+" uses "+fn.FullName())
					}
					return true
				})
			}
		}
	}
	require.Empty(t, violations, "production code must use the process-wide outbound controller (nostrout.Default)")
}

// TestGuardDetectsDisguisedBypasses proves the detector is not a string
// match: aliased imports, method values, method expressions,
// connect-then-publish, pool fan-out, Cascadia helpers, and raw NIP-46 are all
// caught.
func TestGuardDetectsDisguisedBypasses(t *testing.T) {
	found := findRawPublications(loadForGuard(t, "./internal/nostrout/testdata/bypass"))
	sites := map[string][]string{}
	for _, raw := range found {
		name := strings.TrimPrefix(raw.site, bahiaModule+"/internal/nostrout/testdata/bypass|")
		sites[name] = append(sites[name], raw.callee)
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
