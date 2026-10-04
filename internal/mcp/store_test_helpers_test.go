package mcp

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type emptyMCPStateStore struct{}

type legacyMCPReadDeps struct {
	MLRegistry       *service.MLRegistryService
	WorkerReadModels *service.WorkerReadModelService
	BackupReadModels legacyBackupReadModelRepository
	Policies         *service.PolicyService
	Workers          repository.WorkerRepository
	Payments         *service.PaymentService
	DNSEndpoints     legacyDNSEndpointLister
}

var legacyMCPDeps sync.Map

func legacyFor(server *Server) *legacyMCPReadDeps {
	if value, ok := legacyMCPDeps.Load(server); ok {
		return value.(*legacyMCPReadDeps)
	}
	return &legacyMCPReadDeps{}
}

func newTestServerWithLegacyDeps(registry *service.RegistryService, logger *zap.Logger, deps legacyMCPReadDeps) *Server {
	server := newTestServerWithOptions(registry, logger, ServerDeps{})
	legacyMCPDeps.Store(server, &deps)
	return server
}

func (emptyMCPStateStore) QueryEvents(nostr.Filter) iter.Seq[nostr.Event] {
	return func(func(nostr.Event) bool) {}
}

// newTestServerWithOptions supplies a valid but empty local store for tests of
// writes, tool metadata, and repository-only families.
func newTestServerWithOptions(registry *service.RegistryService, logger *zap.Logger, deps ServerDeps) *Server {
	if deps.StateStore == nil {
		deps.StateStore = emptyMCPStateStore{}
		deps.ServicePubkey = nostr.Generate().Public().Hex()
	}
	server, err := NewServerWithOptionsChecked(registry, logger, deps)
	if err != nil {
		panic(err)
	}
	return server
}

func newTestServer(registry *service.RegistryService, logger *zap.Logger) *Server {
	return newTestServerWithOptions(registry, logger, ServerDeps{})
}

type canonicalMCPFixture struct {
	projector  *nostrpool.Projector
	sink       mcpProjectionStore
	store      *localstore.Store
	privateKey string
}

func attachCanonicalMCPFixture(t *testing.T, server *Server) canonicalMCPFixture {
	t.Helper()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	server.stateStore = store
	server.servicePubkey = sk.Public().Hex()
	return canonicalMCPFixture{projector: nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, server.registry, sink, nil, zap.NewNop()), sink: sink, store: store, privateKey: sk.Hex()}
}

func (f canonicalMCPFixture) publishService(t *testing.T, svc *domain.Service) {
	t.Helper()
	require.NoError(t, nostrpool.NewRelayFirstStatePublisher(f.projector, f.sink).PublishServiceRegistry(context.Background(), svc, false))
}

func (f canonicalMCPFixture) publishEnvironment(t *testing.T, env *domain.Environment) {
	t.Helper()
	require.NoError(t, nostrpool.NewRelayFirstStatePublisher(f.projector, f.sink).PublishEnvironmentRegistry(context.Background(), env, nil, false))
}

func (f canonicalMCPFixture) publishIntent(t *testing.T, intent *domain.DeploymentIntent) {
	t.Helper()
	require.NoError(t, nostrpool.NewRelayFirstStatePublisher(f.projector, f.sink).PublishDeploymentIntentRegistry(context.Background(), intent, false))
}

func (f canonicalMCPFixture) publishRun(t *testing.T, run *domain.DeploymentRun) {
	t.Helper()
	require.NoError(t, nostrpool.NewRelayFirstStatePublisher(f.projector, f.sink).PublishDeploymentRunRegistry(context.Background(), run, false))
}

func (f canonicalMCPFixture) publishBuild(t *testing.T, build *domain.Build) {
	t.Helper()
	if build.CreatedAt.IsZero() {
		build.CreatedAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	}
	require.NoError(t, nostrpool.NewRelayFirstStatePublisher(f.projector, f.sink).PublishBuildRegistry(context.Background(), build, false))
}

func (f canonicalMCPFixture) publishState(t *testing.T, state *domain.EnvironmentServiceState) {
	t.Helper()
	require.NoError(t, nostrpool.NewRelayFirstStatePublisher(f.projector, f.sink).PublishState(context.Background(), state, nil))
}

func (f canonicalMCPFixture) publishDNS(t *testing.T, endpoints ...domain.DNSEndpoint) {
	t.Helper()
	for i := range endpoints {
		if endpoints[i].Environment == "" {
			endpoints[i].Environment = "prod"
		}
		if endpoints[i].Zone == "" {
			_, endpoints[i].Zone, _ = strings.Cut(endpoints[i].FQDN, ".")
		}
		if endpoints[i].Address == "" {
			endpoints[i].Address = "192.0.2.1"
		}
		if endpoints[i].Source == "" {
			endpoints[i].Source = "test-fixture"
		}
	}
	_, _, err := nostrpool.NewDNSCanonicalPublisher(f.projector, zap.NewNop()).PublishEndpoints(context.Background(), endpoints)
	require.NoError(t, err)
}

func (f canonicalMCPFixture) publishWorker(t *testing.T, worker *domain.Worker) {
	t.Helper()
	signer, err := controlplane.NewPrivateKeySigner(f.privateKey)
	require.NoError(t, err)
	require.NoError(t, controlplane.NewWorkerStatePublisher(f.sink, signer).Publish(context.Background(), worker))
}

func (f canonicalMCPFixture) publishPolicy(t *testing.T, policy *domain.DeploymentPolicy) {
	t.Helper()
	recordTags, content := controlplane.PolicyRegistryRecord(policy, false)
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: append(nostr.Tags{{"d", policy.ID.String()}, {"domain", "policy"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", fmt.Sprint(nostrpool.KindPolicyRegistry)}, {"deleted", "false"}, {"t", kinds.CPStateTopicPolicyRegistry}}, recordTags...), Content: content}
	signer, err := controlplane.NewPrivateKeySigner(f.privateKey)
	require.NoError(t, err)
	require.NoError(t, controlplane.SignGoNostrEvent(context.Background(), signer, &event))
	_, err = f.store.SaveEvent(event)
	require.NoError(t, err)
}

func (f canonicalMCPFixture) notificationPublisher(t *testing.T, server *Server) *nostrpool.NotificationCanonicalPublisher {
	t.Helper()
	encryptor := f.confidentialEncryptor(t, server)
	return nostrpool.NewNotificationCanonicalPublisher(f.projector, encryptor, nil, zap.NewNop())
}

func (f canonicalMCPFixture) confidentialEncryptor(t *testing.T, server *Server) *controlplane.ConfidentialEncryptor {
	t.Helper()
	signer, err := keyer.New(context.Background(), nil, f.privateKey, nil)
	require.NoError(t, err)
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: signer, ServicePubkey: server.servicePubkey, Publisher: mcpKeyEnvelopeSink{}})
	encryptor := controlplane.NewConfidentialEncryptor(manager, zap.NewNop())
	server.confidentialReader = encryptor
	return encryptor
}

// legacyDNSEndpointLister exposes materialized DNS endpoints to the MCP server.
type legacyDNSEndpointLister interface {
	ListDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error)
}
