package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

func TestOperatorEnvironmentCreateUpdateRequestConstruction(t *testing.T) {
	tests := []struct {
		name   string
		method string
		run    func(context.Context, *OperatorControlPlaneClient) (*EnvironmentCommandResult, error)
		assert func(*testing.T, contextVMRPCRequest)
	}{
		{
			name:   "create",
			method: controlplane.ContextVMMethodEnvironmentCreate,
			run: func(ctx context.Context, c *OperatorControlPlaneClient) (*EnvironmentCommandResult, error) {
				units := []DeploymentUnitRequest{{
					Key:           "max",
					RuntimeType:   "compose",
					EndpointRef:   "max",
					ComposeDir:    "/srv/bahia/gastown",
					OwnershipMode: "bahia_managed",
					ReconcileMode: "auto_apply",
					RuntimeConfig: map[string]any{"execution_mode": "sdk"},
				}}
				return c.CreateEnvironmentNostr(ctx, CreateEnvironmentNostrRequest{
					OrgID:           "31ee612f-93a8-418d-a377-eee0a5cd26dc",
					Name:            "production",
					Targeting:       &EnvironmentTargetingRequest{DefaultUnitKey: "max", SecretScopeMode: "environment", DefaultReconcileMode: "auto_apply"},
					ReconcileMode:   "auto_apply",
					DeploymentUnits: &units,
					DeployStrategy:  "replace",
					Protected:       true,
				}, nil)
			},
			assert: func(t *testing.T, rpc contextVMRPCRequest) {
				if got := rpc.Params["name"]; got != "production" {
					t.Fatalf("name = %#v", got)
				}
				targeting, _ := rpc.Params["targeting"].(map[string]any)
				if targeting["default_unit_key"] != "max" || targeting["secret_scope_mode"] != "environment" {
					t.Fatalf("targeting = %#v", targeting)
				}
				units, _ := rpc.Params["deployment_units"].([]any)
				if len(units) != 1 {
					t.Fatalf("deployment_units = %#v", rpc.Params["deployment_units"])
				}
				unit, _ := units[0].(map[string]any)
				runtimeConfig, _ := unit["runtime_config"].(map[string]any)
				if unit["runtime_type"] != "compose" || unit["endpoint_ref"] != "max" || runtimeConfig["execution_mode"] != "sdk" {
					t.Fatalf("unit = %#v", unit)
				}
			},
		},
		{
			name:   "update preserves explicit empty complete set",
			method: controlplane.ContextVMMethodEnvironmentUpdate,
			run: func(ctx context.Context, c *OperatorControlPlaneClient) (*EnvironmentCommandResult, error) {
				empty := []DeploymentUnitRequest{}
				protected := false
				return c.UpdateEnvironmentNostr(ctx, UpdateEnvironmentNostrRequest{
					ID:              "env-1",
					DeploymentUnits: &empty,
					Protected:       &protected,
				}, nil)
			},
			assert: func(t *testing.T, rpc contextVMRPCRequest) {
				units, exists := rpc.Params["deployment_units"]
				if !exists {
					t.Fatal("deployment_units was omitted")
				}
				if values, ok := units.([]any); !ok || len(values) != 0 {
					t.Fatalf("deployment_units = %#v, want explicit empty array", units)
				}
				if protected, ok := rpc.Params["protected"].(bool); !ok || protected {
					t.Fatalf("protected = %#v, want false", rpc.Params["protected"])
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestKey := nostr.Generate().Hex()
			replyKey := nostr.Generate().Hex()
			transport := newFakeOperatorTransport()
			c := newTestOperatorClient(t, requestKey, transport)
			transport.publishFn = func(ctx context.Context, event nostr.Event) (int, error) {
				transport.events <- signedContextVMResult(t, replyKey, event, map[string]any{
					"status":  "success",
					"payload": map[string]any{"status": "updated", "environment_id": "env-1"},
				})
				return 1, nil
			}
			result, err := test.run(context.Background(), c)
			if err != nil {
				t.Fatalf("environment mutation error = %v", err)
			}
			if result.EnvironmentID != "env-1" {
				t.Fatalf("environment_id = %q", result.EnvironmentID)
			}
			published := transport.onlyPublished(t)
			rpc := decodePublishedContextVMRequest(t, published)
			if rpc.Method != test.method {
				t.Fatalf("method = %q, want %q", rpc.Method, test.method)
			}
			test.assert(t, rpc)
			assertTagValue(t, published.Tags, "method", test.method)
		})
	}
}

func TestOperatorEnvironmentMutationsValidateRequiredFieldsBeforePublish(t *testing.T) {
	transport := newFakeOperatorTransport()
	c := newTestOperatorClient(t, nostr.Generate().Hex(), transport)
	if _, err := c.CreateEnvironmentNostr(context.Background(), CreateEnvironmentNostrRequest{}, nil); err == nil {
		t.Fatal("CreateEnvironmentNostr expected name validation error")
	}
	if _, err := c.UpdateEnvironmentNostr(context.Background(), UpdateEnvironmentNostrRequest{}, nil); err == nil {
		t.Fatal("UpdateEnvironmentNostr expected id validation error")
	}
	if len(transport.published) != 0 {
		t.Fatalf("published %d events for invalid requests", len(transport.published))
	}
}

func TestOperatorRuntimeDeploySubscribesBeforePublishAndHandlesContextVMProgressResultDedup(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	artifactID := "artifact-1"

	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		progress := signedContextVMResult(t, replyKey, ev, map[string]any{"status": "processing", "step": "started", "action": "deploy", "message": "Direct runtime action started"})
		transport.events <- progress
		transport.events <- progress // duplicate delivery from another relay must be ignored
		transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"action": "deploy", "service_id": "svc-1", "environment_id": "env-1"})
		return 1, nil
	}

	var statuses []OperatorStatusEvent
	result, err := client.DeployServiceRuntimeNostr(context.Background(), "svc-1", "env-1", &artifactID, func(status OperatorStatusEvent) {
		statuses = append(statuses, status)
	})
	if err != nil {
		t.Fatalf("DeployServiceRuntimeNostr() error = %v", err)
	}
	if result.Action != "deploy" || result.ServiceID != "svc-1" || result.EnvironmentID != "env-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(statuses) != 1 {
		t.Fatalf("status callback count = %d, want 1", len(statuses))
	}
	if statuses[0].Status != "processing" || statuses[0].Step != "started" || !strings.Contains(statuses[0].Message, "Direct runtime action started") {
		t.Fatalf("unexpected status event: %#v", statuses[0])
	}
	if got := transport.calls; len(got) != 2 || got[0] != "subscribe" || got[1] != "publish" {
		t.Fatalf("calls = %#v, want subscribe before publish", got)
	}
	published := transport.onlyPublished(t)
	assertSignedEvent(t, published)
	if published.Kind != controlplane.KindContextVMMessage {
		t.Fatalf("request kind = %d, want %d", published.Kind, controlplane.KindContextVMMessage)
	}
	rpc := decodePublishedContextVMRequest(t, published)
	if rpc.JSONRPC != "2.0" || rpc.Method != "service/action" || rpc.ID == "" {
		t.Fatalf("unexpected ContextVM request: %#v", rpc)
	}
	for key, want := range map[string]string{"action": "deploy", "service_id": "svc-1", "environment_id": "env-1", "artifact_id": artifactID} {
		if got, _ := rpc.Params[key].(string); got != want {
			t.Fatalf("params[%s] = %q, want %q (params=%#v)", key, got, want, rpc.Params)
		}
	}
	if meta, _ := rpc.Params["_meta"].(map[string]any); meta == nil || meta["progressToken"] != rpc.ID {
		t.Fatalf("missing progress token in ContextVM params: %#v", rpc.Params["_meta"])
	}
	assertTagValue(t, published.Tags, "action", "deploy")
	assertTagValue(t, published.Tags, "service", "svc-1")
	assertTagValue(t, published.Tags, "environment", "env-1")
	assertTagValue(t, published.Tags, "artifact", artifactID)
	assertTagValue(t, published.Tags, "method", "service/action")
	assertTagValue(t, published.Tags, controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion)
	filter := transport.onlyFilter(t)
	if got := filter.Kinds; len(got) != 1 || got[0] != nostr.Kind(controlplane.KindContextVMMessage) {
		t.Fatalf("filter kinds = %#v, want ContextVM kind", got)
	}
	if got := filter.Tags["e"]; len(got) != 1 || got[0] != published.ID.Hex() {
		t.Fatalf("filter #e = %#v, want request id %s", got, published.ID)
	}
	if got := filter.Tags["p"]; len(got) != 1 || got[0] != published.PubKey.Hex() {
		t.Fatalf("filter #p = %#v, want requester pubkey %s", got, published.PubKey)
	}
}

func TestOperatorRuntimeRestartStopRequestConstruction(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, *OperatorControlPlaneClient) (*RuntimeActionResult, error)
	}{
		{name: "restart", run: func(ctx context.Context, c *OperatorControlPlaneClient) (*RuntimeActionResult, error) {
			return c.RestartServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
		}},
		{name: "stop", run: func(ctx context.Context, c *OperatorControlPlaneClient) (*RuntimeActionResult, error) {
			return c.StopServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestKey := nostr.Generate().Hex()
			replyKey := nostr.Generate().Hex()
			transport := newFakeOperatorTransport()
			client := newTestOperatorClient(t, requestKey, transport)
			transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
				transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"action": tc.name, "service_id": "svc-1", "environment_id": "env-1"})
				return 1, nil
			}
			result, err := tc.run(context.Background(), client)
			if err != nil {
				t.Fatalf("runtime action error = %v", err)
			}
			if result.Action != tc.name {
				t.Fatalf("result action = %q, want %q", result.Action, tc.name)
			}
			published := transport.onlyPublished(t)
			rpc := decodePublishedContextVMRequest(t, published)
			if rpc.Method != "service/action" {
				t.Fatalf("method = %q, want service/action", rpc.Method)
			}
			if got, _ := rpc.Params["action"].(string); got != tc.name {
				t.Fatalf("action = %q, want %q", got, tc.name)
			}
			if _, exists := rpc.Params["artifact_id"]; exists {
				t.Fatalf("non-deploy request included artifact_id: %#v", rpc.Params)
			}
		})
	}
}

func TestOperatorRollbackUsesIdempotencyTagOnly(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{
			"status":         "submitted",
			"intent_id":      "rollback-intent",
			"service_id":     "svc-1",
			"environment_id": "env-1",
			"artifact_id":    "artifact-good",
		})
		return 1, nil
	}

	result, err := client.RollbackDeploymentNostr(context.Background(), RollbackDeploymentNostrRequest{
		ServiceID:          "svc-1",
		EnvironmentID:      "env-1",
		DeploymentUnitID:   "unit-1",
		TargetArtifactID:   "artifact-good",
		SupersedesIntentID: "intent-bad",
		IdempotencyKey:     "rollback:test",
	}, nil)
	if err != nil {
		t.Fatalf("RollbackDeploymentNostr() error = %v", err)
	}
	if result.IntentID != "rollback-intent" || result.ArtifactID != "artifact-good" {
		t.Fatalf("unexpected rollback result: %#v", result)
	}
	published := transport.onlyPublished(t)
	rpc := decodePublishedContextVMRequest(t, published)
	if rpc.Method != "service/rollback" {
		t.Fatalf("method = %q, want service/rollback", rpc.Method)
	}
	if _, exists := rpc.Params["idempotency_key"]; exists {
		t.Fatalf("idempotency_key leaked into strict ContextVM params: %#v", rpc.Params)
	}
	for key, want := range map[string]string{
		"service_id":           "svc-1",
		"environment_id":       "env-1",
		"deployment_unit_id":   "unit-1",
		"target_artifact_id":   "artifact-good",
		"supersedes_intent_id": "intent-bad",
	} {
		if got, _ := rpc.Params[key].(string); got != want {
			t.Fatalf("params[%s] = %q, want %q (params=%#v)", key, got, want, rpc.Params)
		}
	}
	assertTagValue(t, published.Tags, "d", "rollback:test")
	assertTagValue(t, published.Tags, "deployment_unit", "unit-1")
}

func TestOperatorRoutesToConfiguredServicePubkey(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	servicePubkey := mustOperatorTestPubKey(t, replyKey)
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	client.servicePubkey = servicePubkey
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedContextVMResult(t, nostr.Generate().Hex(), ev, map[string]any{"action": "spoofed"})
		transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"action": "restart", "service_id": "svc-1", "environment_id": "env-1"})
		return 1, nil
	}
	if _, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil); err != nil {
		t.Fatalf("RestartServiceRuntimeNostr() error = %v", err)
	}
	published := transport.onlyPublished(t)
	assertTagValue(t, published.Tags, "p", servicePubkey)
	filter := transport.onlyFilter(t)
	if got := filter.Authors; len(got) != 1 || got[0].Hex() != servicePubkey {
		t.Fatalf("filter authors = %#v, want service pubkey", got)
	}
}

func TestOperatorIgnoresInvalidUncorrelatedAndDuplicateContextVMReplies(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		invalid := signedContextVMResult(t, replyKey, ev, map[string]any{"action": "restart"})
		invalid.Content = `{"jsonrpc":"2.0","id":"tampered","result":{"action":"tampered"}}`
		transport.events <- invalid
		future := signedContextVMResult(t, replyKey, ev, map[string]any{"action": "future"})
		future.CreatedAt = nostr.Timestamp(int64(nostr.Now()) + 601)
		if err := future.Sign(mustOperatorTestSecret(t, replyKey)); err != nil {
			t.Fatalf("sign future reply: %v", err)
		}
		transport.events <- future
		transport.events <- signedOperatorReply(t, replyKey, controlplane.KindContextVMMessage, nostr.Tags{{"e", "different", "", "reply"}, {"p", ev.PubKey.Hex()}}, contextVMResponseContent(t, ev, map[string]any{"action": "restart"}))
		good := signedContextVMResult(t, replyKey, ev, map[string]any{"action": "restart", "service_id": "svc-1", "environment_id": "env-1"})
		transport.events <- good
		transport.events <- good
		return 1, nil
	}
	result, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	if err != nil {
		t.Fatalf("RestartServiceRuntimeNostr() error = %v", err)
	}
	if result.Action != "restart" {
		t.Fatalf("result action = %q, want restart", result.Action)
	}
}

func TestOperatorRuntimeTerminalFailure(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"status": "failed", "error": "runtime denied"})
		return 1, nil
	}
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	if err == nil || !strings.Contains(err.Error(), "runtime denied") {
		t.Fatalf("error = %v, want runtime denied", err)
	}
}

func TestOperatorAdoptionScanAndImportRequestConstructionAndResults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		method    string
		run       func(context.Context, *OperatorControlPlaneClient) (any, error)
		result    any
	}{
		{
			name:      "scan",
			operation: "scan",
			method:    "adoption/scan",
			result:    []map[string]any{{"target": map[string]any{"name": "prod", "endpoint_ref": "prod-docker", "environment_name": "production"}, "containers": []any{}}},
			run: func(ctx context.Context, c *OperatorControlPlaneClient) (any, error) {
				return c.ScanAdoptionNostr(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker", EnvironmentName: "production"}}}, nil)
			},
		},
		{
			name:      "import",
			operation: "import",
			method:    "adoption/import",
			result:    []map[string]any{{"target_name": "prod", "container_id": "abc", "service_name": "api", "status": "created"}},
			run: func(ctx context.Context, c *OperatorControlPlaneClient) (any, error) {
				return c.ImportAdoptionNostr(ctx, AdoptionImportRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker", EnvironmentName: "production"}}, Selections: []AdoptionSelection{{TargetName: "prod", ContainerID: "abc", ServiceNameOverride: "api"}}, OrgID: "31ee612f-93a8-418d-a377-eee0a5cd26dc"}, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestKey := nostr.Generate().Hex()
			replyKey := nostr.Generate().Hex()
			transport := newFakeOperatorTransport()
			client := newTestOperatorClient(t, requestKey, transport)
			transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
				transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"status": "success", "payload": tc.result})
				return 1, nil
			}
			result, err := tc.run(context.Background(), client)
			if err != nil {
				t.Fatalf("%s error = %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("%s returned nil result", tc.name)
			}
			published := transport.onlyPublished(t)
			rpc := decodePublishedContextVMRequest(t, published)
			if rpc.Method != tc.method {
				t.Fatalf("method = %q, want %q", rpc.Method, tc.method)
			}
			if tc.operation == "import" {
				if orgID, _ := rpc.Params["org_id"].(string); orgID != "31ee612f-93a8-418d-a377-eee0a5cd26dc" {
					t.Fatalf("import params missing org_id: %v", rpc.Params)
				}
			}
			assertTagValue(t, published.Tags, "operation", tc.operation)
			assertTagValue(t, published.Tags, "target", "prod")
			assertTagValue(t, published.Tags, "endpoint_ref", "prod-docker")
			assertTagValue(t, published.Tags, "environment_name", "production")
			if strings.Contains(published.Content, "docker_host") {
				t.Fatalf("signer-first adoption request included docker_host: %s", published.Content)
			}
		})
	}
}

func TestOperatorAdoptionErrorEnvelope(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"status": "failed", "error": "not authorized"})
		return 1, nil
	}
	_, err := client.ScanAdoptionNostr(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("error = %v, want not authorized", err)
	}
}

func TestOperatorContextVMErrorIsPostAcceptanceFailure(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedContextVMError(t, replyKey, ev, "method denied")
		return 1, nil
	}
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) || !reqErr.RequestAccepted || !strings.Contains(err.Error(), "method denied") {
		t.Fatalf("error = %T %v, want accepted ContextVM error", err, err)
	}
}

func TestOperatorContextVMEnvironmentConflictIsTyped(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.events <- signedContextVMErrorCode(
			t, replyKey, ev, controlplane.ContextVMEnvironmentConflictErrorCode,
			"environment revision conflict",
		)
		return 1, nil
	}
	units := []DeploymentUnitRequest{{Key: "max", RuntimeType: "compose"}}
	_, err := client.UpdateEnvironmentNostr(context.Background(), UpdateEnvironmentNostrRequest{
		ID: "env-1", DeploymentUnits: &units,
	}, nil)
	if !errors.Is(err, ErrEnvironmentRevisionConflict) {
		t.Fatalf("error = %T %v, want typed environment revision conflict", err, err)
	}
	var remote *ContextVMRemoteError
	if !errors.As(err, &remote) || remote.Code != controlplane.ContextVMEnvironmentConflictErrorCode {
		t.Fatalf("remote error = %#v", remote)
	}
}

func TestOperatorContextCancelAfterPublishIsPostAcceptanceAbort(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	ctx, cancel := context.WithCancel(context.Background())
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		cancel()
		return 1, nil
	}

	_, err := client.RestartServiceRuntimeNostr(ctx, "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if !reqErr.RequestAccepted || reqErr.PublishedRelays != 1 {
		t.Fatalf("RequestAccepted=%v PublishedRelays=%d, want accepted abort", reqErr.RequestAccepted, reqErr.PublishedRelays)
	}
	if !errors.Is(reqErr, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled cause", reqErr)
	}
}

func TestOperatorReplySubscriptionClosedAfterPublishIsPostAcceptanceFailure(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		close(transport.events)
		return 1, nil
	}

	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if !reqErr.RequestAccepted || reqErr.PublishedRelays != 1 {
		t.Fatalf("RequestAccepted=%v PublishedRelays=%d, want accepted post-publish failure", reqErr.RequestAccepted, reqErr.PublishedRelays)
	}
	if !strings.Contains(reqErr.Error(), "reply subscription closed before terminal result") {
		t.Fatalf("error = %v, want explicit subscription closure", reqErr)
	}
}

func TestOperatorPublishNoRelayAcceptedIsPreAcceptanceFailure(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		return 0, errors.New("all relays rejected")
	}
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if reqErr.RequestAccepted {
		t.Fatalf("RequestAccepted = true, want false")
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want signed publish attempt", len(transport.published))
	}
}

func TestOperatorPublishOKFalseAuthRequiredPreservesPreAcceptanceReason(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	transport.publishResultsFn = func(ctx context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error) {
		transport.mu.Lock()
		transport.calls = append(transport.calls, "publish")
		transport.published = append(transport.published, ev)
		transport.mu.Unlock()
		return []nostrpool.PublishResult{{RelayURL: "wss://auth.example", Accepted: false, Reason: "auth-required: sign in"}}, errors.New("failed to publish to any relay: wss://auth.example rejected event: auth-required: sign in")
	}

	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if reqErr.RequestAccepted || reqErr.PublishedRelays != 0 {
		t.Fatalf("RequestAccepted=%v PublishedRelays=%d, want pre-acceptance zero-accepted failure", reqErr.RequestAccepted, reqErr.PublishedRelays)
	}
	if !strings.Contains(reqErr.Error(), "auth-required: sign in") {
		t.Fatalf("error = %v, want auth-required reason", reqErr)
	}
}

func TestOperatorReplyAuthClosedAuthenticatesAndResubscribesWithoutRepublish(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	client.relays = []string{"wss://auth.example"}
	var published nostr.Event
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		published = ev
		transport.closedEvents <- nostrpool.RelayClosed{RelayURL: "wss://auth.example", Reason: "auth-required: sign in"}
		return 1, nil
	}
	transport.authFn = func(ctx context.Context, relayURL string) error {
		transport.mu.Lock()
		transport.calls = append(transport.calls, "auth")
		transport.mu.Unlock()
		if relayURL != "wss://auth.example" {
			t.Fatalf("AuthenticateRelay relay = %q, want auth relay", relayURL)
		}
		transport.events <- signedContextVMResult(t, replyKey, published, map[string]any{"action": "restart", "service_id": "svc-1", "environment_id": "env-1"})
		return nil
	}

	result, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	if err != nil {
		t.Fatalf("RestartServiceRuntimeNostr() error = %v", err)
	}
	if result.Action != "restart" {
		t.Fatalf("result action = %q, want restart", result.Action)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want no republish after AUTH", len(transport.published))
	}
	if got := transport.calls; len(got) != 4 || got[0] != "subscribe" || got[1] != "publish" || got[2] != "auth" || got[3] != "subscribe" {
		t.Fatalf("calls = %#v, want subscribe, publish, auth, subscribe", got)
	}
}

func TestOperatorReplyAuthClosedExcludesRelayAndWaitsForRemainingResult(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	replyKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	client.relays = []string{"wss://auth.example", "wss://open.example"}
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.closedEvents <- nostrpool.RelayClosed{RelayURL: "wss://auth.example", Reason: "auth-required: sign in"}
		transport.events <- signedContextVMResult(t, replyKey, ev, map[string]any{"action": "restart", "service_id": "svc-1", "environment_id": "env-1"})
		return 1, nil
	}

	result, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	if err != nil {
		t.Fatalf("RestartServiceRuntimeNostr() error = %v", err)
	}
	if result.Action != "restart" {
		t.Fatalf("result action = %q, want restart", result.Action)
	}
}

func TestOperatorReplyClosedAllRelaysAfterPublishIsPostAcceptanceFailure(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	client.relays = []string{"wss://auth.example", "wss://closed.example"}
	transport.publishFn = func(ctx context.Context, ev nostr.Event) (int, error) {
		transport.closedEvents <- nostrpool.RelayClosed{RelayURL: "wss://auth.example", Reason: "auth-required: sign in"}
		transport.closedEvents <- nostrpool.RelayClosed{RelayURL: "wss://closed.example", Reason: "closed: maintenance"}
		return 1, nil
	}

	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("error = %T %v, want ControlPlaneRequestError", err, err)
	}
	if !reqErr.RequestAccepted || reqErr.PublishedRelays != 1 {
		t.Fatalf("RequestAccepted=%v PublishedRelays=%d, want accepted post-publish failure", reqErr.RequestAccepted, reqErr.PublishedRelays)
	}
	if !strings.Contains(reqErr.Error(), "wss://auth.example (auth-required: sign in)") || !strings.Contains(reqErr.Error(), "wss://closed.example (closed: maintenance)") {
		t.Fatalf("error = %v, want closed relay summary", reqErr)
	}
}

func TestOperatorSubscribeFailureIsPreAcceptanceFailure(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	transport.subscribeErr = errors.New("subscription unavailable")
	client := newTestOperatorClient(t, requestKey, transport)
	_, err := client.RestartServiceRuntimeNostr(context.Background(), "svc-1", "env-1", nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) || reqErr.RequestAccepted {
		t.Fatalf("error = %T %#v, want pre-acceptance ControlPlaneRequestError", err, reqErr)
	}
	if len(transport.published) != 0 {
		t.Fatalf("publish called after subscribe failure")
	}
}

func TestOperatorAdoptionRejectsDockerHostBeforePublish(t *testing.T) {
	requestKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	client := newTestOperatorClient(t, requestKey, transport)
	_, err := client.ScanAdoptionNostr(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: "unix:///var/run/docker.sock"}}}, nil)
	var reqErr *ControlPlaneRequestError
	if !errors.As(err, &reqErr) || reqErr.RequestAccepted {
		t.Fatalf("error = %T %#v, want pre-acceptance validation failure", err, reqErr)
	}
	if len(transport.calls) != 0 || len(transport.published) != 0 {
		t.Fatalf("transport was used for invalid adoption target: calls=%#v published=%d", transport.calls, len(transport.published))
	}
}

type fakeOperatorTransport struct {
	mu               sync.Mutex
	events           chan *nostr.Event
	eose             chan struct{}
	closedEvents     chan nostrpool.RelayClosed
	publishFn        func(context.Context, nostr.Event) (int, error)
	publishResultsFn func(context.Context, nostr.Event) ([]nostrpool.PublishResult, error)
	authFn           func(context.Context, string) error
	subscribeErr     error
	published        []nostr.Event
	filters          []nostr.Filter
	calls            []string
	closed           bool
}

func newFakeOperatorTransport() *fakeOperatorTransport {
	return &fakeOperatorTransport{events: make(chan *nostr.Event, 32), eose: make(chan struct{}), closedEvents: make(chan nostrpool.RelayClosed, 8)}
}

func (f *fakeOperatorTransport) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "publish")
	f.published = append(f.published, ev)
	fn := f.publishFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, ev)
	}
	return 1, nil
}

func (f *fakeOperatorTransport) PublishWithResults(ctx context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error) {
	f.mu.Lock()
	fn := f.publishResultsFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, ev)
	}
	published, err := f.Publish(ctx, ev)
	if published <= 0 {
		return nil, err
	}
	results := make([]nostrpool.PublishResult, 0, published)
	for i := 0; i < published; i++ {
		results = append(results, nostrpool.PublishResult{RelayURL: "wss://relay.example", Accepted: true})
	}
	return results, err
}

func (f *fakeOperatorTransport) AuthenticateRelay(ctx context.Context, relayURL string) error {
	f.mu.Lock()
	fn := f.authFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, relayURL)
	}
	return errors.New("no private key configured for NIP-42 AUTH")
}

func (f *fakeOperatorTransport) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "subscribe")
	f.filters = append(f.filters, filters...)
	err := f.subscribeErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &nostrpool.MergedSubscription{Events: f.events, EndOfStoredEvents: f.eose, Closed: f.closedEvents}, nil
}

func (f *fakeOperatorTransport) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeOperatorTransport) onlyPublished(t *testing.T) nostr.Event {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(f.published))
	}
	return f.published[0]
}

func (f *fakeOperatorTransport) onlyFilter(t *testing.T) nostr.Filter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) != 1 {
		t.Fatalf("filter count = %d, want 1", len(f.filters))
	}
	return f.filters[0]
}

func mustOperatorTestSecret(t *testing.T, privateKey string) nostr.SecretKey {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(privateKey))
	if err != nil {
		t.Fatalf("parse nostr private key: %v", err)
	}
	return secret
}

func mustOperatorTestPubKey(t *testing.T, privateKey string) string {
	t.Helper()
	return mustOperatorTestSecret(t, privateKey).Public().Hex()
}

func newTestOperatorClient(t *testing.T, privateKey string, transport operatorRelayTransport) *OperatorControlPlaneClient {
	t.Helper()
	normalized, err := NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("NormalizeNostrPrivateKey() error = %v", err)
	}
	signer, err := controlplane.NewPrivateKeySigner(normalized)
	if err != nil {
		t.Fatalf("NewPrivateKeySigner() error = %v", err)
	}
	pubkey := mustOperatorTestPubKey(t, normalized)
	return &OperatorControlPlaneClient{relays: []string{"wss://relay.example"}, privateKey: normalized, signer: signer, pubkey: pubkey, transport: transport}
}

func decodePublishedContextVMRequest(t *testing.T, event nostr.Event) contextVMRPCRequest {
	t.Helper()
	var rpc contextVMRPCRequest
	if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
		t.Fatalf("decode ContextVM request content: %v", err)
	}
	return rpc
}

func signedContextVMResult(t *testing.T, privateKey string, request nostr.Event, result any) *nostr.Event {
	t.Helper()
	return signedOperatorReply(t, privateKey, controlplane.KindContextVMMessage, nostr.Tags{{"e", request.ID.Hex(), "", "reply"}, {"p", request.PubKey.Hex()}, {controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion}}, contextVMResponseContent(t, request, result))
}

func signedContextVMError(t *testing.T, privateKey string, request nostr.Event, message string) *nostr.Event {
	t.Helper()
	return signedContextVMErrorCode(t, privateKey, request, -32000, message)
}

func signedContextVMErrorCode(t *testing.T, privateKey string, request nostr.Event, code int, message string) *nostr.Event {
	t.Helper()
	rpc := decodePublishedContextVMRequest(t, request)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": code, "message": message}})
	if err != nil {
		t.Fatalf("encode ContextVM error: %v", err)
	}
	return signedOperatorReply(t, privateKey, controlplane.KindContextVMMessage, nostr.Tags{{"e", request.ID.Hex(), "", "reply"}, {"p", request.PubKey.Hex()}, {controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion}}, string(body))
}

func contextVMResponseContent(t *testing.T, request nostr.Event, result any) string {
	t.Helper()
	rpc := decodePublishedContextVMRequest(t, request)
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode ContextVM result: %v", err)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": json.RawMessage(resultBytes)})
	if err != nil {
		t.Fatalf("encode ContextVM response: %v", err)
	}
	return string(body)
}

func signedOperatorReply(t *testing.T, privateKey string, kind int, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := event.Sign(mustOperatorTestSecret(t, privateKey)); err != nil {
		t.Fatalf("sign reply event: %v", err)
	}
	return event
}

func assertSignedEvent(t *testing.T, event nostr.Event) {
	t.Helper()
	if !event.CheckID() {
		t.Fatalf("event ID does not match serialized event: %#v", event)
	}
	if !event.VerifySignature() {
		t.Fatalf("event signature invalid")
	}
}

func assertTagValue(t *testing.T, tags nostr.Tags, name, value string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return
		}
	}
	t.Fatalf("missing tag %s=%s in %#v", name, value, tags)
}
