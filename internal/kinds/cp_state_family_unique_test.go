package kinds

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Every CPStateFamily discriminator must be unique: the number is the
// legacy_kind tag that routes a 30900 record to its family, so two families
// sharing one would read each other's records. Parallel slices have picked
// the same next number independently more than once; this pins it.
func TestCPStateFamilyDiscriminatorsAreUnique(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cp_state_family.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || spec.Type == nil {
			return true
		}
		if ident, ok := spec.Type.(*ast.Ident); !ok || ident.Name != "CPStateFamily" {
			return true
		}
		for i, name := range spec.Names {
			if i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				continue
			}
			value, err := strconv.Atoi(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", name.Name, err)
			}
			if prev, dup := seen[value]; dup {
				t.Errorf("CPStateFamily %d is declared by both %s and %s", value, prev, name.Name)
			}
			seen[value] = name.Name
		}
		return true
	})
	if len(seen) == 0 {
		t.Fatal("no CPStateFamily constants found")
	}
}
