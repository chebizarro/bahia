package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.uber.org/zap"
)

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
			commandEvent := publisher.events[1]
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
		params map[string]any
	}{
		{ContextVMMethodBackupRestoreApprovalAlias, backupActionRestoreApproval, map[string]any{"restore_id": "11111111-1111-1111-1111-111111111111", "approved": true, "idempotency_key": "restore-approval:1"}},
		{ContextVMMethodBackupRepositoryProbe, backupActionRepositoryProbe, map[string]any{"repository_id": "22222222-2222-2222-2222-222222222222", "idempotency_key": "repo-probe:1"}},
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
			if tagValueNostr(publisher.events[1].Tags, "command") != tc.action {
				t.Fatalf("command tag = %q, want %q", tagValueNostr(publisher.events[1].Tags, "command"), tc.action)
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
			RegisterDNSContextVMHandlers(transport, operator)

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
