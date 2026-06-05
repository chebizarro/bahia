package docs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogScansMarkdownDeterministically(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"index.md":                  "# Bahia User Guide\n\nWelcome.",
		"getting-started.md":        "# Getting Started\n\nStart here.",
		"mcp-tools.md":              "# MCP Tools\n\nTools.",
		"features/services.md":      "# Services\n\nServices.",
		"features/fleet-health.md":  "# Fleet Health\n\nFleet.",
		"features/not-markdown.txt": "ignored",
	})

	catalog, err := New(docsDir).Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}

	gotTopics := topicsOnly(catalog)
	wantTopics := []string{"features-fleet-health", "features-services", "getting-started", "index", "mcp-tools"}
	if !reflect.DeepEqual(gotTopics, wantTopics) {
		t.Fatalf("topics mismatch\ngot:  %#v\nwant: %#v", gotTopics, wantTopics)
	}

	byTopic := map[string]Topic{}
	for _, item := range catalog {
		byTopic[item.Topic] = item
	}
	fleet := byTopic["features-fleet-health"]
	if fleet.Title != "Fleet Health" {
		t.Fatalf("fleet title = %q, want Fleet Health", fleet.Title)
	}
	if fleet.Category != "feature" {
		t.Fatalf("fleet category = %q, want feature", fleet.Category)
	}
	if fleet.SourcePath != "features/fleet-health.md" {
		t.Fatalf("fleet source path = %q", fleet.SourcePath)
	}
	if fleet.Href != "/docs/features-fleet-health" {
		t.Fatalf("fleet href = %q", fleet.Href)
	}
	if byTopic["mcp-tools"].Category != "reference" {
		t.Fatalf("mcp-tools category = %q, want reference", byTopic["mcp-tools"].Category)
	}
}

func TestCatalogRejectsDuplicateNormalizedTopics(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"features/services.md": "# Services",
		"features-services.md": "# Duplicate Services",
	})

	if _, err := New(docsDir).Catalog(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate documentation topic") {
		t.Fatalf("duplicate topic error = %v, want duplicate documentation topic", err)
	}
}

func TestCatalogSkipsSymlinkedMarkdown(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"index.md": "# Index",
	})
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsidePath, []byte("# Outside"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(docsDir, "outside.md")); err != nil {
		t.Skipf("symlinks are not available on this platform: %v", err)
	}

	catalog, err := New(docsDir).Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog returned error: %v", err)
	}
	gotTopics := topicsOnly(catalog)
	wantTopics := []string{"index"}
	if !reflect.DeepEqual(gotTopics, wantTopics) {
		t.Fatalf("topics mismatch with symlinked file\ngot:  %#v\nwant: %#v", gotTopics, wantTopics)
	}
}

func TestReadResolvesScannedTopicsAndRejectsUnsafeInput(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"index.md":                 "# Index\n\nIndex body.",
		"features/fleet-health.md": "# Fleet Health\n\nFleet body.",
	})
	service := New(docsDir)

	doc, err := service.Read(context.Background(), "features-fleet-health")
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if doc.Topic.Title != "Fleet Health" || doc.Markdown != "# Fleet Health\n\nFleet body." {
		t.Fatalf("unexpected document: %#v", doc)
	}

	if _, err := service.Read(context.Background(), "missing-topic"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing topic error = %v, want ErrNotFound", err)
	}
	if _, err := service.Read(context.Background(), "../index"); !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("traversal topic error = %v, want ErrInvalidTopic", err)
	}
	if _, err := service.Read(context.Background(), "features/../index"); !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("path separator topic error = %v, want ErrInvalidTopic", err)
	}
}

func TestResolveMarkdownLinkMapsInternalDocsAndPreservesExternalLinks(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"index.md":                 "# Index",
		"core-concepts.md":         "# Core Concepts",
		"features/services.md":     "# Services",
		"features/fleet-health.md": "# Fleet Health",
	})
	service := New(docsDir)

	resolved, err := service.ResolveMarkdownLink(context.Background(), "index.md", "features/fleet-health.md#checks")
	if err != nil {
		t.Fatalf("ResolveMarkdownLink returned error: %v", err)
	}
	if resolved.Href != "/docs/features-fleet-health#checks" || resolved.Topic != "features-fleet-health" || resolved.External {
		t.Fatalf("unexpected resolved link: %#v", resolved)
	}

	resolved, err = service.ResolveMarkdownLink(context.Background(), "features/services.md", "../core-concepts.md")
	if err != nil {
		t.Fatalf("ResolveMarkdownLink for parent doc returned error: %v", err)
	}
	if resolved.Href != "/docs/core-concepts" || resolved.Topic != "core-concepts" {
		t.Fatalf("unexpected parent resolved link: %#v", resolved)
	}

	resolved, err = service.ResolveMarkdownLink(context.Background(), "features/services.md", "#section")
	if err != nil {
		t.Fatalf("ResolveMarkdownLink for fragment returned error: %v", err)
	}
	if resolved.Href != "/docs/features-services#section" || resolved.Topic != "features-services" {
		t.Fatalf("unexpected fragment resolved link: %#v", resolved)
	}

	resolved, err = service.ResolveMarkdownLink(context.Background(), "index.md", "https://example.com/docs")
	if err != nil {
		t.Fatalf("external link returned error: %v", err)
	}
	if !resolved.External || resolved.Href != "https://example.com/docs" {
		t.Fatalf("unexpected external resolution: %#v", resolved)
	}

	resolved, err = service.ResolveMarkdownLink(context.Background(), "index.md", "mailto:support@example.com")
	if err != nil {
		t.Fatalf("mailto link returned error: %v", err)
	}
	if !resolved.External || resolved.Href != "mailto:support@example.com" {
		t.Fatalf("unexpected mailto resolution: %#v", resolved)
	}
}

func TestResolveMarkdownLinkRejectsOutsideRootAndMissingTargets(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"index.md":             "# Index",
		"features/services.md": "# Services",
	})
	service := New(docsDir)

	if _, err := service.ResolveMarkdownLink(context.Background(), "features/services.md", "../../README.md"); !errors.Is(err, ErrOutsideDocsRoot) {
		t.Fatalf("outside-root link error = %v, want ErrOutsideDocsRoot", err)
	}
	if _, err := service.ResolveMarkdownLink(context.Background(), "features/services.md", "missing.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target error = %v, want ErrNotFound", err)
	}
	if _, err := service.ResolveMarkdownLink(context.Background(), "features/services.md", "image.png"); !errors.Is(err, ErrUnsupportedLink) {
		t.Fatalf("non-markdown link error = %v, want ErrUnsupportedLink", err)
	}
	if _, err := service.ResolveMarkdownLink(context.Background(), "features/services.md", "javascript:alert(1)"); !errors.Is(err, ErrUnsupportedLink) {
		t.Fatalf("unsafe external scheme error = %v, want ErrUnsupportedLink", err)
	}
	if _, err := service.ResolveMarkdownLink(context.Background(), "features/services.md", "file:///etc/passwd"); !errors.Is(err, ErrUnsupportedLink) {
		t.Fatalf("local file scheme error = %v, want ErrUnsupportedLink", err)
	}
}

func TestReadWithLinksReturnsResolvedAndRejectedDocumentLinks(t *testing.T) {
	docsDir := writeDocsFixture(t, map[string]string{
		"index.md":             "# Index\n\n[Services](features/services.md) [External](https://example.com) [Bad](../README.md)",
		"features/services.md": "# Services",
	})
	service := New(docsDir)

	doc, err := service.ReadWithLinks(context.Background(), "index")
	if err != nil {
		t.Fatalf("ReadWithLinks returned error: %v", err)
	}
	if len(doc.Links) != 3 {
		t.Fatalf("links len = %d, want 3: %#v", len(doc.Links), doc.Links)
	}
	if doc.Links[0].Original != "features/services.md" || doc.Links[0].Href != "/docs/features-services" || doc.Links[0].Topic != "features-services" || doc.Links[0].Status != "resolved" {
		t.Fatalf("unexpected internal link: %#v", doc.Links[0])
	}
	if doc.Links[1].Original != "https://example.com" || doc.Links[1].Href != "https://example.com" || !doc.Links[1].External || doc.Links[1].Status != "resolved" {
		t.Fatalf("unexpected external link: %#v", doc.Links[1])
	}
	if doc.Links[2].Original != "../README.md" || doc.Links[2].Status != "outside_docs_root" || doc.Links[2].Href != "" {
		t.Fatalf("unexpected rejected link: %#v", doc.Links[2])
	}
}

func writeDocsFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	docsDir := filepath.Join(t.TempDir(), "docs", "user-guide")
	for relPath, content := range files {
		fullPath := filepath.Join(docsDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("write fixture file %s: %v", relPath, err)
		}
	}
	return docsDir
}

func topicsOnly(catalog []Topic) []string {
	topics := make([]string, 0, len(catalog))
	for _, item := range catalog {
		topics = append(topics, item.Topic)
	}
	return topics
}
