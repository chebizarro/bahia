package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/pkg/client"
)

func TestEnvironmentCreateCommandPublishesSignerFirstPayload(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())

	var captured client.CreateEnvironmentNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{environmentCreate: func(req client.CreateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
			captured = req
			return &client.EnvironmentCommandResult{Status: "created", EnvironmentID: "env-1"}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	command := newEnvironmentCreateCommand()
	root.AddCommand(command)
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	root.SetArgs([]string{
		"create",
		"--name", "production",
		"--org", "31ee612f-93a8-418d-a377-eee0a5cd26dc",
		"--default-unit-key", "max",
		"--secret-scope-mode", "environment",
		"--reconcile-mode", "auto_apply",
		"--unit-key", "max",
		"--unit-runtime-type", "compose",
		"--unit-endpoint-ref", "max",
		"--unit-compose-dir", "/srv/bahia/gastown",
		"--unit-execution-mode", "sdk",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute environment create: %v", err)
	}
	if captured.Name != "production" || captured.OrgID == "" || captured.Targeting == nil || captured.Targeting.DefaultUnitKey != "max" {
		t.Fatalf("captured create payload = %#v", captured)
	}
	if captured.DeploymentUnits == nil || len(*captured.DeploymentUnits) != 1 {
		t.Fatalf("deployment_units = %#v", captured.DeploymentUnits)
	}
	unit := (*captured.DeploymentUnits)[0]
	if unit.RuntimeType != "compose" || unit.EndpointRef != "max" || unit.ComposeDir != "/srv/bahia/gastown" || unit.RuntimeConfig["execution_mode"] != "sdk" {
		t.Fatalf("captured unit = %#v", unit)
	}
}

func TestEnvironmentUnitUpdateReadMergesCompleteSetBeforeSignedMutation(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/environments/"+envID {
			t.Fatalf("unexpected HTTP mutation/read path: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"id":"%s","name":"production","targeting":{"default_unit_key":"a","default_reconcile_mode":"observe_only"},"deploy_strategy":"replace","deployment_units":[{"id":"c31244aa-073e-40a9-b2ec-adb1847a163c","environment_id":"%s","key":"a","runtime_type":"compose","endpoint_ref":"old","compose_dir":"/srv/a","ownership_mode":"bahia_managed","reconcile_mode":"observe_only","runtime_config":{"execution_mode":"cli"}},{"id":"63790fa1-1652-48ef-b349-a765d223b716","environment_id":"%s","key":"b","runtime_type":"docker","endpoint_ref":"b","ownership_mode":"external","reconcile_mode":"disabled"}]}}`, envID, envID, envID)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	var captured client.UpdateEnvironmentNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{environmentUpdate: func(req client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
			captured = req
			return &client.EnvironmentCommandResult{Status: "updated", EnvironmentID: envID}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	command := newEnvironmentUnitUpdateCommand()
	root.AddCommand(command)
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	root.SetArgs([]string{"update", envID, "a", "--endpoint-ref", "new", "--execution-mode", "sdk"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute unit update: %v", err)
	}
	if requests != 1 {
		t.Fatalf("read requests = %d, want 1", requests)
	}
	if captured.DeploymentUnits == nil || len(*captured.DeploymentUnits) != 2 {
		t.Fatalf("complete deployment_units = %#v", captured.DeploymentUnits)
	}
	var updated, preserved *client.DeploymentUnitRequest
	for i := range *captured.DeploymentUnits {
		unit := &(*captured.DeploymentUnits)[i]
		switch unit.Key {
		case "a":
			updated = unit
		case "b":
			preserved = unit
		}
	}
	if updated == nil || updated.EndpointRef != "new" || updated.ComposeDir != "/srv/a" || updated.RuntimeConfig["execution_mode"] != "sdk" {
		t.Fatalf("updated unit = %#v", updated)
	}
	if preserved == nil || preserved.EndpointRef != "b" || preserved.OwnershipMode != "external" {
		t.Fatalf("preserved unit = %#v", preserved)
	}
}

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "unit.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	return path
}

func TestDeploymentUnitJSONFileRejectsUnknownFields(t *testing.T) {
	path := writeTestFile(t, `{"key":"max","runtime_type":"compose","secret":"must-not-pass"}`)
	if _, err := readDeploymentUnitFile(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want strict unknown-field rejection", err)
	}
}
