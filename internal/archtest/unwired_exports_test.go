package archtest

import (
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestNoNewTestOnlyExports fails when an exported internal/ symbol is used by
// tests but by no production code (B-24). Such code looks shipped while
// nothing runs it, and it becomes the template later regressions copy
// (ProjectorSource, B-2; relay_first_extended.go, B-7; RelayPool.Subscribe,
// C-8). Symbols with no references at all are out of scope here.
//
// Methods whose name matches a method of any interface in the program are
// skipped, because interface dispatch does not show up as a direct use.
func TestNoNewTestOnlyExports(t *testing.T) {
	found := newViolations()
	collectTestOnlyExports(loadModule(t), found)
	ratchet(t, "unwired_exports", found)
}

type exportedSymbol struct {
	key string
	pos token.Position
}

func collectTestOnlyExports(pkgs []*packages.Package, found *violations) {
	interfaceMethods := collectInterfaceMethodNames(pkgs)
	declared := map[token.Position]exportedSymbol{}
	for _, pkg := range pkgs {
		if isTestVariant(pkg) || !strings.HasPrefix(pkg.PkgPath, modulePath+"/internal/") {
			continue
		}
		rel := strings.TrimPrefix(pkg.PkgPath, modulePath+"/")
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if obj.Exported() && declaredInProductionFile(pkg.Fset, obj) {
				declared[pkg.Fset.Position(obj.Pos())] = exportedSymbol{key: rel + "." + name, pos: pkg.Fset.Position(obj.Pos())}
			}
			typeName, ok := obj.(*types.TypeName)
			if !ok || typeName.IsAlias() {
				continue
			}
			named, ok := typeName.Type().(*types.Named)
			if !ok {
				continue
			}
			for i := 0; i < named.NumMethods(); i++ {
				method := named.Method(i)
				if !method.Exported() || interfaceMethods[method.Name()] || !declaredInProductionFile(pkg.Fset, method) {
					continue
				}
				declared[pkg.Fset.Position(method.Pos())] = exportedSymbol{
					key: rel + "." + name + "." + method.Name(),
					pos: pkg.Fset.Position(method.Pos()),
				}
			}
		}
	}

	prodUses := map[token.Position]int{}
	testUses := map[token.Position]int{}
	for _, pkg := range pkgs {
		for ident, obj := range pkg.TypesInfo.Uses {
			obj = originObject(obj)
			if obj == nil || !obj.Pos().IsValid() {
				continue
			}
			declPos := pkg.Fset.Position(obj.Pos())
			if _, ok := declared[declPos]; !ok {
				continue
			}
			if strings.HasSuffix(pkg.Fset.Position(ident.Pos()).Filename, "_test.go") {
				testUses[declPos]++
			} else {
				prodUses[declPos]++
			}
		}
	}
	for pos, symbol := range declared {
		if testUses[pos] > 0 && prodUses[pos] == 0 {
			found.add(symbol.key, symbol.pos)
		}
	}
}

// originObject maps instantiated generic functions and fields back to the
// declared object so uses of instances count for the declaration.
func originObject(obj types.Object) types.Object {
	switch o := obj.(type) {
	case *types.Func:
		return o.Origin()
	case *types.Var:
		return o.Origin()
	}
	return obj
}

func declaredInProductionFile(fset *token.FileSet, obj types.Object) bool {
	name := fset.Position(obj.Pos()).Filename
	return name != "" && !strings.HasSuffix(name, "_test.go")
}

// collectInterfaceMethodNames gathers method names of every interface type
// declared in the loaded packages and everything they import, including the
// standard library (String, Error, ServeHTTP, MarshalJSON, ...).
func collectInterfaceMethodNames(pkgs []*packages.Package) map[string]bool {
	names := map[string]bool{}
	seen := map[*types.Package]bool{}
	var visit func(pkg *types.Package)
	visit = func(pkg *types.Package) {
		if pkg == nil || seen[pkg] {
			return
		}
		seen[pkg] = true
		scope := pkg.Scope()
		for _, name := range scope.Names() {
			if iface, ok := scope.Lookup(name).Type().Underlying().(*types.Interface); ok {
				for i := 0; i < iface.NumMethods(); i++ {
					names[iface.Method(i).Name()] = true
				}
			}
		}
		for _, imported := range pkg.Imports() {
			visit(imported)
		}
	}
	for _, pkg := range pkgs {
		visit(pkg.Types)
		// Anonymous interfaces (func parameters, struct fields) are not in any
		// package scope; pick them up from the type-checked expressions.
		for _, tv := range pkg.TypesInfo.Types {
			if tv.Type == nil {
				continue
			}
			if iface, ok := tv.Type.Underlying().(*types.Interface); ok {
				for i := 0; i < iface.NumMethods(); i++ {
					names[iface.Method(i).Name()] = true
				}
			}
		}
	}
	return names
}
