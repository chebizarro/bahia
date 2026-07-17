package agentmemory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestClientConfiguredWithoutDurableTaskStoreFailsClosed(t *testing.T) {
	client := NewClient(Config{URL: "http://localhost:8282"}, nil)
	if err := client.RegisterAgent(t.Context(), "agent", "npub", nil); !errors.Is(err, ErrTaskIDStoreNotConfigured) {
		t.Fatalf("RegisterAgent() error = %v, want ErrTaskIDStoreNotConfigured", err)
	}
}

func TestClientTypedHelpersUseMCPToolsCall(t *testing.T) {
	var called []string
	var identityDetails []map[string]any
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
		arguments := params["arguments"].(map[string]any)
		if tool == "memory_event" && arguments["action"] == "agent_identity_registered" {
			identityDetails = append(identityDetails, arguments["detail"].(map[string]any))
		}
		switch tool {
		case "memory_task_start":
			writeAgentMemoryResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "task_id: task-1\n\n## Recent work (warm memory)\nNo recent tasks."}}})
		case "memory_event":
			writeAgentMemoryResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "event recorded: event-1"}}})
		case "context_get":
			writeAgentMemoryResult(t, w, req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": `[{"type":"fact","content":"hello"}]`}}})
		default:
			t.Fatalf("unexpected tool %s", tool)
		}
	}))
	defer server.Close()

	taskIDFile := filepath.Join(t.TempDir(), "task-ids.json")
	client := NewClient(Config{URL: server.URL, TaskIDFile: taskIDFile}, nil)
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

	// A new client process reuses the durable task ID and records updated identity
	// metadata without creating a second upstream task.
	restarted := NewClient(Config{URL: server.URL, TaskIDFile: taskIDFile}, nil)
	if err := restarted.RegisterAgent(t.Context(), "agent-1", "npub2", map[string]interface{}{"tier": "prod"}); err != nil {
		t.Fatalf("RegisterAgent() after restart error = %v", err)
	}

	want := []string{"memory_task_start", "memory_event", "memory_event", "context_get", "memory_event"}
	if len(called) != len(want) {
		t.Fatalf("called = %v", called)
	}
	for i := range want {
		if called[i] != want[i] {
			t.Fatalf("called = %v, want %v", called, want)
		}
	}
	if len(identityDetails) != 2 {
		t.Fatalf("identity event details = %#v, want two registration events", identityDetails)
	}
	if identityDetails[0]["npub"] != "npub1" || identityDetails[1]["npub"] != "npub2" {
		t.Fatalf("identity npubs were not preserved: %#v", identityDetails)
	}
	firstMetadata := identityDetails[0]["metadata"].(map[string]any)
	secondMetadata := identityDetails[1]["metadata"].(map[string]any)
	if firstMetadata["tier"] != "dev" || secondMetadata["tier"] != "prod" {
		t.Fatalf("identity metadata was not preserved: %#v", identityDetails)
	}
}

func writeAgentMemoryResult(t *testing.T, w http.ResponseWriter, id int64, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
