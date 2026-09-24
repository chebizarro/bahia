package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

var mutationPolicyID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

type mutationPolicyRepository struct {
	testPolicyRepo
	policies map[uuid.UUID]domain.DeploymentPolicy
	calls    int
	err      error
}

func (r *mutationPolicyRepository) Create(_ context.Context, p *domain.DeploymentPolicy) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	p.ID = uuid.New()
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	r.policies[p.ID] = *p
	return nil
}

func (r *mutationPolicyRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	p, ok := r.policies[id]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

func (r *mutationPolicyRepository) Update(_ context.Context, p *domain.DeploymentPolicy) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	p.UpdatedAt = time.Now().UTC()
	r.policies[p.ID] = *p
	return nil
}

func (r *mutationPolicyRepository) Delete(_ context.Context, id uuid.UUID) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	delete(r.policies, id)
	return nil
}

func (r *mutationPolicyRepository) ListGlobal(context.Context) ([]domain.DeploymentPolicy, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	var policies []domain.DeploymentPolicy
	for _, p := range r.policies {
		if p.Enabled && p.EnvironmentID == nil {
			policies = append(policies, p)
		}
	}
	return policies, nil
}

type mutationWorkerRepository struct {
	*memoryWorkerRepo
	updates int
	err     error
}

func (r *mutationWorkerRepository) UpdateSchedulingState(ctx context.Context, key string, state domain.WorkerSchedulingState, reason string) error {
	r.updates++
	if r.err != nil {
		return r.err
	}
	return r.memoryWorkerRepo.UpdateSchedulingState(ctx, key, state, reason)
}

func newMutationFixture(t *testing.T, method string, gate *FleetOperatorGate) (*EncryptedRequestTransport, *mockEncryptedPublisher, *mutationWorkerRepository, *mutationPolicyRepository) {
	t.Helper()
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	// Admit both principals at the transport boundary so denial proves the
	// method's operator gate, not just the transport's broader admission check.
	transport := NewEncryptedRequestTransport(nil, responder, []string{
		testNostrPubKeyHexFromPrivateKey(t, testRequesterKey),
		testNostrPubKeyHexFromPrivateKey(t, testOtherKey),
	}, zap.NewNop())
	initial := domain.WorkerSchedulingActive
	switch method {
	case ContextVMMethodWorkerUncordon:
		initial = domain.WorkerSchedulingCordoned
	case ContextVMMethodWorkerUndrain:
		initial = domain.WorkerSchedulingDraining
	}
	workers := &mutationWorkerRepository{memoryWorkerRepo: newMemoryWorkerRepo(domain.Worker{PubKey: testNostrPubKeyHexFromPrivateKey(t, testOtherKey), SchedulingState: initial})}
	policies := &mutationPolicyRepository{policies: map[uuid.UUID]domain.DeploymentPolicy{
		mutationPolicyID: {ID: mutationPolicyID, Name: "signature", Enabled: true, Enforcement: domain.PolicyEnforcementBlock, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}},
	}}
	reactor := NewReactor(Config{}, nil, nil, responder.signer, zap.NewNop(),
		WithControlPlanePublisher(publisher), WithWorkerRepository(workers),
		WithPolicyService(service.NewPolicyService(policies, &testSignatureRepo{}, nil, zap.NewNop())))
	RegisterWorkerContextVMHandlers(transport, gate)
	reactor.RegisterMutationContextVMHandlers(transport, gate)
	return transport, publisher, workers, policies
}

func mutationParams(t *testing.T, method string) map[string]any {
	t.Helper()
	params := map[string]any{"idempotency_key": "mutation-test"}
	switch method {
	case ContextVMMethodWorkerUncordon, ContextVMMethodWorkerUndrain, ContextVMMethodWorkerMaintenanceEnter:
		params["worker_pubkey"] = testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
		params["reason"] = "operator maintenance"
	case ContextVMMethodPolicyCreate:
		params["name"] = "approval"
		params["rules"] = []domain.PolicyRule{{Type: domain.RuleRequireApproval}}
		params["enabled"] = true
	case ContextVMMethodPolicyUpdate:
		params["id"] = mutationPolicyID.String()
		params["name"] = "updated"
	case ContextVMMethodPolicyDelete:
		params["id"] = mutationPolicyID.String()
	case ContextVMMethodPolicyEvaluate:
		params["artifact_id"] = uuid.NewString()
		params["environment_id"] = uuid.NewString()
	}
	return params
}

func mutationMethods() []string {
	return []string{ContextVMMethodWorkerUncordon, ContextVMMethodWorkerUndrain, ContextVMMethodWorkerMaintenanceEnter,
		ContextVMMethodPolicyCreate, ContextVMMethodPolicyUpdate, ContextVMMethodPolicyDelete, ContextVMMethodPolicyEvaluate}
}

func TestReactorContextVMMutationsReachableAndAuthorized(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, method := range mutationMethods() {
		for _, encrypted := range []bool{false, true} {
			for _, authorization := range []string{"allowed", "outsider", "empty", "nil"} {
				name := method + "/" + authorization
				if encrypted {
					name += "/wrapped"
				}
				t.Run(name, func(t *testing.T) {
					gate := NewFleetOperatorGate([]string{operator})
					key := testRequesterKey
					switch authorization {
					case "outsider":
						key = testOtherKey
					case "empty":
						gate = NewFleetOperatorGate(nil)
					case "nil":
						gate = nil
					}
					transport, publisher, workers, policies := newMutationFixture(t, method, gate)
					params := mutationParams(t, method)
					inner := makeRouteRequest(t, method, params)
					inner = makeContextVMEvent(t, key, inner.Content)
					request := inner
					if encrypted {
						request = wrapContextVMEvent(t, inner, KindContextVMEphemeralWrap)
					}
					transport.HandleEvent(t.Context(), request)
					if len(publisher.events) == 0 {
						t.Fatal("no transport response")
					}
					last := publisher.events[len(publisher.events)-1]
					var response ContextVMJSONRPCResponse
					if encrypted {
						response = unwrapContextVMResponse(t, last, key)
					} else {
						response = contextVMResponse(t, last)
					}
					if string(response.ID) != `"route-test"` {
						t.Fatalf("lost JSON-RPC correlation: %s", response.ID)
					}
					if authorization != "allowed" {
						want := fleetOperatorNotConfiguredError
						if authorization == "outsider" {
							want = fleetOperatorUnauthorizedError
						}
						if response.Error == nil || response.Error.Message != want {
							t.Fatalf("response = %+v, want %s", response, want)
						}
						if workers.updates != 0 || policies.calls != 0 || len(publisher.events) != 2 {
							t.Fatalf("unauthorized side effects: workers=%d policies=%d publishes=%d", workers.updates, policies.calls, len(publisher.events))
						}
						return
					}
					if response.Error != nil {
						t.Fatalf("authorized mutation failed: %+v", response.Error)
					}
					states := 0
					for _, event := range publisher.events {
						switch event.Kind {
						case KindCASControlState:
							states++
							if !event.VerifySignature() {
								t.Fatal("unsigned state")
							}
							if strings.HasPrefix(method, "policy/") {
								var state map[string]any
								if err := json.Unmarshal([]byte(event.Content), &state); err != nil {
									t.Fatal(err)
								}
								if tagValueNostr(event.Tags, "domain") != "policy" || tagValueNostr(event.Tags, "schema") != "bahia.cp-state.v1" || tagValueNostr(event.Tags, "d") != state["id"] || state["deleted"] != (method == ContextVMMethodPolicyDelete) {
									t.Fatalf("invalid canonical policy state: %+v", event)
								}
							}
						case KindContextVMMessage:
							var rpc ContextVMJSONRPCRequest
							if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
								t.Fatal(err)
							}
							if rpc.Method != "" && rpc.Method != ContextVMProgressNotificationMethod {
								t.Fatalf("republished a command instead of executing it: %s", rpc.Method)
							}
						}
					}
					if method == ContextVMMethodPolicyEvaluate {
						result := response.Result.(map[string]any)
						if result["allowed"] != false || result["blockers"] != float64(1) || states != 0 {
							t.Fatalf("evaluation did not enforce signature policy: %+v", response)
						}
					} else if states != 1 {
						t.Fatalf("state events = %d, want one", states)
					}
					switch method {
					case ContextVMMethodWorkerUncordon, ContextVMMethodWorkerUndrain, ContextVMMethodWorkerMaintenanceEnter:
						worker, _ := workers.GetByPubKey(t.Context(), params["worker_pubkey"].(string))
						want := domain.WorkerSchedulingActive
						if method == ContextVMMethodWorkerMaintenanceEnter {
							want = domain.WorkerSchedulingMaintenance
						}
						if workers.updates != 1 || worker.SchedulingState != want || worker.SchedulingNote != "operator maintenance" {
							t.Fatalf("scheduling state not persisted: %+v", worker)
						}
					case ContextVMMethodPolicyCreate:
						if len(policies.policies) != 2 {
							t.Fatal("policy not created")
						}
					case ContextVMMethodPolicyUpdate:
						if policies.policies[mutationPolicyID].Name != "updated" {
							t.Fatal("policy not updated")
						}
					case ContextVMMethodPolicyDelete:
						if len(policies.policies) != 0 {
							t.Fatal("policy not deleted")
						}
					}
					// Replay must return the cached outcome, not repeat the mutation.
					calls, updates, published := policies.calls, workers.updates, len(publisher.events)
					transport.HandleEvent(t.Context(), request)
					if calls != policies.calls || updates != workers.updates || len(publisher.events) != published+1 {
						t.Fatal("replay repeated the mutation or failed to replay its result")
					}
				})
			}
		}
	}
}

func TestReactorContextVMMutationsPropagateRepositoryFailure(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	for _, method := range mutationMethods() {
		t.Run(method, func(t *testing.T) {
			transport, publisher, workers, policies := newMutationFixture(t, method, gate)
			workers.err, policies.err = errors.New("storage failed"), errors.New("storage failed")
			transport.HandleEvent(t.Context(), makeRouteRequest(t, method, mutationParams(t, method)))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || !strings.Contains(response.Error.Message, "storage failed") || len(publisher.events) != 2 {
				t.Fatalf("failure masked by success or state publication: %+v, publishes=%d", response, len(publisher.events))
			}
		})
	}
}

func TestReactorContextVMMutationsRejectMissingDependencies(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	for _, method := range mutationMethods() {
		t.Run(method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			responder := newResponder(t, publisher)
			transport := NewEncryptedRequestTransport(nil, responder, contextVMTestAuthorizedPubkeys(t), zap.NewNop())
			reactor := NewReactor(Config{}, nil, nil, responder.signer, zap.NewNop(), WithControlPlanePublisher(publisher))
			reactor.RegisterMutationContextVMHandlers(transport, gate)
			transport.HandleEvent(t.Context(), makeRouteRequest(t, method, mutationParams(t, method)))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || !strings.Contains(response.Error.Message, "not configured") {
				t.Fatalf("unavailable dependency not rejected: %+v", response)
			}
			_, err := transport.contextVMHandlers[method](t.Context(), ContextVMRequest{})
			if err == nil || err.Error() != fleetOperatorUnauthorizedError {
				t.Fatalf("missing principal must fail closed: %v", err)
			}
		})
	}
}

func TestWorkerContextVMMutationValidation(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	for _, method := range mutationMethods()[:3] {
		for _, invalid := range []string{"disabled", "bad_pubkey", "missing_idempotency", "conflicting_worker", "conflicting_idempotency"} {
			t.Run(method+"/"+invalid, func(t *testing.T) {
				transport, publisher, workers, _ := newMutationFixture(t, method, gate)
				params := mutationParams(t, method)
				key := params["worker_pubkey"].(string)
				switch invalid {
				case "disabled":
					workers.workers[key].SchedulingState = domain.WorkerSchedulingDisabled
				case "bad_pubkey":
					params["worker_pubkey"] = "invalid"
				case "missing_idempotency":
					delete(params, "idempotency_key")
				}
				request := makeRouteRequest(t, method, params)
				switch invalid {
				case "conflicting_worker":
					request.Tags = append(request.Tags, nostr.Tag{"worker", testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
				case "conflicting_idempotency":
					request.Tags = append(request.Tags, nostr.Tag{"d", "different"})
				}
				if err := request.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
					t.Fatal(err)
				}
				transport.HandleEvent(t.Context(), request)
				response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
				if response.Error == nil || workers.updates != 0 || len(publisher.events) != 2 {
					t.Fatalf("invalid mutation was not rejected before writes: %+v", response)
				}
			})
		}
	}
}

func TestPolicyContextVMMutationValidation(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	for _, tc := range []struct {
		method string
		field  string
		value  any
	}{
		{ContextVMMethodPolicyCreate, "name", " "},
		{ContextVMMethodPolicyCreate, "enforcement", "ignore"},
		{ContextVMMethodPolicyCreate, "rules", []domain.PolicyRule{{Type: domain.RuleTyposquatCheck}}},
		{ContextVMMethodPolicyCreate, "environment_id", uuid.Nil.String()},
		{ContextVMMethodPolicyCreate, "idempotency_key", ""},
		{ContextVMMethodPolicyUpdate, "enforcement", "ignore"},
		{ContextVMMethodPolicyUpdate, "environment_id", "invalid"},
		{ContextVMMethodPolicyUpdate, "id", "invalid"},
		{ContextVMMethodPolicyDelete, "id", "invalid"},
		{ContextVMMethodPolicyEvaluate, "artifact_id", uuid.Nil.String()},
		{ContextVMMethodPolicyEvaluate, "environment_id", "invalid"},
	} {
		t.Run(tc.method+"/"+tc.field, func(t *testing.T) {
			transport, publisher, _, policies := newMutationFixture(t, tc.method, gate)
			params := mutationParams(t, tc.method)
			params[tc.field] = tc.value
			transport.HandleEvent(t.Context(), makeRouteRequest(t, tc.method, params))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || len(publisher.events) != 2 || len(policies.policies) != 1 || policies.policies[mutationPolicyID].Enforcement != domain.PolicyEnforcementBlock {
				t.Fatalf("invalid policy mutation was not rejected: %+v", response)
			}
		})
	}
}

type rejectMutationStatePublisher struct {
	*mockEncryptedPublisher
}

func (p rejectMutationStatePublisher) Publish(ctx context.Context, event nostr.Event) (int, error) {
	if event.Kind == KindCASControlState {
		return 0, nil
	}
	return p.mockEncryptedPublisher.Publish(ctx, event)
}

func TestReactorContextVMMutationsDoNotAcknowledgeRejectedState(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	for _, method := range mutationMethods()[:6] {
		t.Run(method, func(t *testing.T) {
			transport, publisher, workers, policies := newMutationFixture(t, method, gate)
			reactor := NewReactor(Config{}, nil, nil, transport.responder.signer, zap.NewNop(),
				WithControlPlanePublisher(rejectMutationStatePublisher{publisher}), WithWorkerRepository(workers),
				WithPolicyService(service.NewPolicyService(policies, &testSignatureRepo{}, nil, zap.NewNop())))
			reactor.RegisterMutationContextVMHandlers(transport, gate)
			transport.HandleEvent(t.Context(), makeRouteRequest(t, method, mutationParams(t, method)))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || !strings.Contains(response.Error.Message, "publication failed") || !strings.Contains(response.Error.Message, "no relay accepted") {
				t.Fatalf("state publication failure masked as success: %+v", response)
			}
		})
	}
}

func TestPolicyContextVMStateOrdering(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	transport, publisher, _, _ := newMutationFixture(t, ContextVMMethodPolicyUpdate, gate)
	for _, method := range []string{ContextVMMethodPolicyUpdate, ContextVMMethodPolicyDelete} {
		transport.HandleEvent(t.Context(), makeRouteRequest(t, method, mutationParams(t, method)))
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		if response.Error != nil {
			t.Fatal(response.Error)
		}
	}
	var previous nostr.Timestamp
	states := 0
	for _, event := range publisher.events {
		if event.Kind != KindCASControlState {
			continue
		}
		states++
		if event.CreatedAt <= previous || tagValueNostr(event.Tags, "d") != mutationPolicyID.String() {
			t.Fatal("same-policy writes must have increasing replaceable timestamps")
		}
		previous = event.CreatedAt
	}
	if states != 2 {
		t.Fatalf("state events = %d, want update and tombstone", states)
	}
}
