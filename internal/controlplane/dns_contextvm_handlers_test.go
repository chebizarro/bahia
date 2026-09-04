package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestDNSContextVMZoneCreateRejectsUnknownBackendBeforePersistence(t *testing.T) {
	operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{}, backends: map[string]bool{"primary": true}}}
	params, err := json.Marshal(domain.DNSZone{Name: "new.example", Visibility: domain.ZoneVisibilityInternal, BackendRef: "missing", TTL: 60})
	if err != nil {
		t.Fatal(err)
	}

	result, err := (dnsContextVMHandlers{operator: operator}).zoneCreate(context.Background(), ContextVMRequest{RPC: ContextVMJSONRPCRequest{Params: params}})
	if err != nil {
		t.Fatalf("zoneCreate returned error: %v", err)
	}
	if len(operator.zonesCreated) != 0 {
		t.Fatalf("persisted zones = %#v, want none", operator.zonesCreated)
	}
	if len(operator.reconciled) != 0 {
		t.Fatalf("reconciled zones = %#v, want none", operator.reconciled)
	}
	assertContextVMDNSField(t, result, "status", "error")
	assertContextVMDNSField(t, result, "step", "unknown_backend")
}

func TestDNSContextVMZoneCreatePersistsKnownBackend(t *testing.T) {
	operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{}, backends: map[string]bool{"primary": true}}}
	params, err := json.Marshal(domain.DNSZone{Name: "new.example", Visibility: domain.ZoneVisibilityInternal, BackendRef: "primary", TTL: 60})
	if err != nil {
		t.Fatal(err)
	}

	result, err := (dnsContextVMHandlers{operator: operator}).zoneCreate(context.Background(), ContextVMRequest{RPC: ContextVMJSONRPCRequest{Params: params}})
	if err != nil {
		t.Fatalf("zoneCreate returned error: %v", err)
	}
	if len(operator.zonesCreated) != 1 || operator.zonesCreated[0].BackendRef != "primary" {
		t.Fatalf("persisted zones = %#v", operator.zonesCreated)
	}
	if len(operator.reconciled) != 1 || operator.reconciled[0] != "new.example" {
		t.Fatalf("reconciled zones = %#v", operator.reconciled)
	}
	assertContextVMDNSField(t, result, "status", "success")
}

func TestDNSContextVMPolicyApplyPersistsAndReconciles(t *testing.T) {
	policyRepo := &recordingDNSPolicyRepository{}
	operator := &recordingDNSOperator{zones: map[string]bool{"prod.example": true}, policyRepo: policyRepo}
	ttl := 42
	params, err := json.Marshal(domain.DNSPolicy{Name: "short-prod-ttl", Enabled: true, Rules: []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{TTLOverride: &ttl}}}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := (dnsContextVMHandlers{operator: operator}).policyApply(context.Background(), ContextVMRequest{RPC: ContextVMJSONRPCRequest{Params: params}})
	if err != nil {
		t.Fatalf("policyApply returned error: %v", err)
	}
	if len(policyRepo.created) != 1 || policyRepo.created[0].Name != "short-prod-ttl" {
		t.Fatalf("persisted policies = %#v", policyRepo.created)
	}
	if operator.reconcileAll != 1 {
		t.Fatalf("reconcile calls = %d, want 1", operator.reconcileAll)
	}
	assertContextVMDNSStatus(t, result, "success")
}

func TestDNSContextVMRecordSetPersistsAndReconciles(t *testing.T) {
	operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{"prod.example": true}}}
	params, err := json.Marshal(domain.DNSRecordOverride{ZoneName: "prod.example", RecordName: "api", RecordType: domain.DNSRecordTypeA, Value: "192.0.2.10", TTL: 60, Reason: "maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	_, pubkey := testNostrKeypair()
	requestEvent := &nostr.Event{PubKey: testNostrPubKeyFromHex(t, pubkey)}

	result, err := (dnsContextVMHandlers{operator: operator}).recordSet(context.Background(), ContextVMRequest{Event: requestEvent, RPC: ContextVMJSONRPCRequest{Params: params}})
	if err != nil {
		t.Fatalf("recordSet returned error: %v", err)
	}
	if len(operator.overridesCreated) != 1 || operator.overridesCreated[0].Value != "192.0.2.10" {
		t.Fatalf("persisted overrides = %#v", operator.overridesCreated)
	}
	if len(operator.reconciled) != 1 || operator.reconciled[0] != "prod.example" {
		t.Fatalf("reconciled zones = %#v", operator.reconciled)
	}
	assertContextVMDNSStatus(t, result, "success")
}

func assertContextVMDNSStatus(t *testing.T, result any, want string) {
	t.Helper()
	assertContextVMDNSField(t, result, "status", want)
}

func assertContextVMDNSField(t *testing.T, result any, field string, want any) {
	t.Helper()
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", result)
	}
	if payload[field] != want {
		t.Fatalf("%s = %#v, want %#v; result=%#v", field, payload[field], want, payload)
	}
}
