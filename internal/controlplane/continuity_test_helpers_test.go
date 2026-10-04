package controlplane

import (
	"context"
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
	h := NewContinuityRuntime(gate, nil, store, executor, zap.NewNop())
	return transport, h, store, publisher, mutations
}
