package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func TestRootCommandExposesOperatorFlags(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newRootCommand()
	if flag := cmd.PersistentFlags().Lookup("relay"); flag == nil {
		t.Fatal("root command missing --relay")
	}
	if flag := cmd.PersistentFlags().Lookup("http-fallback"); flag == nil {
		t.Fatal("root command missing --http-fallback")
	}
}

func TestResolveOperatorRelaysPrecedence(t *testing.T) {
	t.Run("flag beats env and system info", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "wss://env.example")
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("relay", "wss://flag.example,wss://flag.example/"); err != nil {
			t.Fatalf("set relay flag: %v", err)
		}
		sys := &fakeSystemInfoClient{info: &client.SystemInfo{Nostr: client.SystemInfoNostr{BrowserRelays: []string{"wss://system.example"}}}}
		relays, err := resolveOperatorRelays(context.Background(), cmd, sys)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://flag.example" {
			t.Fatalf("relays = %#v, want flag relay only", relays)
		}
		if sys.calls != 0 {
			t.Fatalf("system info was called despite flag relays")
		}
	})

	t.Run("env beats system info", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "wss://env1.example, wss://env2.example")
		cmd := newOperatorFlagTestCommand(t)
		sys := &fakeSystemInfoClient{info: &client.SystemInfo{Nostr: client.SystemInfoNostr{BrowserRelays: []string{"wss://system.example"}}}}
		relays, err := resolveOperatorRelays(context.Background(), cmd, sys)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://env1.example,wss://env2.example" {
			t.Fatalf("relays = %#v, want env relays", relays)
		}
		if sys.calls != 0 {
			t.Fatalf("system info was called despite env relays")
		}
	})

	t.Run("system info appends sidecar and dedupes", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "")
		cmd := newOperatorFlagTestCommand(t)
		sys := &fakeSystemInfoClient{info: &client.SystemInfo{Nostr: client.SystemInfoNostr{BrowserRelays: []string{"ws://localhost:3000/relay"}, SidecarURL: "ws://localhost:3000/relay/"}}}
		relays, err := resolveOperatorRelays(context.Background(), cmd, sys)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if len(relays) != 1 || relays[0] != "ws://localhost:3000/relay" {
			t.Fatalf("relays = %#v, want deduped system relay", relays)
		}
		if sys.calls != 1 {
			t.Fatalf("system info calls = %d, want 1", sys.calls)
		}
	})
}

func TestServiceActionCommandUsesSignerFirstClientByDefault(t *testing.T) {
	resetOperatorGlobals(t)
	key := nostr.GeneratePrivateKey()
	factoryCalls := 0
	restoreFactory := replaceOperatorFactory(func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		factoryCalls++
		if cfg.PrivateKey != key {
			t.Fatalf("PrivateKey = %q, want command key", cfg.PrivateKey)
		}
		if len(cfg.Relays) != 1 || cfg.Relays[0] != "wss://relay.example" {
			t.Fatalf("Relays = %#v, want command relay", cfg.Relays)
		}
		return fakeCLIOperatorClient{}, nil
	})
	defer restoreFactory()

	cmd := newRootCommand()
	cmd.SetArgs([]string{"--privkey", key, "--relay", "wss://relay.example", "services", "actions", "restart", "--service", "svc", "--environment", "env"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("operator factory calls = %d, want 1", factoryCalls)
	}
}

func TestAdoptRawTargetCommandRequiresExplicitFallback(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newRootCommand()
	cmd.SetArgs([]string{"adopt", "scan", "--raw-target", "local=unix:///docker.sock"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--raw-target") {
		t.Fatalf("ExecuteContext() error = %v, want raw-target fallback error", err)
	}
}

func TestOperatorHTTPFallbackOnlyForExplicitPreAcceptanceFailures(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newOperatorFlagTestCommand(t)
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.GeneratePrivateKey())
	t.Setenv("BAHIA_NOSTR_RELAYS", "wss://relay.example")
	operatorHTTPFallback = true

	restoreFactory := replaceOperatorFactory(func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return nil, &client.ControlPlaneRequestError{Phase: "test setup", RequestAccepted: false, Cause: errors.New("subscribe failed")}
	})
	defer restoreFactory()

	fallbackCalls := 0
	result, err := runRuntimeActionNostrFirst(cmd, "restart", "svc", "env", nil, func(ctx context.Context) (*client.RuntimeActionResult, error) {
		fallbackCalls++
		return &client.RuntimeActionResult{Action: "restart", ServiceID: "svc", EnvironmentID: "env"}, nil
	})
	if err != nil {
		t.Fatalf("runRuntimeActionNostrFirst() error = %v", err)
	}
	if fallbackCalls != 1 || result.Action != "restart" {
		t.Fatalf("fallbackCalls=%d result=%#v", fallbackCalls, result)
	}

	restoreFactory()
	restoreFactory = replaceOperatorFactory(func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{restartErr: &client.ControlPlaneRequestError{Phase: "await", RequestAccepted: true, Cause: errors.New("terminal failure")}}, nil
	})
	defer restoreFactory()
	fallbackCalls = 0
	_, err = runRuntimeActionNostrFirst(cmd, "restart", "svc", "env", nil, func(ctx context.Context) (*client.RuntimeActionResult, error) {
		fallbackCalls++
		return &client.RuntimeActionResult{}, nil
	})
	if err == nil {
		t.Fatal("expected accepted request error")
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback called after request acceptance")
	}
}

func TestRawTargetRequiresExplicitFallbackAndSkipsNostrWhenAllowed(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newOperatorFlagTestCommand(t)
	scanReq := client.AdoptionScanRequest{Targets: []client.AdoptionTarget{{Name: "local", DockerHost: "unix:///docker.sock"}}}

	fallbackCalls := 0
	_, err := runAdoptionScanNostrFirst(cmd, scanReq, true, func(ctx context.Context) ([]client.AdoptionPreview, error) {
		fallbackCalls++
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "--raw-target") {
		t.Fatalf("error = %v, want raw-target fallback error", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback called without --http-fallback")
	}

	importReq := client.AdoptionImportRequest{Targets: []client.AdoptionTarget{{Name: "local", DockerHost: "unix:///docker.sock"}}, Selections: []client.AdoptionSelection{{TargetName: "local", ContainerID: "abc123"}}}
	_, err = runAdoptionImportNostrFirst(cmd, importReq, true, func(ctx context.Context) ([]client.AdoptionImportResult, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "--raw-target") {
		t.Fatalf("import error = %v, want raw-target fallback error", err)
	}

	operatorHTTPFallback = true
	previews, err := runAdoptionScanNostrFirst(cmd, scanReq, true, func(ctx context.Context) ([]client.AdoptionPreview, error) {
		fallbackCalls++
		return []client.AdoptionPreview{{Target: scanReq.Targets[0]}}, nil
	})
	if err != nil {
		t.Fatalf("raw fallback error = %v", err)
	}
	if fallbackCalls != 1 || len(previews) != 1 {
		t.Fatalf("fallbackCalls=%d previews=%#v", fallbackCalls, previews)
	}

	importFallbackCalls := 0
	results, err := runAdoptionImportNostrFirst(cmd, importReq, true, func(ctx context.Context) ([]client.AdoptionImportResult, error) {
		importFallbackCalls++
		return []client.AdoptionImportResult{{TargetName: "local", ContainerID: "abc123", Status: "created"}}, nil
	})
	if err != nil {
		t.Fatalf("raw import fallback error = %v", err)
	}
	if importFallbackCalls != 1 || len(results) != 1 {
		t.Fatalf("importFallbackCalls=%d results=%#v", importFallbackCalls, results)
	}
}

func TestOperatorStatusCallbackWritesOnlyInTableModeToStderr(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := &cobra.Command{Use: "test"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)

	outputFormat = "json"
	if cb := operatorStatusCallback(cmd, "deploy"); cb != nil {
		cb(client.OperatorStatusEvent{Message: "started"})
	}
	if stderr.Len() != 0 {
		t.Fatalf("structured output mode wrote status to stderr: %q", stderr.String())
	}

	outputFormat = "table"
	cb := operatorStatusCallback(cmd, "deploy")
	if cb == nil {
		t.Fatal("table mode returned nil status callback")
	}
	cb(client.OperatorStatusEvent{Message: "started"})
	if got := stderr.String(); !strings.Contains(got, "→ deploy: started") {
		t.Fatalf("stderr = %q, want deploy status", got)
	}
}

type fakeSystemInfoClient struct {
	info  *client.SystemInfo
	err   error
	calls int
}

func (f *fakeSystemInfoClient) GetSystemInfo(ctx context.Context) (*client.SystemInfo, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

type fakeCLIOperatorClient struct {
	restartErr error
}

func (f fakeCLIOperatorClient) Close() {}
func (f fakeCLIOperatorClient) DeployServiceRuntimeNostr(context.Context, string, string, *string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error) {
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) RestartServiceRuntimeNostr(context.Context, string, string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error) {
	if f.restartErr != nil {
		return nil, f.restartErr
	}
	return &client.RuntimeActionResult{Action: "restart"}, nil
}
func (f fakeCLIOperatorClient) StopServiceRuntimeNostr(context.Context, string, string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error) {
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) ScanAdoptionNostr(context.Context, client.AdoptionScanRequest, func(client.OperatorStatusEvent)) ([]client.AdoptionPreview, error) {
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) ImportAdoptionNostr(context.Context, client.AdoptionImportRequest, func(client.OperatorStatusEvent)) ([]client.AdoptionImportResult, error) {
	return nil, errors.New("not implemented")
}

func newOperatorFlagTestCommand(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "bahia"}
	root.PersistentFlags().StringArrayVar(&operatorRelays, "relay", nil, "")
	root.PersistentFlags().BoolVar(&operatorHTTPFallback, "http-fallback", false, "")
	root.PersistentFlags().StringVar(&nostrNsec, "nsec", "", "")
	root.PersistentFlags().StringVar(&nostrPrivateKey, "privkey", "", "")
	cmd := &cobra.Command{Use: "test"}
	root.AddCommand(cmd)
	return cmd
}

func replaceOperatorFactory(factory func(client.OperatorControlPlaneConfig) (cliOperatorClient, error)) func() {
	previous := newCLIOperatorClient
	newCLIOperatorClient = factory
	return func() { newCLIOperatorClient = previous }
}

func resetOperatorGlobals(t *testing.T) {
	t.Helper()
	serverURL = ""
	outputFormat = "table"
	apiClient = nil
	nostrNsec = ""
	nostrPrivateKey = ""
	operatorRelays = nil
	operatorHTTPFallback = false
	t.Setenv("BAHIA_NOSTR_RELAYS", "")
	t.Setenv("BAHIA_OPERATOR_HTTP_FALLBACK", "")
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Cleanup(func() {
		serverURL = ""
		outputFormat = "table"
		apiClient = nil
		nostrNsec = ""
		nostrPrivateKey = ""
		operatorRelays = nil
		operatorHTTPFallback = false
	})
}
