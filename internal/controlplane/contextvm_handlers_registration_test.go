package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
)

// commandEventFromPublishes returns the non-progress, non-response publish.
// The progress ack is published concurrently with the handler, so its order
// relative to the command event is nondeterministic; the response is last.
func commandEventFromPublishes(t *testing.T, events []nostr.Event) nostr.Event {
	t.Helper()
	for _, ev := range events[:len(events)-1] {
		var rpc ContextVMJSONRPCRequest
		if err := json.Unmarshal([]byte(ev.Content), &rpc); err == nil && rpc.Method == ContextVMProgressNotificationMethod {
			continue
		}
		return ev
	}
	t.Fatalf("no command event found among %d publishes", len(events))
	return nostr.Event{}
}

func TestWorkerContextVMHandlersDispatchAllWebMethods(t *testing.T) {
	tests := []struct {
		method  string
		command string
		params  map[string]any
	}{
		{ContextVMMethodWorkerCleanup, WorkerCommandCleanupRequest, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-cleanup:1", "cleanup_mode": "reclaimable_only"}},
		{ContextVMMethodWorkerCordon, WorkerCommandCordon, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-cordon:1"}},
		{ContextVMMethodWorkerUncordon, WorkerCommandUncordon, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-uncordon:1"}},
		{ContextVMMethodWorkerDrain, WorkerCommandDrain, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-drain:1"}},
		{ContextVMMethodWorkerUndrain, WorkerCommandUndrain, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-undrain:1"}},
		{ContextVMMethodWorkerMaintenanceEnter, WorkerCommandMaintenanceEnter, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-maintenance-enter:1"}},
		{ContextVMMethodWorkerMaintenanceExit, WorkerCommandMaintenanceExit, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-maintenance-exit:1"}},
		{ContextVMMethodWorkerLabelsUpdate, WorkerCommandLabelsUpdate, map[string]any{"worker_pubkey": "worker-1", "idempotency_key": "worker-labels-update:1", "labels": map[string]string{"region": "us-west"}}},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
			RegisterWorkerContextVMHandlers(transport)

			transport.HandleEvent(context.Background(), makeRouteRequest(t, tc.method, tc.params))
			if len(publisher.events) != 3 {
				t.Fatalf("published events = %d, want progress ack + command + response", len(publisher.events))
			}
			commandEvent := commandEventFromPublishes(t, publisher.events)
			var rpc ContextVMJSONRPCRequest
			if err := json.Unmarshal([]byte(commandEvent.Content), &rpc); err != nil {
				t.Fatalf("command event content is not ContextVM JSON-RPC: %v", err)
			}
			if rpc.Method != tc.method || tagValueNostr(commandEvent.Tags, "command") != tc.command {
				t.Fatalf("command event method/tag = %q/%q, want %q/%q", rpc.Method, tagValueNostr(commandEvent.Tags, "command"), tc.method, tc.command)
			}
			payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
			if payload["status"] != "accepted" || payload["request_event_id"] == "" {
				t.Fatalf("unexpected worker ack: %#v", payload)
			}
		})
	}
}

func TestBackupAliasContextVMHandlersDispatchWebAliases(t *testing.T) {
	tests := []struct {
		method string
		action string
		kind   int
		params map[string]any
	}{
		{ContextVMMethodBackupRepositoryRegister, backupActionRepositoryRegister, KindBackupRepositoryRegister, map[string]any{"name": "archive", "backend": "kopia", "repository_uri": "kopia://archive", "idempotency_key": "repo-register:1"}},
		{ContextVMMethodBackupPolicyApply, backupActionPolicyApply, KindBackupPolicyApply, map[string]any{"name": "verified", "require_verification": true, "verification_mode": "kopia_snapshot_verify", "idempotency_key": "policy-apply:1"}},
		{ContextVMMethodBackupRecipeApply, backupActionRecipeApply, KindBackupRecipeApply, map[string]any{"name": "daily", "version": "v1", "backend": "kopia", "repository_id": "33333333-3333-3333-3333-333333333333", "target_ref": "fs:/srv/app", "idempotency_key": "recipe-apply:1"}},
		{ContextVMMethodBackupDefinitionApply, backupActionDefinitionApply, KindBackupDefinitionApply, map[string]any{"name": "daily-app", "repository_id": "33333333-3333-3333-3333-333333333333", "policy_id": "44444444-4444-4444-4444-444444444444", "recipe_id": "55555555-5555-5555-5555-555555555555", "idempotency_key": "definition-apply:1"}},
		{ContextVMMethodBackupRun, backupActionRun, KindBackupRunRequest, map[string]any{"recipe_id": "55555555-5555-5555-5555-555555555555", "idempotency_key": "run:1"}},
		{ContextVMMethodBackupVerification, backupActionVerification, KindBackupVerificationRequest, map[string]any{"backup_run_id": "66666666-6666-6666-6666-666666666666", "mode": "kopia_snapshot_verify", "idempotency_key": "verification:1"}},
		{ContextVMMethodBackupRestore, backupActionRestore, KindBackupRestoreRequest, map[string]any{"backup_run_id": "66666666-6666-6666-6666-666666666666", "restore_target_ref": "fs:/restore", "idempotency_key": "restore:1"}},
		{ContextVMMethodBackupRetention, backupActionRetention, KindBackupRetentionEnforce, map[string]any{"repository_id": "33333333-3333-3333-3333-333333333333", "policy_id": "44444444-4444-4444-4444-444444444444", "dry_run": true, "idempotency_key": "retention:1"}},
		{ContextVMMethodBackupRestoreApprovalAlias, backupActionRestoreApproval, KindBackupRestoreApproval, map[string]any{"restore_id": "11111111-1111-1111-1111-111111111111", "approved": true, "idempotency_key": "restore-approval:1"}},
		{ContextVMMethodBackupRepositoryProbe, backupActionRepositoryProbe, KindBackupRepositoryProbe, map[string]any{"repository_id": "22222222-2222-2222-2222-222222222222", "idempotency_key": "repo-probe:1"}},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
			RegisterBackupAliasContextVMHandlers(transport)

			transport.HandleEvent(context.Background(), makeRouteRequest(t, tc.method, tc.params))
			if len(publisher.events) != 3 {
				t.Fatalf("published events = %d, want progress ack + command + response", len(publisher.events))
			}
			commandEvent := commandEventFromPublishes(t, publisher.events)
			if commandEvent.Kind != nostr.Kind(tc.kind) {
				t.Fatalf("command kind = %d, want %d", commandEvent.Kind, tc.kind)
			}
			if tagValueNostr(commandEvent.Tags, "command") != tc.action {
				t.Fatalf("command tag = %q, want %q", tagValueNostr(commandEvent.Tags, "command"), tc.action)
			}
			payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
			if payload["status"] != "submitted" || payload["action"] != tc.action || payload["request_event_id"] == "" {
				t.Fatalf("unexpected backup ack: %#v", payload)
			}
		})
	}
}

func TestDNSContextVMHandlersDispatchWebMethods(t *testing.T) {
	tests := []struct {
		method string
		action string
		params map[string]any
	}{
		{ContextVMMethodDNSZoneCreate, dnsActionZoneCreate, map[string]any{"name": "prod.example"}},
		{ContextVMMethodDNSPolicyApply, dnsActionPolicyApply, map[string]any{"name": "prod-policy"}},
		{ContextVMMethodDNSRecordSet, dnsActionRecordOverride, map[string]any{"zone_name": "prod.example"}},
		{ContextVMMethodDNSDriftRemediate, dnsActionDriftRemediate, map[string]any{"zone": "prod.example"}},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			operator := &fakeDNSContextVMOperator{zones: map[string]bool{"prod.example": true}}
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
			RegisterDNSContextVMHandlers(transport, operator, true)

			transport.HandleEvent(context.Background(), makeRouteRequest(t, tc.method, tc.params))
			if len(publisher.events) != 2 {
				t.Fatalf("published events = %d, want progress ack + response", len(publisher.events))
			}
			payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
			if payload["action"] != tc.action {
				t.Fatalf("action = %#v, want %s payload=%#v", payload["action"], tc.action, payload)
			}
			if tc.method == ContextVMMethodDNSZoneCreate || tc.method == ContextVMMethodDNSDriftRemediate {
				if payload["status"] != "success" || len(operator.reconciledZones) != 1 {
					t.Fatalf("expected successful zone reconcile, payload=%#v reconciled=%v", payload, operator.reconciledZones)
				}
			}
		})
	}
}

func TestDNSContextVMHandlersReturnConfigurationErrorWhenDisabled(t *testing.T) {
	methods := []string{
		ContextVMMethodDNSZoneCreate,
		ContextVMMethodDNSPolicyApply,
		ContextVMMethodDNSRecordSet,
		ContextVMMethodDNSDriftRemediate,
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
			RegisterDNSContextVMHandlers(transport, nil, false)

			transport.HandleEvent(context.Background(), makeRouteRequest(t, method, map[string]any{"unexpected": true}))
			if len(publisher.events) != 2 {
				t.Fatalf("published events = %d, want progress ack + response", len(publisher.events))
			}
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || response.Error.Code != -32000 || response.Error.Message != dnsOrchestrationDisabledMessage {
				t.Fatalf("disabled DNS response = %+v, want -32000 %q", response, dnsOrchestrationDisabledMessage)
			}
		})
	}
}

type fakeDNSContextVMOperator struct {
	zones           map[string]bool
	reconciledZones []string
	reconcileAll    int
}

func (o *fakeDNSContextVMOperator) ReconcileAll(context.Context) error {
	o.reconcileAll++
	return nil
}

func (o *fakeDNSContextVMOperator) ReconcileZone(_ context.Context, zoneName string) error {
	if !o.HasZone(zoneName) {
		return fmt.Errorf("unknown zone %s", zoneName)
	}
	o.reconciledZones = append(o.reconciledZones, zoneName)
	return nil
}

func (o *fakeDNSContextVMOperator) HasZone(zoneName string) bool {
	return o.zones[zoneName]
}

var _ DNSControlPlaneOperator = (*fakeDNSContextVMOperator)(nil)
