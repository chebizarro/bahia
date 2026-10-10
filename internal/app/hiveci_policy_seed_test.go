package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	hiveciAdapter "github.com/openagentsinc/bahia/internal/adapters/hiveci"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestConfiguredHiveCIPolicyPublishesFromRelayLocalEntitiesWithoutPostgres(t *testing.T) {
	const serviceKey = "2222222222222222222222222222222222222222222222222222222222222222"
	ctx := context.Background()
	dir := t.TempDir()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: serviceKey, PublishEnabled: true}
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(serviceKey)
	require.NoError(t, err)
	fixtureKeyer, err := controlplane.NewPrivateKeySigner(serviceKey)
	require.NoError(t, err)
	publisher := nostrAdapter.NewPublisher(cfg, nostrAdapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), nostrAdapter.WithPublisherSigner(fixtureKeyer),
		nostrAdapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostrAdapter.WithLocalOutbox(outbox, store))
	t.Cleanup(func() {
		publisher.Close()
		require.NoError(t, outbox.Close())
		require.NoError(t, store.Close())
	})
	history := nostrAdapter.NewLocalEventRepository(store, nil).Authored(pubkey)
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	projector := nostrAdapter.NewProjector(cfg, registry, publisher, history, zap.NewNop(), nostrAdapter.WithProjectorSigner(fixtureKeyer, pubkey))
	publisher.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	orgID := uuid.New()
	svc := domain.Service{ID: uuid.New(), OrgID: orgID, Name: "api"}
	env := domain.Environment{ID: uuid.New(), OrgID: orgID, Name: "prod"}
	saveSignedHivePolicyEntity(t, store, serviceKey, kinds.CPStateTopicServiceRegistry, kinds.ServiceRegistry, svc.ID, svc)
	saveSignedHivePolicyEntity(t, store, serviceKey, kinds.CPStateTopicEnvironmentRegistry, kinds.EnvironmentRegistry, env.ID, env)
	localState, err := service.NewLocalSupervisionState(store, pubkey)
	require.NoError(t, err)
	signer, err := controlplane.NewPrivateKeySigner(serviceKey)
	require.NoError(t, err)
	ock := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
		Signer: signer, ServicePubkey: pubkey, Publisher: projector,
		History: nostrAdapter.NewProjectorOCKEnvelopeHistory(history), Logger: zap.NewNop(),
	})
	canonical := nostrAdapter.NewHiveCICanonicalPublisher(projector, controlplane.NewConfidentialEncryptor(ock, zap.NewNop()), zap.NewNop())
	repo := hiveciAdapter.NewCanonicalRepository(store, canonical, nil, nil, zap.NewNop())
	policies := []config.HiveCIPolicyConfig{{
		RepoCoordinate: "30617:author:api", WorkflowPath: ".hive-ci/build.yml", ServiceName: "api", EnvironmentName: "prod",
	}}
	view := service.NewLocalAdoptionView(localState)
	require.NoError(t, ensureConfiguredHiveCIPipelinePolicies(ctx, policies, view, repo))
	bound, err := repo.GetPolicyByRepoAndWorkflow(ctx, policies[0].RepoCoordinate, policies[0].WorkflowPath)
	require.NoError(t, err)
	require.NotNil(t, bound)
	require.Equal(t, svc.ID, bound.ServiceID)
	require.Equal(t, env.ID, bound.EnvironmentID)
	before, err := outbox.Counts()
	require.NoError(t, err)
	require.NoError(t, ensureConfiguredHiveCIPipelinePolicies(ctx, policies, view, repo))
	after, err := outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, before, after, "repeated hydration with unchanged config must not add a signed policy event")
}

func saveSignedHivePolicyEntity(t *testing.T, store *localstore.Store, keyHex, topic string, legacyKind int, id uuid.UUID, entity any) {
	t.Helper()
	keyBytes, err := hex.DecodeString(keyHex)
	require.NoError(t, err)
	var key [32]byte
	copy(key[:], keyBytes)
	content, err := json.Marshal(entity)
	require.NoError(t, err)
	event := nostr.Event{
		Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"d", id.String()}, {"t", topic},
			{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
			{kinds.CASControlStateTagLegacyKind, strconv.Itoa(legacyKind)},
		},
		Content: string(content),
	}
	require.NoError(t, event.Sign(key))
	stored, err := store.SaveEvent(event)
	require.NoError(t, err)
	require.True(t, stored)
}

type testHivePolicyEntities struct {
	services     []domain.Service
	environments []service.AdoptionEnvironment
}

func (v testHivePolicyEntities) ListServices(context.Context) ([]domain.Service, error) {
	return v.services, nil
}

func (v testHivePolicyEntities) ListEnvironments(context.Context) ([]service.AdoptionEnvironment, error) {
	return v.environments, nil
}

type testHivePolicyState struct {
	hiveciAdapter.CanonicalState
	policies  []domain.HiveCIPipelinePolicy
	publishes int
}

func (s *testHivePolicyState) PublishPipelinePolicy(_ context.Context, policy domain.HiveCIPipelinePolicy) error {
	s.publishes++
	for i := range s.policies {
		if s.policies[i].ID == policy.ID {
			s.policies[i] = policy
			return nil
		}
	}
	s.policies = append(s.policies, policy)
	return nil
}

func (s *testHivePolicyState) ListPipelinePolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return append([]domain.HiveCIPipelinePolicy(nil), s.policies...), nil
}

type testHivePolicyIndex struct {
	repository.HiveCIRepository
	writes int
}

func (*testHivePolicyIndex) ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	panic("canonical policy publication must not read PostgreSQL")
}
func (i *testHivePolicyIndex) EnsurePipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error {
	i.writes++
	return nil
}

func TestConfiguredHiveCIPolicyResolvesCanonicalEntitiesAndBindsResult(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	state := &testHivePolicyState{}
	index := &testHivePolicyIndex{}
	repo := hiveciAdapter.NewCanonicalRepository(store, state, index, nil, zap.NewNop())
	orgID, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	view := testHivePolicyEntities{
		services:     []domain.Service{{ID: serviceID, OrgID: orgID, Name: "api"}},
		environments: []service.AdoptionEnvironment{{Environment: domain.Environment{ID: environmentID, OrgID: orgID, Name: "prod"}}},
	}
	policies := []config.HiveCIPolicyConfig{{
		RepoCoordinate: "30617:author:api", WorkflowPath: ".hive-ci/build.yml",
		ServiceName: "api", EnvironmentName: "prod", Metadata: map[string]any{"source": "fleet"},
	}}
	require.NoError(t, ensureConfiguredHiveCIPipelinePolicies(ctx, policies, view, repo))
	bound, err := repo.GetPolicyByRepoAndWorkflow(ctx, policies[0].RepoCoordinate, policies[0].WorkflowPath)
	require.NoError(t, err)
	require.NotNil(t, bound, "Bridge.ProcessResult must see the configured canonical policy")
	require.Equal(t, serviceID, bound.ServiceID)
	require.Equal(t, environmentID, bound.EnvironmentID)
	require.True(t, bound.Enabled)
	require.Equal(t, 1, state.publishes)
	require.Equal(t, 1, index.writes, "the SQL index is mirrored only after canonical publication")
	require.NoError(t, ensureConfiguredHiveCIPipelinePolicies(ctx, policies, view, repo))
	require.Equal(t, 1, state.publishes, "unchanged config must not re-sign a policy on restart")
	require.Equal(t, 1, index.writes)
}

func TestConfiguredHiveCIPolicyFailsClosedBeforePublishingOnMissingCanonicalEntity(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	state := &testHivePolicyState{}
	repo := hiveciAdapter.NewCanonicalRepository(store, state, nil, nil, zap.NewNop())
	view := testHivePolicyEntities{
		services:     []domain.Service{{ID: uuid.New(), Name: "api"}},
		environments: []service.AdoptionEnvironment{{Environment: domain.Environment{ID: uuid.New(), Name: "prod"}}},
	}
	policies := []config.HiveCIPolicyConfig{
		{RepoCoordinate: "repo-a", WorkflowPath: "build.yml", ServiceName: "api", EnvironmentName: "prod"},
		{RepoCoordinate: "repo-b", WorkflowPath: "build.yml", ServiceName: "missing", EnvironmentName: "prod"},
	}
	require.ErrorContains(t, ensureConfiguredHiveCIPipelinePolicies(ctx, policies, view, repo), "absent from canonical state")
	require.Zero(t, state.publishes, "one invalid config policy must not leave a partially seeded set")
}
