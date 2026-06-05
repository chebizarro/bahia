package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	userdocs "github.com/openagentsinc/bahia/internal/docs"
	"go.uber.org/zap"
)

func TestDocsReadRoutesReturnCentralCatalogAndDocumentLinks(t *testing.T) {
	docsDir := writeRouterDocsFixture(t, map[string]string{
		"index.md":             "# Bahia User Guide\n\n[Services](features/services.md) and [External](https://example.com).",
		"features/services.md": "# Services\n\nService docs.",
		"mcp-tools.md":         "# MCP Tools\n\nReference.",
	})
	docsService := userdocs.New(docsDir)
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Docs: &docsService})

	catalogReq := httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil)
	catalogW := httptest.NewRecorder()
	h.ServeHTTP(catalogW, catalogReq)
	if catalogW.Code != http.StatusOK {
		t.Fatalf("catalog status=%d, want 200, body=%s", catalogW.Code, catalogW.Body.String())
	}
	var catalogEnvelope map[string]any
	if err := json.Unmarshal(catalogW.Body.Bytes(), &catalogEnvelope); err != nil {
		t.Fatalf("decode catalog response: %v", err)
	}
	catalogData := catalogEnvelope["data"].(map[string]any)
	if catalogData["count"].(float64) != 3 {
		t.Fatalf("catalog count = %#v, want 3", catalogData["count"])
	}
	groups := catalogData["groups"].([]any)
	if groups[0].(map[string]any)["category"] != "guide" || groups[1].(map[string]any)["category"] != "feature" || groups[2].(map[string]any)["category"] != "reference" {
		t.Fatalf("unexpected docs groups: %#v", groups)
	}

	docReq := httptest.NewRequest(http.MethodGet, "/api/v1/docs/index", nil)
	docW := httptest.NewRecorder()
	h.ServeHTTP(docW, docReq)
	if docW.Code != http.StatusOK {
		t.Fatalf("document status=%d, want 200, body=%s", docW.Code, docW.Body.String())
	}
	var docEnvelope map[string]any
	if err := json.Unmarshal(docW.Body.Bytes(), &docEnvelope); err != nil {
		t.Fatalf("decode document response: %v", err)
	}
	docData := docEnvelope["data"].(map[string]any)
	metadata := docData["metadata"].(map[string]any)
	if metadata["topic"] != "index" || metadata["href"] != "/docs/index" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if !strings.Contains(docData["markdown"].(string), "# Bahia User Guide") {
		t.Fatalf("document markdown missing heading: %#v", docData["markdown"])
	}
	links := docData["links"].([]any)
	if links[0].(map[string]any)["href"] != "/docs/features-services" || links[0].(map[string]any)["topic"] != "features-services" {
		t.Fatalf("internal link was not resolved by central docs service: %#v", links[0])
	}
	if links[1].(map[string]any)["href"] != "https://example.com" || links[1].(map[string]any)["external"] != true {
		t.Fatalf("external link was not preserved: %#v", links[1])
	}
	for _, forbidden := range []string{"request_event_id", "result_kind", "published_relays"} {
		if _, ok := docData[forbidden]; ok {
			t.Fatalf("docs read route returned Nostr mutation metadata %q: %#v", forbidden, docData)
		}
	}
}

func TestDocsReadRoutesReturnExplicitErrorsAndRejectWrites(t *testing.T) {
	docsDir := writeRouterDocsFixture(t, map[string]string{"index.md": "# Index"})
	docsService := userdocs.New(docsDir)
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Docs: &docsService})

	tests := []struct {
		method string
		path   string
		status int
		body   string
	}{
		{http.MethodGet, "/api/v1/docs/missing", http.StatusNotFound, "documentation topic not found: missing"},
		{http.MethodGet, "/api/v1/docs/..%2Findex", http.StatusBadRequest, "invalid documentation topic"},
		{http.MethodPost, "/api/v1/docs", http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status=%d, want %d, body=%s", w.Code, tt.status, w.Body.String())
			}
			if tt.body != "" && !strings.Contains(w.Body.String(), tt.body) {
				t.Fatalf("body=%s, want it to contain %q", w.Body.String(), tt.body)
			}
			if strings.Contains(w.Body.String(), "request_event_id") || strings.Contains(w.Body.String(), "subscribe to Nostr result") {
				t.Fatalf("docs error/write route must not claim Nostr command semantics: %s", w.Body.String())
			}
		})
	}
}

func TestDocsReadRoutesTreatMissingDocsRootAsServerError(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "does-not-exist")
	docsService := userdocs.New(missingRoot)
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{Docs: &docsService})

	for _, path := range []string{"/api/v1/docs", "/api/v1/docs/index"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d, want 500, body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "documentation root is unavailable") {
				t.Fatalf("body=%s, want missing docs root error", w.Body.String())
			}
		})
	}
}

func writeRouterDocsFixture(t *testing.T, files map[string]string) string {
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
