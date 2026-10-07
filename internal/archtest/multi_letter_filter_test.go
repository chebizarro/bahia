package archtest

import (
	"go/ast"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// bannedFilterKeys are multi-letter tag keys that must not appear in
// nostr.TagMap REQ-filter constructions because NIP-01 relays only index
// single-letter tags. Use a "t" topic instead (bahia-irsry.43).
var bannedFilterKeys = map[string]bool{
	"schema": true,
	"domain": true,
}

// TestNoNewMultiLetterREQFilters prevents new uses of multi-letter tag
// keys ("schema", "domain") in nostr.TagMap filter constructions.
// NIP-01 relays only index single-letter tags, so these filters silently
// match nothing on real relays. Use a single-letter "t" topic instead.
func TestNoNewMultiLetterREQFilters(t *testing.T) {
	found := newViolations()
	collectMultiLetterFilterKeys(loadModule(t), found)
	ratchet(t, "multi_letter_req_filter", found)
}

func collectMultiLetterFilterKeys(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		ast.Inspect(file, func(node ast.Node) bool {
			comp, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if !isTagMapType(pkg, comp) {
				return true
			}
			for _, elt := range comp.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok {
					continue
				}
				// Strip quotes from the string literal.
				key := lit.Value
				if len(key) >= 2 && key[0] == '"' {
					key = key[1 : len(key)-1]
				}
				if bannedFilterKeys[key] {
					found.add(path+" TagMap[\""+key+"\"]", pkg.Fset.Position(lit.Pos()))
				}
			}
			return true
		})
	})
}

// isTagMapType reports whether the composite literal is of type nostr.TagMap
// (fiatjaf.com/nostr.TagMap).
func isTagMapType(pkg *packages.Package, comp *ast.CompositeLit) bool {
	if comp.Type == nil {
		return false
	}
	tv := pkg.TypesInfo.Types[comp.Type]
	if !tv.IsType() {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == libraryNostrPath && obj.Name() == "TagMap"
}
