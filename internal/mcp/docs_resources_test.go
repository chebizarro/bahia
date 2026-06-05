package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestDocsToolDefinitions(t *testing.T) {
	tools := docsToolDefinitions()
	if len(tools) != 2 {
		t.Errorf("expected 2 docs tools, got %d", len(tools))
	}

	names := map[string]bool{"bahia_docs_read": false, "bahia_docs_list": false}
	for _, tool := range tools {
		if _, ok := names[tool.Name]; ok {
			names[tool.Name] = true
		}
		if tool.Name == "bahia_docs_read" {
			if !strings.Contains(tool.Description, "bahia_docs_list") {
				t.Fatalf("read tool description should direct agents to bahia_docs_list: %q", tool.Description)
			}
			if strings.Contains(tool.Description, "features-services, features-environments") {
				t.Fatalf("read tool description embeds a static topic list: %q", tool.Description)
			}
		}
	}
	for name, found := range names {
		if !found {
			t.Errorf("missing expected tool: %s", name)
		}
	}
}

func TestDocsResourceCatalogScansDocsRoot(t *testing.T) {
	docsDir := writeMCPDocsFixture(t, map[string]string{
		"index.md":                 "# Index",
		"getting-started.md":       "# Getting Started",
		"features/services.md":     "# Services",
		"features/fleet-health.md": "# Fleet Health",
	})
	withDocsBasePath(t, docsDir)

	catalog := DocsResourceCatalog()
	want := []string{"features-fleet-health", "features-services", "getting-started", "index"}
	if !reflect.DeepEqual(catalog, want) {
		t.Fatalf("catalog mismatch\ngot:  %#v\nwant: %#v", catalog, want)
	}
}

func TestHandleDocsListReturnsScannedCatalog(t *testing.T) {
	docsDir := writeMCPDocsFixture(t, map[string]string{
		"index.md":                 "# Index",
		"features/services.md":     "# Services",
		"features/fleet-health.md": "# Fleet Health",
	})
	withDocsBasePath(t, docsDir)

	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err := server.handleDocsList(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("handleDocsList returned error: %v", err)
	}
	if result.IsError {
		t.Error("handleDocsList returned error result")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content in result")
	}

	var payload struct {
		Topics  []string `json:"topics"`
		Catalog []struct {
			Topic      string `json:"topic"`
			Title      string `json:"title"`
			Category   string `json:"category"`
			SourcePath string `json:"sourcePath"`
			Href       string `json:"href"`
		} `json:"catalog"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("failed to unmarshal docs list payload: %v", err)
	}
	wantTopics := []string{"features-fleet-health", "features-services", "index"}
	if !reflect.DeepEqual(payload.Topics, wantTopics) {
		t.Fatalf("topics mismatch\ngot:  %#v\nwant: %#v", payload.Topics, wantTopics)
	}
	if payload.Count != len(wantTopics) || len(payload.Catalog) != len(wantTopics) {
		t.Fatalf("unexpected count/catalog length: count=%d catalog=%d", payload.Count, len(payload.Catalog))
	}
	fleet := payload.Catalog[0]
	if fleet.Topic != "features-fleet-health" || fleet.Title != "Fleet Health" || fleet.Category != "feature" || fleet.Href != "/docs/features-fleet-health" {
		t.Fatalf("unexpected fleet catalog entry: %#v", fleet)
	}
}

func TestHandleDocsRead(t *testing.T) {
	docsDir := writeMCPDocsFixture(t, map[string]string{
		"test-topic.md": "# Test Documentation\n\nThis is test content.",
	})
	withDocsBasePath(t, docsDir)

	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})

	result, err := server.handleDocsRead(context.Background(), map[string]interface{}{
		"topic": "test-topic",
	})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if result.IsError {
		t.Error("handleDocsRead returned error result for existing topic")
	}
	if len(result.Content) == 0 {
		t.Error("expected content in result")
	}
	if result.Content[0].Text != "# Test Documentation\n\nThis is test content." {
		t.Errorf("content mismatch: got %q", result.Content[0].Text)
	}

	result, err = server.handleDocsRead(context.Background(), map[string]interface{}{
		"topic": "nonexistent",
	})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for non-existent topic")
	}

	result, err = server.handleDocsRead(context.Background(), map[string]interface{}{
		"topic": "../test-topic",
	})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "invalid documentation topic") {
		t.Fatalf("expected invalid topic error result, got %#v", result)
	}

	result, err = server.handleDocsRead(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing topic")
	}
}

func TestHandleDocsReadReportsOperationalErrors(t *testing.T) {
	docsFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(docsFile, []byte("# Not a directory"), 0644); err != nil {
		t.Fatalf("failed to write docs file fixture: %v", err)
	}
	withDocsBasePath(t, docsFile)

	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err := server.handleDocsRead(context.Background(), map[string]interface{}{
		"topic": "index",
	})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "failed to read documentation topic") {
		t.Fatalf("expected operational read failure, got %#v", result)
	}
}

func TestListDocsResources(t *testing.T) {
	docsDir := writeMCPDocsFixture(t, map[string]string{
		"index.md":                 "# Index",
		"features/services.md":     "# Services",
		"features/fleet-health.md": "# Fleet Health",
	})
	withDocsBasePath(t, docsDir)

	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	resources, err := server.listDocsResources(context.Background())
	if err != nil {
		t.Fatalf("listDocsResources returned error: %v", err)
	}

	if len(resources) != 3 {
		t.Errorf("expected 3 resources, got %d", len(resources))
	}

	byURI := map[string]Resource{}
	for _, r := range resources {
		if r.URI == "" {
			t.Error("resource has empty URI")
		}
		if r.MIMEType != "text/markdown" {
			t.Errorf("expected text/markdown MIME type, got %s", r.MIMEType)
		}
		byURI[r.URI] = r
	}

	fleet := byURI["bahia://docs/features-fleet-health"]
	if fleet.Name != "docs:features-fleet-health" || fleet.Description != "Fleet Health" {
		t.Fatalf("unexpected fleet resource: %#v", fleet)
	}
	if fleet.Metadata["path"] != "features/fleet-health.md" || fleet.Metadata["category"] != "feature" || fleet.Metadata["href"] != "/docs/features-fleet-health" {
		t.Fatalf("unexpected fleet metadata: %#v", fleet.Metadata)
	}
}

func writeMCPDocsFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	docsDir := filepath.Join(t.TempDir(), "docs", "user-guide")
	for relPath, content := range files {
		fullPath := filepath.Join(docsDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("failed to create temp docs dir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", relPath, err)
		}
	}
	return docsDir
}

func withDocsBasePath(t *testing.T, docsDir string) {
	t.Helper()
	originalPath := DocsBasePath
	DocsBasePath = docsDir
	t.Cleanup(func() { DocsBasePath = originalPath })
}
