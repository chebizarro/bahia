package nostr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Adoption output is relay-canonical. These tests run the
// adoption service against a real local event store and the control-plane
// outbox the way app.go wires them, with no SQL repository at all unless a
// test adds one as the optional index.

// adoptionFamilies are the cp-state topics one adoption publishes, in
// publication order.
var adoptionFamilies = []string{
	kinds.CPStateTopicAdoptionBinding, kinds.CPStateTopicEnvironmentRegistry, kinds.CPStateTopicServiceRegistry,
	kinds.CPStateTopicBuildRegistry, kinds.CPStateTopicArtifactRegistry, kinds.CPStateTopicRuntimeObservation, kinds.CPStateTopicServiceState,
}

func newAdoptionLocalDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/demo-web-1"],"Image":"registry.example/web:1.2.3","ImageID":"sha256:image123","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-123/json":
			_, _ = w.Write([]byte(`{"Id":"container-123","Name":"/demo-web-1","Image":"sha256:image123",
				"Config":{"Image":"registry.example/web:1.2.3","Env":["APP_ENV=prod"],"Labels":{"com.docker.compose.project":"demo","com.docker.compose.service":"web"},"Cmd":["serve"],"WorkingDir":"/app"},
				"State":{"Status":"running","Health":{"Status":"healthy"}},
				"HostConfig":{"NetworkMode":"demo_default","RestartPolicy":{"Name":"unless-stopped"}},
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostPort":"8080"}]},"Networks":{"demo_default":{"Aliases":["web"]}}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:image123/json":
			_, _ = w.Write([]byte(`{"Id":"sha256:image123","RepoDigests":["registry.example/web@sha256:repo123"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

// memoryServiceIndex stands in for the optional SQL service index.
type memoryServiceIndex struct {
	mu   sync.Mutex
	rows map[uuid.UUID]domain.Service
	err  error
}

func newMemoryServiceIndex() *memoryServiceIndex {
	return &memoryServiceIndex{rows: map[uuid.UUID]domain.Service{}}
}

func (m *memoryServiceIndex) Create(_ context.Context, svc *domain.Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.rows[svc.ID] = *svc
	return nil
}
func (m *memoryServiceIndex) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if svc, ok := m.rows[id]; ok {
		return &svc, nil
	}
	return nil, nil
}
func (m *memoryServiceIndex) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, errors.New("adoption must not read the SQL index")
}
func (m *memoryServiceIndex) List(context.Context) ([]domain.Service, error) {
	return nil, errors.New("adoption must not read the SQL index")
}
func (m *memoryServiceIndex) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, errors.New("adoption must not read the SQL index")
}
func (m *memoryServiceIndex) Update(ctx context.Context, svc *domain.Service) error {
	return m.Create(ctx, svc)
}
func (m *memoryServiceIndex) Delete(context.Context, uuid.UUID) error { return nil }
func (m *memoryServiceIndex) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

// interruptedPublisher fails every publish once remaining successful ones
// have gone through, the way a crash between resources leaves the records
// before it published and the ones after it not.
type interruptedPublisher struct {
	service.AdoptionCanonicalPublisher
	mu        sync.Mutex
	remaining int
}

func (p *interruptedPublisher) admit() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.remaining <= 0 {
		return errors.New("daemon crashed")
	}
	p.remaining--
	return nil
}

func (p *interruptedPublisher) PublishAdoptionBinding(ctx context.Context, b *domain.AdoptionBinding) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishAdoptionBinding(ctx, b)
}
func (p *interruptedPublisher) PublishEnvironmentRegistry(ctx context.Context, env *domain.Environment, units []domain.DeploymentUnit) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishEnvironmentRegistry(ctx, env, units)
}
func (p *interruptedPublisher) PublishServiceRegistry(ctx context.Context, svc *domain.Service) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishServiceRegistry(ctx, svc)
}
func (p *interruptedPublisher) PublishBuildRegistry(ctx context.Context, b *domain.Build) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishBuildRegistry(ctx, b)
}
func (p *interruptedPublisher) PublishArtifactRegistry(ctx context.Context, a *domain.Artifact) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishArtifactRegistry(ctx, a)
}
func (p *interruptedPublisher) PublishRuntimeObservation(ctx context.Context, o *domain.RuntimeObservation) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishRuntimeObservation(ctx, o)
}
func (p *interruptedPublisher) PublishServiceState(ctx context.Context, s *domain.EnvironmentServiceState, o *domain.RuntimeObservation) error {
	if err := p.admit(); err != nil {
		return err
	}
	return p.AdoptionCanonicalPublisher.PublishServiceState(ctx, s, o)
}

func localAdoptionView(t *testing.T, d *localHistoryDaemon) *service.LocalAdoptionView {
	t.Helper()
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	state, err := service.NewLocalSupervisionState(d.store, servicePubkey)
	require.NoError(t, err)
	return service.NewLocalAdoptionView(state)
}

func localAdoptionService(t *testing.T, d *localHistoryDaemon, canonical service.AdoptionCanonicalPublisher, opts ...service.AdoptionServiceOption) *service.AdoptionService {
	t.Helper()
	if canonical == nil {
		canonical = NewAdoptionCanonicalPublisher(d.projector, &nonceConfidentialEncryptor{}, zap.NewNop())
	}
	return service.NewAdoptionService(canonical, localAdoptionView(t, d), nil, zap.NewNop(), opts...)
}

func adoptionImportRequest(server *httptest.Server, requestID string) service.AdoptionImportRequest {
	return service.AdoptionImportRequest{
		Targets:   []service.AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
		RequestID: requestID,
	}
}

// storedAdoptionEvents returns the daemon's retained events of one cp-state
// family, by its #t topic.
func storedAdoptionEvents(t *testing.T, store *localstore.Store, topic string) []nostr.Event {
	t.Helper()
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	var out []nostr.Event
	for ev := range store.QueryEvents(nostr.Filter{
		Kinds:   []nostr.Kind{KindCASControlState},
		Authors: []nostr.PubKey{nostr.MustPubKeyFromHex(servicePubkey)},
		Tags:    nostr.TagMap{"t": []string{topic}},
	}) {
		out = append(out, ev)
	}
	return out
}

func requireOneRecordPerAdoptionFamily(t *testing.T, store *localstore.Store) {
	t.Helper()
	for _, topic := range adoptionFamilies {
		require.Len(t, storedAdoptionEvents(t, store, topic), 1, "family %s", topic)
	}
}

func TestAdoptionCanonicalDBLessEndToEndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	server := newAdoptionLocalDockerServer(t)
	defer server.Close()

	first := startLocalHistoryDaemon(t, dir, script)
	results, err := localAdoptionService(t, first, nil).Import(ctx, adoptionImportRequest(server, "intent-1"))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "created", results[0].Status)
	require.Equal(t, "complete", results[0].Step)
	require.Empty(t, results[0].IndexError)
	requireOneRecordPerAdoptionFamily(t, first.store)
	// Eight signed events (the binding opens and closes the adoption), each
	// to both control-plane relays.
	require.Equal(t, 16, script.totalCalls())
	binding := storedAdoptionEvents(t, first.store, kinds.CPStateTopicAdoptionBinding)[0]
	require.Equal(t, "complete", tagValue(binding.Tags, "status"))
	require.Equal(t, AdoptionBindingDTag(*results[0].ServiceID, *results[0].EnvironmentID), tagValue(binding.Tags, "d"))
	first.close()

	// A restarted, SQL-less daemon plans from the reopened local store: the
	// adopted service and its binding are known, and re-processing the same
	// request converges onto the same coordinates without duplicates.
	restarted := startLocalHistoryDaemon(t, dir, script)
	view := localAdoptionView(t, restarted)
	services, err := view.ListServices(ctx)
	require.NoError(t, err)
	require.Len(t, services, 1)
	require.Equal(t, *results[0].ServiceID, services[0].ID)
	require.Equal(t, "demo-web", services[0].Name)
	bindings, err := view.ListAdoptionBindings(ctx)
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	require.Equal(t, domain.AdoptionBindingComplete, bindings[0].Status)
	require.Len(t, bindings[0].Fingerprints, 4)
	calls := script.totalCalls()
	replayed, err := localAdoptionService(t, restarted, nil).Import(ctx, adoptionImportRequest(server, "intent-1"))
	require.NoError(t, err)
	require.Equal(t, "created", replayed[0].Status, "the same request reports the outcome it had")
	require.Equal(t, *results[0].ServiceID, *replayed[0].ServiceID)
	require.Equal(t, *results[0].EnvironmentID, *replayed[0].EnvironmentID)
	require.Equal(t, *results[0].BuildID, *replayed[0].BuildID)
	require.Equal(t, *results[0].ArtifactID, *replayed[0].ArtifactID)
	requireOneRecordPerAdoptionFamily(t, restarted.store)
	// Only the binding's progress changes; every other record is unchanged
	// on its coordinate and is not signed again after the restart.
	require.Equal(t, calls+4, script.totalCalls(), "unchanged adoption records must not be re-signed after a restart")

	// A later request for the same workload records a new observation on the
	// same coordinate.
	later, err := localAdoptionService(t, restarted, nil).Import(ctx, adoptionImportRequest(server, "intent-2"))
	require.NoError(t, err)
	require.Equal(t, "updated", later[0].Status)
	require.Equal(t, *results[0].ServiceID, *later[0].ServiceID)
	requireOneRecordPerAdoptionFamily(t, restarted.store)
	obs, err := view.GetRuntimeObservation(ctx, *results[0].ServiceID, *results[0].EnvironmentID)
	require.NoError(t, err)
	require.Equal(t, "container-123", obs.ObservedContainerID)
}

func TestAdoptionCanonicalRejectedPublishIsReturnedAndNothingIsStored(t *testing.T) {
	ctx := context.Background()
	script := newRelayScript()
	script.reject[cpRelayA] = "blocked: maintenance"
	script.reject[cpRelayB] = "blocked: maintenance"
	server := newAdoptionLocalDockerServer(t)
	defer server.Close()
	daemon := startLocalHistoryDaemon(t, t.TempDir(), script)
	index := newMemoryServiceIndex()
	svc := localAdoptionService(t, daemon, nil, service.WithAdoptionIndex(service.AdoptionIndexRepositories{Services: index}))

	results, err := svc.Import(ctx, adoptionImportRequest(server, "intent-1"))
	require.ErrorIs(t, err, service.ErrAdoptionIncomplete)
	require.ErrorIs(t, err, ErrPublishAbandoned, "the relay rejection is the reason returned to the intent")
	require.Len(t, results, 1)
	require.True(t, results[0].Incomplete)
	require.Equal(t, "binding", results[0].Step)
	for _, topic := range adoptionFamilies {
		require.Empty(t, storedAdoptionEvents(t, daemon.store, topic), "a rejected record is not the daemon's output")
	}
	require.Zero(t, index.count(), "SQL must stay untouched when the canonical publish is rejected")

	script.mu.Lock()
	script.reject = map[string]string{}
	script.mu.Unlock()
	results, err = svc.Import(ctx, adoptionImportRequest(server, "intent-1"))
	require.NoError(t, err)
	require.Equal(t, "created", results[0].Status)
	requireOneRecordPerAdoptionFamily(t, daemon.store)
	require.Equal(t, 1, index.count())
}

// With every relay unreachable the signed records are still durable: they
// are held by the publish outbox and the local store before the index is
// written, and a failed index write does not fail the adoption.
func TestAdoptionCanonicalQueuedPublishIsDurableAndIndexIsRebuildable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	server := newAdoptionLocalDockerServer(t)
	defer server.Close()
	first := startLocalHistoryDaemon(t, dir, script)
	index := newMemoryServiceIndex()
	index.err = errors.New("SQL down")

	results, err := localAdoptionService(t, first, nil, service.WithAdoptionIndex(service.AdoptionIndexRepositories{Services: index})).Import(ctx, adoptionImportRequest(server, "intent-1"))
	require.NoError(t, err, "a queued publish and a failed index write are both non-fatal")
	require.Equal(t, "created", results[0].Status)
	require.Contains(t, results[0].IndexError, "SQL down")
	requireOneRecordPerAdoptionFamily(t, first.store)
	for _, topic := range adoptionFamilies {
		entry, held, err := first.outbox.Get(storedAdoptionEvents(t, first.store, topic)[0].ID)
		require.NoError(t, err)
		require.True(t, held, "the signed %s record is durably queued for relay delivery", topic)
		require.Equal(t, localstore.OutboxPending, entry.State)
	}
	require.Zero(t, index.count())
	first.close()

	// After a restart the index is rebuilt from the canonical records alone.
	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	restarted := startLocalHistoryDaemon(t, dir, script)
	rebuilt := newMemoryServiceIndex()
	require.NoError(t, localAdoptionService(t, restarted, nil, service.WithAdoptionIndex(service.AdoptionIndexRepositories{Services: rebuilt})).RebuildIndex(ctx))
	require.Equal(t, 1, rebuilt.count())
	row, err := rebuilt.GetByID(ctx, *results[0].ServiceID)
	require.NoError(t, err)
	require.Equal(t, "demo-web", row.Name)
	require.Equal(t, "demo-web-1", row.RuntimeConfig.Adopted.TargetName)
}

// A daemon that crashes between any two resources leaves the records before
// the crash published and the binding in progress; re-processing the same
// request after a restart completes the remainder on the same coordinates.
func TestAdoptionCanonicalCrashResumeConvergesAtEveryStepBoundary(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionLocalDockerServer(t)
	defer server.Close()

	baselineScript := newRelayScript()
	baseline := startLocalHistoryDaemon(t, t.TempDir(), baselineScript)
	baselineResults, err := localAdoptionService(t, baseline, nil).Import(ctx, adoptionImportRequest(server, "intent-1"))
	require.NoError(t, err)
	baselineBinding, err := localAdoptionView(t, baseline).ListAdoptionBindings(ctx)
	require.NoError(t, err)
	require.Len(t, baselineBinding, 1)

	// Eight publishes; a crash after 0..7 of them.
	for crashAfter := 1; crashAfter < 8; crashAfter++ {
		t.Run(fmt.Sprintf("crash_after_%d_publishes", crashAfter), func(t *testing.T) {
			dir := t.TempDir()
			script := newRelayScript()
			first := startLocalHistoryDaemon(t, dir, script)
			canonical := &interruptedPublisher{AdoptionCanonicalPublisher: NewAdoptionCanonicalPublisher(first.projector, &nonceConfidentialEncryptor{}, zap.NewNop()), remaining: crashAfter}
			results, err := localAdoptionService(t, first, canonical).Import(ctx, adoptionImportRequest(server, "intent-1"))
			require.ErrorIs(t, err, service.ErrAdoptionIncomplete)
			require.True(t, results[0].Incomplete)
			require.NotEqual(t, "complete", results[0].Step)
			bindings, err := localAdoptionView(t, first).ListAdoptionBindings(ctx)
			require.NoError(t, err)
			require.Len(t, bindings, 1, "the in-progress binding is published first and makes the partial adoption visible")
			require.Equal(t, domain.AdoptionBindingInProgress, bindings[0].Status)
			require.Equal(t, crashAfter*2, script.totalCalls())
			first.close()

			restarted := startLocalHistoryDaemon(t, dir, script)
			resumed, err := localAdoptionService(t, restarted, nil).Import(ctx, adoptionImportRequest(server, "intent-1"))
			require.NoError(t, err)
			require.Equal(t, "created", resumed[0].Status)
			require.Equal(t, "complete", resumed[0].Step)
			require.Equal(t, *baselineResults[0].ServiceID, *resumed[0].ServiceID)
			require.Equal(t, *baselineResults[0].EnvironmentID, *resumed[0].EnvironmentID)
			require.Equal(t, *baselineResults[0].BuildID, *resumed[0].BuildID)
			require.Equal(t, *baselineResults[0].ArtifactID, *resumed[0].ArtifactID)
			requireOneRecordPerAdoptionFamily(t, restarted.store)
			bindings, err = localAdoptionView(t, restarted).ListAdoptionBindings(ctx)
			require.NoError(t, err)
			require.Len(t, bindings, 1)
			require.Equal(t, domain.AdoptionBindingComplete, bindings[0].Status)
			require.Equal(t, baselineBinding[0].Fingerprints, bindings[0].Fingerprints)
			// Every record published before the crash, the in-progress
			// binding included, is unchanged on its coordinate and not signed
			// again: across both runs exactly the eight events of one
			// uninterrupted adoption reach the relays.
			require.Equal(t, 2*8, script.totalCalls(), "resume must not re-sign records that were already published")
		})
	}
}
