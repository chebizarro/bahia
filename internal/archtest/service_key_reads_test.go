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

// TestServiceKeyReadsConfined keeps the raw service private key
// (config.NostrConfig.PrivateKey) behind one seam. The daemon builds its
// service identity once in internal/app/service_keyer.go and injects the
// resulting nostr.Keyer into every consumer that signs, AUTHs or NIP-44s;
// raw-key derivations obtain material only through
// nostrutil.RequireServiceKeyMaterial, which fails closed with
// ErrServiceKeyMaterialRequired when the signer is remote (NIP-46/NIP-55L).
//
// Every non-test read or write of the field is listed below as
// "<file>|<enclosing func>" with its classification:
//   - seam: the single construction point;
//   - (b): a raw-key derivation whose signature is fixed by another slice;
//   - (c): an offline operator tool that signs locally by design;
//   - config: load/validation owned by internal/config;
//   - pending: a separate process awaiting the signer factory.
//
// The list is exact: a new read anywhere else fails, and so does an entry
// that no longer matches a real read. Route new consumers through the
// injected Keyer instead of widening it.
var serviceKeyReadAllowlist = map[string]string{
	"internal/app/service_keyer.go|newServiceKeyer": "seam",
	// (b) assistant transcript key = SHA-256 over the nsec; signature fixed by
	// assistant_wrapped_startup.go/assistant_wrapped_keys.go (bahia-cd0wr.4.8).
	"internal/app/app.go|assistantTranscriptKeyProvider": "(b) bahia-cd0wr.4.8",
	// (b) the wrapped-key manifest re-derives the deployed v1 key; owned by the
	// signer-factory slice (bahia-cd0wr.4.8).
	"internal/app/assistant_wrapped_keys.go|createAssistantWrappedKeyManifest": "(b) bahia-cd0wr.4.8",
	// (c) offline operator tools.
	"cmd/bahia-policy-census/main.go|run":            "(c)",
	"cmd/bahia-migrate/f74a_import.go|runF74aImport": "(c)",
	"cmd/bahia-migrate/main.go|runNostrMigration":    "(c)",
	"internal/config/config.go|":                     "config",
	// Separate relay-sidecar process: NIP-11 pubkey and config-ack signing are
	// sign-only, pending the signer factory in cmd/relay (bahia-cd0wr.3.5).
	"internal/relaysidecar/policy.go|newPolicy": "pending bahia-cd0wr.3.5",
	"internal/relaysidecar/server.go|New":       "pending bahia-cd0wr.3.5",
}

func TestServiceKeyReadsConfined(t *testing.T) {
	pkgs := loadModule(t)
	found := map[string][]string{}
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		for _, decl := range file.Decls {
			name := declarationSite(decl)
			ast.Inspect(decl, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "PrivateKey" || !isNostrConfigField(pkg.TypesInfo, sel) {
					return true
				}
				key := path + "|" + name
				if strings.HasPrefix(path, "internal/config/") {
					key = path + "|"
				}
				pos := pkg.Fset.Position(sel.Pos())
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
		if _, ok := serviceKeyReadAllowlist[key]; !ok {
			t.Errorf("raw service key read outside the seam at %s: take the injected nostr.Keyer from internal/app/service_keyer.go, or nostrutil.RequireServiceKeyMaterial for a raw-key derivation", strings.Join(found[key], ", "))
		}
	}
	for key := range serviceKeyReadAllowlist {
		if _, ok := found[key]; !ok {
			t.Errorf("stale service key allowlist entry %q: it no longer reads cfg.Nostr.PrivateKey; remove it", key)
		}
	}
}

// isNostrConfigField reports whether sel selects the PrivateKey field of
// internal/config.NostrConfig (through any value, pointer or embedding).
func isNostrConfigField(info *types.Info, sel *ast.SelectorExpr) bool {
	selection, ok := info.Selections[sel]
	if !ok || selection.Kind() != types.FieldVal {
		return false
	}
	recv := selection.Recv()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	return ok && named.Obj().Name() == "NostrConfig" && named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == modulePath+"/internal/config"
}
