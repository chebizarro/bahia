package mcp

import (
	"context"
	"os"
	"path/filepath"
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
	}
	for name, found := range names {
		if !found {
			t.Errorf("missing expected tool: %s", name)
		}
	}
}

func TestDocsResourceCatalog(t *testing.T) {
	catalog := DocsResourceCatalog()
	if len(catalog) == 0 {
		t.Error("expected non-empty docs catalog")
	}

	// Should include key topics
	required := []string{"index", "getting-started", "mcp-tools", "features-services"}
	catalogMap := make(map[string]bool)
	for _, topic := range catalog {
		catalogMap[topic] = true
	}
	for _, req := range required {
		if !catalogMap[req] {
			t.Errorf("catalog missing required topic: %s", req)
		}
	}
}

func TestHandleDocsList(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err := server.handleDocsList(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("handleDocsList returned error: %v", err)
	}
	if result.IsError {
		t.Error("handleDocsList returned error result")
	}
	if len(result.Content) == 0 {
		t.Error("expected content in result")
	}
}

func TestHandleDocsRead(t *testing.T) {
	// Create a temporary docs directory with a test file
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "user-guide")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("failed to create temp docs dir: %v", err)
	}

	testContent := "# Test Documentation\n\nThis is test content."
	if err := os.WriteFile(filepath.Join(docsDir, "test-topic.md"), []byte(testContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Override the docs base path
	originalPath := DocsBasePath
	DocsBasePath = docsDir
	defer func() { DocsBasePath = originalPath }()

	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})

	// Test reading existing topic
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
	if result.Content[0].Text != testContent {
		t.Errorf("content mismatch: got %q, want %q", result.Content[0].Text, testContent)
	}

	// Test reading non-existent topic
	result, err = server.handleDocsRead(context.Background(), map[string]interface{}{
		"topic": "nonexistent",
	})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for non-existent topic")
	}

	// Test missing topic argument
	result, err = server.handleDocsRead(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("handleDocsRead returned error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing topic")
	}
}

func TestListDocsResources(t *testing.T) {
	// Create a temporary docs directory
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs", "user-guide")
	featuresDir := filepath.Join(docsDir, "features")
	if err := os.MkdirAll(featuresDir, 0755); err != nil {
		t.Fatalf("failed to create temp docs dir: %v", err)
	}

	// Create test files
	if err := os.WriteFile(filepath.Join(docsDir, "index.md"), []byte("# Index"), 0644); err != nil {
		t.Fatalf("failed to write index.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(featuresDir, "services.md"), []byte("# Services"), 0644); err != nil {
		t.Fatalf("failed to write services.md: %v", err)
	}

	// Override the docs base path
	originalPath := DocsBasePath
	DocsBasePath = docsDir
	defer func() { DocsBasePath = originalPath }()

	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	resources, err := server.listDocsResources(context.Background())
	if err != nil {
		t.Fatalf("listDocsResources returned error: %v", err)
	}

	if len(resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(resources))
	}

	// Check URIs are properly formatted
	for _, r := range resources {
		if r.URI == "" {
			t.Error("resource has empty URI")
		}
		if r.MIMEType != "text/markdown" {
			t.Errorf("expected text/markdown MIME type, got %s", r.MIMEType)
		}
	}
}
