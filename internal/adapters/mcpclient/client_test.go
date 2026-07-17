package mcpclient

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientHardensInjectedHTTPClient(t *testing.T) {
	injected := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec -- proves hardening removes the unsafe test input
	client := NewClient(Config{Name: "fixture", URL: "https://mcp.example", Timeout: -time.Second, HTTPClient: injected}, nil)
	if client.httpClient == injected {
		t.Fatal("injected HTTP client was mutated instead of cloned")
	}
	if client.httpClient.Timeout <= 0 {
		t.Fatalf("timeout = %s, want positive timeout", client.httpClient.Timeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatalf("transport = %#v, want TLS-configured *http.Transport", client.httpClient.Transport)
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("TLS minimum = %d, want TLS 1.2 or newer", transport.TLSClientConfig.MinVersion)
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS certificate verification remains disabled")
	}
}

func TestClientInitializeListAndCallWithAuthAndPrefix(t *testing.T) {
	var methods []string
	var sawAuth bool
	var calledTool string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("path = %s, want /mcp", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer test-token" {
			sawAuth = true
		}
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		methods = append(methods, req.Method)
		switch req.Method {
		case "initialize":
			writeRPCResult(t, w, req.ID, map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "fixture"}})
		case "tools/list":
			writeRPCResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{
				"name":        "search",
				"description": "Search docs",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
			}}})
		case "tools/call":
			params := req.Params.(map[string]any)
			calledTool = params["name"].(string)
			writeRPCResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": `{"ok":true}`}}})
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := NewClient(Config{Name: "docs", URL: server.URL, ToolPrefix: "docs_", AuthHeaders: map[string]string{"Authorization": "Bearer test-token"}}, nil)
	init, err := client.Initialize(t.Context())
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if init.ProtocolVersion != "2024-11-05" {
		t.Fatalf("protocol version = %q", init.ProtocolVersion)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "docs_search" || tools[0].RawName != "search" {
		t.Fatalf("tools = %#v", tools)
	}
	result, err := client.CallTool(t.Context(), "docs_search", map[string]any{"q": "bahia"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if calledTool != "search" {
		t.Fatalf("called upstream tool = %q, want search", calledTool)
	}
	if got := strings.TrimSpace(string(result.ResultJSON())); got != `{"ok":true}` {
		t.Fatalf("ResultJSON() = %s", got)
	}
	if !sawAuth {
		t.Fatal("server did not receive configured auth header")
	}
	wantMethods := []string{"initialize", "tools/list", "tools/call"}
	if len(methods) != len(wantMethods) {
		t.Fatalf("methods = %v", methods)
	}
	for i := range wantMethods {
		if methods[i] != wantMethods[i] {
			t.Fatalf("methods = %v, want %v", methods, wantMethods)
		}
	}
}

func TestClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client := NewClient(Config{Name: "slow", URL: server.URL, Timeout: 10 * time.Millisecond}, nil)
	_, err := client.Initialize(t.Context())
	if err == nil {
		t.Fatal("Initialize() error = nil, want timeout")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("Initialize() error = %v, want timeout", err)
	}
}

func TestClientListToolsRejectsPrefixedNameCollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeRPCResult(t, w, req.ID, map[string]any{"tools": []map[string]any{{"name": "search"}, {"name": "docs_search"}}})
	}))
	defer server.Close()

	client := NewClient(Config{Name: "docs", URL: server.URL, ToolPrefix: "docs_"}, nil)
	_, err := client.ListTools(t.Context())
	if err == nil || !strings.Contains(err.Error(), "duplicate prefixed tool name") {
		t.Fatalf("ListTools() error = %v, want duplicate prefixed tool name", err)
	}
}

func TestClientMissingURL(t *testing.T) {
	client := NewClient(Config{}, nil)
	if client.Configured() {
		t.Fatal("empty URL should not configure client")
	}
	_, err := client.Initialize(t.Context())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Initialize() error = %v, want ErrNotConfigured", err)
	}
}

func writeRPCResult(t *testing.T, w http.ResponseWriter, id int64, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
