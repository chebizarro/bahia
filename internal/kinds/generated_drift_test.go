package kinds

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestGeneratedFrontendKindsMatchCanonicalGoKinds(t *testing.T) {
	repo := repositoryRoot(t)
	goKinds := parseGoKindConstants(t, filepath.Join(repo, "internal", "kinds", "kinds.go"))
	jsKinds := parseGeneratedJSKindConstants(t, filepath.Join(repo, "web", "src", "lib", "nostr", "kinds.gen.js"))

	for name, goValue := range goKinds {
		jsName := goConstNameToJS(name)
		jsValue, ok := jsKinds[jsName]
		if !ok {
			t.Fatalf("web/src/lib/nostr/kinds.gen.js missing generated constant %s for internal/kinds.%s", jsName, name)
		}
		if jsValue != goValue {
			t.Fatalf("kind drift for %s: frontend %d, internal/kinds %d", jsName, jsValue, goValue)
		}
	}
}

func parseGoKindConstants(t *testing.T, path string) map[string]int {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]int{}
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec := spec.(*ast.ValueSpec)
			for i, ident := range valueSpec.Names {
				if len(valueSpec.Values) <= i {
					continue
				}
				literal, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.INT {
					continue
				}
				value, err := strconv.Atoi(literal.Value)
				if err != nil {
					t.Fatalf("parse int for %s: %v", ident.Name, err)
				}
				out[ident.Name] = value
			}
		}
	}
	return out
}

func parseGeneratedJSKindConstants(t *testing.T, path string) map[string]int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile(`(?m)^export const ([A-Z0-9_]+) = ([0-9]+);$`)
	out := map[string]int{}
	for _, match := range re.FindAllStringSubmatch(string(content), -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("parse generated value for %s: %v", match[1], err)
		}
		out[match[1]] = value
	}
	return out
}

func goConstNameToJS(name string) string {
	var out []rune
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && shouldInsertUnderscore(runes, i) {
			out = append(out, '_')
		}
		out = append(out, toUpperASCII(r))
	}
	return string(out)
}

func shouldInsertUnderscore(runes []rune, i int) bool {
	prev := runes[i-1]
	cur := runes[i]
	if isDigitASCII(cur) {
		return isLowerASCII(prev)
	}
	if !isUpperASCII(cur) {
		return false
	}
	if isLowerASCII(prev) || isDigitASCII(prev) {
		return true
	}
	return i+1 < len(runes) && isUpperASCII(prev) && isLowerASCII(runes[i+1])
}

func toUpperASCII(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}

func isUpperASCII(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLowerASCII(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigitASCII(r rune) bool { return r >= '0' && r <= '9' }

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir || strings.TrimSpace(parent) == "" {
			t.Fatalf("could not find repository root from %s", filename)
		}
		dir = parent
	}
}
