package archtest

import (
	"go/ast"
	"go/types"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// pollGatedPaths are the packages where periodic work must be justified: a
// ticker there is usually a reconciler polling Postgres instead of reacting
// to relay events.
var pollGatedPaths = []string{
	"internal/service/",
	"internal/reconcile/",
	// Daemon background runners: a ticker there is a SQL scan standing in
	// for an event trigger unless it is annotated housekeeping.
	"internal/app/",
}

// allowPollAnnotation must carry a reason, on the ticker's line or the line
// directly above it: //nostr:allow-poll <reason>.
var allowPollAnnotation = regexp.MustCompile(`^//\s*nostr:allow-poll\s+\S`)

// TestNoNewUnannotatedPollTickers bans new time.NewTicker/time.Tick calls in
// internal/service and internal/reconcile unless they are annotated.
func TestNoNewUnannotatedPollTickers(t *testing.T) {
	found := newViolations()
	collectUnannotatedPollTickers(loadModule(t), found)
	ratchet(t, "poll_tickers", found)
}

func collectUnannotatedPollTickers(pkgs []*packages.Package, found *violations) {
	productionFiles(pkgs, func(pkg *packages.Package, file *ast.File, path string) {
		gated := false
		for _, prefix := range pollGatedPaths {
			gated = gated || strings.HasPrefix(path, prefix)
		}
		if !gated {
			return
		}
		annotated := map[int]bool{}
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if allowPollAnnotation.MatchString(comment.Text) {
					annotated[pkg.Fset.Position(comment.Pos()).Line] = true
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "NewTicker" && sel.Sel.Name != "Tick") {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pkgName, ok := pkg.TypesInfo.Uses[ident].(*types.PkgName)
			if !ok || pkgName.Imported().Path() != "time" {
				return true
			}
			pos := pkg.Fset.Position(sel.Pos())
			if annotated[pos.Line] || annotated[pos.Line-1] {
				return true
			}
			found.add(path+" time."+sel.Sel.Name, pos)
			return true
		})
	})
}
