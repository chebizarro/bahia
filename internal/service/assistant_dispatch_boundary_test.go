package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The unified executor is the only dispatcher of assistant work. This guard
// fails if any production file in this package gains another call site for a
// provider dispatch entry point, or if the runtime's dispatch function is
// called from anywhere but the executor and the gated subagent child path.
func TestAssistantDispatchHasOneBoundary(t *testing.T) {
	calls := assistantPackageCallSites(t, "InvokeAssistantAsyncTool", "DispatchPreparedWork")
	want := map[string][]string{
		"InvokeAssistantAsyncTool": {"assistant_tool_runtime.go:(*AssistantToolRuntime).DispatchPreparedWork"},
		"DispatchPreparedWork": {
			"assistant_execution.go:(*AssistantExecutionEngine).invoke",
			"assistant_tool_runtime.go:(*AssistantToolRuntime).ExecuteSubagentTool",
		},
	}
	for name, sites := range want {
		if strings.Join(calls[name], ",") != strings.Join(sites, ",") {
			t.Fatalf("%s call sites = %v, want %v", name, calls[name], sites)
		}
	}
}

func assistantPackageCallSites(t *testing.T, names ...string) map[string][]string {
	t.Helper()
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			owner := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if ident, ok := star.X.(*ast.Ident); ok {
						owner = "(*" + ident.Name + ")." + owner
					}
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && wanted[sel.Sel.Name] {
					out[sel.Sel.Name] = append(out[sel.Sel.Name], path+":"+owner)
				}
				return true
			})
		}
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}
