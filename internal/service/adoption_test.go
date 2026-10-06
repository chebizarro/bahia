package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sort"

	"github.com/google/uuid"
	runtimeAdapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// fakeAdoptionCanonical stands in for the relay-canonical record set: it is the
// publisher the adoption service publishes through and the view it plans from,
// keeping the latest record per coordinate exactly like the local event store.
// failAfter, when >= 0, fails the publish after that many successful ones, the
// way a relay rejection or a crash between resources would.
type fakeAdoptionCanonical struct {
	mu           sync.Mutex
	services     map[uuid.UUID]domain.Service
	environments map[uuid.UUID]AdoptionEnvironment
	builds       map[uuid.UUID]domain.Build
	artifacts    map[uuid.UUID]domain.Artifact
	states       map[string]domain.EnvironmentServiceState
	observations map[string]domain.RuntimeObservation
	bindings     map[string]domain.AdoptionBinding
	secretRefs   map[uuid.UUID]domain.SecretRef
	published    []string
	failAfter    int
	failErr      error
}

func newFakeAdoptionCanonical() *fakeAdoptionCanonical {
	return &fakeAdoptionCanonical{
		services:     map[uuid.UUID]domain.Service{},
		environments: map[uuid.UUID]AdoptionEnvironment{},
		builds:       map[uuid.UUID]domain.Build{},
		artifacts:    map[uuid.UUID]domain.Artifact{},
		states:       map[string]domain.EnvironmentServiceState{},
		observations: map[string]domain.RuntimeObservation{},
		bindings:     map[string]domain.AdoptionBinding{},
		secretRefs:   map[uuid.UUID]domain.SecretRef{},
		failAfter:    -1,
		failErr:      errors.New("relay rejected: blocked"),
	}
}

func (f *fakeAdoptionCanonical) failPublishesAfter(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAfter = n
}

func (f *fakeAdoptionCanonical) admit(step string) error {
	if f.failAfter >= 0 && len(f.published) >= f.failAfter {
		return f.failErr
	}
	f.published = append(f.published, step)
	return nil
}

func (f *fakeAdoptionCanonical) publishedSteps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.published...)
}

func (f *fakeAdoptionCanonical) PublishAdoptionBinding(_ context.Context, binding *domain.AdoptionBinding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("binding:" + binding.Status); err != nil {
		return err
	}
	copied := *binding
	copied.Fingerprints = copyStringMap(binding.Fingerprints)
	f.bindings[adoptionBindingKey(binding.ServiceID, binding.EnvironmentID)] = copied
	return nil
}

func (f *fakeAdoptionCanonical) PublishEnvironmentRegistry(_ context.Context, env *domain.Environment, units []domain.DeploymentUnit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("environment"); err != nil {
		return err
	}
	f.environments[env.ID] = AdoptionEnvironment{Environment: *env, Units: append([]domain.DeploymentUnit(nil), units...)}
	return nil
}

func (f *fakeAdoptionCanonical) PublishServiceRegistry(_ context.Context, svc *domain.Service) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("service"); err != nil {
		return err
	}
	f.services[svc.ID] = *svc
	return nil
}

func (f *fakeAdoptionCanonical) PublishBuildRegistry(_ context.Context, build *domain.Build) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("build"); err != nil {
		return err
	}
	f.builds[build.ID] = *build
	return nil
}

func (f *fakeAdoptionCanonical) PublishArtifactRegistry(_ context.Context, artifact *domain.Artifact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("artifact"); err != nil {
		return err
	}
	f.artifacts[artifact.ID] = *artifact
	return nil
}

func (f *fakeAdoptionCanonical) PublishSecretRef(_ context.Context, _ uuid.UUID, ref domain.SecretRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("secret"); err != nil {
		return err
	}
	f.secretRefs[ref.ID] = ref
	return nil
}

func (f *fakeAdoptionCanonical) PublishRuntimeObservation(_ context.Context, obs *domain.RuntimeObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("observation"); err != nil {
		return err
	}
	f.observations[stateKey(obs.ServiceID, obs.EnvironmentID)] = *obs
	return nil
}

func (f *fakeAdoptionCanonical) PublishServiceState(_ context.Context, state *domain.EnvironmentServiceState, _ *domain.RuntimeObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.admit("state"); err != nil {
		return err
	}
	f.states[stateKey(state.ServiceID, state.EnvironmentID)] = *state
	return nil
}

func (f *fakeAdoptionCanonical) ListServices(context.Context) ([]domain.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Service, 0, len(f.services))
	for _, svc := range f.services {
		out = append(out, svc)
	}
	return out, nil
}

func (f *fakeAdoptionCanonical) ListEnvironments(context.Context) ([]AdoptionEnvironment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AdoptionEnvironment, 0, len(f.environments))
	for _, env := range f.environments {
		out = append(out, env)
	}
	return out, nil
}

func (f *fakeAdoptionCanonical) GetBuild(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if build, ok := f.builds[id]; ok {
		return &build, nil
	}
	return nil, nil
}

func (f *fakeAdoptionCanonical) ListBuilds(context.Context) ([]domain.Build, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Build, 0, len(f.builds))
	for _, build := range f.builds {
		out = append(out, build)
	}
	return out, nil
}

func (f *fakeAdoptionCanonical) GetArtifact(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if artifact, ok := f.artifacts[id]; ok {
		return &artifact, nil
	}
	return nil, nil
}

func (f *fakeAdoptionCanonical) ListArtifacts(context.Context) ([]domain.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Artifact, 0, len(f.artifacts))
	for _, artifact := range f.artifacts {
		out = append(out, artifact)
	}
	return out, nil
}

func (f *fakeAdoptionCanonical) GetServiceState(_ context.Context, serviceID, environmentID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state, ok := f.states[stateKey(serviceID, environmentID)]; ok {
		return &state, nil
	}
	return nil, nil
}

func (f *fakeAdoptionCanonical) GetRuntimeObservation(_ context.Context, serviceID, environmentID uuid.UUID) (*domain.RuntimeObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if obs, ok := f.observations[stateKey(serviceID, environmentID)]; ok {
		return &obs, nil
	}
	return nil, nil
}

func (f *fakeAdoptionCanonical) ListAdoptionBindings(context.Context) ([]domain.AdoptionBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.AdoptionBinding, 0, len(f.bindings))
	for _, binding := range f.bindings {
		out = append(out, binding)
	}
	return out, nil
}

func (f *fakeAdoptionCanonical) serviceByName(name string) *domain.Service {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, svc := range f.services {
		if svc.Name == name {
			copied := svc
			return &copied
		}
	}
	return nil
}

func (f *fakeAdoptionCanonical) environmentByName(name string) *AdoptionEnvironment {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, env := range f.environments {
		if env.Environment.Name == name {
			copied := env
			return &copied
		}
	}
	return nil
}

func (f *fakeAdoptionCanonical) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]int{
		"services": len(f.services), "environments": len(f.environments), "builds": len(f.builds), "artifacts": len(f.artifacts),
		"states": len(f.states), "observations": len(f.observations), "bindings": len(f.bindings), "secrets": len(f.secretRefs),
	}
}

var _ AdoptionCanonicalPublisher = (*fakeAdoptionCanonical)(nil)
var _ AdoptionCanonicalView = (*fakeAdoptionCanonical)(nil)

// adoptionIndexFixture is the optional SQL index of a test, built from the
// registry mocks, with a transaction executor that records the write order.
type adoptionIndexFixture struct {
	services   *mockServiceRepo
	envs       *mockEnvRepo
	builds     *mockBuildRepo
	artifacts  *mockArtifactRepo
	units      *mockDeploymentUnitRepo
	state      *mockStateRepo
	obs        *mockObsRepo
	identities *mockAdoptedIdentityRepo
	tx         *recordingTxExecutor
}

// recordingTxExecutor hands the index repositories to the write function and
// counts transactions, so a test can tell whether the index was touched.
type recordingTxExecutor struct {
	mu    sync.Mutex
	repos repository.TxRepos
	calls int
}

func (e *recordingTxExecutor) WithinTx(_ context.Context, fn func(repository.TxRepos) error) error {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return fn(e.repos)
}

func (e *recordingTxExecutor) transactions() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func newAdoptionIndexFixture() *adoptionIndexFixture {
	f := &adoptionIndexFixture{
		services: newMockServiceRepo(), envs: newMockEnvRepo(), builds: newMockBuildRepo(), artifacts: newMockArtifactRepo(),
		units: &mockDeploymentUnitRepo{}, state: newMockStateRepo(), obs: newMockObsRepo(), identities: newMockAdoptedIdentityRepo(),
	}
	f.tx = &recordingTxExecutor{repos: repository.TxRepos{
		Services: f.services, Environments: f.envs, Builds: f.builds, Artifacts: f.artifacts,
		DeploymentUnits: f.units, State: f.state, Observations: f.obs, AdoptedIdentities: f.identities,
	}}
	return f
}

func (f *adoptionIndexFixture) option() AdoptionServiceOption {
	return WithAdoptionIndex(AdoptionIndexRepositories{
		Services: f.services, Environments: f.envs, Builds: f.builds, Artifacts: f.artifacts,
		DeploymentUnits: f.units, State: f.state, Observations: f.obs, AdoptedIdentities: f.identities, Tx: f.tx,
	})
}

func (f *adoptionIndexFixture) rowCounts() map[string]int {
	return map[string]int{
		"services": len(f.services.services), "environments": len(f.envs.envs), "builds": len(f.builds.builds), "artifacts": len(f.artifacts.artifacts),
		"units": len(f.units.units), "states": len(f.state.states), "observations": len(f.obs.observations), "identities": len(f.identities.identities),
	}
}

type mockDeploymentUnitRepo struct {
	units []*domain.DeploymentUnit
}

func (r *mockDeploymentUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	copied := *unit
	r.units = append(r.units, &copied)
	return nil
}

func (r *mockDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	for _, unit := range r.units {
		if unit.ID == id {
			return unit, nil
		}
	}
	return nil, nil
}

func (r *mockDeploymentUnitRepo) GetByEnvironmentKey(_ context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for _, unit := range r.units {
		if unit.EnvironmentID == environmentID && unit.Key == key {
			return unit, nil
		}
	}
	return nil, nil
}

func (r *mockDeploymentUnitRepo) ListByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	var out []domain.DeploymentUnit
	for _, unit := range r.units {
		if unit.EnvironmentID == environmentID {
			out = append(out, *unit)
		}
	}
	return out, nil
}

func (r *mockDeploymentUnitRepo) ResolveDefault(_ context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	return domain.NewImplicitDefaultDeploymentUnit(env)
}

func newTestAdoption(canonical *fakeAdoptionCanonical, publisher events.Publisher, opts ...AdoptionServiceOption) *AdoptionService {
	return NewAdoptionService(canonical, canonical, publisher, zap.NewNop(), opts...)
}

func localTarget(server *httptest.Server) AdoptionImportRequest {
	return AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
		RequestID: "intent-1",
	}
}

// adoptionPublishOrder is the canonical publication order of one candidate
// without secrets: the binding opens and closes it, and the activating state
// record is the last resource before the close.
var adoptionPublishOrder = []string{"binding:in_progress", "environment", "service", "build", "artifact", "observation", "state", "binding:complete"}

func TestAdoptionImportPublishesEveryResourceCanonicalFirstThenIndexes(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	index := newAdoptionIndexFixture()
	publisher := &capturePublisher{}
	adoption := newTestAdoption(canonical, publisher, index.option())

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated || results[0].Error != "" || results[0].IndexError != "" || results[0].Step != adoptionStepComplete {
		t.Fatalf("unexpected import result: %#v", results)
	}
	if got := canonical.publishedSteps(); strings.Join(got, ",") != strings.Join(adoptionPublishOrder, ",") {
		t.Fatalf("publish order = %v, want %v", got, adoptionPublishOrder)
	}
	if index.tx.transactions() != 1 {
		t.Fatalf("index transactions = %d, want exactly one after canonical publication", index.tx.transactions())
	}
	want := map[string]int{"services": 1, "environments": 1, "builds": 1, "artifacts": 1, "units": 1, "states": 1, "observations": 1, "identities": 4}
	if got := index.rowCounts(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("index rows = %v, want %v", got, want)
	}
	result := results[0]
	svc := canonical.services[*result.ServiceID]
	if svc.Name != "demo-web" || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil || svc.RuntimeConfig.Adopted.TargetName != "demo-web-1" || svc.RuntimeConfig.Adopted.Environment["APP_ENV"] != "prod" {
		t.Fatalf("unexpected canonical service: %#v", svc)
	}
	env := canonical.environments[*result.EnvironmentID]
	if env.Environment.Name != "prod" || env.Environment.RuntimeConfig["docker_host"] != server.URL || len(env.Units) != 1 || env.Units[0].Key != "demo-web" || env.Units[0].RuntimeType != domain.RuntimeTypeCompose {
		t.Fatalf("unexpected canonical environment: %#v", env)
	}
	state := canonical.states[stateKey(*result.ServiceID, *result.EnvironmentID)]
	if state.DesiredArtifactID == nil || *state.DesiredArtifactID != *result.ArtifactID || state.CurrentObservationID == nil || state.DeploymentUnitID == nil || *state.DeploymentUnitID != env.Units[0].ID {
		t.Fatalf("unexpected canonical state: %#v", state)
	}
	binding := canonical.bindings[adoptionBindingKey(*result.ServiceID, *result.EnvironmentID)]
	if binding.Status != domain.AdoptionBindingComplete || binding.BuildID == nil || *binding.BuildID != *result.BuildID || len(binding.Fingerprints) != 4 {
		t.Fatalf("unexpected canonical binding: %#v", binding)
	}
	if indexed := index.services.services[*result.ServiceID]; indexed == nil || indexed.Name != "demo-web" {
		t.Fatalf("service not indexed: %#v", index.services.services)
	}
	if indexed := index.state.states[stateKey(*result.ServiceID, *result.EnvironmentID)]; indexed == nil || indexed.CurrentObservationID == nil {
		t.Fatalf("state not indexed: %#v", index.state.states)
	}
	if !publisher.hasEvent(adoptionImportedEvent) {
		t.Fatalf("expected adoption.imported event, got %#v", publisher.events)
	}
	for _, kind := range []string{"container_id", "image_digest", "compose_coordinates", "endpoint_target"} {
		if index.identities.byKind(kind) == nil {
			t.Fatalf("missing indexed adopted runtime identity kind %q", kind)
		}
	}
}

func TestAdoptionImportCanonicalRecordsSurviveIndexFailure(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	index := newAdoptionIndexFixture()
	index.state.upsertErr = fmt.Errorf("simulated SQL failure")
	publisher := &capturePublisher{}
	adoption := newTestAdoption(canonical, publisher, index.option())

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated || !strings.Contains(results[0].IndexError, "simulated SQL failure") {
		t.Fatalf("expected a created result with an index warning, got %#v", results)
	}
	if got := canonical.publishedSteps(); len(got) != len(adoptionPublishOrder) {
		t.Fatalf("canonical records must be complete although the index failed: %v", got)
	}
	if len(index.state.states) != 0 {
		t.Fatalf("failed index write must not leave state rows: %#v", index.state.states)
	}
	if !publisher.hasEvent(adoptionImportedEvent) {
		t.Fatalf("expected adoption.imported event after canonical completion, got %#v", publisher.events)
	}
	// The index is repaired from the canonical records.
	index.state.upsertErr = nil
	if err := adoption.RebuildIndex(ctx); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if len(index.state.states) != 1 {
		t.Fatalf("rebuilt index lacks the state row: %#v", index.state.states)
	}
}

func TestAdoptionImportRejectedPublishLeavesIndexUntouchedAndIsReturned(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	canonical.failPublishesAfter(0)
	index := newAdoptionIndexFixture()
	publisher := &capturePublisher{}
	adoption := newTestAdoption(canonical, publisher, index.option())

	results, err := adoption.Import(ctx, localTarget(server))
	if !errors.Is(err, ErrAdoptionIncomplete) || !strings.Contains(err.Error(), "relay rejected") {
		t.Fatalf("Import error = %v, want ErrAdoptionIncomplete with the publish reason", err)
	}
	if len(results) != 1 || !results[0].Incomplete || results[0].Status != adoptionStatusFailed || results[0].Step != adoptionStepBinding || !strings.Contains(results[0].Error, "relay rejected") {
		t.Fatalf("unexpected result: %#v", results)
	}
	if index.tx.transactions() != 0 {
		t.Fatalf("SQL index must stay untouched when the canonical publish is rejected, got %d transactions", index.tx.transactions())
	}
	if publisher.hasEvent(adoptionImportedEvent) {
		t.Fatalf("rejected adoption must not announce an import: %#v", publisher.events)
	}
	if n := canonical.counts(); n["services"] != 0 || n["bindings"] != 0 {
		t.Fatalf("rejected publish left records: %v", n)
	}
}

func TestAdoptionImportResumesAtEveryStepBoundaryAndConverges(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	baseline := newFakeAdoptionCanonical()
	baselineResults, err := newTestAdoption(baseline, &events.NoopPublisher{}).Import(ctx, localTarget(server))
	if err != nil || len(baselineResults) != 1 || baselineResults[0].Status != adoptionStatusCreated {
		t.Fatalf("baseline import: results=%#v err=%v", baselineResults, err)
	}
	baselineCounts := fmt.Sprint(baseline.counts())

	for crashAfter := 0; crashAfter < len(adoptionPublishOrder); crashAfter++ {
		t.Run(fmt.Sprintf("crash_after_%d_%s", crashAfter, strings.ReplaceAll(adoptionPublishOrder[max(crashAfter-1, 0)], ":", "_")), func(t *testing.T) {
			canonical := newFakeAdoptionCanonical()
			canonical.failPublishesAfter(crashAfter)
			index := newAdoptionIndexFixture()
			adoption := newTestAdoption(canonical, &events.NoopPublisher{}, index.option())

			interrupted, err := adoption.Import(ctx, localTarget(server))
			if !errors.Is(err, ErrAdoptionIncomplete) {
				t.Fatalf("interrupted import error = %v", err)
			}
			if len(interrupted) != 1 || !interrupted[0].Incomplete || interrupted[0].Step == adoptionStepComplete {
				t.Fatalf("unexpected interrupted result: %#v", interrupted)
			}
			if index.tx.transactions() != 0 {
				t.Fatalf("an interrupted adoption must not be indexed")
			}
			if crashAfter > 0 {
				binding := canonical.bindings[adoptionBindingKey(*interrupted[0].ServiceID, *interrupted[0].EnvironmentID)]
				if binding.Status != domain.AdoptionBindingInProgress {
					t.Fatalf("partial adoption must be visible as an in-progress binding: %#v", binding)
				}
			}

			// Re-processing the same request completes the remainder onto the
			// same coordinates.
			canonical.failPublishesAfter(-1)
			resumed, err := adoption.Import(ctx, localTarget(server))
			if err != nil {
				t.Fatalf("resumed import error = %v", err)
			}
			if len(resumed) != 1 || resumed[0].Status != adoptionStatusCreated || resumed[0].Step != adoptionStepComplete || resumed[0].IndexError != "" {
				t.Fatalf("unexpected resumed result: %#v", resumed)
			}
			if *resumed[0].ServiceID != *baselineResults[0].ServiceID || *resumed[0].EnvironmentID != *baselineResults[0].EnvironmentID ||
				*resumed[0].BuildID != *baselineResults[0].BuildID || *resumed[0].ArtifactID != *baselineResults[0].ArtifactID {
				t.Fatalf("resumed adoption minted different ids: resumed=%#v baseline=%#v", resumed[0], baselineResults[0])
			}
			if got := fmt.Sprint(canonical.counts()); got != baselineCounts {
				t.Fatalf("resumed canonical record set = %s, want %s (no duplicates, no orphans)", got, baselineCounts)
			}
			if binding := canonical.bindings[adoptionBindingKey(*resumed[0].ServiceID, *resumed[0].EnvironmentID)]; binding.Status != domain.AdoptionBindingComplete {
				t.Fatalf("resumed binding = %#v", binding)
			}
			if index.tx.transactions() != 1 || len(index.services.services) != 1 || len(index.state.states) != 1 {
				t.Fatalf("resumed adoption must index once: tx=%d rows=%v", index.tx.transactions(), index.rowCounts())
			}
		})
	}
}

func TestAdoptionImportSameRequestAfterSuccessConvergesWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	index := newAdoptionIndexFixture()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{}, index.option())

	first, err := adoption.Import(ctx, localTarget(server))
	if err != nil || first[0].Status != adoptionStatusCreated {
		t.Fatalf("first import: %#v %v", first, err)
	}
	firstObs := canonical.observations[stateKey(*first[0].ServiceID, *first[0].EnvironmentID)]
	// The same request reports the outcome it had: it created the service.
	second, err := adoption.Import(ctx, localTarget(server))
	if err != nil || second[0].Status != adoptionStatusCreated {
		t.Fatalf("second import: %#v %v", second, err)
	}
	if *first[0].ServiceID != *second[0].ServiceID || *first[0].BuildID != *second[0].BuildID || *first[0].ArtifactID != *second[0].ArtifactID {
		t.Fatalf("re-delivered request changed ids: %#v vs %#v", first[0], second[0])
	}
	if n := canonical.counts(); n["services"] != 1 || n["environments"] != 1 || n["builds"] != 1 || n["artifacts"] != 1 || n["states"] != 1 || n["observations"] != 1 || n["bindings"] != 1 {
		t.Fatalf("re-delivery duplicated canonical records: %v", n)
	}
	secondObs := canonical.observations[stateKey(*first[0].ServiceID, *first[0].EnvironmentID)]
	if firstObs.ID != secondObs.ID {
		t.Fatalf("same request must address the same observation coordinate: %s vs %s", firstObs.ID, secondObs.ID)
	}
	if rows := index.rowCounts(); rows["services"] != 1 || rows["builds"] != 1 || rows["artifacts"] != 1 || rows["observations"] != 1 || rows["units"] != 1 {
		t.Fatalf("index diverged on re-delivery: %v", rows)
	}
	svc := canonical.serviceByName("demo-web")
	if svc == nil || svc.RuntimeConfig.Adopted.ContainerID != "container-123" {
		t.Fatalf("expected adopted service, got %#v", svc)
	}
	for _, build := range canonical.builds {
		if build.CIRunID != "local:demo-web-1:sha256:repo123" {
			t.Fatalf("stable adoption run id = %q", build.CIRunID)
		}
	}
	// A different request observes again, on the same coordinate.
	third := localTarget(server)
	third.RequestID = "intent-2"
	results, err := adoption.Import(ctx, third)
	if err != nil || results[0].Status != adoptionStatusUpdated {
		t.Fatalf("third import: %#v %v", results, err)
	}
	if canonical.observations[stateKey(*first[0].ServiceID, *first[0].EnvironmentID)].ID == firstObs.ID {
		t.Fatal("a new request must record a new observation")
	}
	if len(index.obs.observations) != 2 {
		t.Fatalf("expected two indexed observations, got %d", len(index.obs.observations))
	}
}

func TestAdoptionRebuildIndexReproducesAdoptedResourcesFromCanonicalRecords(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	// A DB-less adoption: no index at all.
	results, err := newTestAdoption(canonical, &events.NoopPublisher{}).Import(ctx, localTarget(server))
	if err != nil || len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("DB-less import: %#v %v", results, err)
	}

	index := newAdoptionIndexFixture()
	indexed := newTestAdoption(canonical, &events.NoopPublisher{}, index.option())
	if err := indexed.RebuildIndex(ctx); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	want := map[string]int{"services": 1, "environments": 1, "builds": 1, "artifacts": 1, "units": 1, "states": 1, "observations": 1, "identities": 4}
	if got := index.rowCounts(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rebuilt index rows = %v, want %v", got, want)
	}
	svc, _ := index.services.GetByName(ctx, "demo-web")
	if svc == nil || svc.ID != *results[0].ServiceID || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		t.Fatalf("rebuilt service = %#v", svc)
	}
	env, _ := index.envs.GetByName(ctx, "prod")
	if env == nil || env.ID != *results[0].EnvironmentID {
		t.Fatalf("rebuilt environment = %#v", env)
	}
	if build := index.builds.builds[*results[0].BuildID]; build == nil || build.CISystem != adoptionCISystem {
		t.Fatalf("rebuilt build = %#v", build)
	}
	if artifact := index.artifacts.artifacts[*results[0].ArtifactID]; artifact == nil || artifact.ImageDigest != "sha256:repo123" {
		t.Fatalf("rebuilt artifact = %#v", artifact)
	}
	if state := index.state.states[stateKey(*results[0].ServiceID, *results[0].EnvironmentID)]; state == nil || *state.DesiredArtifactID != *results[0].ArtifactID {
		t.Fatalf("rebuilt state = %#v", state)
	}
	if err := indexed.RebuildIndex(ctx); err != nil {
		t.Fatalf("second RebuildIndex: %v", err)
	}
	if got := index.rowCounts(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("repeated rebuild changed rows: %v", got)
	}
}

func TestAdoptionRebuildIndexSkipsIncompleteBindings(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	canonical.failPublishesAfter(5) // after the artifact: observation never published
	if _, err := newTestAdoption(canonical, &events.NoopPublisher{}).Import(ctx, localTarget(server)); !errors.Is(err, ErrAdoptionIncomplete) {
		t.Fatalf("expected incomplete import, got %v", err)
	}
	index := newAdoptionIndexFixture()
	if err := newTestAdoption(canonical, &events.NoopPublisher{}, index.option()).RebuildIndex(ctx); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if index.tx.transactions() != 0 {
		t.Fatalf("an in-progress adoption must not be indexed as adopted: %v", index.rowCounts())
	}
}

type listingIdentityRepo struct {
	*mockAdoptedIdentityRepo
}

func (r listingIdentityRepo) List(_ context.Context, limit, offset int) ([]domain.AdoptedRuntimeIdentity, error) {
	keys := make([]string, 0, len(r.identities))
	for key := range r.identities {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var out []domain.AdoptedRuntimeIdentity
	for i := offset; i < len(keys) && len(out) < limit; i++ {
		out = append(out, r.identities[keys[i]])
	}
	return out, nil
}

type memoryControlRecords struct {
	mu      sync.Mutex
	records map[string][]byte
}

func (m *memoryControlRecords) GetControlRecord(family, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.records[family+"/"+id], nil
}

func (m *memoryControlRecords) PutControlRecord(family, id string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records == nil {
		m.records = map[string][]byte{}
	}
	m.records[family+"/"+id] = value
	return nil
}

func TestAdoptionBackfillPublishesSQLEraIdentitiesAsBindingsOnce(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, envID := uuid.New(), uuid.New(), uuid.New()
	identities := newMockAdoptedIdentityRepo()
	target := AdoptionTarget{Name: "local", EndpointRef: "prod-docker", EnvironmentName: "prod"}
	discovered := adoptionDiscoveredFixture()
	for kind, fingerprint := range adoptedRuntimeFingerprintsByKind(target, discovered) {
		_ = identities.UpsertMany(ctx, []domain.AdoptedRuntimeIdentity{{OrgID: orgID, ServiceID: serviceID, EnvironmentID: envID, FingerprintKind: kind, Fingerprint: fingerprint, ContainerID: discovered.ContainerID, ImageDigest: discovered.ImageDigest, EndpointRef: target.EndpointRef, HostAlias: target.Name, TargetName: discovered.TargetName}})
	}
	canonical := newFakeAdoptionCanonical()
	canonical.services[serviceID] = domain.Service{ID: serviceID, OrgID: orgID, Name: "demo-web", RuntimeType: domain.RuntimeTypeDocker, RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: adoptedRuntimeConfig(target, discovered, classifyDiscoveredSensitiveData(discovered))}}
	sqlEraEnv := domain.Environment{ID: envID, OrgID: orgID, Name: "prod", RuntimeConfig: map[string]any{"type": "docker", "host_alias": "local", "management_mode": "direct_runtime", "endpoint_ref": "prod-docker"}}
	domain.NormalizeEnvironmentTargeting(&sqlEraEnv)
	canonical.environments[envID] = AdoptionEnvironment{Environment: sqlEraEnv}
	adoption := newTestAdoption(canonical, &events.NoopPublisher{}, WithAdoptionIndex(AdoptionIndexRepositories{AdoptedIdentities: listingIdentityRepo{identities}}))
	marker := &memoryControlRecords{}

	if err := adoption.BackfillFromIndex(ctx, marker); err != nil {
		t.Fatalf("BackfillFromIndex: %v", err)
	}
	binding, ok := canonical.bindings[adoptionBindingKey(serviceID, envID)]
	if !ok || binding.Status != domain.AdoptionBindingComplete || len(binding.Fingerprints) != 4 || binding.OrgID != orgID || binding.BuildID != nil {
		t.Fatalf("unexpected backfilled binding: %#v", binding)
	}
	if err := adoption.BackfillFromIndex(ctx, marker); err != nil {
		t.Fatalf("second BackfillFromIndex: %v", err)
	}
	if got := canonical.publishedSteps(); len(got) != 1 {
		t.Fatalf("backfill must publish once, got %v", got)
	}
	// The backfilled binding now identifies the workload without SQL: a new
	// import of the same container under another name updates that service.
	server := newAdoptionDockerServer(t)
	defer server.Close()
	req := AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", EndpointRef: "prod-docker", EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123", ServiceNameOverride: "renamed"}},
		OrgID:      orgID,
		RequestID:  "intent-3",
	}
	resolver := newTestAdoption(canonical, &events.NoopPublisher{}, WithAdoptionRuntimeConfig(config.RuntimeConfig{Endpoints: map[string]config.RuntimeEndpointConfig{"prod-docker": {DockerHost: server.URL}}}, false))
	results, err := resolver.Import(ctx, req)
	if err != nil || len(results) != 1 || results[0].Status != adoptionStatusUpdated || *results[0].ServiceID != serviceID || results[0].ServiceName != "demo-web" {
		t.Fatalf("expected the backfilled identity to resolve the service: %#v %v", results, err)
	}
}

func TestAdoptionImportRefusesAmbiguousIdentityEvidence(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	orgID := uuid.New()
	target := AdoptionTarget{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}
	discovered := adoptionDiscoveredFixture()
	fingerprints := adoptedRuntimeFingerprintsByKind(target, discovered)
	for _, name := range []string{"one", "two"} {
		serviceID, envID := uuid.New(), uuid.New()
		canonical.services[serviceID] = domain.Service{ID: serviceID, OrgID: orgID, Name: name, RuntimeType: domain.RuntimeTypeDocker}
		canonical.bindings[adoptionBindingKey(serviceID, envID)] = domain.AdoptionBinding{OrgID: orgID, ServiceID: serviceID, EnvironmentID: envID, Fingerprints: copyStringMap(fingerprints), Status: domain.AdoptionBindingComplete}
	}
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})
	req := localTarget(server)
	req.OrgID = orgID
	results, err := adoption.Import(ctx, req)
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || results[0].Incomplete || !strings.Contains(results[0].Error, "matches multiple services") {
		t.Fatalf("expected ambiguous identity refusal, got %#v", results)
	}
	if got := canonical.publishedSteps(); len(got) != 0 {
		t.Fatalf("a refusal must publish nothing, got %v", got)
	}
}

// adoptionDiscoveredFixture is the container newAdoptionDockerServer serves,
// as discovery reports it.
func adoptionDiscoveredFixture() runtimeDiscoveredContainer {
	return runtimeDiscoveredContainer{
		TargetName: "demo-web-1", ContainerID: "container-123", ContainerName: "demo-web-1",
		ImageRepo: "registry.example/web", ImageDigest: "sha256:repo123", ImageTag: "1.2.3", SourceRuntime: "compose",
		Compose: &domain.ComposeMetadata{ProjectName: "demo", ServiceName: "web"},
	}
}

func TestAdoptionServiceImportInfersSingleOrg(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	orgID := uuid.New()
	orgRepo := &mockOrgRepo{orgs: []domain.Organization{{ID: orgID, Name: "platform", DisplayName: "Platform"}}}
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{}, WithAdoptionOrganizations(orgRepo))

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil || len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v err=%v", results, err)
	}
	if svc := canonical.serviceByName("demo-web"); svc == nil || svc.OrgID != orgID {
		t.Fatalf("service org = %#v, want %s", svc, orgID)
	}
	if env := canonical.environmentByName("prod"); env == nil || env.Environment.OrgID != orgID {
		t.Fatalf("environment org = %#v, want %s", env, orgID)
	}
	if binding := canonical.bindings[adoptionBindingKey(*results[0].ServiceID, *results[0].EnvironmentID)]; binding.OrgID != orgID {
		t.Fatalf("binding org = %s", binding.OrgID)
	}
}

func TestAdoptionServiceImportRequiresOrgWhenMultipleOrgsAreAvailable(t *testing.T) {
	orgRepo := &mockOrgRepo{orgs: []domain.Organization{
		{ID: uuid.New(), Name: "platform-a", DisplayName: "Platform A"},
		{ID: uuid.New(), Name: "platform-b", DisplayName: "Platform B"},
	}}
	adoption := newTestAdoption(newFakeAdoptionCanonical(), &events.NoopPublisher{}, WithAdoptionOrganizations(orgRepo))
	_, err := adoption.Import(context.Background(), AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: "http://docker.example", EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires org_id") {
		t.Fatalf("Import error = %v, want org_id requirement", err)
	}
}

func TestAdoptionServiceRequiresCanonicalPublisherAndView(t *testing.T) {
	err := NewAdoptionService(nil, nil, nil, nil).Ready()
	if err == nil || !strings.Contains(err.Error(), "canonical publisher") {
		t.Fatalf("Ready = %v, want missing publisher", err)
	}
	if _, err := NewAdoptionService(nil, nil, nil, nil).Import(context.Background(), AdoptionImportRequest{}); err == nil {
		t.Fatal("Import without a canonical publisher must fail closed")
	}
}

func TestAdoptionServiceComposeTakeoverPolicyWarningsAndBlocksImport(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{}, WithAdoptionComposeTakeoverPolicy(false))

	previews, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}}})
	if err != nil || len(previews) != 1 || len(previews[0].Containers) != 1 {
		t.Fatalf("unexpected previews: %#v err=%v", previews, err)
	}
	container := previews[0].Containers[0]
	if container.Adoptable || !containsString(container.Warnings, "compose takeover is disabled by adoption policy") {
		t.Fatalf("expected compose takeover block, got %#v", container)
	}
	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil || len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "unsupported adoption warnings") {
		t.Fatalf("expected blocked import, got %#v err=%v", results, err)
	}
	if got := canonical.publishedSteps(); len(got) != 0 {
		t.Fatalf("blocked import must publish nothing: %v", got)
	}
}

func TestAdoptionServiceScanRedactsSensitiveEnvironmentAndLabels(t *testing.T) {
	ctx := context.Background()
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()
	adoption := newTestAdoption(newFakeAdoptionCanonical(), &events.NoopPublisher{})

	previews, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}}})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(previews) != 1 || len(previews[0].Containers) != 1 {
		t.Fatalf("unexpected previews: %#v", previews)
	}
	container := previews[0].Containers[0]
	if got := container.SafeEnvironment["APP_ENV"]; got != "prod" {
		t.Fatalf("safe env APP_ENV = %q", got)
	}
	for _, key := range []string{"DB_PASSWORD", "MINT_LND_REST_MACAROON", "FLEET_CURATOR_BUNKER_URL"} {
		if _, ok := container.SafeEnvironment[key]; ok {
			t.Fatalf("sensitive env %s leaked into safe preview environment", key)
		}
	}
	for _, key := range []string{"DB_PASSWORD", "AWS_SECRET_ACCESS_KEY", "DATABASE_URL", "MINT_LND_REST_MACAROON", "FLEET_CURATOR_BUNKER_URL"} {
		if !containsString(container.RedactedEnvironmentKeys, key) {
			t.Fatalf("missing redacted env key %s: %#v", key, container.RedactedEnvironmentKeys)
		}
	}
	if _, ok := container.SafeLabels["com.example.secret-token"]; ok {
		t.Fatal("sensitive label leaked into safe preview labels")
	}
	if !containsString(container.RedactedLabelKeys, "com.example.secret-token") {
		t.Fatalf("missing redacted label keys: %#v", container.RedactedLabelKeys)
	}
}

func TestAdoptionServiceImportStoresSensitiveEnvironmentAsSecretsAndPublishesRefs(t *testing.T) {
	ctx := context.Background()
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	secretRepo := newMockSecretRepo()
	encryptor, err := secretsAdapter.NewEncryptor("4444444444444444444444444444444444444444444444444444444444444444")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	adoption := newTestAdoption(canonical, &events.NoopPublisher{}, WithAdoptionSecrets(secretRepo, encryptor))

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v", results)
	}
	if !containsString(results[0].RedactedEnvironmentKeys, "DB_PASSWORD") {
		t.Fatalf("import result missing redacted env keys: %#v", results[0].RedactedEnvironmentKeys)
	}
	svc := canonical.serviceByName("secret-app")
	if svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		t.Fatalf("expected adopted service, svc=%#v", svc)
	}
	if svc.RuntimeConfig.Adopted.Environment["APP_ENV"] != "prod" {
		t.Fatalf("safe env not retained: %#v", svc.RuntimeConfig.Adopted.Environment)
	}
	if _, ok := svc.RuntimeConfig.Adopted.Environment["DB_PASSWORD"]; ok {
		t.Fatalf("sensitive env published in runtime config: %#v", svc.RuntimeConfig.Adopted.Environment)
	}
	if _, ok := svc.RuntimeConfig.Adopted.Labels["com.example.secret-token"]; ok {
		t.Fatalf("sensitive label published in adopted labels: %#v", svc.RuntimeConfig.Adopted.Labels)
	}
	secrets, err := secretRepo.ListEffective(ctx, svc.ID, *results[0].EnvironmentID)
	if err != nil {
		t.Fatalf("ListEffective secrets: %v", err)
	}
	secretValues := map[string]string{}
	for _, secret := range secrets {
		value, err := encryptor.Decrypt(secret.EncryptedValue, secret.EncryptionMethod)
		if err != nil {
			t.Fatalf("decrypt secret %q: %v", secret.Name, err)
		}
		secretValues[secret.Name] = value
		ref, ok := canonical.secretRefs[secret.ID]
		if !ok || ref.Name != secret.Name || ref.ServiceID != svc.ID {
			t.Fatalf("secret %q has no canonical reference: %#v", secret.Name, canonical.secretRefs)
		}
	}
	if secretValues["DB_PASSWORD"] != "super-secret" || secretValues["AWS_SECRET_ACCESS_KEY"] != "aws-secret" || secretValues["DATABASE_URL"] != "postgres://user:pass@db/prod" {
		t.Fatalf("unexpected imported secret values: %#v", secretValues)
	}
	steps := canonical.publishedSteps()
	if steps[4] != "artifact" || steps[5] != "secret" || steps[len(steps)-3] != "observation" {
		t.Fatalf("secret references must be published after the artifact and before the observation: %v", steps)
	}
}

func TestAdoptionServiceImportRejectsSensitiveEnvironmentWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "secret storage") {
		t.Fatalf("expected sensitive import failure, got %#v", results)
	}
	if n := canonical.counts(); n["services"] != 0 || n["environments"] != 0 {
		t.Fatalf("sensitive import without secrets must publish nothing: %v", n)
	}
}

func TestAdoptionServiceUsesManagedEndpointRefAndDoesNotPublishDockerHost(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{}, WithAdoptionRuntimeConfig(config.RuntimeConfig{
		Endpoints: map[string]config.RuntimeEndpointConfig{"prod-docker": {DockerHost: server.URL}},
	}, false))

	results, err := adoption.Import(ctx, AdoptionImportRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}, ImportAll: true})
	if err != nil || len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v err=%v", results, err)
	}
	env := canonical.environmentByName("prod")
	if env == nil || env.Environment.RuntimeConfig["endpoint_ref"] != "prod-docker" {
		t.Fatalf("endpoint_ref not published: %#v", env)
	}
	if _, ok := env.Environment.RuntimeConfig["docker_host"]; ok {
		t.Fatalf("managed endpoint import published docker_host: %#v", env.Environment.RuntimeConfig)
	}
	svc := canonical.serviceByName("demo-web")
	if svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil || svc.RuntimeConfig.Adopted.EndpointRef != "prod-docker" {
		t.Fatalf("adopted endpoint_ref = %#v", svc)
	}
	if env.Units[0].EndpointRef != "prod-docker" {
		t.Fatalf("deployment unit endpoint_ref = %#v", env.Units[0])
	}
}

func TestAdoptionServiceRejectsRawDockerHostWhenPolicyDisabled(t *testing.T) {
	adoption := newTestAdoption(newFakeAdoptionCanonical(), &events.NoopPublisher{}, WithAdoptionRuntimeConfig(config.RuntimeConfig{Endpoints: map[string]config.RuntimeEndpointConfig{}}, false))
	_, err := adoption.Scan(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: "unix:///docker.sock"}}})
	if err == nil || !strings.Contains(err.Error(), "raw docker_host targets are disabled") {
		t.Fatalf("Scan error = %v, want raw host policy rejection", err)
	}
}

func TestAdoptionServiceImportUsesExistingAdoptedIdentityDespiteNewOverride(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})

	first, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123", ServiceNameOverride: "first-name"}},
		RequestID:  "intent-1",
	})
	if err != nil || len(first) != 1 || first[0].Status != adoptionStatusCreated {
		t.Fatalf("first import failed: results=%#v err=%v", first, err)
	}
	second, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123", ServiceNameOverride: "second-name"}},
		RequestID:  "intent-2",
	})
	if err != nil || len(second) != 1 || second[0].Status != adoptionStatusUpdated {
		t.Fatalf("second import failed: results=%#v err=%v", second, err)
	}
	if second[0].ServiceName != "first-name" || *second[0].ServiceID != *first[0].ServiceID {
		t.Fatalf("expected re-import to update the bound service, first=%#v second=%#v", first[0], second[0])
	}
	if n := canonical.counts(); n["services"] != 1 {
		t.Fatalf("expected one service after re-import, got %v", n)
	}
}

func TestAdoptionServiceImportRejectsForeignArtifactDigest(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	foreign := domain.Artifact{ID: uuid.New(), BuildID: uuid.New(), ServiceID: uuid.New(), ImageRepo: "registry.example/web", ImageTag: "other", ImageDigest: "sha256:repo123", ScanStatus: domain.ScanStatusUnknown}
	canonical.artifacts[foreign.ID] = foreign
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "already belongs to service") {
		t.Fatalf("expected foreign artifact failure, got %#v", results)
	}
	if got := canonical.publishedSteps(); len(got) != 0 {
		t.Fatalf("refusal must publish nothing: %v", got)
	}
}

func TestAdoptionServiceImportRejectsIncompatibleExistingEnvironment(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	envID := uuid.New()
	canonical.environments[envID] = AdoptionEnvironment{Environment: domain.Environment{ID: envID, Name: "prod", RuntimeConfig: map[string]any{"type": "compose", "compose_dir": "/srv/app"}}}
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "incompatible runtime type") {
		t.Fatalf("expected incompatible environment failure, got %#v", results)
	}
	if got := canonical.publishedSteps(); len(got) != 0 {
		t.Fatalf("refusal must publish nothing: %v", got)
	}
}

func TestAdoptionServiceImportReusesExistingEnvironmentAndUnits(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	canonical := newFakeAdoptionCanonical()
	envID, otherUnit := uuid.New(), uuid.New()
	existing := domain.Environment{ID: envID, Name: "prod", RuntimeConfig: map[string]any{}}
	domain.NormalizeEnvironmentTargeting(&existing)
	canonical.environments[envID] = AdoptionEnvironment{Environment: existing, Units: []domain.DeploymentUnit{{ID: otherUnit, EnvironmentID: envID, Key: "other", RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged}}}
	adoption := newTestAdoption(canonical, &events.NoopPublisher{})

	results, err := adoption.Import(ctx, localTarget(server))
	if err != nil || len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v err=%v", results, err)
	}
	if *results[0].EnvironmentID != envID {
		t.Fatalf("existing environment must be reused, got %s", *results[0].EnvironmentID)
	}
	env := canonical.environments[envID]
	if len(env.Units) != 2 || env.Units[0].ID != otherUnit || env.Units[1].Key != "demo-web" {
		t.Fatalf("existing units must be preserved and the adoption unit added: %#v", env.Units)
	}
	if env.Environment.RuntimeConfig["host_alias"] != "local" || env.Environment.RuntimeConfig["docker_host"] != server.URL {
		t.Fatalf("environment runtime config not updated: %#v", env.Environment.RuntimeConfig)
	}
}

func TestAdoptionServiceImportSelectionReportsScanFailure(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "docker unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()
	adoption := newTestAdoption(newFakeAdoptionCanonical(), &events.NoopPublisher{})

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123"}},
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || results[0].ContainerID != "container-123" {
		t.Fatalf("expected selected scan failure result, got %#v", results)
	}
}

func TestAdoptionServiceImportSelectionReportsUndiscoveredContainer(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	adoption := newTestAdoption(newFakeAdoptionCanonical(), &events.NoopPublisher{})

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "missing-container"}},
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "not discovered") {
		t.Fatalf("expected undiscovered selection failure, got %#v", results)
	}
}

func TestAdoptionServiceScanReportsNonAdoptableWarnings(t *testing.T) {
	ctx := context.Background()
	server := newUnsafeAdoptionDockerServer(t)
	defer server.Close()
	adoption := newTestAdoption(newFakeAdoptionCanonical(), &events.NoopPublisher{})

	previews, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: server.URL}}})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(previews) != 1 || len(previews[0].Containers) != 1 {
		t.Fatalf("unexpected previews: %#v", previews)
	}
	container := previews[0].Containers[0]
	if container.Adoptable || len(container.Warnings) == 0 {
		t.Fatalf("expected unsafe container to be non-adoptable with warnings, got %#v", container)
	}
}

func TestAdoptionBindingIdentitiesAndCoordinates(t *testing.T) {
	serviceID, envID := uuid.New(), uuid.New()
	binding := domain.AdoptionBinding{OrgID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, Fingerprints: map[string]string{"image_digest": "b", "container_id": "a"}}
	identities := binding.Identities()
	if len(identities) != 2 || identities[0].FingerprintKind != "container_id" || identities[1].Fingerprint != "b" || identities[0].ServiceID != serviceID {
		t.Fatalf("unexpected identities: %#v", identities)
	}
	if got := adoptionBindingKey(serviceID, envID); got != kinds.AdoptionBindingDTag(serviceID.String(), envID.String()) || !strings.HasPrefix(got, "adoption:binding:") {
		t.Fatalf("binding coordinate = %q", got)
	}
}

type runtimeDiscoveredContainer = runtimeAdapter.DiscoveredContainer

func sortStrings(values []string) { sort.Strings(values) }
func newAdoptionDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			if r.URL.Query().Get("all") != "1" {
				t.Errorf("containers query did not include all=1")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/demo-web-1"],"Image":"registry.example/web:1.2.3","ImageID":"sha256:image123","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-123",
				"Name":"/demo-web-1",
				"Image":"sha256:image123",
				"Config":{
					"Image":"registry.example/web:1.2.3",
					"Env":["APP_ENV=prod"],
					"Labels":{
						"com.docker.compose.project":"demo",
						"com.docker.compose.service":"web",
						"org.opencontainers.image.revision":"abc123"
					},
					"Cmd":["serve"],
					"Entrypoint":["/entrypoint.sh"],
					"WorkingDir":"/app"
				},
				"State":{"Status":"running","Health":{"Status":"healthy"}},
				"HostConfig":{"Binds":["/host/data:/data:ro"],"NetworkMode":"demo_default","RestartPolicy":{"Name":"unless-stopped"}},
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostPort":"8080"}]},"Networks":{"demo_default":{"Aliases":["web","demo-web-1"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:image123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:image123","RepoDigests":["registry.example/web@sha256:repo123"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

func newSensitiveAdoptionDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-secret","Names":["/secret-app"],"Image":"registry.example/secret-app:2.0.0","ImageID":"sha256:secretimage","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-secret/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-secret",
				"Name":"/secret-app",
				"Image":"sha256:secretimage",
				"Config":{
					"Image":"registry.example/secret-app:2.0.0",
					"Env":["APP_ENV=prod","DB_PASSWORD=super-secret","AWS_SECRET_ACCESS_KEY=aws-secret","DATABASE_URL=postgres://user:pass@db/prod","MINT_LND_REST_MACAROON=hex-secret","FLEET_CURATOR_BUNKER_URL=bunker://pub?secret=secret"],
					"Labels":{"com.example.owner":"platform","com.example.secret-token":"label-secret"},
					"Cmd":["serve"]
				},
				"State":{"Status":"running"},
				"HostConfig":{"NetworkMode":"bridge","RestartPolicy":{"Name":"always"}},
				"NetworkSettings":{"Ports":{},"Networks":{"bridge":{"Aliases":["secret-app"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:secretimage/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:secretimage","RepoDigests":["registry.example/secret-app@sha256:secretrepo"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

func newUnsafeAdoptionDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-unsafe","Names":["/unsafe"],"Image":"registry.example/unsafe:latest","ImageID":"sha256:unsafe","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-unsafe/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-unsafe",
				"Name":"/unsafe",
				"Image":"sha256:unsafe",
				"Config":{"Image":"registry.example/unsafe:latest","Labels":{}},
				"State":{"Status":"running"},
				"HostConfig":{"NetworkMode":"custom"},
				"Mounts":[{"Type":"tmpfs","Destination":"/cache"}],
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostIP":"127.0.0.1","HostPort":"8080"},{"HostIP":"::","HostPort":"8080"}]},"Networks":{"custom":{"Aliases":["surprise"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:unsafe/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:unsafe","RepoDigests":["registry.example/unsafe@sha256:unsafe"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

type mockOrgRepo struct {
	orgs []domain.Organization
}

func (m *mockOrgRepo) Create(_ context.Context, org *domain.Organization) error {
	if org.ID == uuid.Nil {
		org.ID = uuid.New()
	}
	m.orgs = append(m.orgs, *org)
	return nil
}

func (m *mockOrgRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	for i := range m.orgs {
		if m.orgs[i].ID == id {
			return &m.orgs[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockOrgRepo) GetByName(_ context.Context, name string) (*domain.Organization, error) {
	for i := range m.orgs {
		if m.orgs[i].Name == name {
			return &m.orgs[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockOrgRepo) List(_ context.Context) ([]domain.Organization, error) {
	out := make([]domain.Organization, len(m.orgs))
	copy(out, m.orgs)
	return out, nil
}

func (m *mockOrgRepo) Update(_ context.Context, org *domain.Organization) error {
	for i := range m.orgs {
		if m.orgs[i].ID == org.ID {
			m.orgs[i] = *org
			return nil
		}
	}
	return repository.ErrNotFound
}

func (m *mockOrgRepo) Delete(_ context.Context, id uuid.UUID) error {
	for i := range m.orgs {
		if m.orgs[i].ID == id {
			m.orgs = append(m.orgs[:i], m.orgs[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

type mockAdoptedIdentityRepo struct {
	identities map[string]domain.AdoptedRuntimeIdentity
}

func newMockAdoptedIdentityRepo() *mockAdoptedIdentityRepo {
	return &mockAdoptedIdentityRepo{identities: map[string]domain.AdoptedRuntimeIdentity{}}
}

func (m *mockAdoptedIdentityRepo) UpsertMany(_ context.Context, identities []domain.AdoptedRuntimeIdentity) error {
	for _, identity := range identities {
		if identity.ID == uuid.Nil {
			identity.ID = uuid.New()
		}
		m.identities[identity.OrgID.String()+"/"+identity.Fingerprint] = identity
	}
	return nil
}

func (m *mockAdoptedIdentityRepo) FindByFingerprints(_ context.Context, orgID uuid.UUID, fingerprints []string) ([]domain.AdoptedRuntimeIdentity, error) {
	var out []domain.AdoptedRuntimeIdentity
	for _, fingerprint := range fingerprints {
		if identity, ok := m.identities[orgID.String()+"/"+fingerprint]; ok {
			out = append(out, identity)
		}
	}
	return out, nil
}

func (m *mockAdoptedIdentityRepo) byKind(kind string) *domain.AdoptedRuntimeIdentity {
	for _, identity := range m.identities {
		if identity.FingerprintKind == kind {
			copy := identity
			return &copy
		}
	}
	return nil
}

type capturePublisher struct {
	events []events.Event
}

func (p *capturePublisher) Publish(_ context.Context, e events.Event) {
	p.events = append(p.events, e)
}

func (p *capturePublisher) Subscribe(_ events.EventType, _ events.Handler) {}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (p *capturePublisher) hasEvent(eventType events.EventType) bool {
	for _, event := range p.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type mockAdoptionTxExecutor struct {
	failures     []error
	services     *mockServiceRepo
	environments *mockEnvRepo
	builds       *mockBuildRepo
	artifacts    *mockArtifactRepo
	state        *mockStateRepo
	observations *mockObsRepo
	secrets      *mockSecretRepo
	identities   *mockAdoptedIdentityRepo
}

func newMockAdoptionTxExecutor(services *mockServiceRepo, environments *mockEnvRepo, builds *mockBuildRepo, artifacts *mockArtifactRepo, state *mockStateRepo, observations *mockObsRepo, secrets *mockSecretRepo) *mockAdoptionTxExecutor {
	return &mockAdoptionTxExecutor{services: services, environments: environments, builds: builds, artifacts: artifacts, state: state, observations: observations, secrets: secrets}
}

func (e *mockAdoptionTxExecutor) WithinTx(ctx context.Context, fn func(repos repository.TxRepos) error) error {
	if len(e.failures) > 0 {
		err := e.failures[0]
		e.failures = e.failures[1:]
		return err
	}
	txServices := cloneMockServiceRepo(e.services)
	txEnvironments := cloneMockEnvRepo(e.environments)
	txBuilds := cloneMockBuildRepo(e.builds)
	txArtifacts := cloneMockArtifactRepo(e.artifacts)
	txState := cloneMockStateRepo(e.state)
	txObservations := cloneMockObsRepo(e.observations)
	txSecrets := cloneMockSecretRepo(e.secrets)
	txIdentities := cloneMockAdoptedIdentityRepo(e.identities)

	txRepos := repository.TxRepos{
		Services:     txServices,
		Environments: txEnvironments,
		Builds:       txBuilds,
		Artifacts:    txArtifacts,
		State:        txState,
		Observations: txObservations,
		Secrets:      txSecrets,
	}
	if txIdentities != nil {
		txRepos.AdoptedIdentities = txIdentities
	}
	if err := fn(txRepos); err != nil {
		return err
	}

	e.services.services = txServices.services
	e.environments.envs = txEnvironments.envs
	e.builds.builds = txBuilds.builds
	e.artifacts.artifacts = txArtifacts.artifacts
	e.state.replaceFrom(txState)
	e.observations.observations = txObservations.observations
	if e.secrets != nil && txSecrets != nil {
		e.secrets.secrets = txSecrets.secrets
	}
	if e.identities != nil && txIdentities != nil {
		e.identities.identities = txIdentities.identities
	}
	_ = ctx
	return nil
}

func cloneMockServiceRepo(src *mockServiceRepo) *mockServiceRepo {
	clone := newMockServiceRepo()
	for id, svc := range src.services {
		copied := *svc
		clone.services[id] = &copied
	}
	return clone
}

func cloneMockEnvRepo(src *mockEnvRepo) *mockEnvRepo {
	clone := newMockEnvRepo()
	for id, env := range src.envs {
		copied := *env
		if env.RuntimeConfig != nil {
			copied.RuntimeConfig = map[string]any{}
			for k, v := range env.RuntimeConfig {
				copied.RuntimeConfig[k] = v
			}
		}
		clone.envs[id] = &copied
	}
	return clone
}

func cloneMockBuildRepo(src *mockBuildRepo) *mockBuildRepo {
	clone := newMockBuildRepo()
	for id, build := range src.builds {
		copied := *build
		clone.builds[id] = &copied
	}
	return clone
}

func cloneMockArtifactRepo(src *mockArtifactRepo) *mockArtifactRepo {
	clone := newMockArtifactRepo()
	clone.getByIDErr = src.getByIDErr
	for id, artifact := range src.artifacts {
		copied := *artifact
		clone.artifacts[id] = &copied
	}
	return clone
}

func cloneMockStateRepo(src *mockStateRepo) *mockStateRepo {
	clone := newMockStateRepo()
	clone.replaceFrom(src)
	return clone
}

func cloneMockObsRepo(src *mockObsRepo) *mockObsRepo {
	clone := newMockObsRepo()
	for id, obs := range src.observations {
		copied := *obs
		clone.observations[id] = &copied
	}
	return clone
}

func cloneMockSecretRepo(src *mockSecretRepo) *mockSecretRepo {
	if src == nil {
		return nil
	}
	clone := newMockSecretRepo()
	for id, secret := range src.secrets {
		copied := *secret
		clone.secrets[id] = &copied
	}
	return clone
}

func cloneMockAdoptedIdentityRepo(src *mockAdoptedIdentityRepo) *mockAdoptedIdentityRepo {
	if src == nil {
		return nil
	}
	clone := newMockAdoptedIdentityRepo()
	for fingerprint, identity := range src.identities {
		clone.identities[fingerprint] = identity
	}
	return clone
}
