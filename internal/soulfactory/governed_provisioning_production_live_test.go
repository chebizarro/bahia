package soulfactory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
	"go.uber.org/zap"
)

// ---- fakes -----------------------------------------------------------------

// liveIntentRepo extends the shared intent mock with the runtime-release
// lookups the production adapter uses for live verification.
type liveIntentRepo struct {
	*sfMockIntentRepo
}

func (m *liveIntentRepo) CreateForRuntimeRelease(_ context.Context, _ uuid.UUID, intent *domain.DeploymentIntent) (bool, error) {
	m.intents[intent.ID] = intent
	return true, nil
}

func (m *liveIntentRepo) GetByServiceRuntimeRelease(_ context.Context, serviceID, releaseID uuid.UUID) (*domain.DeploymentIntent, error) {
	for _, intent := range m.intents {
		if intent.ServiceID == serviceID && intent.RuntimeReleaseID != nil && *intent.RuntimeReleaseID == releaseID {
			return intent, nil
		}
	}
	return nil, nil
}

type liveUnitRepo struct {
	units map[uuid.UUID]*domain.DeploymentUnit
}

func (m *liveUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	m.units[unit.ID] = unit
	return nil
}
func (m *liveUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	return m.units[id], nil
}
func (m *liveUnitRepo) GetByEnvironmentKey(_ context.Context, envID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for _, u := range m.units {
		if u.EnvironmentID == envID && u.Key == key {
			return u, nil
		}
	}
	return nil, nil
}
func (m *liveUnitRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.DeploymentUnit, error) {
	var out []domain.DeploymentUnit
	for _, u := range m.units {
		if u.EnvironmentID == envID {
			out = append(out, *u)
		}
	}
	return out, nil
}
func (m *liveUnitRepo) ResolveDefault(_ context.Context, _ *domain.Environment) (*domain.DeploymentUnit, error) {
	return nil, nil
}

type liveReleaseRepo struct {
	releases map[uuid.UUID]*domain.AgentRuntimeRelease
	bindings map[uuid.UUID]*domain.AgentServiceReleaseBinding
}

func (m *liveReleaseRepo) CreateSource(context.Context, *domain.AgentRuntimeSource) error { return nil }
func (m *liveReleaseRepo) CreateRelease(_ context.Context, r *domain.AgentRuntimeRelease) error {
	m.releases[r.ID] = r
	return nil
}
func (m *liveReleaseRepo) GetSource(context.Context, uuid.UUID, uuid.UUID) (*domain.AgentRuntimeSource, error) {
	return nil, nil
}
func (m *liveReleaseRepo) GetRelease(_ context.Context, _, id uuid.UUID) (*domain.AgentRuntimeRelease, error) {
	return m.releases[id], nil
}
func (m *liveReleaseRepo) GetReleaseByDigest(context.Context, uuid.UUID, string, string) (*domain.AgentRuntimeRelease, error) {
	return nil, nil
}
func (m *liveReleaseRepo) BindRelease(_ context.Context, b *domain.AgentServiceReleaseBinding) error {
	m.bindings[b.ID] = b
	return nil
}
func (m *liveReleaseRepo) ListServiceReleases(context.Context, uuid.UUID, uuid.UUID) ([]domain.AgentServiceRuntimeRelease, error) {
	return nil, nil
}
func (m *liveReleaseRepo) GetServiceRelease(_ context.Context, _, serviceID, releaseID uuid.UUID) (*domain.AgentServiceRuntimeRelease, error) {
	for _, b := range m.bindings {
		if b.ServiceID == serviceID && b.ReleaseID == releaseID {
			rel := m.releases[releaseID]
			if rel == nil {
				return nil, nil
			}
			return &domain.AgentServiceRuntimeRelease{Binding: *b, Release: *rel}, nil
		}
	}
	return nil, nil
}
func (m *liveReleaseRepo) GetRollbackRelease(context.Context, uuid.UUID, string, uuid.UUID, string) (*domain.AgentServiceRuntimeRelease, error) {
	return nil, nil
}

// ---- fixture ---------------------------------------------------------------

// liveFixture is a production port over in-memory Bahia stores together with a
// completed adapter ledger, so every test can mutate the LIVE records out from
// under the ledger and prove the replay observer refuses to adopt them.
type liveFixture struct {
	port     *productionProvisioningPort
	spec     ProvisioningSpec
	state    *productionProvisioningState
	services *sfMockServiceRepo
	units    *liveUnitRepo
	releases *liveReleaseRepo
	intents  *liveIntentRepo
	orgID    uuid.UUID
	service  *domain.Service
	unit     *domain.DeploymentUnit
	binding  *domain.AgentServiceReleaseBinding
	intent   *domain.DeploymentIntent
}

// newLiveFixture seeds live records exactly as the real Ensure path writes
// them (governed markers on unit + intent, request correlation on the binding)
// and a ledger that records them as created by this run unless overridden.
func newLiveFixture(t *testing.T, ownership saga.Ownership) *liveFixture {
	t.Helper()
	orgID := uuid.New()
	spec := ProvisioningSpec{
		RequestID: "req-" + uuid.NewString(),
		RunID:     "run-" + uuid.NewString(),
		AgentID:   "scout",
		SpecHash:  "spec-hash-scout",
		Runtime:   domain.RuntimeTargetOpenClaw,
	}
	envID := uuid.New()
	svc := &domain.Service{ID: uuid.New(), OrgID: orgID, Name: soulServiceName(spec.AgentID), ArtifactRepo: soulServiceArtifactRepo(spec.AgentID), RuntimeType: domain.RuntimeTypeCompose}
	unit := &domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: envID, Key: soulServiceName(spec.AgentID), RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: map[string]any{"agent_id": spec.AgentID, governedMetadataSpec: spec.SpecHash}}
	release := &domain.AgentRuntimeRelease{ID: uuid.New(), OrgID: orgID, ImageRepo: "cascadia/openclaw", ImageDigest: "sha256:abc", CreatedAt: time.Now()}
	binding := &domain.AgentServiceReleaseBinding{ID: uuid.New(), OrgID: orgID, AgentID: spec.AgentID, ServiceID: svc.ID, ReleaseID: release.ID,
		ReleaseChannel: "stable", SourceEventID: spec.RequestID, CreatedAt: time.Now()}
	unitID := unit.ID
	intent := &domain.DeploymentIntent{ID: uuid.New(), ServiceID: svc.ID, EnvironmentID: envID, DeploymentUnitID: &unitID, RuntimeReleaseID: &release.ID,
		Metadata: map[string]any{
			"runtime_release_id":    release.ID.String(),
			governedMetadataRequest: spec.RequestID,
			governedMetadataRun:     spec.RunID,
			governedMetadataSpec:    spec.SpecHash,
			"agent_id":              spec.AgentID,
		}}

	services := &sfMockServiceRepo{services: map[uuid.UUID]*domain.Service{svc.ID: svc}}
	envs := &sfMockEnvRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, OrgID: orgID, Name: "agents"}}}
	intents := &liveIntentRepo{sfMockIntentRepo: &sfMockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{intent.ID: intent}}}
	releases := &liveReleaseRepo{releases: map[uuid.UUID]*domain.AgentRuntimeRelease{release.ID: release}, bindings: map[uuid.UUID]*domain.AgentServiceReleaseBinding{binding.ID: binding}}
	units := &liveUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	registry := service.NewRegistryService(
		services, envs,
		&sfMockBuildRepo{builds: map[uuid.UUID]*domain.Build{}},
		&sfMockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		intents,
		&sfMockRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}},
		&sfMockObservationRepo{observations: map[string][]domain.RuntimeObservation{}},
		&sfMockStateRepo{states: map[string]*domain.EnvironmentServiceState{}},
		sfRuntimeArtifactVerifier{}, &events.NoopPublisher{}, zap.NewNop(),
		service.WithManualArtifactRegistration(true),
		service.WithAgentRuntimeReleaseRepository(releases),
	)
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{OrganizationID: orgID.String()}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration: %v", err)
	}
	states, err := newProductionStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("newProductionStateStore: %v", err)
	}
	engine := &ProductionGovernedProvisioner{
		full:     &FullProvisioner{bahiaIntegration: integration},
		states:   states,
		releases: service.NewAgentRuntimeReleaseService(releases, services),
		units:    units,
	}

	owned := func(kind, id string) ObservedResource {
		r := ObservedResource{Kind: kind, ExternalID: id, Ownership: ownership, SpecHash: spec.SpecHash, CorrelationID: spec.RequestID}
		if ownership == saga.OwnershipCreated {
			r.OwnerRunID = spec.RunID
		}
		return r
	}
	// The ledger keeps its own copies so tests mutating LIVE records never
	// touch what the ledger remembers.
	ledgerRelease, ledgerBinding := *release, *binding
	state := &productionProvisioningState{
		RequestID: spec.RequestID, RunID: spec.RunID, AgentID: spec.AgentID, SpecHash: spec.SpecHash, Runtime: spec.Runtime,
		Soul:     domain.AgentSoul{ID: uuid.New(), AgentID: spec.AgentID, Name: "Scout"},
		Prepared: true,
		Release:  &ledgerRelease, ReleaseBinding: &ledgerBinding,
		ServiceID: svc.ID, EnvironmentID: envID, DeploymentUnitID: unit.ID, DeploymentIntentID: intent.ID,
		Steps: map[OrderedStep]productionStepState{
			StepRegisterServiceUnit:  {Complete: true, Resources: []ObservedResource{owned("service", svc.ID.String()), owned("deployment_unit", unit.ID.String())}},
			StepSelectRuntimeRelease: {Complete: true, Resources: []ObservedResource{owned("runtime_release", release.ID.String()), owned("runtime_release_binding", binding.ID.String())}},
			StepDeployViaBahia:       {Complete: true, Resources: []ObservedResource{owned("bahia_deployment", intent.ID.String())}},
		},
	}
	if err := states.save(context.Background(), state); err != nil {
		t.Fatalf("save ledger: %v", err)
	}
	return &liveFixture{
		port: &productionProvisioningPort{engine: engine}, spec: spec, state: state,
		services: services, units: units, releases: releases, intents: intents, orgID: orgID,
		service: svc, unit: unit, binding: binding, intent: intent,
	}
}

func requireOwnershipConflict(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected ownership_conflict containing %q, got nil", want)
	}
	var safe *saga.SafeError
	if !errors.As(err, &safe) || safe.Code != "ownership_conflict" || safe.Retryable {
		t.Fatalf("expected non-retryable ownership_conflict SafeError, got %v", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err.Error(), want)
	}
}

// ---- tests -----------------------------------------------------------------

// The reviewer's probe: the ledger records an accepted deployment unit, but
// the live unit's governed_spec_hash was changed externally. Observe must fail
// closed instead of handing back the ledger-derived adoption.
func TestProductionObserveRejectsLiveUnitSpecDrift(t *testing.T) {
	f := newLiveFixture(t, saga.OwnershipCreated)
	f.unit.RuntimeConfig[governedMetadataSpec] = "different-spec"
	res, err := f.port.Observe(context.Background(), f.spec, StepRegisterServiceUnit)
	requireOwnershipConflict(t, err, governedMetadataSpec)
	if res != nil {
		t.Fatalf("conflicting observe must not return ledger resources, got %v", res)
	}
}

func TestProductionObserveHappyPathAdoptsVerifiedLedger(t *testing.T) {
	f := newLiveFixture(t, saga.OwnershipCreated)
	for _, step := range []OrderedStep{StepRegisterServiceUnit, StepSelectRuntimeRelease, StepDeployViaBahia} {
		res, err := f.port.Observe(context.Background(), f.spec, step)
		if err != nil {
			t.Fatalf("Observe(%v) error = %v", step, err)
		}
		if len(res) != len(f.state.Steps[step].Resources) {
			t.Fatalf("Observe(%v) returned %d resources, want %d", step, len(res), len(f.state.Steps[step].Resources))
		}
	}
}

func TestProductionInspectRejectsExternalUnitChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *liveFixture)
		want   string
	}{
		{"unit re-owned by another agent", func(f *liveFixture) { f.unit.RuntimeConfig["agent_id"] = "intruder" }, "bound to agent"},
		{"created unit lost its governed marker", func(f *liveFixture) { delete(f.unit.RuntimeConfig, governedMetadataSpec) }, "no longer carries"},
		{"unit key rewritten", func(f *liveFixture) { f.unit.Key = "default" }, "identity"},
		{"unit moved to another environment", func(f *liveFixture) { f.unit.EnvironmentID = uuid.New() }, "identity"},
		{"service renamed", func(f *liveFixture) { f.service.Name = "someone-else" }, "identity"},
		{"service artifact repo hijacked", func(f *liveFixture) { f.service.ArtifactRepo = "evil/repo" }, "identity"},
		{"service moved to another org", func(f *liveFixture) { f.service.OrgID = uuid.New() }, "organization"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveFixture(t, saga.OwnershipCreated)
			tc.mutate(f)
			err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepRegisterServiceUnit)
			requireOwnershipConflict(t, err, tc.want)
		})
	}
}

func TestProductionInspectRejectsExternalBindingChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *liveFixture)
		want   string
	}{
		{"binding correlated to a different request", func(f *liveFixture) { f.binding.SourceEventID = "req-other" }, "correlation"},
		{"binding re-owned by another agent", func(f *liveFixture) { f.binding.AgentID = "intruder" }, "does not match governed state"},
		{"binding channel changed", func(f *liveFixture) { f.binding.ReleaseChannel = "canary" }, "channel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveFixture(t, saga.OwnershipCreated)
			tc.mutate(f)
			err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepSelectRuntimeRelease)
			requireOwnershipConflict(t, err, tc.want)
		})
	}
}

func TestProductionInspectRejectsExternalIntentChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *liveFixture)
		want   string
	}{
		{"intent spec hash changed", func(f *liveFixture) { f.intent.Metadata[governedMetadataSpec] = "different-spec" }, governedMetadataSpec},
		{"intent request marker changed", func(f *liveFixture) { f.intent.Metadata[governedMetadataRequest] = "req-other" }, governedMetadataRequest},
		{"intent run marker stripped", func(f *liveFixture) { delete(f.intent.Metadata, governedMetadataRun) }, governedMetadataRun},
		{"intent re-owned by another agent", func(f *liveFixture) { f.intent.Metadata["agent_id"] = "intruder" }, "bound to agent"},
		{"intent retargeted to another unit", func(f *liveFixture) { other := uuid.New(); f.intent.DeploymentUnitID = &other }, "targets"},
		{"intent replaced by a different intent for the same release", func(f *liveFixture) {
			replacement := *f.intent
			replacement.ID = uuid.New()
			delete(f.intents.intents, f.intent.ID)
			f.intents.intents[replacement.ID] = &replacement
		}, "is not the governed intent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveFixture(t, saga.OwnershipCreated)
			tc.mutate(f)
			err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepDeployViaBahia)
			requireOwnershipConflict(t, err, tc.want)
		})
	}
}

// Adopted / pre-existing resources may lack governed markers (they were not
// written by this run), but a marker that belongs to someone else is still a
// conflict.
func TestProductionInspectAdoptedResourcesTolerateAbsentMarkersOnly(t *testing.T) {
	t.Run("adopted unit without markers is accepted", func(t *testing.T) {
		f := newLiveFixture(t, saga.OwnershipAdopted)
		f.unit.RuntimeConfig = nil
		if err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepRegisterServiceUnit); err != nil {
			t.Fatalf("adopted unit without markers must be accepted, got %v", err)
		}
	})
	t.Run("adopted intent without governed markers is accepted", func(t *testing.T) {
		f := newLiveFixture(t, saga.OwnershipAdopted)
		f.intent.Metadata = map[string]any{"runtime_release_id": f.intent.Metadata["runtime_release_id"]}
		if err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepDeployViaBahia); err != nil {
			t.Fatalf("adopted intent without markers must be accepted, got %v", err)
		}
	})
	t.Run("adopted unit carrying a foreign spec marker is a conflict", func(t *testing.T) {
		f := newLiveFixture(t, saga.OwnershipAdopted)
		f.unit.RuntimeConfig[governedMetadataSpec] = "someone-elses-spec"
		requireOwnershipConflict(t, f.port.inspectRealStep(context.Background(), f.spec, f.state, StepRegisterServiceUnit), governedMetadataSpec)
	})
	t.Run("adopted intent carrying a foreign agent marker is a conflict", func(t *testing.T) {
		f := newLiveFixture(t, saga.OwnershipAdopted)
		f.intent.Metadata["agent_id"] = "intruder"
		requireOwnershipConflict(t, f.port.inspectRealStep(context.Background(), f.spec, f.state, StepDeployViaBahia), "bound to agent")
	})
	t.Run("pre-existing binding still requires this request's correlation", func(t *testing.T) {
		f := newLiveFixture(t, saga.OwnershipPreExisting)
		f.binding.SourceEventID = "req-other"
		requireOwnershipConflict(t, f.port.inspectRealStep(context.Background(), f.spec, f.state, StepSelectRuntimeRelease), "correlation")
	})
}

// Live records that vanished are hard errors (not conflicts, not adoption).
func TestProductionInspectMissingLiveRecordsFailClosed(t *testing.T) {
	f := newLiveFixture(t, saga.OwnershipCreated)
	delete(f.units.units, f.unit.ID)
	if err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepRegisterServiceUnit); err == nil {
		t.Fatal("missing live unit must fail")
	}
	f = newLiveFixture(t, saga.OwnershipCreated)
	delete(f.releases.bindings, f.binding.ID)
	if err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepSelectRuntimeRelease); err == nil {
		t.Fatal("missing live binding must fail")
	}
	f = newLiveFixture(t, saga.OwnershipCreated)
	delete(f.intents.intents, f.intent.ID)
	if err := f.port.inspectRealStep(context.Background(), f.spec, f.state, StepDeployViaBahia); err == nil {
		t.Fatal("missing live intent must fail")
	}
}
