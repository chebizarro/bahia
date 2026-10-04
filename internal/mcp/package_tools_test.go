package mcp

import (
	"testing"

	"go.uber.org/zap"
)

func TestGetToolsIncludesPackageTools(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	tools := server.GetTools()
	required := map[string]bool{"bahia_package_repository_apply": false, "bahia_package_upload": false, "bahia_package_promote": false, "bahia_package_yank": false, "bahia_package_list": false, "bahia_package_get": false, "bahia_package_status": false}
	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing package tool %s", name)
		}
	}
}
