package mcp

import (
	"testing"

	"go.uber.org/zap"
)

func TestGetToolsIncludesMLTools(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	tools := server.GetTools()
	required := map[string]bool{"bahia_ml_import_model": false, "bahia_ml_run_recipe": false, "bahia_ml_deploy": false, "bahia_ml_rollback": false, "bahia_ml_list_state": false, "bahia_ml_get_state": false, "bahia_ml_get_provenance": false}
	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing ML tool %s", name)
		}
	}
}

func TestMLMutatingToolsRequireIntentProcessor(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	for _, name := range []string{"bahia_ml_import_model", "bahia_ml_run_recipe", "bahia_ml_deploy", "bahia_ml_rollback"} {
		res, err := server.CallTool(authorizedMCPContext(), name, map[string]interface{}{})
		if err != nil || res == nil || !res.IsError {
			t.Fatalf("%s: result=%#v err=%v", name, res, err)
		}
	}
}
