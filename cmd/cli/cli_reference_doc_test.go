package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestCLIReferenceDocumentsEveryCommand keeps docs/user-guide/cli-reference.md
// in lockstep with the cobra command tree: every runnable command must appear
// in the reference, and every `bahia <group> <sub>` the reference shows must
// exist.
func TestCLIReferenceDocumentsEveryCommand(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "user-guide", "cli-reference.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Drop global `--flag value` pairs so `bahia --relay wss://… config drift`
	// reads as `bahia config drift`.
	doc := regexp.MustCompile(` --[a-z][a-z-]*(?: "[^"\n]*"| '[^'\n]*'| [^ \n-][^ \n]*)?`).ReplaceAllString(string(raw), "")

	root := newRootCommand()
	known := map[string]bool{}
	var missing []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		known[c.CommandPath()] = true
		if len(c.Commands()) == 0 && c.Runnable() {
			if !documented(doc, c) {
				missing = append(missing, c.CommandPath())
			}
		}
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			walk(sub)
		}
	}
	walk(root)
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("CLI commands absent from cli-reference.md: %s", strings.Join(missing, ", "))
	}

	// Reverse check: each documented `bahia <group> <sub>` must exist.
	var stale []string
	seen := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^\s*bahia ([a-z][a-z-]*)(?: ([a-z][a-z-]*))?(?: ([a-z][a-z-]*))?`).FindAllStringSubmatch(doc, -1) {
		segments := []string{"bahia"}
		for _, part := range match[1:] {
			if part == "" {
				break
			}
			segments = append(segments, part)
			candidate := strings.Join(segments, " ")
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			if !known[candidate] && !knownAlternatives(known, segments) {
				stale = append(stale, candidate)
			}
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("cli-reference.md shows commands that do not exist: %s", strings.Join(stale, ", "))
	}
}

// documented reports whether the command path appears on one line of the
// reference, allowing the leaf to be written as a `a|b|c` alternative list.
func documented(doc string, c *cobra.Command) bool {
	parent := c.Parent().CommandPath()
	leaf := regexp.QuoteMeta(c.Name())
	pattern := regexp.MustCompile(`(?m)^[^\n]*` + regexp.QuoteMeta(parent) + ` [^\n ]*\b` + leaf + `\b`)
	return pattern.MatchString(doc)
}

// knownAlternatives resolves a trailing `a|b|c` token against the tree.
func knownAlternatives(known map[string]bool, segments []string) bool {
	last := segments[len(segments)-1]
	if !strings.Contains(last, "|") {
		return false
	}
	prefix := strings.Join(segments[:len(segments)-1], " ")
	for _, alt := range strings.Split(last, "|") {
		if !known[prefix+" "+alt] {
			return false
		}
	}
	return true
}
