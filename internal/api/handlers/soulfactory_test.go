package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestSoulFactoryHandlerReturnsEnabledAgentRuntimes(t *testing.T) {
	cfg := &config.Config{}
	cfg.SoulFactory.AgentRuntimes = []string{"openclaw", "metiq", "synthetic-3"}
	handler := NewSoulFactoryHandler(cfg)

	recorder := httptest.NewRecorder()
	handler.GetRuntimes(recorder, httptest.NewRequest(http.MethodGet, "/soulfactory/runtimes", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response struct {
		Data struct {
			AgentRuntimes []string `json:"agent_runtimes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data.AgentRuntimes) != 3 || response.Data.AgentRuntimes[2] != "synthetic-3" {
		t.Fatalf("agent_runtimes = %v", response.Data.AgentRuntimes)
	}
}

func TestSoulFactoryHandlerToleratesNilConfig(t *testing.T) {
	handler := NewSoulFactoryHandler(nil)
	recorder := httptest.NewRecorder()
	handler.GetRuntimes(recorder, httptest.NewRequest(http.MethodGet, "/soulfactory/runtimes", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response struct {
		Data struct {
			AgentRuntimes []string `json:"agent_runtimes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data.AgentRuntimes) != 0 {
		t.Fatalf("agent_runtimes = %v, want empty", response.Data.AgentRuntimes)
	}
}
