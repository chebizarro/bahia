package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func TestRootCommandExposesOperatorFlags(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newRootCommand()
	if flag := cmd.PersistentFlags().Lookup("relay"); flag == nil {
		t.Fatal("root command missing --relay")
	}
	if flag := cmd.PersistentFlags().Lookup("bootstrap-relay"); flag == nil {
		t.Fatal("root command missing --bootstrap-relay")
	}
	if flag := cmd.PersistentFlags().Lookup("service-pubkey"); flag == nil {
		t.Fatal("root command missing --service-pubkey")
	}
	if flag := cmd.PersistentFlags().Lookup("trusted-service-pubkey"); flag == nil {
		t.Fatal("root command missing --trusted-service-pubkey")
	}
	if flag := cmd.PersistentFlags().Lookup("http-fallback"); flag == nil {
		t.Fatal("root command missing --http-fallback")
	}
	if flag := cmd.PersistentFlags().Lookup("nostr-bunker-file"); flag == nil {
		t.Fatal("root command missing --nostr-bunker-file")
	}
	if flag := cmd.PersistentFlags().Lookup("nostr-client-key-file"); flag == nil {
		t.Fatal("root command missing --nostr-client-key-file")
	}
	if flag := cmd.PersistentFlags().Lookup("nostr-bunker-relay"); flag == nil {
		t.Fatal("root command missing --nostr-bunker-relay")
	}
}

func TestResolveOperatorRelaysPrecedence(t *testing.T) {
	t.Run("flag beats env and discovery", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "wss://env.example")
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("relay", "wss://flag.example,wss://flag.example/"); err != nil {
			t.Fatalf("set relay flag: %v", err)
		}
		discoveryCalls := 0
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			discoveryCalls++
			return []string{"wss://discovered.example"}, nil
		})
		defer restoreDiscovery()
		relays, err := resolveOperatorRelays(cmd)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://flag.example" {
			t.Fatalf("relays = %#v, want flag relay only", relays)
		}
		if discoveryCalls != 0 {
			t.Fatalf("discoveryCalls = %d, want no discovery when --relay is explicit", discoveryCalls)
		}
	})

	t.Run("env is used when flag is absent", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "wss://env1.example, wss://env2.example")
		cmd := newOperatorFlagTestCommand(t)
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			t.Fatal("discovery must not run when BAHIA_NOSTR_RELAYS is configured")
			return nil, nil
		})
		defer restoreDiscovery()
		relays, err := resolveOperatorRelays(cmd)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://env1.example,wss://env2.example" {
			t.Fatalf("relays = %#v, want env relays", relays)
		}
	})

	t.Run("trusted bootstrap discovery runs only after final relay config is absent", func(t *testing.T) {
		resetOperatorGlobals(t)
		servicePubkey := nostr.Generate().Public().Hex()
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("bootstrap-relay", "wss://bootstrap.example"); err != nil {
			t.Fatalf("set bootstrap relay: %v", err)
		}
		if err := cmd.Root().PersistentFlags().Set("trusted-service-pubkey", servicePubkey); err != nil {
			t.Fatalf("set trusted service pubkey: %v", err)
		}
		discoveryCalls := 0
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			discoveryCalls++
			if strings.Join(cfg.BootstrapRelays, ",") != "wss://bootstrap.example" {
				t.Fatalf("BootstrapRelays = %#v", cfg.BootstrapRelays)
			}
			if strings.Join(cfg.TrustedServicePubkeys, ",") != servicePubkey {
				t.Fatalf("TrustedServicePubkeys = %#v", cfg.TrustedServicePubkeys)
			}
			return []string{"wss://contextvm.example"}, nil
		})
		defer restoreDiscovery()
		relays, err := resolveOperatorRelays(cmd)
		if err != nil {
			t.Fatalf("resolveOperatorRelays() error = %v", err)
		}
		if discoveryCalls != 1 || strings.Join(relays, ",") != "wss://contextvm.example" {
			t.Fatalf("discoveryCalls=%d relays=%#v", discoveryCalls, relays)
		}
	})

	t.Run("service pubkey can provide single-service discovery trust", func(t *testing.T) {
		resetOperatorGlobals(t)
		servicePubkey := nostr.Generate().Public().Hex()
		t.Setenv("BAHIA_NOSTR_BOOTSTRAP_RELAYS", "wss://bootstrap.example")
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("service-pubkey", servicePubkey); err != nil {
			t.Fatalf("set service pubkey: %v", err)
		}
		restoreDiscovery := replaceOperatorDiscovery(func(ctx context.Context, cfg client.OperatorRelayDiscoveryConfig) ([]string, error) {
			if strings.Join(cfg.TrustedServicePubkeys, ",") != servicePubkey {
				t.Fatalf("TrustedServicePubkeys = %#v", cfg.TrustedServicePubkeys)
			}
			return []string{"wss://contextvm.example"}, nil
		})
		defer restoreDiscovery()
		if relays, err := resolveOperatorRelays(cmd); err != nil || strings.Join(relays, ",") != "wss://contextvm.example" {
			t.Fatalf("relays=%#v error=%v", relays, err)
		}
	})

	t.Run("missing final and bootstrap configuration fails deterministically", func(t *testing.T) {
		resetOperatorGlobals(t)
		t.Setenv("BAHIA_NOSTR_RELAYS", "")
		cmd := newOperatorFlagTestCommand(t)
		relays, err := resolveOperatorRelays(cmd)
		if err == nil || !strings.Contains(err.Error(), "no operator relays configured") {
			t.Fatalf("resolveOperatorRelays() error = %v, want explicit relay failure", err)
		}
		if relays != nil {
			t.Fatalf("relays = %#v, want nil on missing explicit config", relays)
		}
	})

	t.Run("partial bootstrap discovery configuration fails deterministically", func(t *testing.T) {
		resetOperatorGlobals(t)
		cmd := newOperatorFlagTestCommand(t)
		if err := cmd.Root().PersistentFlags().Set("bootstrap-relay", "wss://bootstrap.example"); err != nil {
			t.Fatalf("set bootstrap relay: %v", err)
		}
		if _, err := resolveOperatorRelays(cmd); err == nil || !strings.Contains(err.Error(), "trusted service pubkey") {
			t.Fatalf("bootstrap-only error = %v, want trusted pubkey requirement", err)
		}
	})
}

func TestPolicyCreateUsesSignerFirstOperatorClient(t *testing.T) {
	resetOperatorGlobals(t)
	key := nostr.Generate().Hex()
	cmd := newOperatorFlagTestCommand(t)
	cmd.SetContext(context.Background())
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", key)
	operatorRelays = []string{"wss://relay.example"}
	if err := cmd.Root().PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	var captured *controlplane.PolicyMutationCommand
	restoreFactory := replaceOperatorFactory(func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		if cfg.PrivateKey != key {
			t.Fatalf("PrivateKey = %q, want configured key", cfg.PrivateKey)
		}
		return fakeCLIOperatorClient{policyCreate: func(cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
			captured = &cmd
			return &controlplane.PolicyCommandReceipt{RequestEventID: "policy-event", RequestKind: controlplane.KindPolicyCreate, ResultKind: controlplane.KindContextVMMessage, PublishedRelays: 1, Status: "submitted"}, nil
		}}, nil
	})
	defer restoreFactory()

	enabled := true
	receipt, err := runPolicyCreateNostrFirst(cmd, controlplane.PolicyMutationCommand{Name: "require-sbom", Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enforcement: "block", Enabled: &enabled, IdempotencyKey: "policy:create:test"})
	if err != nil {
		t.Fatalf("run policy create: %v", err)
	}
	if receipt.RequestKind != controlplane.KindPolicyCreate || captured == nil || captured.IdempotencyKey != "policy:create:test" || captured.Name != "require-sbom" {
		t.Fatalf("unexpected receipt=%#v captured=%#v", receipt, captured)
	}
}

func TestBuildCLIOperatorClientUsesNIP46SignerWithoutIdentityKey(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newOperatorFlagTestCommand(t)
	cmd.SetContext(context.Background())
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "bunker://operator?relay=wss://relay.example&secret=connect")
	t.Setenv("BAHIA_NOSTR_CLIENT_PRIVATE_KEY", nostr.Generate().Hex())
	t.Setenv("BAHIA_NOSTR_RELAYS", "wss://relay.example")

	operatorKey := nostr.Generate()
	operatorSigner, err := controlplane.NewPrivateKeySigner(operatorKey.Hex())
	if err != nil {
		t.Fatalf("NewPrivateKeySigner() error = %v", err)
	}
	closed := false
	previousSignerFactory := newCLINIP46Signer
	newCLINIP46Signer = func(_ context.Context, bunkerURI, clientKey string) (nostr.Signer, string, func() error, error) {
		if !strings.HasPrefix(bunkerURI, "bunker://") || clientKey == "" {
			t.Fatalf("unexpected NIP-46 inputs bunker=%q clientKeyEmpty=%v", bunkerURI, clientKey == "")
		}
		return operatorSigner, operatorKey.Public().Hex(), func() error {
			closed = true
			return nil
		}, nil
	}
	defer func() { newCLINIP46Signer = previousSignerFactory }()

	restoreFactory := replaceOperatorFactory(func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		if cfg.PrivateKey != "" {
			t.Fatal("NIP-46 operator client received local identity key material")
		}
		if cfg.Signer == nil || cfg.Pubkey != operatorKey.Public().Hex() || cfg.CloseSigner == nil {
			t.Fatalf("unexpected remote signer config: %#v", cfg)
		}
		return fakeCLIOperatorClient{}, nil
	})
	defer restoreFactory()

	op, err := buildCLIOperatorClient(cmd)
	if err != nil {
		t.Fatalf("buildCLIOperatorClient() error = %v", err)
	}
	op.Close()
	if closed {
		t.Fatal("fake operator client does not own the configured close callback")
	}
}

func TestResolveNIP46OperatorInputRequiresDurablePair(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newOperatorFlagTestCommand(t)
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "bunker://operator?relay=wss://relay.example")
	if _, _, err := resolveNIP46OperatorInput(cmd); err == nil || !strings.Contains(err.Error(), "requires both") {
		t.Fatalf("resolveNIP46OperatorInput() error = %v, want paired-input failure", err)
	}
}

func TestResolveNIP46OperatorInputAddsSeparateBunkerRelay(t *testing.T) {
	resetOperatorGlobals(t)
	cmd := newOperatorFlagTestCommand(t)
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "bunker://operator")
	t.Setenv("BAHIA_NOSTR_CLIENT_PRIVATE_KEY", nostr.Generate().Hex())
	t.Setenv("BAHIA_NOSTR_BUNKER_RELAYS", "wss://bunker.example")
	bunkerURI, _, err := resolveNIP46OperatorInput(cmd)
	if err != nil {
		t.Fatalf("resolveNIP46OperatorInput() error = %v", err)
	}
	if !strings.Contains(bunkerURI, "relay=wss%3A%2F%2Fbunker.example") {
		t.Fatalf("bunker URI = %q, want encoded separate relay", bunkerURI)
	}
}

func TestParsePolicyRulesJSONAcceptsArrayAndRejectsEmpty(t *testing.T) {
	rules, err := parsePolicyRulesJSON(`[{"type":"require_sbom"}]`)
	if err != nil || len(rules) != 1 || rules[0].Type != domain.RuleRequireSBOM {
		t.Fatalf("rules=%#v err=%v", rules, err)
	}
	if _, err := parsePolicyRulesJSON(`[]`); err == nil {
		t.Fatalf("expected empty rules error")
	}
}

func TestServiceActionCommandUsesSignerFirstClientByDefault(t *testing.T) {
	resetOperatorGlobals(t)
	key := nostr.Generate().Hex()
	servicePubkey := nostr.Generate().Public().Hex()
	factoryCalls := 0
	restoreFactory := replaceOperatorFactory(func(cfg client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		factoryCalls++
		if cfg.PrivateKey != key {
			t.Fatalf("PrivateKey = %q, want command key", cfg.PrivateKey)
		}
		if len(cfg.Relays) != 1 || cfg.Relays[0] != "wss://relay.example" {
			t.Fatalf("Relays = %#v, want command relay", cfg.Relays)
		}
		if cfg.ServicePubkey != servicePubkey {
			t.Fatalf("ServicePubkey = %q, want command service pubkey", cfg.ServicePubkey)
		}
		return fakeCLIOperatorClient{}, nil
	})
	defer restoreFactory()

	keyFile := filepath.Join(t.TempDir(), "nostr.key")
	if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"--nostr-key-file", keyFile, "--relay", "wss://relay.example", "--service-pubkey", servicePubkey, "services", "actions", "restart", "--service", "svc", "--environment", "env"})
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
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
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

func TestDeploymentsApproveCommandPublishesSignerFirstApproval(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())

	var captured client.DeploymentApprovalNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{deploymentApproval: func(req client.DeploymentApprovalNostrRequest) (*client.DeploymentCommandResult, error) {
			captured = req
			return &client.DeploymentCommandResult{Status: "submitted", IntentID: req.IntentID}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(deployCommands())
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	root.SetArgs([]string{"deployments", "approve", "--intent", "11111111-1111-1111-1111-111111111111", "--idempotency-key", "approval:test"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute deployments approve: %v", err)
	}
	if captured.IntentID != "11111111-1111-1111-1111-111111111111" || captured.Decision != "approve" || captured.IdempotencyKey != "approval:test" {
		t.Fatalf("captured approval = %#v", captured)
	}
}

func TestDeploymentsDeployCommandPublishesExplicitIdempotencyKey(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())

	var captured client.DeploymentIntentNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{deploymentIntent: func(req client.DeploymentIntentNostrRequest) (*client.DeploymentCommandResult, error) {
			captured = req
			return &client.DeploymentCommandResult{
				Status:        "submitted",
				ServiceID:     req.ServiceID,
				EnvironmentID: req.EnvironmentID,
				ArtifactID:    req.ArtifactID,
			}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(deployCommands())
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	root.SetArgs([]string{
		"deployments", "deploy",
		"--service", "11111111-1111-1111-1111-111111111111",
		"--environment", "22222222-2222-2222-2222-222222222222",
		"--artifact", "33333333-3333-3333-3333-333333333333",
		"--requested-by", "ignored-by-server",
		"--idempotency-key", "deploy:test",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute deployments deploy: %v", err)
	}
	if captured.ServiceID != "11111111-1111-1111-1111-111111111111" ||
		captured.EnvironmentID != "22222222-2222-2222-2222-222222222222" ||
		captured.ArtifactID != "33333333-3333-3333-3333-333333333333" ||
		captured.RequestedBy != "ignored-by-server" ||
		captured.IdempotencyKey != "deploy:test" {
		t.Fatalf("captured deployment intent = %#v", captured)
	}
}

func TestServicesCreateCommandPublishesSignerFirstManagedService(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())

	configPath := writeTempFile(t, `{
		"schema_version":"1",
		"service_name":"astillero",
		"ports":["127.0.0.1:18088:8080"],
		"restart_policy":"unless-stopped",
		"pull_policy":"if-not-present"
	}`)

	var captured client.CreateServiceNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{serviceCreate: func(req client.CreateServiceNostrRequest) (*client.ServiceCommandResult, error) {
			captured = req
			return &client.ServiceCommandResult{Status: "created", ServiceID: "svc-1"}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(servicesCommands())
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	root.SetArgs([]string{
		"services", "create",
		"--name", "astillero",
		"--artifact-repo", "harbor.sharegap.net/cascadia/astillero",
		"--repo-url", "https://git.sharegap.net/chebizar-coinos.io-336e0b4c237a0c000c1e/astillero.git",
		"--repo-source", "gitea",
		"--repo-coordinate", "chebizar-coinos.io-336e0b4c237a0c000c1e/astillero",
		"--ci-provider", "hiveci",
		"--default-branch", "main",
		"--managed-runtime-config-file", configPath,
		"--idempotency-key", "service:create:astillero",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute services create: %v", err)
	}
	if captured.Name != "astillero" ||
		captured.ArtifactRepo != "harbor.sharegap.net/cascadia/astillero" ||
		captured.Repository == nil ||
		captured.Repository.RepoCoordinate != "chebizar-coinos.io-336e0b4c237a0c000c1e/astillero" ||
		captured.Repository.CI == nil ||
		captured.Repository.CI.Provider != "hiveci" ||
		captured.ManagedRuntimeConfig == nil ||
		captured.ManagedRuntimeConfig.ServiceName != "astillero" ||
		captured.IdempotencyKey != "service:create:astillero" {
		t.Fatalf("captured service create = %#v", captured)
	}
}

func TestServicesUpdateCommandPublishesOnlyChangedFields(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())

	var captured client.UpdateServiceNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{serviceUpdate: func(req client.UpdateServiceNostrRequest) (*client.ServiceCommandResult, error) {
			captured = req
			return &client.ServiceCommandResult{Status: "updated", ServiceID: req.ID}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(servicesCommands())
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	root.SetArgs([]string{
		"services", "update",
		"--service", "11111111-1111-1111-1111-111111111111",
		"--org", "22222222-2222-2222-2222-222222222222",
		"--artifact-repo", "harbor.sharegap.net/cascadia/astillero",
		"--idempotency-key", "service:update:astillero",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute services update: %v", err)
	}
	if captured.ID != "11111111-1111-1111-1111-111111111111" ||
		captured.OrgID == nil ||
		*captured.OrgID != "22222222-2222-2222-2222-222222222222" ||
		captured.ArtifactRepo == nil ||
		*captured.ArtifactRepo != "harbor.sharegap.net/cascadia/astillero" ||
		captured.Name != nil ||
		captured.RuntimeType != nil ||
		captured.IdempotencyKey != "service:update:astillero" {
		t.Fatalf("captured service update = %#v", captured)
	}
}

func TestArtifactsRegisterCommandPublishesSignerFirstContextVMRequest(t *testing.T) {
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())

	var captured client.RegisterArtifactNostrRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{artifactRegister: func(req client.RegisterArtifactNostrRequest) (*client.ArtifactCommandResult, error) {
			captured = req
			return &client.ArtifactCommandResult{Status: "registered", ArtifactID: "artifact-1"}, nil
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(artifactsCommands())
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root.SetArgs([]string{
		"artifacts", "register",
		"--build", "11111111-1111-1111-1111-111111111111",
		"--service", "22222222-2222-2222-2222-222222222222",
		"--image-repo", "harbor.sharegap.net/cascadia/astillero",
		"--image-tag", "7bb3076",
		"--image-digest", digest,
		"--idempotency-key", "artifact:register:astillero",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute artifacts register: %v", err)
	}
	if captured.BuildID != "11111111-1111-1111-1111-111111111111" ||
		captured.ServiceID != "22222222-2222-2222-2222-222222222222" ||
		captured.ImageRepo != "harbor.sharegap.net/cascadia/astillero" ||
		captured.ImageTag != "7bb3076" ||
		captured.ImageDigest != digest ||
		captured.IdempotencyKey != "artifact:register:astillero" {
		t.Fatalf("captured artifact register = %#v", captured)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

type fakeCLIOperatorClient struct {
	restartErr         error
	policyCreate       func(controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error)
	serviceCreate      func(client.CreateServiceNostrRequest) (*client.ServiceCommandResult, error)
	serviceUpdate      func(client.UpdateServiceNostrRequest) (*client.ServiceCommandResult, error)
	artifactRegister   func(client.RegisterArtifactNostrRequest) (*client.ArtifactCommandResult, error)
	environmentCreate  func(client.CreateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error)
	environmentUpdate  func(client.UpdateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error)
	deploymentIntent   func(client.DeploymentIntentNostrRequest) (*client.DeploymentCommandResult, error)
	deploymentApproval func(client.DeploymentApprovalNostrRequest) (*client.DeploymentCommandResult, error)
}

func (f fakeCLIOperatorClient) Close() {}
func (f fakeCLIOperatorClient) CreateServiceNostr(_ context.Context, req client.CreateServiceNostrRequest, _ func(client.OperatorStatusEvent)) (*client.ServiceCommandResult, error) {
	if f.serviceCreate != nil {
		return f.serviceCreate(req)
	}
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) UpdateServiceNostr(_ context.Context, req client.UpdateServiceNostrRequest, _ func(client.OperatorStatusEvent)) (*client.ServiceCommandResult, error) {
	if f.serviceUpdate != nil {
		return f.serviceUpdate(req)
	}
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) RegisterArtifactNostr(_ context.Context, req client.RegisterArtifactNostrRequest, _ func(client.OperatorStatusEvent)) (*client.ArtifactCommandResult, error) {
	if f.artifactRegister != nil {
		return f.artifactRegister(req)
	}
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) CreateEnvironmentNostr(_ context.Context, req client.CreateEnvironmentNostrRequest, _ func(client.OperatorStatusEvent)) (*client.EnvironmentCommandResult, error) {
	if f.environmentCreate != nil {
		return f.environmentCreate(req)
	}
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) UpdateEnvironmentNostr(_ context.Context, req client.UpdateEnvironmentNostrRequest, _ func(client.OperatorStatusEvent)) (*client.EnvironmentCommandResult, error) {
	if f.environmentUpdate != nil {
		return f.environmentUpdate(req)
	}
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) DeployServiceRuntimeNostr(context.Context, string, string, *string, func(client.OperatorStatusEvent)) (*client.RuntimeActionResult, error) {
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) CreateDeploymentIntentNostr(context.Context, string, string, string, string, string, string, func(client.OperatorStatusEvent)) (*client.DeploymentCommandResult, error) {
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) CreateDeploymentIntentWithRequestNostr(_ context.Context, req client.DeploymentIntentNostrRequest, _ func(client.OperatorStatusEvent)) (*client.DeploymentCommandResult, error) {
	if f.deploymentIntent != nil {
		return f.deploymentIntent(req)
	}
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) RollbackDeploymentNostr(context.Context, client.RollbackDeploymentNostrRequest, func(client.OperatorStatusEvent)) (*client.DeploymentCommandResult, error) {
	return nil, errors.New("not implemented")
}
func (f fakeCLIOperatorClient) ApproveDeploymentNostr(_ context.Context, req client.DeploymentApprovalNostrRequest, _ func(client.OperatorStatusEvent)) (*client.DeploymentCommandResult, error) {
	if f.deploymentApproval != nil {
		return f.deploymentApproval(req)
	}
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
func (f fakeCLIOperatorClient) PublishPolicyCreateNostr(_ context.Context, cmd controlplane.PolicyMutationCommand) (*controlplane.PolicyCommandReceipt, error) {
	if f.policyCreate != nil {
		return f.policyCreate(cmd)
	}
	return nil, errors.New("not implemented")
}

func newOperatorFlagTestCommand(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "bahia"}
	root.PersistentFlags().StringArrayVar(&operatorRelays, "relay", nil, "")
	root.PersistentFlags().StringArrayVar(&operatorBootstrapRelays, "bootstrap-relay", nil, "")
	root.PersistentFlags().StringVar(&operatorServicePubkey, "service-pubkey", "", "")
	root.PersistentFlags().StringArrayVar(&operatorTrustedServicePubkeys, "trusted-service-pubkey", nil, "")
	root.PersistentFlags().BoolVar(&operatorHTTPFallback, "http-fallback", false, "")
	root.PersistentFlags().StringVar(&nostrKeyFile, "nostr-key-file", "", "")
	root.PersistentFlags().StringVar(&nostrBunkerFile, "nostr-bunker-file", "", "")
	root.PersistentFlags().StringArrayVar(&nostrBunkerRelays, "nostr-bunker-relay", nil, "")
	root.PersistentFlags().StringVar(&nostrClientKeyFile, "nostr-client-key-file", "", "")
	cmd := &cobra.Command{Use: "test"}
	root.AddCommand(cmd)
	return cmd
}

func replaceOperatorFactory(factory func(client.OperatorControlPlaneConfig) (cliOperatorClient, error)) func() {
	previous := newCLIOperatorClient
	newCLIOperatorClient = factory
	return func() { newCLIOperatorClient = previous }
}

func replaceOperatorDiscovery(discover func(context.Context, client.OperatorRelayDiscoveryConfig) ([]string, error)) func() {
	previous := discoverOperatorRelaysForCLI
	discoverOperatorRelaysForCLI = discover
	return func() { discoverOperatorRelaysForCLI = previous }
}

func resetOperatorGlobals(t *testing.T) {
	t.Helper()
	serverURL = ""
	outputFormat = "table"
	apiClient = nil
	nostrKeyFile = ""
	nostrBunkerFile = ""
	nostrBunkerRelays = nil
	nostrClientKeyFile = ""
	operatorRelays = nil
	operatorBootstrapRelays = nil
	operatorServicePubkey = ""
	operatorTrustedServicePubkeys = nil
	operatorHTTPFallback = false
	t.Setenv("BAHIA_NOSTR_RELAYS", "")
	t.Setenv("BAHIA_NOSTR_BOOTSTRAP_RELAYS", "")
	t.Setenv("BAHIA_NOSTR_SERVICE_PUBKEY", "")
	t.Setenv("BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS", "")
	t.Setenv("BAHIA_OPERATOR_HTTP_FALLBACK", "")
	t.Setenv("BAHIA_NOSTR_KEY_FILE", "")
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_FILE", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_RELAYS", "")
	t.Setenv("BAHIA_NOSTR_CLIENT_KEY_FILE", "")
	t.Setenv("BAHIA_NOSTR_CLIENT_PRIVATE_KEY", "")
	t.Cleanup(func() {
		serverURL = ""
		outputFormat = "table"
		apiClient = nil
		nostrKeyFile = ""
		nostrBunkerFile = ""
		nostrBunkerRelays = nil
		nostrClientKeyFile = ""
		operatorRelays = nil
		operatorBootstrapRelays = nil
		operatorServicePubkey = ""
		operatorTrustedServicePubkeys = nil
		operatorHTTPFallback = false
	})
}
