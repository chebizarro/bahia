package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type continuityTestStore struct {
	service.ContinuityDefinitionStore
	touches   atomic.Int32
	mutations atomic.Int32
	applied   chan struct{}
}

func (s *continuityTestStore) stored(changed bool, err error) (bool, error) {
	s.touches.Add(1)
	if changed {
		s.mutations.Add(1)
	}
	if s.applied != nil {
		s.applied <- struct{}{}
	}
	return changed, err
}
func (s *continuityTestStore) StoreProfile(p domain.ServiceContinuityProfile) (bool, error) {
	return s.stored(s.ContinuityDefinitionStore.StoreProfile(p))
}
func (s *continuityTestStore) StoreRecipe(p domain.ContinuityRecipe) (bool, error) {
	return s.stored(s.ContinuityDefinitionStore.StoreRecipe(p))
}
func (s *continuityTestStore) StoreReplicationPolicy(p domain.ReplicationPolicy) (bool, error) {
	return s.stored(s.ContinuityDefinitionStore.StoreReplicationPolicy(p))
}
func (s *continuityTestStore) GetRecipe(key string, kind domain.ContinuityRecipeKind) (domain.ContinuityRecipe, bool) {
	s.touches.Add(1)
	return s.ContinuityDefinitionStore.GetRecipe(key, kind)
}
func (s *continuityTestStore) GetProfile(key string) (domain.ServiceContinuityProfile, bool) {
	s.touches.Add(1)
	return s.ContinuityDefinitionStore.GetProfile(key)
}

func continuityDefinitionEvents(t *testing.T, key string) []nostr.Event {
	t.Helper()
	profile, err := nostradapter.EncodeContinuityProfileEvent(domain.ServiceContinuityProfile{ServiceKey: "api", PrimaryWorkerPubKey: testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{domain.ContinuityModeFull: {}, domain.ContinuityModeDegraded: {}}})
	require.NoError(t, err)
	recipe := domain.ContinuityRecipe{Name: "switch", ServiceKey: "api", Kind: domain.ContinuityRecipeKindFailover, Trigger: &domain.RecipeTrigger{Type: domain.RecipeTriggerTypeHeartbeatLoss, Target: "primary", Timeout: time.Minute}, Steps: []domain.RecipeStep{{Name: "apply", Action: domain.RecipeActionEmitEvent, Params: map[string]string{"type": "continuity.test.apply"}}}}
	failover, err := nostradapter.EncodeFailoverPolicyEvent(recipe)
	require.NoError(t, err)
	recipe.Kind = domain.ContinuityRecipeKindRecovery
	recovery, err := nostradapter.EncodeRecoveryWorkflowEvent(recipe)
	require.NoError(t, err)
	replication, err := nostradapter.EncodeReplicationPolicyEvent(domain.ReplicationPolicy{ServiceKey: "api", Targets: []domain.ReplicationTarget{{WorkerPubKey: testNostrPubKeyHexFromPrivateKey(t, testOtherKey), Strategy: "event_mirror", MaxStaleness: time.Minute}}})
	require.NoError(t, err)
	result := []nostr.Event{profile, failover, replication, recovery}
	for i := range result {
		require.NoError(t, result[i].Sign(testNostrSecretKey(t, key)))
	}
	return result
}

func continuityFixture(t *testing.T, gate *FleetOperatorGate) (*EncryptedRequestTransport, *ContinuityRuntime, *continuityTestStore, *mockEncryptedPublisher, *atomic.Int32) {
	t.Helper()
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), testNostrPubKeyHexFromPrivateKey(t, testOtherKey)}, zap.NewNop())
	store := &continuityTestStore{ContinuityDefinitionStore: service.NewInMemoryContinuityDefinitionStore()}
	mutations := &atomic.Int32{}
	executor := service.NewContinuityRecipeExecutor(&events.NoopPublisher{}, service.WithContinuityRecipeActionHandler(domain.RecipeActionEmitEvent, func(_ context.Context, _ domain.RecipeStep, run service.ContinuityRecipeRunContext) error {
		require.Equal(t, testNostrPubKeyHexFromPrivateKey(t, testRequesterKey), run.RequestedBy)
		require.Equal(t, testNostrPubKeyHexFromPrivateKey(t, testOtherKey), run.SelectedStandbyPubKey)
		require.Equal(t, "continuity-test", run.RunID)
		mutations.Add(1)
		return nil
	}))
	h := RegisterContinuityContextVMHandlers(transport, gate, nil, store, executor, zap.NewNop())
	return transport, h, store, publisher, mutations
}

func continuityRequest(t *testing.T, method, key string, wrap int) *nostr.Event {
	t.Helper()
	plain := makeRouteRequest(t, method, map[string]any{"service_key": "api", "target_worker_pubkey": testNostrPubKeyHexFromPrivateKey(t, testOtherKey), "recipe_name": "switch", "idempotency_key": "continuity-test", "requested_by": "untrusted payload author", "request_event_id": "untrusted payload id"})
	plain = makeContextVMEvent(t, key, plain.Content)
	if wrap != 0 {
		wrapperKey := key
		if wrap == KindContextVMEphemeralWrap {
			wrapperKey = nostr.Generate().Hex()
		}
		return wrapContextVMEventWithWrapperKey(t, plain, wrapperKey, wrap)
	}
	return plain
}

func TestContinuityContextVMReachableAuthorizedAndReplaySafe(t *testing.T) {
	operator := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, method := range []string{ContextVMMethodContinuityFailover, ContextVMMethodContinuityRecovery} {
		for _, wrap := range []int{0, KindContextVMGiftWrap, KindContextVMEphemeralWrap} {
			for _, auth := range []string{"allowed", "outsider", "empty", "nil"} {
				t.Run(fmt.Sprintf("%s/%d/%s", method, wrap, auth), func(t *testing.T) {
					gate := NewFleetOperatorGate([]string{operator})
					key := testRequesterKey
					switch auth {
					case "outsider":
						key = testOtherKey
					case "empty":
						gate = NewFleetOperatorGate(nil)
					case "nil":
						gate = nil
					}
					transport, h, store, publisher, mutations := continuityFixture(t, gate)
					if auth == "allowed" {
						for _, ev := range continuityDefinitionEvents(t, key) {
							require.NoError(t, h.handleDefinition(t.Context(), &ev))
						}
						h.readyOnce.Do(func() { close(h.ready) })
						store.touches.Store(0)
					}
					request := continuityRequest(t, method, key, wrap)
					transport.HandleEvent(t.Context(), request)
					require.NotEmpty(t, publisher.events)
					last := publisher.events[len(publisher.events)-1]
					var response ContextVMJSONRPCResponse
					if wrap != 0 {
						response = unwrapContextVMResponse(t, last, key)
					} else {
						response = contextVMResponse(t, last)
					}
					require.Equal(t, `"route-test"`, string(response.ID))
					if auth != "allowed" {
						require.NotNil(t, response.Error)
						expected := fleetOperatorNotConfiguredError
						if auth == "outsider" {
							expected = fleetOperatorUnauthorizedError
						}
						require.Equal(t, expected, response.Error.Message)
						require.Zero(t, store.touches.Load(), "authorization must precede all domain storage access")
						require.Zero(t, mutations.Load())
						return
					}
					require.Nil(t, response.Error, "wired method must execute, not return method not found: %+v", response.Error)
					require.Equal(t, int32(1), mutations.Load())
					require.Equal(t, "succeeded", response.Result.(map[string]any)["status"])
					before := store.touches.Load()
					transport.HandleEvent(t.Context(), request)
					require.Equal(t, before, store.touches.Load())
					require.Equal(t, int32(1), mutations.Load(), "replay repeated recipe actions")
					// Re-enveloped retry with a different RPC id still shares the semantic key.
					retry := makeContextVMEvent(t, key, replaceRPCID(t, method))
					if wrap != 0 {
						wrapperKey := key
						if wrap == KindContextVMEphemeralWrap {
							wrapperKey = nostr.Generate().Hex()
						}
						retry = wrapContextVMEventWithWrapperKey(t, retry, wrapperKey, wrap)
					}
					transport.HandleEvent(t.Context(), retry)
					require.Equal(t, before, store.touches.Load())
					require.Equal(t, int32(1), mutations.Load())
				})
			}
		}
	}
}

func replaceRPCID(t *testing.T, method string) string {
	t.Helper()
	ev := continuityRequest(t, method, testRequesterKey, 0)
	return strings.Replace(ev.Content, `"route-test"`, `"retry"`, 1)
}

func TestContinuityMissingRegistrationIsMethodNotFound(t *testing.T) {
	for _, method := range []string{ContextVMMethodContinuityFailover, ContextVMMethodContinuityRecovery} {
		transport, _, store, publisher, mutations := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
		delete(transport.contextVMHandlers, method)
		transport.HandleEvent(t.Context(), continuityRequest(t, method, testRequesterKey, 0))
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		require.NotNil(t, response.Error)
		require.Equal(t, -32601, response.Error.Code)
		require.Zero(t, mutations.Load())
		require.Zero(t, store.touches.Load())
	}
}

type continuityFailingExecutor struct{}

func (continuityFailingExecutor) ExecuteFailover(context.Context, service.FailoverExecutionRequest) error {
	return errors.New("runtime adapter unavailable")
}
func (continuityFailingExecutor) ExecuteRecovery(context.Context, service.RecoveryExecutionRequest) error {
	return errors.New("runtime adapter unavailable")
}

func TestContinuityContextVMPropagatesExecutionFailure(t *testing.T) {
	for _, method := range []string{ContextVMMethodContinuityFailover, ContextVMMethodContinuityRecovery} {
		transport, h, _, publisher, _ := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
		for _, ev := range continuityDefinitionEvents(t, testRequesterKey) {
			require.NoError(t, h.handleDefinition(t.Context(), &ev))
		}
		h.readyOnce.Do(func() { close(h.ready) })
		h.executor = continuityFailingExecutor{}
		transport.HandleEvent(t.Context(), continuityRequest(t, method, testRequesterKey, 0))
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		require.NotNil(t, response.Error)
		require.Equal(t, "runtime adapter unavailable", response.Error.Message)
	}
}

func TestContinuityContextVMReplayAfterTransportRestart(t *testing.T) {
	gate := NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)})
	for _, method := range []string{ContextVMMethodContinuityFailover, ContextVMMethodContinuityRecovery} {
		transport, h, store, publisher, mutations := continuityFixture(t, gate)
		responses := newMemoryContextVMResponseStore()
		transport.contextVMResponseStore = responses
		for _, ev := range continuityDefinitionEvents(t, testRequesterKey) {
			require.NoError(t, h.handleDefinition(t.Context(), &ev))
		}
		h.readyOnce.Do(func() { close(h.ready) })
		request := continuityRequest(t, method, testRequesterKey, 0)
		transport.HandleEvent(t.Context(), request)
		require.Nil(t, contextVMResponse(t, publisher.events[len(publisher.events)-1]).Error)
		before := store.touches.Load()
		restarted := NewEncryptedRequestTransport(nil, newResponder(t, publisher), gate.authorizedPubkeys, zap.NewNop(), WithContextVMResponseStore(responses, time.Hour))
		RegisterContinuityContextVMHandlers(restarted, gate, nil, store, h.executor, zap.NewNop())
		// The new runtime has not caught up; a persisted response must bypass it.
		restarted.HandleEvent(t.Context(), request)
		require.Nil(t, contextVMResponse(t, publisher.events[len(publisher.events)-1]).Error)
		require.Equal(t, before, store.touches.Load())
		require.Equal(t, int32(1), mutations.Load())
	}
}

func TestContinuityContextVMInvalidParamsDoNotAccessStore(t *testing.T) {
	for _, method := range []string{ContextVMMethodContinuityFailover, ContextVMMethodContinuityRecovery} {
		for _, field := range []string{"service_key", "target_worker_pubkey", "target_profile", "idempotency_key"} {
			t.Run(method+"/"+field, func(t *testing.T) {
				transport, _, store, publisher, mutations := continuityFixture(t, NewFleetOperatorGate([]string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}))
				params := map[string]any{"service_key": "api", "target_worker_pubkey": testNostrPubKeyHexFromPrivateKey(t, testOtherKey), "idempotency_key": "valid", "target_profile": "full"}
				params[field] = ""
				if field == "target_profile" {
					params[field] = "invalid"
				}
				transport.HandleEvent(t.Context(), makeRouteRequest(t, method, params))
				require.NotNil(t, contextVMResponse(t, publisher.events[len(publisher.events)-1]).Error)
				require.Zero(t, store.touches.Load())
				require.Zero(t, mutations.Load())
			})
		}
	}
}
