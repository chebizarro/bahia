package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// This guard is frame-level: it bans every production code path that can put
// an outbound client→relay frame on the wire outside the approved transport
// gateways, where the process-wide admission controller (internal/nostrout)
// charges the frame immediately before it is written.
//
// Guarded frame writers:
//   - EVENT frames: any Publish* function or method from fiatjaf.com/nostr or
//     cascadia-go — call, method value, or method expression, under any import
//     alias — including uses through the nostr.Publisher/nostr.QuerierPublisher
//     interfaces, and Bahia's own raw-send seam (the publishOnRelay var);
//   - AUTH frames: (*nostr.Relay).Auth, which additionally must be called
//     with the pool's admission-wrapped signer (authSignerFor), so a permit
//     is provably charged before the library writes the frame;
//   - raw frame writes: (*nostr.Relay).Write / WriteWithError, and raw
//     websocket dials/writes (github.com/coder/websocket), which could carry
//     any frame;
//   - NIP-46 client symbols (fiatjaf.com/nostr/nip46): the library builds and
//     publishes kind-24133 request frames internally with no publish hook, so
//     only the admission-gated nostrout.Bunker may use it;
//   - NIP-77 session helpers (fiatjaf.com/nostr/nip77): they wire a raw relay
//     connection as the upload publisher and write NEG-* session frames, so
//     only the pool gateway that paces uploads through admission may start a
//     session.
//
// Explicit boundary — NOT guarded here: REQ, CLOSE and COUNT frames are reads,
// not publications; direct library use for them is covered by the
// relay_subscribe ratchet. Relay-side (server) response writes inside Bahia's
// own relay implementations (internal/relaysidecar, cmd/bahia-relay via
// khatru) answer inbound clients and are not outbound publications either.
//
// The allowlist below is exact: a new use anywhere else fails, and so does an
// allowlist entry that no longer matches a real use. Do not widen it; route
// new traffic through RelayPool.Publish*, the pool's AUTH helpers, or a
// nostrout permit.
var relayFrameGuardRules = map[string][]string{
	// EVENT frames go through the pool's raw-send seam ...
	"(*fiatjaf.com/nostr.Relay).Publish": {
		modulePath + "/internal/adapters/nostr|var publishOnRelay",
	},
	// ... which only the two admitted senders may call: the pool fan-out
	// (BeforeAttempt immediately before each frame, including AUTH retries)
	// and the paced NIP-77 session upload.
	"var " + modulePath + "/internal/adapters/nostr.publishOnRelay": {
		modulePath + "/internal/adapters/nostr|(*RelayPool).publishToRelayWithResult",
		modulePath + "/internal/adapters/nostr|(*RelayPool).publishAdmittedToSessionRelay",
	},
	// Interface-mediated EVENT publication (a nostr.Publisher whose value
	// may be a raw relay): only the NIP-77 download direction, whose To is
	// always the local sync target, never a relay.
	"(fiatjaf.com/nostr.Publisher).Publish": {
		modulePath + "/internal/adapters/nostr|(*RelayPool).downloadNegentropyItems",
	},
	// AUTH frames: the pool's three NIP-42 entry points, each of which must
	// pass the admission-wrapped signer (enforced by authCallAdmitted).
	"(*fiatjaf.com/nostr.Relay).Auth": {
		modulePath + "/internal/adapters/nostr|(*RelayPool).authenticateLiveRelay",
		modulePath + "/internal/adapters/nostr|(*RelayPool).authenticateRelayAhead",
		modulePath + "/internal/adapters/nostr|(*RelayPool).AuthenticateRelay",
	},
	// NIP-77 sessions: only the pool's per-relay reconcile, which uploads
	// through paced admission operations.
	"fiatjaf.com/nostr/nip77.NegentropySyncWithOptions": {
		modulePath + "/internal/adapters/nostr|(*RelayPool).negentropySyncRelay",
	},
}

const (
	nip46PackagePath     = "fiatjaf.com/nostr/nip46"
	nip77PackagePath     = "fiatjaf.com/nostr/nip77"
	websocketPackagePath = "github.com/coder/websocket"
	cascadiaGoModulePath = "git.sharegap.net/cascadia/cascadia-go"
	bahiaNostrAdapter    = modulePath + "/internal/adapters/nostr"
	authSignerForMethod  = "(*" + bahiaNostrAdapter + ".RelayPool).authSignerFor"
)

// rawFramePublishPackages define EVENT publication outside Bahia's admission
// layer.
var rawFramePublishPackages = []string{
	"fiatjaf.com/nostr",
	cascadiaGoModulePath,
}

func inRawFramePublishPackage(path string) bool {
	for _, prefix := range rawFramePublishPackages {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func inPackage(path, pkg string) bool {
	return path == pkg || strings.HasPrefix(path, pkg+"/")
}

// guardedFrameSymbol classifies an identifier's object as a guarded frame
// writer and returns its allowlist key. Identifier objects cover qualified
// and unqualified calls, method values, and method expressions alike, under
// any import alias. Type names are inert: writing a frame requires calling a
// guarded function or method.
func guardedFrameSymbol(obj types.Object) (string, bool) {
	switch o := obj.(type) {
	case *types.Func:
		if o.Pkg() == nil {
			return "", false
		}
		path := o.Pkg().Path()
		switch {
		case inPackage(path, nip46PackagePath):
			return o.FullName(), true
		case inPackage(path, nip77PackagePath):
			return o.FullName(), true
		case inPackage(path, websocketPackagePath) &&
			(o.Name() == "Dial" || strings.HasPrefix(o.Name(), "Write")):
			return o.FullName(), true
		case path == "fiatjaf.com/nostr" && o.Type().(*types.Signature).Recv() != nil &&
			(o.Name() == "Auth" || o.Name() == "Write" || o.Name() == "WriteWithError"):
			return o.FullName(), true
		case strings.HasPrefix(o.Name(), "Publish") && inRawFramePublishPackage(path):
			return o.FullName(), true
		}
	case *types.Var:
		if o.Parent() == o.Pkg().Scope() {
			name := "var " + o.Pkg().Path() + "." + o.Name()
			if _, tracked := relayFrameGuardRules[name]; tracked {
				return name, true
			}
		}
	}
	return "", false
}

// nip46SiteAllowed: the only declarations that may use the NIP-46 client are
// the admission-gated Bunker wrapper and its constructor.
func nip46SiteAllowed(site string) bool {
	const pkg = modulePath + "/internal/nostrout|"
	return site == pkg+"ConnectBunker" || strings.HasPrefix(site, pkg+"(*Bunker).")
}

type frameGuardUse struct {
	site   string
	symbol string
	pos    token.Position
	node   ast.Node
	info   *types.Info
}

func frameUseAllowed(use frameGuardUse) bool {
	if strings.HasPrefix(use.symbol, nip46PackagePath+".") ||
		strings.Contains(use.symbol, "("+nip46PackagePath+".") ||
		strings.Contains(use.symbol, "(*"+nip46PackagePath+".") {
		return nip46SiteAllowed(use.site)
	}
	for _, site := range relayFrameGuardRules[use.symbol] {
		if site == use.site {
			return true
		}
	}
	return false
}

// authCallAdmitted proves a (*Relay).Auth call site passes the pool's
// admission-wrapped signer, so the AUTH frame's permit is charged immediately
// before the library writes it. A bare signer (the pre-fix shape) fails here.
func authCallAdmitted(use frameGuardUse) bool {
	call, ok := use.node.(*ast.CallExpr)
	if !ok {
		return false // method value/expression: cannot prove the argument
	}
	if len(call.Args) < 2 {
		return false
	}
	wrapped, ok := call.Args[1].(*ast.CallExpr)
	if !ok {
		return false
	}
	var funIdent *ast.Ident
	switch fun := wrapped.Fun.(type) {
	case *ast.Ident:
		funIdent = fun
	case *ast.SelectorExpr:
		funIdent = fun.Sel
	default:
		return false
	}
	obj, ok := use.info.Uses[funIdent].(*types.Func)
	return ok && obj.FullName() == authSignerForMethod
}

func collectRawRelayFrameWrites(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		for _, decl := range file.Decls {
			site := pkg.PkgPath + "|" + declarationSite(decl)
			ast.Inspect(decl, func(node ast.Node) bool {
				ident, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				obj := pkg.TypesInfo.Uses[ident]
				symbol, guarded := guardedFrameSymbol(obj)
				if !guarded {
					return true
				}
				pos := pkg.Fset.Position(ident.Pos())
				if !frameUseAllowed(frameGuardUse{site: site, symbol: symbol}) {
					found.add(symbol+" @ "+site, pos)
					return true
				}
				if symbol == "(*fiatjaf.com/nostr.Relay).Auth" {
					if call, ok := findEnclosingCall(node, decl); ok {
						if !authCallAdmitted(frameGuardUse{site: site, symbol: symbol, node: call, info: pkg.TypesInfo}) {
							found.add(symbol+" without the admission-wrapped signer @ "+site, pos)
						}
					}
				}
				return true
			})
		}
	})
}

// findEnclosingCall returns the call expression whose function position is
// node, by re-walking the declaration. (Uses are reported at the selector
// ident; the argument check needs the call.)
func findEnclosingCall(node ast.Node, decl ast.Decl) (*ast.CallExpr, bool) {
	var found *ast.CallExpr
	ast.Inspect(decl, func(candidate ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := candidate.(*ast.CallExpr)
		if !ok {
			return true
		}
		if callExprSelectsIdent(call, node) {
			found = call
			return false
		}
		return true
	})
	return found, found != nil
}

func callExprSelectsIdent(call *ast.CallExpr, node ast.Node) bool {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel == node || fun.X == node
	case *ast.Ident:
		return fun == node
	}
	return false
}

func declarationSite(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil || len(d.Recv.List) == 0 {
			return d.Name.Name
		}
		return "(" + receiverTypeName(d.Recv.List[0].Type) + ")." + d.Name.Name
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

func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + receiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverTypeName(e.X)
	case *ast.IndexListExpr:
		return receiverTypeName(e.X)
	}
	return "?"
}

// TestNoNewDirectLibraryRelayPublications enforces the zero-bypass guard: no
// production code outside the approved transport gateways writes an outbound
// relay frame — EVENT, AUTH, NIP-46, NIP-77 session, or raw websocket — and
// every (*Relay).Auth call provably passes the admission-wrapped signer. The
// baseline is empty and must stay empty.
func TestNoNewDirectLibraryRelayPublications(t *testing.T) {
	found := newViolations()
	collectRawRelayFrameWrites(loadModule(t), found)
	ratchet(t, "relay_publish", found)

	// Stale allowlist entries hide renamed gateways: every entry must match
	// a real use.
	seen := map[string]bool{}
	for _, pkg := range loadModule(t) {
		if isTestVariant(pkg) {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				site := pkg.PkgPath + "|" + declarationSite(decl)
				ast.Inspect(decl, func(node ast.Node) bool {
					ident, ok := node.(*ast.Ident)
					if !ok {
						return true
					}
					if symbol, guarded := guardedFrameSymbol(pkg.TypesInfo.Uses[ident]); guarded {
						if frameUseAllowed(frameGuardUse{site: site, symbol: symbol}) {
							seen[symbol+" @ "+site] = true
						}
					}
					return true
				})
			}
		}
	}
	for symbol, sites := range relayFrameGuardRules {
		for _, site := range sites {
			if !seen[symbol+" @ "+site] {
				t.Errorf("guard rule %s @ %s no longer matches a real use; remove it", symbol, site)
			}
		}
	}
	if !seen[nip46PackagePath+".NewBunker @ "+modulePath+"/internal/nostrout|ConnectBunker"] {
		t.Error("the gated NIP-46 wrapper must remain the NIP-46 entry point")
	}
}

// TestRelayPublishGuardDetectsDisguisedBypasses proves the guard is not a
// string match: aliased imports, method values, method expressions,
// connect-then-publish, pool fan-out, Cascadia helpers, raw NIP-46, bare
// AUTH, raw frame writes, NIP-77 session uploads, interface-mediated
// publication, and raw websocket writes are all caught in the fixture.
func TestRelayPublishGuardDetectsDisguisedBypasses(t *testing.T) {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  repoRoot(t),
	}, "./internal/archtest/testdata/relay_publish_bypass")
	if err != nil {
		t.Fatalf("load bypass fixture: %v", err)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatalf("bypass fixture failed to load: %v", pkg.Errors)
		}
	}
	found := newViolations()
	collectRawRelayFrameWrites(pkgs, found)
	if len(found.counts) == 0 {
		t.Fatal("guard found no violations in the bypass fixture")
	}
	var joined []string
	for key, locations := range found.locations {
		for _, location := range locations {
			joined = append(joined, key+" @ "+location)
		}
	}
	sort.Strings(joined)
	report := strings.Join(joined, "\n")
	for _, want := range []string{
		"(*fiatjaf.com/nostr.Relay).Publish",
		"(*fiatjaf.com/nostr.Pool).PublishMany",
		"git.sharegap.net/cascadia/cascadia-go/nostr.Publish",
		"fiatjaf.com/nostr/nip46.ConnectBunker",
		"(*fiatjaf.com/nostr/nip46.BunkerClient).GetPublicKey",
		"(*fiatjaf.com/nostr.Relay).Auth",
		"(*fiatjaf.com/nostr.Relay).Write",
		"(*fiatjaf.com/nostr.Relay).WriteWithError",
		"fiatjaf.com/nostr/nip77.NegentropySyncWithOptions",
		"fiatjaf.com/nostr/nip77.SyncEventsFromIDs",
		"(fiatjaf.com/nostr.Publisher).Publish",
		"github.com/coder/websocket.Dial",
		"(*github.com/coder/websocket.Conn).Write",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("guard missed the %s bypass; found:\n%s", want, report)
		}
	}
}
