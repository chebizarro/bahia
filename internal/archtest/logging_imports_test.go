package archtest

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestProductionLoggingImportsUseZap keeps the non-test logging surface on zap.
// The Khatru sidecar uses zap.NewStdLog to satisfy its stdlib logger API, so
// Bahia does not need a source import of log even at that integration boundary.
func TestProductionLoggingImportsUseZap(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "third_party") {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".gen.go") || strings.HasSuffix(name, "_generated.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.HasPrefix(bytes.TrimSpace(source), []byte("// Code generated ")) && bytes.Contains(source[:min(len(source), 2048)], []byte("DO NOT EDIT.")) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			imported, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if imported == "log" || imported == "log/slog" {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports %q; use go.uber.org/zap", rel, imported)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
