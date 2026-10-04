package mcp

import (
	"testing"

	"go.uber.org/zap"
)

func TestGetToolsIncludesWorkerManagementAndReadModelTools(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	required := map[string]bool{"bahia_worker_cordon": false, "bahia_worker_uncordon": false, "bahia_worker_drain": false, "bahia_worker_undrain": false, "bahia_worker_maintenance_enter": false, "bahia_worker_maintenance_exit": false, "bahia_worker_labels_update": false, "bahia_worker_get_assignments": false, "bahia_worker_get_drain_status": false, "bahia_worker_preview_eligibility": false}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing worker tool %s", name)
		}
	}
}
