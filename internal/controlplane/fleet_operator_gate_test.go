package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type fleetOperatorMethod struct {
	method string
	params json.RawMessage
}

type fleetOperatorRegistration struct {
	name     string
	methods  []fleetOperatorMethod
	register func(*testing.T, *EncryptedRequestTransport, *FleetOperatorGate)
}

func TestFleetOperatorGateAllowsAuthorizedOperatorForEveryProtectedMethod(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	assertFleetOperatorRegistrationAuthorization(t, []string{requester}, testRequesterKey, "")
}

func TestFleetOperatorGateDeniesUnauthorizedOperatorForEveryProtectedMethod(t *testing.T) {
	_, authorized := testNostrKeypair()
	assertFleetOperatorRegistrationAuthorization(t, []string{authorized}, testRequesterKey, fleetOperatorUnauthorizedError)
}

func TestFleetOperatorGateEmptyAllowlistDeniesEveryProtectedMethod(t *testing.T) {
	assertFleetOperatorRegistrationAuthorization(t, nil, testRequesterKey, fleetOperatorNotConfiguredError)
}

func assertFleetOperatorRegistrationAuthorization(t *testing.T, authorizedPubkeys []string, requesterKey, wantAuthorizationError string) {
	t.Helper()
	for _, registration := range fleetOperatorRegistrations(t) {
		registration := registration
		t.Run(registration.name, func(t *testing.T) {
			for _, method := range registration.methods {
				method := method
				t.Run(method.method, func(t *testing.T) {
					publisher := &mockEncryptedPublisher{}
					transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
					registration.register(t, transport, NewFleetOperatorGate(authorizedPubkeys))
					handler, ok := transport.contextVMHandlers[method.method]
					if !ok {
						t.Fatalf("%s was not registered", method.method)
					}
					request := ContextVMRequest{
						Event: &nostr.Event{
							ID:     testNostrID("fleet-operator-gate-" + method.method),
							PubKey: testNostrPubKeyFromPrivateKey(t, requesterKey),
						},
						RPC: ContextVMJSONRPCRequest{Params: method.params},
					}
					result, err := handler(context.Background(), request)
					if wantAuthorizationError != "" {
						if err == nil || err.Error() != wantAuthorizationError {
							t.Fatalf("authorization error = %v, want %q", err, wantAuthorizationError)
						}
						return
					}
					if err != nil {
						t.Fatalf("authorized operator call failed: %v", err)
					}
					if result == nil {
						t.Fatal("authorized operator call returned no result")
					}
				})
			}
		})
	}
}

func fleetOperatorRegistrations(t *testing.T) []fleetOperatorRegistration {
	t.Helper()
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	overrideID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	relayAdminConfig := &config.Config{Nostr: config.NostrConfig{RelayAdministration: config.RelayAdministrationConfig{
		Enabled: true,
		Targets: []config.RelayAdministrationTarget{{
			Ref: "sidecar", RelayURL: "wss://sidecar.example",
			Authorization:        config.RelayAdministrationBahiaAuthorized,
			AdministratorPubkeys: []string{requester},
		}},
	}}}
	configFabricRepo := repository.NewInMemoryNostrEventRepository()
	configFabric := service.NewConfigFabricService(configFabricRepo, relayConfigFabricTestPublisher{}, relayConfigFabricTestSigner{secret: nostr.Generate()})
	if _, err := configFabric.Publish(t.Context(), service.ConfigPublishRequest{
		Kind:      service.ConfigFabricPolicyKind,
		ServiceID: "bahia-relay-sidecar", PolicyName: "relay-sidecar", Scope: "prod", Version: 1,
		Schema: "cascadia.config.relay-sidecar.v1", Policy: map[string]any{"allowed_pubkeys": []any{}},
	}); err != nil {
		t.Fatalf("publish desired relay config event: %v", err)
	}

	dnsOperator := &fleetOperatorDNSOperator{
		recordingDNSPersistentOperator: &recordingDNSPersistentOperator{
			recordingDNSOperator: &recordingDNSOperator{
				zones:      map[string]bool{"prod.example": true},
				backends:   map[string]bool{"primary": true},
				policyRepo: &recordingDNSPolicyRepository{},
			},
			overridesCreated: []domain.DNSRecordOverride{{
				ID: overrideID, ZoneName: "prod.example", RecordName: "api",
				RecordType: domain.DNSRecordTypeA, Value: "192.0.2.10", TTL: 60,
				Reason: "maintenance", CreatedAt: time.Now().UTC(),
			}},
		},
	}

	securityAccepted := &service.SecurityScanAccepted{
		Status:        "accepted",
		RunID:         uuid.New(),
		TargetKeyHash: "target-hash",
		TargetType:    domain.SecurityTargetPackage,
	}

	return []fleetOperatorRegistration{
		{
			name: "workers",
			methods: []fleetOperatorMethod{
				{ContextVMMethodWorkerCleanup, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-cleanup:1","cleanup_mode":"reclaimable_only"}`)},
				{ContextVMMethodWorkerCordon, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-cordon:1"}`)},
				{ContextVMMethodWorkerUncordon, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-uncordon:1"}`)},
				{ContextVMMethodWorkerDrain, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-drain:1"}`)},
				{ContextVMMethodWorkerUndrain, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-undrain:1"}`)},
				{ContextVMMethodWorkerMaintenanceEnter, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-maintenance-enter:1"}`)},
				{ContextVMMethodWorkerMaintenanceExit, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-maintenance-exit:1"}`)},
				{ContextVMMethodWorkerLabelsUpdate, []byte(`{"worker_pubkey":"worker-1","idempotency_key":"worker-labels-update:1","labels":{"region":"us-west"}}`)},
			},
			register: func(_ *testing.T, transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
				RegisterWorkerContextVMHandlers(transport, gate)
			},
		},
		{
			name: "dns",
			methods: []fleetOperatorMethod{
				{ContextVMMethodDNSZoneCreate, []byte(`{"name":"new.example","visibility":"internal","backend_ref":"primary","ttl":60}`)},
				{ContextVMMethodDNSPolicyApply, []byte(`{"name":"prod-policy","enabled":true,"rules":[{"match":{"environment":"prod"},"action":{"ttl_override":60}}]}`)},
				{ContextVMMethodDNSRecordSet, []byte(`{"zone_name":"prod.example","record_name":"api-2","record_type":"A","value":"192.0.2.11","ttl":60,"reason":"maintenance"}`)},
				{ContextVMMethodDNSOverrideRetire, []byte(`{"override_id":"11111111-1111-1111-1111-111111111111","reason":"complete"}`)},
				{ContextVMMethodDNSDriftRemediate, []byte(`{"zone":"prod.example"}`)},
			},
			register: func(_ *testing.T, transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
				RegisterDNSContextVMHandlers(transport, dnsOperator, true, gate)
			},
		},
		{
			name: "relay settings and config",
			methods: []fleetOperatorMethod{
				{ContextVMMethodRelayPolicyApply, []byte(`{"browser_relays":["wss://browser.example"],"contextvm_relays":["wss://contextvm.example"],"service_relays":["wss://service.example"]}`)},
				{ContextVMMethodRelayAdminCall, []byte(`{"target_ref":"sidecar","method":"supportedmethods"}`)},
				{ContextVMMethodConfigReconcile, []byte(`{"target_ref":"sidecar","service_id":"bahia-relay-sidecar","scope":"prod","policy_coordinate":"service:bahia-relay-sidecar:relay-sidecar"}`)},
				{ContextVMMethodConfigReload, []byte(`{"target_ref":"sidecar","service_id":"bahia-relay-sidecar","scope":"prod","policy_coordinate":"service:bahia-relay-sidecar:relay-sidecar"}`)},
			},
			register: func(_ *testing.T, transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
				RegisterRelaySettingsContextVMHandlers(transport, RelaySettingsHandlerConfig{
					Config:            relayAdminConfig,
					AdminClient:       &fakeRelayAdminClient{},
					ProjectionStore:   &memoryRelayPolicyProjectionStore{},
					ServicePubkey:     testNostrPubKeyHexFromPrivateKey(t, testServiceKey),
					Logger:            zap.NewNop(),
					ConfigFabric:      configFabric,
					FleetOperatorGate: gate,
				})
			},
		},
		{
			name: "security",
			methods: []fleetOperatorMethod{
				{ContextVMMethodSecurityScan, []byte(`{"target":{"type":"package","package":{"ecosystem":"npm","name":"lodash","version":"4.17.21"}}}`)},
				{ContextVMMethodSecurityRescan, []byte(`{"target_key_hash":"target-hash"}`)},
				{ContextVMMethodSecurityFindingsList, []byte(`{"target_key_hash":"target-hash"}`)},
				{ContextVMMethodSecuritySchedulesList, []byte(`{"limit":25}`)},
			},
			register: func(_ *testing.T, transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
				RegisterSecurityContextVMHandlers(transport, &fakeSecurityScannerControlPlane{
					accepted:  securityAccepted,
					findings:  &service.SecurityFindingsListResult{Status: "ok"},
					schedules: &service.SecuritySchedulesListResult{Status: "ok"},
				}, gate)
			},
		},
		{
			name: "sbom",
			methods: []fleetOperatorMethod{
				{ContextVMMethodSBOMGenerate, []byte(`{"idempotencyKey":"generate-1"}`)},
				{ContextVMMethodSBOMImport, []byte(`{"idempotencyKey":"import-1"}`)},
			},
			register: func(_ *testing.T, transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
				RegisterSBOMContextVMHandlers(transport, &fakeSBOMRequestRunner{
					generateAck: mustSBOMAcceptedAck(t, "generate-1"),
					importAck:   mustSBOMAcceptedAck(t, "import-1"),
				}, gate)
			},
		},
	}
}

func mustSBOMAcceptedAck(t *testing.T, idempotencyKey string) service.SBOMAcceptedAck {
	t.Helper()
	ack, err := service.NewSBOMAcceptedAck(idempotencyKey)
	if err != nil {
		t.Fatalf("new SBOM accepted ack: %v", err)
	}
	return ack
}

type fleetOperatorDNSOperator struct {
	*recordingDNSPersistentOperator
}

func (o *fleetOperatorDNSOperator) GetOverride(_ context.Context, id uuid.UUID) (*domain.DNSRecordOverride, error) {
	for index := range o.overridesCreated {
		if o.overridesCreated[index].ID == id {
			override := o.overridesCreated[index]
			return &override, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (o *fleetOperatorDNSOperator) ExpireOverride(_ context.Context, id uuid.UUID, at time.Time, _ string) error {
	for index := range o.overridesCreated {
		if o.overridesCreated[index].ID == id {
			o.overridesCreated[index].ExpiresAt = &at
			return nil
		}
	}
	return repository.ErrNotFound
}
