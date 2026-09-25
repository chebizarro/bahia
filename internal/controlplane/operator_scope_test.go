package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Nil embedded interfaces panic on any access: rejection must precede every
// dependency, including persisted replay responses and Loom publication.
type untouchedOperatorDependencies struct {
	repository.OrgMemberRepository
	repository.RelayPolicyProjectionRepository
	RelayAdminCaller
	loomJobClient
}

type untouchedContextVMResponseStore struct {
	repository.ContextVMResponseStore
}

// Exercise method authorization independently of the mandatory transport gate.
func authzTestTransportAuthors(t *testing.T) []string {
	t.Helper()
	return []string{
		testNostrPubKeyHexFromPrivateKey(t, testRequesterKey),
		testNostrPubKeyHexFromPrivateKey(t, testOtherKey),
		testNostrPubKeyHexFromPrivateKey(t, testServiceKey),
	}
}

func registerOperatorScopeFixtures(t *testing.T, transport *EncryptedRequestTransport, gate *FleetOperatorGate) {
	t.Helper()
	untouched := &untouchedOperatorDependencies{}
	RegisterBackupAliasContextVMHandlers(transport, auth.NewRBAC(untouched), gate)
	RegisterLoomContextVMHandlers(transport, untouched, nil, gate)
	RegisterAssistantContextVMHandlers(transport, &service.AssistantOrchestrator{}, gate)
	RegisterRelaySettingsContextVMHandlers(transport, RelaySettingsHandlerConfig{
		ProjectionStore: untouched, AdminClient: untouched, FleetOperatorGate: gate,
	})
}

func protectedOperatorMethods() []string {
	return []string{
		ContextVMMethodRelayPolicyApply, ContextVMMethodRelayAdminCall,
		ContextVMMethodConfigReconcile, ContextVMMethodConfigReload, ContextVMMethodConfigStatus,
		ContextVMMethodBackupRepositoryRegister, ContextVMMethodBackupPolicyApply,
		ContextVMMethodBackupRecipeApply, ContextVMMethodBackupDefinitionApply,
		ContextVMMethodBackupRun, ContextVMMethodBackupVerification,
		ContextVMMethodBackupRestore, ContextVMMethodBackupRetention,
		ContextVMMethodBackupRestoreApprovalAlias, ContextVMMethodBackupRepositoryProbe,
		ContextVMMethodLoomSubmit,
		domain.AssistantContextVMMethodPrompt, domain.AssistantContextVMMethodApproval,
	}
}

func TestOperatorMethodsRejectBeforeStorageProgressOrCommandSigning(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, tc := range []struct {
		name string
		gate *FleetOperatorGate
		want string
	}{
		{"unauthorized signer", NewFleetOperatorGate([]string{operator}), fleetOperatorUnauthorizedError},
		{"empty allowlist", NewFleetOperatorGate(nil), fleetOperatorNotConfiguredError},
		{"missing gate", nil, fleetOperatorNotConfiguredError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range protectedOperatorMethods() {
				t.Run(method, func(t *testing.T) {
					publisher := &mockEncryptedPublisher{}
					transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop(),
						WithContextVMResponseStore(&untouchedContextVMResponseStore{}, time.Hour))
					registerOperatorScopeFixtures(t, transport, tc.gate)
					if _, classified := transport.contextVMOperatorGates[method]; !classified {
						t.Fatalf("%s is not classified as operator-scoped", method)
					}
					request := backupAuthorityRequest(t, testOtherKey, method, map[string]any{
						"idempotency_key":  "must-not-read-replay-storage",
						"requester_pubkey": operator, "approved_by": operator, "submitter": operator,
					})
					transport.HandleEvent(t.Context(), request)
					if len(publisher.events) != 1 {
						t.Fatalf("published %d events, want only the rejection (no progress or command)", len(publisher.events))
					}
					response := contextVMResponse(t, publisher.events[0])
					if response.Error == nil || response.Error.Code != -32001 || response.Error.Message != tc.want {
						t.Fatalf("response error = %+v, want -32001 %q", response.Error, tc.want)
					}
				})
			}
		})
	}
}

func TestRemovedOperatorMethodsReturnMethodNotFoundBeforeAuthorizationOrReplay(t *testing.T) {
	for _, method := range protectedOperatorMethods() {
		t.Run(method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop(),
				WithContextVMResponseStore(&untouchedContextVMResponseStore{}, time.Hour))
			registerOperatorScopeFixtures(t, transport, nil)
			delete(transport.contextVMHandlers, method)
			transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testOtherKey, method,
				map[string]any{"idempotency_key": "removed-method"}))
			if len(publisher.events) != 1 {
				t.Fatalf("published events = %d, want one error", len(publisher.events))
			}
			response := contextVMResponse(t, publisher.events[0])
			if response.Error == nil || response.Error.Code != -32601 || response.Error.Message != "method not found" {
				t.Fatalf("response error = %+v, want -32601 method not found", response.Error)
			}
		})
	}
}

func TestOperatorGatePrecedesCachedSuccess(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop())
	method := ContextVMMethodRelayPolicyApply
	params := map[string]any{"idempotency_key": "cached-success"}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := contextVMRequestFingerprint(raw)
	if err != nil {
		t.Fatal(err)
	}
	transport.cacheContextVMResponse(testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), method, "cached-success", fingerprint,
		ContextVMJSONRPCResponse{JSONRPC: "2.0", Result: map[string]any{"status": "accepted"}})
	registerOperatorScopeFixtures(t, transport, nil)
	transport.HandleEvent(t.Context(), backupAuthorityRequest(t, testRequesterKey, method, params))
	response := contextVMResponse(t, publisher.events[0])
	if response.Error == nil || response.Error.Message != fleetOperatorNotConfiguredError {
		t.Fatalf("cached success bypassed operator gate: %+v", response)
	}
}

func TestNonOperatorMethodScopesRemainExplicit(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop())
	RegisterServiceContextVMHandlers(transport, EncryptedServiceHandlersConfig{})
	RegisterLoomContextVMHandlers(transport, &mockLoomClient{}, nil, nil)
	RegisterRelaySettingsContextVMHandlers(transport, RelaySettingsHandlerConfig{})
	for _, method := range []string{
		ContextVMMethodServiceDeployPreview, ContextVMMethodServiceDeploy, ContextVMMethodServiceRouteAttach,
		ContextVMMethodServiceRollback, ContextVMMethodApprovalApprove, ContextVMMethodApprovalReject,
		ContextVMMethodLoomCancel, ContextVMMethodRelayPolicyGet,
	} {
		if transport.contextVMHandlers[method] == nil {
			t.Errorf("missing registration: %s", method)
		}
		if _, gated := transport.contextVMOperatorGates[method]; gated {
			t.Errorf("non-operator method acquired a fleet operator gate: %s", method)
		}
	}
	transport.HandleEvent(context.Background(), backupAuthorityRequest(t, testOtherKey, ContextVMMethodRelayPolicyGet, nil))
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error != nil {
		t.Fatalf("read-only relay policy access without operator gate: %+v", response.Error)
	}
}

func TestTenantServiceRouteAttachRequiresTransportAccessAndTenantRBAC(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key         string
		authors     []string
		wantSuccess bool
	}{
		{"allowlisted tenant admin", testRequesterKey, authzTestTransportAuthors(t), true},
		{"allowlisted non-member", testOtherKey, authzTestTransportAuthors(t), false},
		{"tenant admin without transport allowlist", testRequesterKey, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRouteAttachFixture(t, false, false)
			h := fixture.handlers
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), tc.authors, zap.NewNop())
			RegisterServiceContextVMHandlers(transport, EncryptedServiceHandlersConfig{
				Registry: h.registry, Policy: h.policy, PublicRoutes: h.publicRoutes,
				Services: h.authorizer.services.(*testServiceRepo), DeploymentUnits: h.deploymentUnits, RBAC: h.authorizer.rbac,
			})
			params := fixture.request(t, fixture.serviceID, validRouteAttachRequest()).RPC.Params
			content, err := json.Marshal(ContextVMJSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`"tenant"`), Method: ContextVMMethodServiceRouteAttach, Params: params})
			if err != nil {
				t.Fatal(err)
			}
			transport.HandleEvent(t.Context(), makeContextVMEvent(t, tc.key, string(content)))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if tc.wantSuccess {
				if response.Error != nil || len(fixture.intentRepo.intents) != 2 {
					t.Fatalf("tenant member denied route attachment: %+v", response.Error)
				}
			} else if response.Error == nil || len(fixture.intentRepo.intents) != 1 {
				t.Fatal("unauthorized signer created a route attachment intent")
			}
			if len(tc.authors) == 0 && response.Error.Code != -32001 {
				t.Fatalf("tenant request did not reach the mandatory transport pre-filter: %+v", response.Error)
			}
		})
	}
}

func TestBackupAndLoomRejectForgedEventSigner(t *testing.T) {
	for _, method := range []string{ContextVMMethodBackupRun, ContextVMMethodLoomSubmit, ContextVMMethodLoomCancel} {
		t.Run(method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authzTestTransportAuthors(t), zap.NewNop(),
				WithContextVMResponseStore(&untouchedContextVMResponseStore{}, time.Hour))
			registerOperatorScopeFixtures(t, transport, NewFleetOperatorGate(contextVMTestAuthorizedPubkeys(t)))
			request := backupAuthorityRequest(t, testOtherKey, method, map[string]any{"idempotency_key": "forged-signer"})
			request.PubKey = testNostrPubKeyFromPrivateKey(t, testRequesterKey)
			request.SetID()
			transport.HandleEvent(t.Context(), request)
			if len(publisher.events) != 0 {
				t.Fatal("forged signer reached dispatch")
			}
		})
	}
}
