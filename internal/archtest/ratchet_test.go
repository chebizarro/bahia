package archtest

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

// updateBaselineEnv rewrites the baselines instead of checking them.
const updateBaselineEnv = "ARCHTEST_UPDATE_BASELINE"

const regenerateHint = "if the new usage is intended, fix it instead; baselines only record pre-existing debt. " +
	"Regenerate after removing violations with: make arch-baseline"

const modulePath = "github.com/openagentsinc/bahia"

// violations counts gate hits per baseline key and remembers where they are.
type violations struct {
	counts    map[string]int
	locations map[string][]string
}

func newViolations() *violations {
	return &violations{counts: map[string]int{}, locations: map[string][]string{}}
}

func (v *violations) add(key string, pos token.Position) {
	v.counts[key]++
	v.locations[key] = append(v.locations[key], fmt.Sprintf("%s:%d", relPath(pos.Filename), pos.Line))
}

// ratchet compares current violations against testdata/<name>.baseline. It
// fails for any key whose count grew; shrinking debt is only logged.
func ratchet(t *testing.T, name string, current *violations) {
	t.Helper()
	path := filepath.Join(repoRoot(t), "internal", "archtest", "testdata", name+".baseline")
	if os.Getenv(updateBaselineEnv) == "1" {
		previous := readBaseline(t, path)
		writeBaseline(t, path, name, current.counts)
		t.Logf("wrote %d %s baseline entries to %s", len(current.counts), name, relPath(path))
		t.Log(baselineChangeSummary(name, previous, current.counts))
		return
	}
	baseline := readBaseline(t, path)
	keys := make([]string, 0, len(current.counts))
	for key := range current.counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if got, allowed := current.counts[key], baseline[key]; got > allowed {
			t.Errorf("%s: new violation %q (%d found, baseline allows %d) at %s\n%s",
				name, key, got, allowed, strings.Join(current.locations[key], ", "), regenerateHint)
		}
	}
	for key, allowed := range baseline {
		if got := current.counts[key]; got < allowed {
			t.Logf("%s: %q dropped from %d to %d; tighten the baseline with make arch-baseline", name, key, allowed, got)
		}
	}
}

// baselineChangeSummary lists what a regeneration changed so a reviewer can
// confirm the ratchet only tightened: "+" entries are new or grown debt and
// need a reason, "-" entries were paid down.
func baselineChangeSummary(name string, previous, current map[string]int) string {
	keys := map[string]bool{}
	for key := range previous {
		keys[key] = true
	}
	for key := range current {
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	var added, removed []string
	for _, key := range sorted {
		before, after := previous[key], current[key]
		switch {
		case after > before:
			added = append(added, fmt.Sprintf("  + %s (%d -> %d)", key, before, after))
		case after < before:
			removed = append(removed, fmt.Sprintf("  - %s (%d -> %d)", key, before, after))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BASELINE SUMMARY %s: %d added/grown, %d removed/shrunk", name, len(added), len(removed))
	for _, line := range append(added, removed...) {
		b.WriteString("\n" + line)
	}
	return b.String()
}

func writeBaseline(t *testing.T, path, name string, counts map[string]int) {
	t.Helper()
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s architecture ratchet baseline.\n", name)
	b.WriteString("# Pre-existing violations only: entries may shrink, never grow.\n")
	b.WriteString("# Regenerate with: make arch-baseline\n")
	b.WriteString("# Format: <count>\\t<key>\n")
	for _, key := range keys {
		fmt.Fprintf(&b, "%d\t%s\n", counts[key], key)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write baseline %s: %v", path, err)
	}
}

func readBaseline(t *testing.T, path string) map[string]int {
	t.Helper()
	out := map[string]int{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatalf("open baseline %s: %v", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		countText, key, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed baseline line in %s: %q", path, line)
		}
		count, err := strconv.Atoi(countText)
		if err != nil {
			t.Fatalf("malformed baseline count in %s: %q", path, line)
		}
		out[key] = count
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read baseline %s: %v", path, err)
	}
	return out
}

var (
	loadOnce   sync.Once
	loadedPkgs []*packages.Package
	loadErr    error
	rootDir    string
)

// loadModule type-checks every package under internal/, cmd/ and pkg/,
// including test variants, once per test binary.
func loadModule(t *testing.T) []*packages.Package {
	t.Helper()
	root := repoRoot(t)
	loadOnce.Do(func() {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax |
				packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
			Dir:   root,
			Tests: true,
			Env:   append(os.Environ(), "CGO_ENABLED=0"),
		}
		loadedPkgs, loadErr = packages.Load(cfg, "./internal/...", "./cmd/...", "./pkg/...")
		if loadErr == nil {
			var errs []string
			packages.Visit(loadedPkgs, nil, func(pkg *packages.Package) {
				for _, err := range pkg.Errors {
					errs = append(errs, err.Error())
				}
			})
			if len(errs) > 0 {
				loadErr = fmt.Errorf("package load errors:\n%s", strings.Join(errs, "\n"))
			}
		}
	})
	if loadErr != nil {
		t.Fatalf("load module packages: %v", loadErr)
	}
	return loadedPkgs
}

// isTestVariant reports whether pkg is a test build of a package (either the
// in-package "p [p.test]" variant or the external "p_test" package).
func isTestVariant(pkg *packages.Package) bool {
	return strings.Contains(pkg.ID, " [") || strings.HasSuffix(pkg.PkgPath, "_test") || strings.HasSuffix(pkg.ID, ".test")
}

// productionFiles yields the non-test source files of the non-test package
// variants. Test variants repeat the same production files, so they are
// skipped to avoid double counting.
func productionFiles(pkgs []*packages.Package, visit func(pkg *packages.Package, file *ast.File, path string)) {
	for _, pkg := range pkgs {
		if isTestVariant(pkg) {
			continue
		}
		for i, file := range pkg.Syntax {
			path := relPath(pkg.CompiledGoFiles[i])
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			visit(pkg, file, path)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	if rootDir != "" {
		return rootDir
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			rootDir = dir
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repository root from %s", filename)
		}
		dir = parent
	}
}

func relPath(path string) string {
	if rootDir == "" {
		return path
	}
	if rel, err := filepath.Rel(rootDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
