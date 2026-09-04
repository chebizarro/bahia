package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
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

func TestEnvironmentTargetingOnlyUpdateUsesOneSignedClientAndNoHTTP(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests++
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	clientBuilds := 0
	clientCloses := 0
	signedReads := 0
	var captured client.UpdateEnvironmentNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		clientBuilds++
		return fakeCLIOperatorClient{
			closeClient: func() { clientCloses++ },
			environmentGetDetails: func(id string) (*client.EnvironmentDetails, error) {
				signedReads++
				if id != envID {
					t.Fatalf("signed read environment id = %q", id)
				}
				return &client.EnvironmentDetails{Environment: domain.Environment{Targeting: domain.EnvironmentTargeting{
					DefaultUnitKey:       "default",
					FailureDomainLabels:  map[string]string{"region": "west"},
					SecretScopeMode:      domain.SecretScopeModeEnvironment,
					DefaultReconcileMode: domain.ReconcileModeObserveOnly,
				}}}, nil
			},
			environmentUpdate: func(req client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
				captured = req
				return &client.EnvironmentCommandResult{Status: "updated", EnvironmentID: envID}, nil
			},
		}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(newEnvironmentUpdateCommand())
	_ = root.PersistentFlags().Set("relay", "wss://relay.example")
	root.SetArgs([]string{"update", envID, "--default-unit-key", "max"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute targeting-only environment update: %v", err)
	}
	if httpRequests != 0 || signedReads != 1 || clientBuilds != 1 || clientCloses != 1 {
		t.Fatalf("HTTP requests=%d signed reads=%d client builds=%d closes=%d, want 0, 1, 1, 1", httpRequests, signedReads, clientBuilds, clientCloses)
	}
	if captured.Targeting == nil || captured.Targeting.DefaultUnitKey != "max" || captured.Targeting.FailureDomainLabels["region"] != "west" || captured.Targeting.SecretScopeMode != "environment" || captured.Targeting.DefaultReconcileMode != "observe_only" {
		t.Fatalf("targeting update = %#v", captured.Targeting)
	}
	if captured.DeploymentUnits != nil || captured.ExpectedUpdatedAt != nil {
		t.Fatalf("targeting-only update unexpectedly included complete-set fields: %#v", captured)
	}
}

func TestEnvironmentUnitUpdateReadMergesCompleteSetBeforeSignedMutation(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests++
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	var captured client.UpdateEnvironmentNostrRequest
	signedReads := 0
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{
			environmentGetDetails: func(id string) (*client.EnvironmentDetails, error) {
				signedReads++
				if id != envID {
					t.Fatalf("signed read environment id = %q", id)
				}
				return &client.EnvironmentDetails{
					Environment: domain.Environment{Name: "production", Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "a", DefaultReconcileMode: domain.ReconcileModeObserveOnly}, DeployStrategy: domain.DeployStrategyReplace, UpdatedAt: time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)},
					DeploymentUnits: []domain.DeploymentUnit{
						{Key: "a", RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "old", ComposeDir: "/srv/a", OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeObserveOnly, RuntimeConfig: map[string]any{"execution_mode": "cli"}},
						{Key: "b", RuntimeType: domain.RuntimeTypeDocker, EndpointRef: "b", OwnershipMode: domain.OwnershipModeExternal, ReconcileMode: domain.ReconcileModeDisabled, GitSource: &domain.GitSourceBinding{RepositoryURL: "https://git.example/b.git", Ref: "refs/heads/main", Branch: "main", CommitSHA: "abc123"}},
					},
				}, nil
			},
			environmentUpdate: func(req client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
				captured = req
				return &client.EnvironmentCommandResult{Status: "updated", EnvironmentID: envID}, nil
			},
		}, nil
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
	if signedReads != 1 || httpRequests != 0 {
		t.Fatalf("signed reads=%d HTTP requests=%d, want 1 and 0", signedReads, httpRequests)
	}
	if captured.ExpectedUpdatedAt == nil || captured.ExpectedUpdatedAt.Format(time.RFC3339) != "2026-08-02T08:00:00Z" {
		t.Fatalf("expected_updated_at = %v", captured.ExpectedUpdatedAt)
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
	if preserved.GitSource == nil || preserved.GitSource.RepositoryURL != "https://git.example/b.git" || preserved.GitSource.Ref != "refs/heads/main" || preserved.GitSource.Branch != "main" || preserved.GitSource.CommitSHA != "abc123" {
		t.Fatalf("preserved git_source = %#v", preserved.GitSource)
	}
}

func TestEnvironmentUnitUpdateRetriesConflictWithFreshCompleteSet(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	reads := 0
	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests++
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	var captured []client.UpdateEnvironmentNostrRequest
	clientBuilds := 0
	clientCloses := 0
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		clientBuilds++
		return fakeCLIOperatorClient{
			closeClient: func() { clientCloses++ },
			environmentGetDetails: func(string) (*client.EnvironmentDetails, error) {
				reads++
				units := []domain.DeploymentUnit{{Key: "a", RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "old", ComposeDir: "/srv/a", OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeObserveOnly}}
				if reads > 1 {
					units = append(units, domain.DeploymentUnit{Key: "b", RuntimeType: domain.RuntimeTypeDocker, EndpointRef: "b", OwnershipMode: domain.OwnershipModeExternal, ReconcileMode: domain.ReconcileModeDisabled})
				}
				return &client.EnvironmentDetails{Environment: domain.Environment{Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "a", DefaultReconcileMode: domain.ReconcileModeObserveOnly}, UpdatedAt: time.Date(2026, 8, 2, 8, 0, reads-1, 0, time.UTC)}, DeploymentUnits: units}, nil
			},
			environmentUpdate: func(req client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
				captured = append(captured, req)
				if len(captured) == 1 {
					return nil, client.ErrEnvironmentRevisionConflict
				}
				return &client.EnvironmentCommandResult{Status: "updated", EnvironmentID: envID}, nil
			},
		}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(newEnvironmentUnitUpdateCommand())
	_ = root.PersistentFlags().Set("relay", "wss://relay.example")
	root.SetArgs([]string{"update", envID, "a", "--endpoint-ref", "new"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute unit update: %v", err)
	}
	if reads != 2 || len(captured) != 2 || httpRequests != 0 || clientBuilds != 1 || clientCloses != 1 {
		t.Fatalf("signed reads=%d mutations=%d HTTP requests=%d client builds=%d closes=%d, want 2, 2, 0, 1, 1", reads, len(captured), httpRequests, clientBuilds, clientCloses)
	}
	if captured[0].ExpectedUpdatedAt == nil || captured[1].ExpectedUpdatedAt == nil ||
		!captured[1].ExpectedUpdatedAt.After(*captured[0].ExpectedUpdatedAt) {
		t.Fatalf("attempt revisions = %v, %v", captured[0].ExpectedUpdatedAt, captured[1].ExpectedUpdatedAt)
	}
	if captured[1].DeploymentUnits == nil || len(*captured[1].DeploymentUnits) != 2 {
		t.Fatalf("second complete set = %#v", captured[1].DeploymentUnits)
	}
	if (*captured[1].DeploymentUnits)[1].Key != "b" {
		t.Fatalf("concurrent unit was not preserved: %#v", *captured[1].DeploymentUnits)
	}
}

func TestEnvironmentUnitsListUsesSignedReadOnly(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests++
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	signedReads := 0
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{environmentGetDetails: func(id string) (*client.EnvironmentDetails, error) {
			signedReads++
			if id != envID {
				t.Fatalf("signed read environment id = %q", id)
			}
			return &client.EnvironmentDetails{DeploymentUnits: []domain.DeploymentUnit{{Key: "default", RuntimeType: domain.RuntimeTypeDocker, Implicit: true}}}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(newEnvironmentUnitsListCommand())
	_ = root.PersistentFlags().Set("relay", "wss://relay.example")
	root.SetArgs([]string{"list", envID})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute units list: %v", err)
	}
	if signedReads != 1 || httpRequests != 0 {
		t.Fatalf("signed reads=%d HTTP requests=%d, want 1 and 0", signedReads, httpRequests)
	}
}

func TestEnvironmentUnitCreateSetsNonDefaultKeyAtomically(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests++
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	var captured client.UpdateEnvironmentNostrRequest
	signedReads := 0
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{
			environmentGetDetails: func(string) (*client.EnvironmentDetails, error) {
				signedReads++
				return &client.EnvironmentDetails{
					Environment:     domain.Environment{Name: "production", Targeting: domain.EnvironmentTargeting{DefaultUnitKey: domain.DefaultDeploymentUnitKey, SecretScopeMode: domain.SecretScopeModeEnvironment, DefaultReconcileMode: domain.ReconcileModeObserveOnly}, DeployStrategy: domain.DeployStrategyReplace, UpdatedAt: time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)},
					DeploymentUnits: []domain.DeploymentUnit{{Key: domain.DefaultDeploymentUnitKey, RuntimeType: domain.RuntimeTypeDocker, OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeObserveOnly, Implicit: true}},
				}, nil
			},
			environmentUpdate: func(req client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
				captured = req
				return &client.EnvironmentCommandResult{Status: "updated", EnvironmentID: envID}, nil
			},
		}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(newEnvironmentUnitCreateCommand())
	_ = root.PersistentFlags().Set("relay", "wss://relay.example")
	root.SetArgs([]string{
		"create", envID,
		"--key", "max",
		"--runtime-type", "compose",
		"--endpoint-ref", "max",
		"--compose-dir", "/srv/bahia/gastown",
		"--default-unit-key", "max",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute unit create: %v", err)
	}
	if signedReads != 1 || httpRequests != 0 {
		t.Fatalf("signed reads=%d HTTP requests=%d, want 1 and 0", signedReads, httpRequests)
	}
	if captured.Targeting == nil || captured.Targeting.DefaultUnitKey != "max" {
		t.Fatalf("targeting = %#v", captured.Targeting)
	}
	if captured.DeploymentUnits == nil || len(*captured.DeploymentUnits) != 1 || (*captured.DeploymentUnits)[0].Key != "max" {
		t.Fatalf("atomic explicit unit set = %#v", captured.DeploymentUnits)
	}
	if captured.ExpectedUpdatedAt == nil {
		t.Fatalf("expected_updated_at was omitted")
	}
}

func TestEnvironmentUnitCommandsExposeAtomicDefaultUnitKeyFlag(t *testing.T) {
	for _, cmd := range []*cobra.Command{newEnvironmentUnitCreateCommand(), newEnvironmentUnitUpdateCommand()} {
		if flag := cmd.Flags().Lookup("default-unit-key"); flag == nil {
			t.Fatalf("%s command is missing --default-unit-key", cmd.Name())
		}
	}
}

func TestEnvironmentCompleteSetUpdateStopsAfterBoundedConflicts(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	envID := "5ab7a568-b765-4e78-af52-305b16b1e262"

	reads := 0
	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpRequests++
		t.Fatalf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	apiClient = client.New(server.URL)

	mutations := 0
	clientBuilds := 0
	clientCloses := 0
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		clientBuilds++
		return fakeCLIOperatorClient{
			closeClient: func() { clientCloses++ },
			environmentGetDetails: func(string) (*client.EnvironmentDetails, error) {
				reads++
				return &client.EnvironmentDetails{
					Environment:     domain.Environment{Targeting: domain.EnvironmentTargeting{DefaultUnitKey: domain.DefaultDeploymentUnitKey, DefaultReconcileMode: domain.ReconcileModeObserveOnly}, UpdatedAt: time.Date(2026, 8, 2, 8, 0, reads, 0, time.UTC)},
					DeploymentUnits: []domain.DeploymentUnit{{Key: domain.DefaultDeploymentUnitKey, RuntimeType: domain.RuntimeTypeDocker, OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeObserveOnly}},
				}, nil
			},
			environmentUpdate: func(client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
				mutations++
				return nil, client.ErrEnvironmentRevisionConflict
			},
		}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(newEnvironmentUnitUpdateCommand())
	_ = root.PersistentFlags().Set("relay", "wss://relay.example")
	root.SetArgs([]string{"update", envID, "default", "--endpoint-ref", "changed"})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("error = %v, want bounded conflict", err)
	}
	if reads != environmentCompleteSetUpdateMaxAttempts || mutations != environmentCompleteSetUpdateMaxAttempts || httpRequests != 0 || clientBuilds != 1 || clientCloses != 1 {
		t.Fatalf("signed reads=%d mutations=%d HTTP requests=%d client builds=%d closes=%d, want %d, %d, 0, 1, 1", reads, mutations, httpRequests, clientBuilds, clientCloses, environmentCompleteSetUpdateMaxAttempts, environmentCompleteSetUpdateMaxAttempts)
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
