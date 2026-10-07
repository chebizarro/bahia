package archtest

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// relayPublishOwners are the only files allowed to put an EVENT frame on the
// wire with the Nostr library directly: the relay pool's send hook, which the
// process-wide outbound admission controller (internal/nostrout) charges
// immediately before every frame. Everything else — SoulFactory included —
// publishes through RelayPool.Publish*, so budgets, the shared circuit
// breaker, duplicate suppression, and the kill switch apply process-wide.
var relayPublishOwners = []string{
	"internal/adapters/nostr/relay_pool",
}

// nip46Owners are the only files allowed to use the NIP-46 client library: it
// builds and publishes kind-24133 request frames internally with no publish
// hook, so only the admission-gated nostrout.Bunker wrapper may touch it.
var nip46Owners = []string{
	"internal/nostrout/bunker",
}

const nip46PackagePath = "fiatjaf.com/nostr/nip46"

// rawPublishPackages define EVENT publication outside Bahia's admission
// layer. Any use of a Publish* function or method from these packages — a
// call, a method value, or a method expression, under any import alias — is a
// raw relay publication.
var rawPublishPackages = []string{
	"fiatjaf.com/nostr",
	"git.sharegap.net/cascadia/cascadia-go",
}

func inRawPublishPackage(path string) bool {
	for _, prefix := range rawPublishPackages {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func inNIP46Package(path string) bool {
	return path == nip46PackagePath || strings.HasPrefix(path, nip46PackagePath+"/")
}

// guardedPublishSymbol classifies an identifier's object as a guarded raw
// publication symbol and returns its ratchet key. Identifier objects cover
// qualified and unqualified calls, method values, and method expressions
// alike, under any import alias. Type names are inert: publishing requires
// calling a guarded function or method.
func guardedPublishSymbol(obj types.Object) (string, bool) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return "", false
	}
	path := fn.Pkg().Path()
	switch {
	case inNIP46Package(path):
		return fn.FullName(), true
	case strings.HasPrefix(fn.Name(), "Publish") && inRawPublishPackage(path):
		return fn.FullName(), true
	}
	return "", false
}

func hasOwnerPrefix(path string, owners []string) bool {
	for _, owner := range owners {
		if strings.HasPrefix(path, owner) {
			return true
		}
	}
	return false
}

func collectRawRelayPublications(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		publishOwner := hasOwnerPrefix(path, relayPublishOwners)
		nip46Owner := hasOwnerPrefix(path, nip46Owners)
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			obj := pkg.TypesInfo.Uses[ident]
			key, guarded := guardedPublishSymbol(obj)
			if !guarded {
				return true
			}
			fn := obj.(*types.Func)
			if inNIP46Package(fn.Pkg().Path()) {
				if nip46Owner {
					return true
				}
			} else if publishOwner {
				return true
			}
			found.add(key, pkg.Fset.Position(ident.Pos()))
			return true
		})
	})
}

// TestNoNewDirectLibraryRelayPublications enforces the zero-bypass guard: no
// production code outside the approved transport gateways publishes to a
// relay with the Nostr library or uses the NIP-46 client directly. The
// baseline is empty and must stay empty — route new traffic through
// RelayPool.Publish* or a nostrout permit instead of widening the owners.
func TestNoNewDirectLibraryRelayPublications(t *testing.T) {
	found := newViolations()
	collectRawRelayPublications(loadModule(t), found)
	ratchet(t, "relay_publish", found)
}

// TestRelayPublishGuardDetectsDisguisedBypasses proves the guard is not a
// string match: aliased imports, method values, method expressions,
// connect-then-publish, pool fan-out, Cascadia helpers, and raw NIP-46 use
// are all caught in the testdata fixture.
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
	collectRawRelayPublications(pkgs, found)
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
	} {
		if !strings.Contains(report, want) {
			t.Errorf("guard missed the %s bypass; found:\n%s", want, report)
		}
	}
}
