package archtest

import (
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// legacyKindRanges are the deprecated Bahia kind families (audit C-44 and
// docs/nostr-event-implementation-guide.md): regular-kind request, status and
// result families, the 31000-31099 audit range, the deprecated 31100-31105
// command kinds and the 31975-31978 DNS read models superseded by CAS 30900.
var legacyKindRanges = [][2]int64{
	{5941, 6006},
	{6941, 6997},
	{7941, 7997},
	{31000, 31105},
	{31975, 31978},
}

// legacyKindExemptPaths may define or use legacy kinds: the kind registry's
// declarations and the migration package that exists to read old events.
var legacyKindExemptPaths = []string{
	"internal/kinds/kinds.go",
	"internal/nostrmigration/",
}

func isLegacyKindValue(value int64) bool {
	for _, r := range legacyKindRanges {
		if value >= r[0] && value <= r[1] {
			return true
		}
	}
	return false
}

// isLegacyKindConst matches integer constants whose value is in a legacy
// range (which also catches re-exported aliases) or whose name marks them as
// legacy (the Legacy* worker kinds share values with live read models).
func isLegacyKindConst(obj *types.Const) bool {
	if obj.Pkg() == nil || !strings.HasPrefix(obj.Pkg().Path(), modulePath) {
		return false
	}
	if value, ok := constant.Int64Val(constant.ToInt(obj.Val())); ok && isLegacyKindValue(value) {
		return true
	}
	return strings.Contains(obj.Name(), "Legacy") && isKindConst(obj)
}

func isKindConst(obj *types.Const) bool {
	return obj.Pkg().Path() == modulePath+"/internal/kinds" || strings.Contains(obj.Name(), "Kind")
}

func exemptFromLegacyKindGate(path string) bool {
	for _, prefix := range legacyKindExemptPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// TestNoNewLegacyKindUsage bans new references to legacy kind constants and
// new legacy kind literals outside internal/nostrmigration (C-44).
func TestNoNewLegacyKindUsage(t *testing.T) {
	found := newViolations()
	collectLegacyKindUsage(loadModule(t), found)
	ratchet(t, "legacy_kinds", found)
}

func collectLegacyKindUsage(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		if exemptFromLegacyKindGate(path) {
			return
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.Ident:
				obj, ok := pkg.TypesInfo.Uses[n].(*types.Const)
				if ok && isLegacyKindConst(obj) {
					found.add(path+" "+obj.Pkg().Name()+"."+obj.Name(), pkg.Fset.Position(n.Pos()))
				}
			case *ast.BasicLit:
				if n.Kind != token.INT {
					return true
				}
				value, err := strconv.ParseInt(n.Value, 0, 64)
				if err == nil && isLegacyKindValue(value) {
					found.add(path+" literal "+n.Value, pkg.Fset.Position(n.Pos()))
				}
			}
			return true
		})
	})
}
