package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const materialDesiredHash = "sha256:desired-state"

// materialHarness wires a reconciler over shared, persisted mocks so passes —
// and restarts (a fresh Reconciler over the same repos) — see the same state.
type materialHarness struct {
	serviceID, envID uuid.UUID
	stateKey         string
	stateRepo        *mockStateRepo
	obsRepo          *mockObservationRepo
	rt               *mockRuntime
	publisher        *mockPublisher
	statePublisher   *countingRuntimeStatePublisher
	services         *mockServiceRepo
	envs             *mockEnvironmentRepo
	artifacts        *mockArtifactRepo
	units            *mockDeploymentUnitRepo
}

type countingRuntimeStatePublisher struct {
	records int
}

func (p *countingRuntimeStatePublisher) PublishState(_ context.Context, _ *domain.EnvironmentServiceState, _ *domain.RuntimeObservation) error {
	p.records++
	return nil
}
func (p *countingRuntimeStatePublisher) PublishStateTombstone(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func newMaterialHarness(t *testing.T, initial domain.DriftStatus) *materialHarness {
	t.Helper()
	serviceID, envID := uuid.New(), uuid.New()
	h := &materialHarness{
		serviceID: serviceID, envID: envID, stateKey: stateMapKey(serviceID, envID),
		obsRepo:        &mockObservationRepo{},
		rt:             &mockRuntime{observeNormHash: materialDesiredHash, observeHealth: domain.HealthStatusHealthy},
		publisher:      &mockPublisher{},
		statePublisher: &countingRuntimeStatePublisher{},
		services:       &mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api"}}},
		envs: &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {
			ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly},
		}}},
		artifacts: &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		units:     &mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
	}
	h.stateRepo = &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		h.stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredHash: materialDesiredHash, DriftStatus: initial},
	}}
	return h
}

// seedObservedBaseline makes the harness realistic for a steady in_sync unit:
// in production such a unit always has a prior healthy observation linked from
// its state. Without this, the first pass is (correctly) the never-observed ->
// observed transition, which is a real event and is tested separately.
func (h *materialHarness) seedObservedBaseline(t *testing.T) {
	t.Helper()
	prior := &domain.RuntimeObservation{
		ServiceID: h.serviceID, EnvironmentID: h.envID,
		NormalizedHash: materialDesiredHash, HealthStatus: domain.HealthStatusHealthy,
		Source: "mock", ObservedAt: time.Now().UTC(),
	}
	require.NoError(t, h.obsRepo.Create(context.Background(), prior))
	h.stateRepo.states[h.stateKey].CurrentObservationID = &prior.ID
}

// reconciler builds a Reconciler over the harness. Calling it again simulates
// a process restart: new instance, same persisted repos.
func (h *materialHarness) reconciler() *Reconciler {
	r := NewReconciler(h.services, h.envs, h.artifacts, h.units, h.obsRepo, h.stateRepo,
		&mockRuntimeResolver{rt: h.rt}, h.publisher, time.Minute, zap.NewNop())
	r.SetRuntimeStatePublisher(h.statePublisher)
	return r
}

func (h *materialHarness) pass(t *testing.T, r *Reconciler) {
	t.Helper()
	require.NoError(t, r.reconcileOne(context.Background(), h.stateRepo.states[h.stateKey]))
}

func (h *materialHarness) counts() (stateChanged, drift int) {
	h.publisher.mu.Lock()
	defer h.publisher.mu.Unlock()
	for _, e := range h.publisher.events {
		switch e.Type {
		case events.EventEnvironmentServiceStateChanged:
			stateChanged++
		case events.EventDriftDetected:
			drift++
		}
	}
	return
}

func (h *materialHarness) lastStateChanged(t *testing.T) events.ResourceData {
	t.Helper()
	h.publisher.mu.Lock()
	defer h.publisher.mu.Unlock()
	for i := len(h.publisher.events) - 1; i >= 0; i-- {
		if h.publisher.events[i].Type == events.EventEnvironmentServiceStateChanged {
			data, ok := h.publisher.events[i].Data.(events.ResourceData)
			require.True(t, ok, "state-changed Data must remain events.ResourceData for the projector")
			return data
		}
	}
	t.Fatal("no state-changed event published")
	return events.ResourceData{}
}

// TestReconcilerRepeatedUnchangedHealthyObservationsEmitZeroEvents is the core
// P0 regression: a steady in_sync unit reconciled repeatedly must update its
// bookkeeping without growing observation history or emitting events.
func TestReconcilerRepeatedUnchangedHealthyObservationsEmitZeroEvents(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusInSync)
	h.seedObservedBaseline(t)
	r := h.reconciler()
	const passes = 5
	var firstObservationID uuid.UUID
	for i := 0; i < passes; i++ {
		h.pass(t, r)
		state := h.stateRepo.states[h.stateKey]
		require.Equal(t, domain.DriftStatusInSync, state.DriftStatus)
		require.NotNil(t, state.LastReconciledAt, "bookkeeping must still be updated")
		require.NotNil(t, state.CurrentObservationID)
		if i == 0 {
			firstObservationID = *state.CurrentObservationID
		}
	}
	// The persisted link remains stable across repeated no-op samples.
	require.Equal(t, firstObservationID, *h.stateRepo.states[h.stateKey].CurrentObservationID)
	require.Len(t, h.obsRepo.observations, 1)
	stateChanged, drift := h.counts()
	require.Zero(t, stateChanged, "unchanged observations must not emit state-changed events")
	require.Zero(t, drift, "unchanged observations must not emit drift events")
	require.Zero(t, h.statePublisher.records, "unchanged observations must not publish canonical state")
}

// TestReconcilerDriftEnterAndExitEmitExactlyOneEventEach proves a real drift
// transition emits exactly one state-changed (with deterministic reasons) and
// exactly one drift.detected on entry, then nothing while steadily drifted, and
// exactly one state-changed (no drift event) on exit.
func TestReconcilerDriftEnterAndExitEmitExactlyOneEventEach(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusInSync)
	h.seedObservedBaseline(t)
	r := h.reconciler()
	h.pass(t, r) // steady baseline
	stateChanged, drift := h.counts()
	require.Zero(t, stateChanged)
	require.Zero(t, drift)

	// Enter drift: observed hash diverges from desired.
	h.rt.observeNormHash = "sha256:something-else"
	h.pass(t, r)
	require.Equal(t, domain.DriftStatusDrifted, h.stateRepo.states[h.stateKey].DriftStatus)
	stateChanged, drift = h.counts()
	require.Equal(t, 1, stateChanged, "drift entry emits exactly one state-changed")
	require.Equal(t, 1, drift, "drift entry emits exactly one drift.detected")
	data := h.lastStateChanged(t)
	require.Equal(t, string(domain.DriftStatusInSync), data.PreviousDriftStatus)
	require.Equal(t, string(domain.DriftStatusDrifted), data.DriftStatus)
	require.Contains(t, strings.Split(data.ChangeReason, ","), changeReasonDriftStatus)
	require.Contains(t, strings.Split(data.ChangeReason, ","), changeReasonObservedHash)

	// Steadily drifted with identical evidence: no repeats.
	for i := 0; i < 3; i++ {
		h.pass(t, r)
	}
	stateChanged, drift = h.counts()
	require.Equal(t, 1, stateChanged, "steady drift must not repeat state-changed")
	require.Equal(t, 1, drift, "steady drift must not repeat drift.detected")
	require.Len(t, h.obsRepo.observations, 2, "only baseline and drift entry are retained")

	// Exit drift: observed hash converges again.
	h.rt.observeNormHash = materialDesiredHash
	h.pass(t, r)
	require.Equal(t, domain.DriftStatusInSync, h.stateRepo.states[h.stateKey].DriftStatus)
	stateChanged, drift = h.counts()
	require.Equal(t, 2, stateChanged, "drift exit emits exactly one more state-changed")
	require.Equal(t, 1, drift, "drift exit must not emit a drift.detected")
	require.Len(t, h.obsRepo.observations, 3, "drift exit retains one more observation")
	data = h.lastStateChanged(t)
	require.Equal(t, string(domain.DriftStatusDrifted), data.PreviousDriftStatus)
	require.Equal(t, string(domain.DriftStatusInSync), data.DriftStatus)
}

// TestReconcilerHealthTransitionEmitsExactlyOneEventWithReason proves a real
// health transition (desired config still applied, container becomes
// unhealthy) emits exactly one state-changed event carrying the health reason.
func TestReconcilerHealthTransitionEmitsExactlyOneEventWithReason(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusInSync)
	h.seedObservedBaseline(t)
	r := h.reconciler()
	h.pass(t, r) // steady healthy baseline
	h.rt.mu.Lock()
	h.rt.observeHealth = domain.HealthStatusStopped
	h.rt.mu.Unlock()
	h.pass(t, r)
	stateChanged, _ := h.counts()
	require.Equal(t, 1, stateChanged, "a health transition emits exactly one state-changed event")
	data := h.lastStateChanged(t)
	require.Contains(t, strings.Split(data.ChangeReason, ","), changeReasonHealth)
	// Repeating the same unhealthy observation adds nothing.
	h.pass(t, r)
	stateChanged, _ = h.counts()
	require.Equal(t, 1, stateChanged)
	require.Len(t, h.obsRepo.observations, 2, "repeated unhealthy samples must not grow history")
}

func TestReconcilerContainerRestartRetainsAndPublishesObservation(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusInSync)
	h.seedObservedBaseline(t)
	h.obsRepo.observations[0].ObservedContainerID = "container-1"
	h.rt.observeContainerID = "container-2"
	oldID := h.obsRepo.observations[0].ID
	r := h.reconciler()
	h.pass(t, r)

	require.Len(t, h.obsRepo.observations, 2)
	require.NotEqual(t, oldID, *h.stateRepo.states[h.stateKey].CurrentObservationID)
	stateChanged, drift := h.counts()
	require.Equal(t, 1, stateChanged, "container restart must reach downstream state subscribers")
	require.Zero(t, drift)
	require.Contains(t, strings.Split(h.lastStateChanged(t).ChangeReason, ","), changeReasonRuntimeObservation)
	require.Equal(t, 1, h.statePublisher.records, "retained restart must publish canonical state")

	h.pass(t, r)
	require.Len(t, h.obsRepo.observations, 2, "unchanged restarted container must not create another row")
	stateChanged, _ = h.counts()
	require.Equal(t, 1, stateChanged)
	require.Equal(t, 1, h.statePublisher.records)
}

func TestReconcilerStateWriteRetryReusesUnlinkedObservation(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusDrifted)
	h.seedObservedBaseline(t)
	h.obsRepo.observations[0].HealthStatus = domain.HealthStatusUnhealthy
	h.rt.observeHealth = domain.HealthStatusStopped
	linkedID := h.obsRepo.observations[0].ID
	persistedState := *h.stateRepo.states[h.stateKey]
	h.stateRepo.upsertErr = errors.New("state write failed")

	err := h.reconciler().reconcileOne(context.Background(), h.stateRepo.states[h.stateKey])
	require.ErrorIs(t, err, h.stateRepo.upsertErr)
	require.Len(t, h.obsRepo.observations, 2, "material observation is durable before state update")
	stateChanged, _ := h.counts()
	require.Zero(t, stateChanged, "failed state update must not publish")
	require.Zero(t, h.statePublisher.records)

	// Rehydrate the prior persisted state, as a process restart does after the
	// in-memory state object was mutated by the failed pass.
	h.stateRepo.states[h.stateKey] = &persistedState
	h.stateRepo.upsertErr = nil
	h.pass(t, h.reconciler())
	require.Len(t, h.obsRepo.observations, 2, "retry must reuse the unlinked row")
	require.NotEqual(t, linkedID, *h.stateRepo.states[h.stateKey].CurrentObservationID)
	stateChanged, _ = h.counts()
	require.Equal(t, 1, stateChanged, "retry must publish the missed transition")
	require.Contains(t, strings.Split(h.lastStateChanged(t).ChangeReason, ","), changeReasonHealth)
	require.Equal(t, 1, h.statePublisher.records, "retry must publish canonical state")
}

// TestReconcilerNoOpObservationsStaySuppressedAcrossRestart proves the
// baseline is derived from PERSISTED state + its linked observation, so a process
// restart does not re-emit events for an unchanged unit.
func TestReconcilerNoOpObservationsStaySuppressedAcrossRestart(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusInSync)
	h.seedObservedBaseline(t)
	h.pass(t, h.reconciler())
	stateChanged, drift := h.counts()
	require.Zero(t, stateChanged)
	require.Zero(t, drift)

	// Restart: brand-new reconciler instance over the same persisted repos.
	restarted := h.reconciler()
	for i := 0; i < 3; i++ {
		h.pass(t, restarted)
	}
	stateChanged, drift = h.counts()
	require.Zero(t, stateChanged, "restart must not re-emit for an unchanged unit")
	require.Zero(t, drift)
	require.Len(t, h.obsRepo.observations, 1, "restart must reuse the persisted observation")
}

// TestReconcilerFirstObservationOnUnknownStateEmitsOneEvent proves the very
// first observation of a unit (no prior observation link, unknown drift) is a
// real transition that emits exactly one event, then goes quiet.
func TestReconcilerFirstObservationOnUnknownStateEmitsOneEvent(t *testing.T) {
	h := newMaterialHarness(t, domain.DriftStatusUnknown)
	r := h.reconciler()
	h.pass(t, r)
	stateChanged, _ := h.counts()
	require.Equal(t, 1, stateChanged, "first observation is a real transition")
	data := h.lastStateChanged(t)
	reasons := strings.Split(data.ChangeReason, ",")
	require.Contains(t, reasons, changeReasonObservationLink)
	require.Contains(t, reasons, changeReasonDriftStatus)
	h.pass(t, r)
	h.pass(t, r)
	stateChanged, _ = h.counts()
	require.Equal(t, 1, stateChanged, "subsequent unchanged observations are silent")
}

func TestReconcilerBaselineReadFailureDoesNotManufactureTransition(t *testing.T) {
	for _, tc := range []struct {
		name           string
		seedBaseline   bool
		initialDrift   domain.DriftStatus
		wantAfterRetry int
	}{
		{name: "persisted unchanged baseline", seedBaseline: true, initialDrift: domain.DriftStatusInSync, wantAfterRetry: 0},
		{name: "genuinely absent baseline", initialDrift: domain.DriftStatusUnknown, wantAfterRetry: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMaterialHarness(t, tc.initialDrift)
			if tc.seedBaseline {
				h.seedObservedBaseline(t)
			}
			stateBefore := *h.stateRepo.states[h.stateKey]
			observationsBefore := len(h.obsRepo.observations)
			readErr := errors.New("observation repository unavailable")
			h.obsRepo.getLatestErr = readErr

			err := h.reconciler().reconcileOne(context.Background(), h.stateRepo.states[h.stateKey])
			require.ErrorIs(t, err, readErr)
			require.Equal(t, stateBefore, *h.stateRepo.states[h.stateKey], "failed baseline read must not mutate state or linkage")
			require.Len(t, h.obsRepo.observations, observationsBefore, "failed baseline read must not create an observation")
			stateChanged, drift := h.counts()
			require.Zero(t, stateChanged, "failed baseline read must not publish a transition")
			require.Zero(t, drift, "failed baseline read must not publish drift")

			h.obsRepo.getLatestErr = nil
			r := h.reconciler()
			h.pass(t, r)
			stateChanged, drift = h.counts()
			require.Equal(t, tc.wantAfterRetry, stateChanged)
			require.Zero(t, drift)
			wantObservations := observationsBefore + 1
			if tc.seedBaseline {
				wantObservations = observationsBefore
			}
			require.Len(t, h.obsRepo.observations, wantObservations)
			require.NotNil(t, h.stateRepo.states[h.stateKey].CurrentObservationID)
			h.pass(t, r)
			stateChanged, drift = h.counts()
			require.Equal(t, tc.wantAfterRetry, stateChanged, "unchanged retry must not repeat a transition")
			require.Zero(t, drift)
		})
	}
}

func TestObservationMateriallyChanged(t *testing.T) {
	unitID := uuid.New()
	previous := &domain.RuntimeObservation{
		ID: uuid.New(), DeploymentUnitID: &unitID,
		ObservedImageRepo: "example/api", ObservedImageDigest: "sha256:abc",
		ObservedContainerID: "container-1", ObservedHost: "node-1", ObservedVersion: "v1",
		HealthStatus: domain.HealthStatusHealthy, Source: "compose", NormalizedHash: "config-1",
		ObservedAt: time.Now().UTC(), Metadata: map[string]any{"sample": 1},
	}
	sample := *previous
	sample.ID = uuid.New()
	sample.ObservedAt = sample.ObservedAt.Add(time.Minute)
	sample.Metadata = map[string]any{"sample": 2}
	require.False(t, observationMateriallyChanged(previous, &sample), "sampling fields must not grow history")

	for _, tc := range []struct {
		name   string
		change func(*domain.RuntimeObservation)
	}{
		{"container restart", func(o *domain.RuntimeObservation) { o.ObservedContainerID = "container-2" }},
		{"health transition", func(o *domain.RuntimeObservation) { o.HealthStatus = domain.HealthStatusUnhealthy }},
		{"configuration transition", func(o *domain.RuntimeObservation) { o.NormalizedHash = "config-2" }},
		{"host migration", func(o *domain.RuntimeObservation) { o.ObservedHost = "node-2" }},
		{"source transition", func(o *domain.RuntimeObservation) { o.Source = "docker" }},
		{"deployment unit transition", func(o *domain.RuntimeObservation) { id := uuid.New(); o.DeploymentUnitID = &id }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := sample
			tc.change(&changed)
			require.True(t, observationMateriallyChanged(previous, &changed))
		})
	}
	require.True(t, observationMateriallyChanged(nil, &sample), "first observation must be retained")
}

// TestMaterialStateDiffIgnoresBookkeepingAndIsDeterministic unit-tests the
// fingerprint: volatile fields never register as change; real fields do, with
// sorted, stable reason codes.
func TestMaterialStateDiffIgnoresBookkeepingAndIsDeterministic(t *testing.T) {
	serviceID, envID := uuid.New(), uuid.New()
	obsA, obsB := uuid.New(), uuid.New()
	t1, t2 := time.Unix(1_800_000_000, 0).UTC(), time.Unix(1_800_000_600, 0).UTC()
	base := &domain.EnvironmentServiceState{
		ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync,
		DesiredHash: materialDesiredHash, CurrentObservationID: &obsA, LastReconciledAt: &t1, UpdatedAt: t1,
		ReconcileConsecutiveFailures: 0,
	}
	obs := &domain.RuntimeObservation{NormalizedHash: materialDesiredHash, HealthStatus: domain.HealthStatusHealthy}

	// Only bookkeeping differs: no change.
	volatile := *base
	volatile.CurrentObservationID = &obsB
	volatile.LastReconciledAt = &t2
	volatile.UpdatedAt = t2
	volatile.ReconcileConsecutiveFailures = 3
	volatile.ReconcileBackoffUntil = &t2
	volatile.ReconcileFailureMetadata = map[string]any{"starting_since": t2}
	changed, reasons := materialStateOf(base, obs).diff(materialStateOf(&volatile, obs))
	require.False(t, changed, "bookkeeping must never register as a material change: %v", reasons)

	// Real transitions register, with sorted deterministic reasons.
	drifted := *base
	drifted.DriftStatus = domain.DriftStatusDrifted
	artifact := uuid.New()
	drifted.DesiredArtifactID = &artifact
	changed, reasons = materialStateOf(base, obs).diff(materialStateOf(&drifted, obs))
	require.True(t, changed)
	require.Equal(t, []string{changeReasonDesiredArtifact, changeReasonDriftStatus}, reasons)

	// Observation-derived transitions register too.
	unhealthy := &domain.RuntimeObservation{NormalizedHash: "sha256:other", HealthStatus: domain.HealthStatusStopped}
	changed, reasons = materialStateOf(base, obs).diff(materialStateOf(base, unhealthy))
	require.True(t, changed)
	require.Equal(t, []string{changeReasonHealth, changeReasonObservedHash}, reasons)

	// Determinism: same inputs, same output.
	_, again := materialStateOf(base, obs).diff(materialStateOf(base, unhealthy))
	require.Equal(t, reasons, again)
}
