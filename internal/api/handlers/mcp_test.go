package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	mcpserver "github.com/openagentsinc/bahia/internal/mcp"
	"go.uber.org/zap"
)

func TestMCPHandler_HandleJSONRPCInitializeAndListTools(t *testing.T) {
	h := NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())

	for _, tc := range []struct {
		name   string
		method string
	}{
		{name: "initialize", method: "initialize"},
		{name: "tools list", method: "tools/list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `"}`)
			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
			w := httptest.NewRecorder()

			h.HandleJSONRPC(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["jsonrpc"] != "2.0" || resp["error"] != nil || resp["result"] == nil {
				t.Fatalf("unexpected response: %#v", resp)
			}
		})
	}
}

func TestMCPHandler_HandleJSONRPCResourcesList(t *testing.T) {
	h := NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())

	body := []byte(`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleJSONRPC(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["jsonrpc"] != "2.0" || resp["error"] != nil || resp["result"] == nil {
		t.Fatalf("unexpected response: %#v", resp)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got %T", resp["result"])
	}
	resources, ok := result["resources"].([]any)
	if !ok {
		t.Fatalf("expected resources array, got %T", result["resources"])
	}
	// No DNS lister configured, so resources should be empty
	if len(resources) != 0 {
		t.Fatalf("expected empty resources, got %d", len(resources))
	}
}

func TestMCPHandler_HandleJSONRPCResourcesListIncludesFIPSMeshResources(t *testing.T) {
	server := mcpserver.NewServerWithOptions(nil, zap.NewNop(), mcpserver.ServerDeps{DNSEndpoints: handlerDNSEndpointLister(func(ctx context.Context) ([]domain.DNSEndpoint, error) {
		return []domain.DNSEndpoint{{Family: domain.DNSEndpointFamilyMesh, Name: "node-a", Environment: "mesh", Zone: "mesh.example", FQDN: "node-a.mesh.example", Address: "fd00::1", Health: domain.HealthStatusHealthy, DriftStatus: domain.DriftStatusInSync}}, nil
	})})
	h := NewMCPHandler(server, zap.NewNop())

	body := []byte(`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleJSONRPC(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	result := resp["result"].(map[string]any)
	resources := result["resources"].([]any)
	found := false
	for _, raw := range resources {
		resource := raw.(map[string]any)
		if resource["uri"] == "bahia://fips/mesh/node/node-a.mesh.example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("FIPS mesh resource missing from resources/list: %#v", resources)
	}
}

func TestMCPHandler_NostrCorrelationMetadataExtractsIDs(t *testing.T) {
	h := NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())
	result := &mcpserver.ToolResult{Content: []mcpserver.Content{{Type: "text", Text: `{"status":"submitted","intent_id":"intent-1","service_id":"svc-1"}`}}}

	meta := h.nostrCorrelationMetadata("bahia_deploy", map[string]interface{}{"environment_id": "env-1"}, result)

	if meta["intent_id"] != "intent-1" || meta["service_id"] != "svc-1" || meta["environment_id"] != "env-1" {
		t.Fatalf("expected request/result identifiers in metadata, got %#v", meta)
	}
	if len(meta["transport_kinds"].([]int)) == 0 || len(meta["observable_kinds"].([]int)) == 0 {
		t.Fatalf("expected canonical transport/observable kind catalogs in metadata: %#v", meta)
	}
}

type handlerDNSEndpointLister func(context.Context) ([]domain.DNSEndpoint, error)

func (f handlerDNSEndpointLister) ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	return f(ctx)
}

func TestMCPHandler_NostrCorrelationMetadataMapsGenericIDs(t *testing.T) {
	h := NewMCPHandler(mcpserver.NewServer(nil, zap.NewNop()), zap.NewNop())
	result := &mcpserver.ToolResult{Content: []mcpserver.Content{{Type: "text", Text: `{"id":"svc-1","name":"api"}`}}}

	meta := h.nostrCorrelationMetadata("bahia_get_service", nil, result)

	if meta["service_id"] != "svc-1" {
		t.Fatalf("expected generic id to map to service_id, got %#v", meta)
	}
}
