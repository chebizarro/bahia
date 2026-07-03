package agentmemory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/adapters/mcpclient"
)

func TestClientMissingURLIsNotConfigured(t *testing.T) {
	client := NewClient(Config{}, nil)
	if client.Configured() {
		t.Fatal("zero-value agent-memory config should not be configured")
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Health() error = %v, want ErrNotConfigured", err)
	}
	if err := client.RegisterAgent(t.Context(), "agent", "npub", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RegisterAgent() error = %v, want ErrNotConfigured", err)
	}
}

func TestClientConfiguredTrimsURL(t *testing.T) {
	client := NewClient(Config{URL: " http://localhost:8282/ "}, nil)
	if !client.Configured() {
		t.Fatal("explicit agent-memory URL should configure client")
	}
	if client.baseURL != "http://localhost:8282" {
		t.Fatalf("baseURL = %q, want trimmed URL", client.baseURL)
	}
}

func TestClientTypedHelpersUseMCPToolsCall(t *testing.T) {
	var called []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcpclient.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "tools/call" {
			t.Fatalf("method = %s, want tools/call", req.Method)
		}
		params := req.Params.(map[string]any)
		tool := params["name"].(string)
		called = append(called, tool)
		switch tool {
		case "agent_register", "memory_add":
			writeAgentMemoryResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": `{}`}}})
		case "context_get":
			writeAgentMemoryResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": `[{"type":"fact","content":"hello"}]`}}})
		default:
			t.Fatalf("unexpected tool %s", tool)
		}
	}))
	defer server.Close()

	client := NewClient(Config{URL: server.URL}, nil)
	if err := client.RegisterAgent(t.Context(), "agent-1", "npub1", map[string]interface{}{"tier": "dev"}); err != nil {
		t.Fatalf("RegisterAgent() error = %v", err)
	}
	if err := client.SeedMemory(t.Context(), "agent-1", []MemoryEntry{{Type: "fact", Content: "hello"}}); err != nil {
		t.Fatalf("SeedMemory() error = %v", err)
	}
	entries, err := client.GetAgentContext(t.Context(), "agent-1")
	if err != nil {
		t.Fatalf("GetAgentContext() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "hello" {
		t.Fatalf("entries = %#v", entries)
	}
	want := []string{"agent_register", "memory_add", "context_get"}
	if len(called) != len(want) {
		t.Fatalf("called = %v", called)
	}
	for i := range want {
		if called[i] != want[i] {
			t.Fatalf("called = %v, want %v", called, want)
		}
	}
}

func writeAgentMemoryResult(t *testing.T, w http.ResponseWriter, id int64, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
