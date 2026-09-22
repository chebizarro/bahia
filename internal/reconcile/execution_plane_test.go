package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

type planeReconcileRepo struct {
	repository.VirtualizationRepository
	mu                 sync.Mutex
	plane              domain.ExecutionPlaneDeployment
	host               domain.VirtualizationHost
	reservations       []domain.VMCapacityReservation
	rotatedFrom        uuid.UUID
	accepted, released int
}
type planeReconcileResources struct {
	repository.ExecutionPlaneDeploymentRepository
	r *planeReconcileRepo
}

func (s planeReconcileResources) Get(_ context.Context, org, id uuid.UUID) (*domain.ExecutionPlaneDeployment, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if s.r.plane.OrgID != org || s.r.plane.ID != id {
		return nil, repository.ErrNotFound
	}
	b, _ := json.Marshal(s.r.plane)
	var p domain.ExecutionPlaneDeployment
	err := json.Unmarshal(b, &p)
	return &p, err
}

type planeReconcileHosts struct {
	repository.VirtualizationHostRepository
	r *planeReconcileRepo
}

func (s planeReconcileHosts) Get(_ context.Context, _, _ uuid.UUID) (*domain.VirtualizationHost, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	h := s.r.host
	return &h, nil
}
func (r *planeReconcileRepo) ExecutionPlanes() repository.ExecutionPlaneDeploymentRepository {
	return planeReconcileResources{r: r}
}
func (r *planeReconcileRepo) Hosts() repository.VirtualizationHostRepository {
	return planeReconcileHosts{r: r}
}
func (r *planeReconcileRepo) WithOperationLock(ctx context.Context, _, _ uuid.UUID, fn func(context.Context) error) error {
	return fn(ctx)
}
func (r *planeReconcileRepo) RotateObservationSession(_ context.Context, ref repository.VirtualizationResourceRef, generation int64, old, next uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref.OrgID != r.plane.OrgID || ref.ID != r.plane.ID || ref.Kind != domain.ExecutionPlaneResource || generation != r.plane.Generation || r.plane.ObservationCursor.SessionID != old {
		return repository.ErrConflict
	}
	r.rotatedFrom = old
	r.plane.ObservationCursor = &domain.VMObservationCursor{SessionID: next}
	return nil
}
func (r *planeReconcileRepo) AcceptPlaneObservation(_ context.Context, org, id uuid.UUID, o domain.ExecutionPlaneObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if org != r.plane.OrgID || id != r.plane.ID || o.SessionID != r.plane.ObservationCursor.SessionID || o.Sequence <= r.plane.ObservationCursor.Sequence {
		return repository.ErrConflict
	}
	if err := domain.ValidateExecutionPlaneObservation(&r.plane, &o); err != nil {
		return err
	}
	if previous := r.plane.Observation; previous != nil && previous.Probe != nil && previous.SessionID == o.SessionID && o.Probe != nil && (o.Probe.Sequence < previous.Probe.Sequence || (o.Probe.Sequence == previous.Probe.Sequence && !reflect.DeepEqual(o.Probe, previous.Probe))) {
		return repository.ErrConflict
	}
	r.plane.Observation = &o
	r.plane.ObservationCursor.Sequence = o.Sequence
	r.accepted++
	return nil
}
func (r *planeReconcileRepo) ListReservations(context.Context, uuid.UUID, uuid.UUID) ([]domain.VMCapacityReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.VMCapacityReservation(nil), r.reservations...), nil
}
func (r *planeReconcileRepo) ReleaseCapacity(_ context.Context, org, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reservations) > 0 && (r.reservations[0].ID != id || r.reservations[0].OrgID != org) {
		return repository.ErrConflict
	}
	r.reservations = nil
	r.released++
	return nil
}

type planeReconcileAdmission struct {
	r   *planeReconcileRepo
	err error
}

func (a *planeReconcileAdmission) ValidateDesired(_ context.Context, p *domain.ExecutionPlaneDeployment) error {
	if a.err != nil {
		return a.err
	}
	copy := *p
	copy.Observation = nil
	return domain.ValidateExecutionPlaneDeployment(&copy)
}
func (a *planeReconcileAdmission) ReserveDesiredCapacity(_ context.Context, p *domain.ExecutionPlaneDeployment) error {
	if a.err != nil {
		return a.err
	}
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	if p.Desired.State == domain.ExecutionPlaneEnabled {
		a.r.reservations = []domain.VMCapacityReservation{{ID: uuid.NewSHA1(p.ID, []byte("execution-plane-capacity")), OrgID: p.OrgID, HostID: p.HostID, ResourceID: p.ID, ResourceKind: domain.ExecutionPlaneResource, LifecycleClass: p.Desired.LifecycleClasses[0], Capacity: p.Desired.ReservedCapacity}}
	}
	return nil
}

type planeReconcileWorkers struct {
	repository.WorkerRepository
	mu     sync.Mutex
	worker domain.Worker
}

func (w *planeReconcileWorkers) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	copy := w.worker
	return &copy, nil
}

type planeClientBoundary struct {
	domain.ExecutionPlaneClient
	state                     domain.ExecutionPlaneObservation
	now                       time.Time
	discoverError, probeError error
	applyRequests             []domain.ExecutionPlaneApplyRequest
	probeRequests             []domain.ExecutionPlaneProbeRequest
	onProbe                   func(domain.ExecutionPlaneProbeRequest)
	observations              chan domain.ExecutionPlaneObservation
	observeDone               chan struct{}
	controls                  chan func(domain.ExecutionPlaneObserver) error
}

func (c *planeClientBoundary) Discover(context.Context, domain.ExecutionPlaneEndpoint) (domain.ExecutionPlaneSupport, error) {
	return domain.ExecutionPlaneSupport{ProtocolVersion: 1, Tools: []string{domain.ExecutionPlaneInspectTool, domain.ExecutionPlaneApplyTool, domain.ExecutionPlaneProbeTool}, LifecycleClasses: c.state.LifecycleClasses}, c.discoverError
}
func (c *planeClientBoundary) Inspect(context.Context, domain.ExecutionPlaneEndpoint, uuid.UUID) (*domain.ExecutionPlaneObservation, error) {
	copy := c.state
	return &copy, nil
}
func (c *planeClientBoundary) Apply(_ context.Context, _ domain.ExecutionPlaneEndpoint, v domain.ExecutionPlaneApplyRequest) (*domain.ExecutionPlaneAcknowledgment, error) {
	c.applyRequests = append(c.applyRequests, v)
	return &domain.ExecutionPlaneAcknowledgment{OperationID: v.OperationID, Accepted: true}, nil
}
func (c *planeClientBoundary) Probe(_ context.Context, _ domain.ExecutionPlaneEndpoint, q domain.ExecutionPlaneProbeRequest) (*domain.ExecutionPlaneProbeEvidence, error) {
	c.probeRequests = append(c.probeRequests, q)
	if c.onProbe != nil {
		c.onProbe(q)
	}
	if c.probeError != nil {
		return nil, c.probeError
	}
	c.state.VMObservationStamp = domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: q.Generation, SessionID: q.SessionID, Sequence: q.Sequence, ObservedAt: c.now}
	evidence := &domain.ExecutionPlaneProbeEvidence{VMObservationStamp: c.state.VMObservationStamp, PlaneID: q.PlaneID, Author: c.state.Author, LifecycleClasses: q.LifecycleClasses, Successful: true, PackageDigest: c.state.PackageDigest, ConfigRevision: c.state.ConfigRevision, ImagePins: c.state.ImagePins, Capabilities: []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "3"}}}
	c.state.Probe = evidence
	if c.observations != nil {
		c.observations <- c.state
	}
	return evidence, nil
}
func (c *planeClientBoundary) Observe(ctx context.Context, _ domain.ExecutionPlaneEndpoint, _ uuid.UUID, o domain.ExecutionPlaneObserver) error {
	defer close(c.observeDone)
	if err := o.OnEOSE(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case control := <-c.controls:
			if err := control(o); err != nil {
				return err
			}
		case state := <-c.observations:
			if err := o.OnObservation(ctx, state); err != nil {
				return err
			}
		}
	}
}

func planeReconcileFixture(t *testing.T) (*ExecutionPlaneReconciler, *planeReconcileRepo, *planeClientBoundary, *planeRun) {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	author := strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	org, id, host := uuid.New(), uuid.New(), uuid.New()
	classes := []domain.VMLifecycleClass{domain.VMLifecycleLoomQEMU}
	capability := domain.ExecutionPlaneCapability{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "3"}
	p := domain.ExecutionPlaneDeployment{ObservationCursor: &domain.VMObservationCursor{}, VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: org, Generation: 1, CreatedBy: author}, HostID: host, WorkerPubKey: author, ManagementAuthor: author, ManagementEndpointRef: uuid.New(), Desired: domain.ExecutionPlaneDesired{LifecycleClasses: classes, Package: domain.ExecutionPlanePackagePin{Digest: digest, Version: "1", Provenance: domain.VMProvenance{EventID: strings.Repeat("c", 64), Signer: author, Verified: true, VerifiedAt: now}}, Configuration: domain.ExecutionPlaneConfiguration{Revision: digest, Network: domain.VMNetwork{Mode: domain.VMNetworkIsolated}}, ImagePins: []domain.ExecutionPlaneImagePin{{LifecycleClass: domain.VMLifecycleLoomQEMU, ImageID: uuid.New(), ManifestDigest: digest}}, ReservedCapacity: domain.VMCapacity{VCPU: 2, MemoryBytes: 4 << 30, DiskBytes: 10 << 30}, Concurrency: 2, ExpectedCapabilities: []domain.ExecutionPlaneCapability{capability}, State: domain.ExecutionPlaneEnabled, ProbePolicy: domain.DefaultExecutionPlaneProbePolicy()}}
	o := domain.ExecutionPlaneObservation{VMObservationStamp: domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: 1, SessionID: uuid.New(), Sequence: 1, ObservedAt: now}, PlaneID: id, HostID: host, Author: author, LifecycleClasses: classes, Availability: domain.VMObservationAvailable, Drift: domain.VMDriftInSync, PackageDigest: digest, ConfigRevision: digest, ImagePins: p.Desired.ImagePins, ReservedCapacity: p.Desired.ReservedCapacity, Concurrency: 2, State: domain.ExecutionPlaneEnabled}
	repo := &planeReconcileRepo{plane: p, host: domain.VirtualizationHost{Enabled: true, OperationLimits: domain.DefaultVMOperationLimits()}}
	client := &planeClientBoundary{state: o, now: now}
	admission := &planeReconcileAdmission{r: repo}
	workers := &planeReconcileWorkers{worker: domain.Worker{PubKey: author, Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, MaxConcurrentJobs: 2}}
	r := NewExecutionPlaneReconciler(repo, client, admission, workers, nil)
	r.now = func() time.Time { return now }
	run := &planeRun{plane: p}
	r.runs[planeKey{org, id}] = run
	require.NoError(t, admission.ReserveDesiredCapacity(t.Context(), &p))
	return r, repo, client, run
}
func verifyPlaneFixture(t *testing.T, r *ExecutionPlaneReconciler, c *planeClientBoundary, run *planeRun) {
	t.Helper()
	require.NoError(t, r.rotate(t.Context(), run))
	require.NoError(t, r.inspectAndConverge(t.Context(), run))
	require.Empty(t, run.verified.Capabilities, "a probe response alone is not plane state")
	require.NoError(t, r.accept(t.Context(), run, c.state))
	require.Len(t, run.verified.Capabilities, 1)
}

func TestPlaneReconcileRecoversPersistedCursorWithoutObservation(t *testing.T) {
	r, repo, _, run := planeReconcileFixture(t)
	hidden := uuid.New()
	repo.plane.ObservationCursor = &domain.VMObservationCursor{SessionID: hidden}
	require.Nil(t, repo.plane.Observation)
	require.NoError(t, r.rotate(t.Context(), run))
	require.Equal(t, hidden, repo.rotatedFrom)
	require.NotEqual(t, hidden, run.session)
	require.Empty(t, run.verified.Capabilities)
}
func TestPlaneReconcileApplyAcknowledgmentNeverGrantsAndPinsAreExact(t *testing.T) {
	for _, dimension := range []string{"package", "config", "image", "capacity", "concurrency"} {
		t.Run(dimension, func(t *testing.T) {
			r, _, client, run := planeReconcileFixture(t)
			require.NoError(t, r.rotate(t.Context(), run))
			switch dimension {
			case "package":
				client.state.PackageDigest = "sha256:" + strings.Repeat("d", 64)
			case "config":
				client.state.ConfigRevision = "sha256:" + strings.Repeat("d", 64)
			case "image":
				client.state.ImagePins = append([]domain.ExecutionPlaneImagePin(nil), client.state.ImagePins...)
				client.state.ImagePins[0].ManifestDigest = "sha256:" + strings.Repeat("d", 64)
			case "capacity":
				client.state.ReservedCapacity.VCPU--
			case "concurrency":
				client.state.Concurrency--
			}
			require.NoError(t, r.inspectAndConverge(t.Context(), run))
			require.Len(t, client.applyRequests, 1)
			require.Equal(t, run.plane.Desired, client.applyRequests[0].Desired)
			require.Empty(t, run.verified.Capabilities)
			require.Empty(t, client.probeRequests)
			require.NoError(t, r.apply(t.Context(), run))
			require.Len(t, client.applyRequests, 1)
		})
	}
}
func TestPlaneReconcileLiveProbeFailureExpiryAndReplay(t *testing.T) {
	r, repo, client, run := planeReconcileFixture(t)
	verifyPlaneFixture(t, r, client, run)
	eligible, err := r.VerifiedCapabilities(t.Context(), run.plane.WorkerPubKey, r.now())
	require.NoError(t, err)
	require.Len(t, eligible, 1)
	success := client.state
	failed := success
	q := *success.Probe
	failed.Probe = &q
	failed.Sequence++
	q.Sequence = failed.Sequence
	q.Successful = false
	q.Capabilities = nil
	q.Diagnostic.Code = domain.VMErrorUnavailable
	signals := make(chan planeSignal, 1)
	observer := planeObserver{r: r, run: run, signals: signals}
	require.NoError(t, observer.OnObservation(t.Context(), failed))
	require.Empty(t, run.verified.Capabilities, "failure must retract before queued observation persistence")
	require.NoError(t, r.accept(t.Context(), run, failed))
	require.NoError(t, r.accept(t.Context(), run, success))
	require.Empty(t, run.verified.Capabilities)
	require.False(t, repo.plane.Observation.Probe.Successful)
	require.NoError(t, r.probe(t.Context(), run, failed.Sequence))
	require.NoError(t, r.accept(t.Context(), run, client.state))
	require.Len(t, run.verified.Capabilities, 1)
	expires := run.verified.ExpiresAt
	eligible, err = r.VerifiedCapabilities(t.Context(), run.plane.WorkerPubKey, expires)
	require.NoError(t, err)
	require.Empty(t, eligible)
	r.now = func() time.Time { return expires }
	require.NoError(t, r.expire(t.Context(), run))
	require.Empty(t, run.verified.Capabilities)
}
func TestPlaneReconcileCurrentSessionAndSelectionFences(t *testing.T) {
	for name, mutate := range map[string]func(*ExecutionPlaneReconciler, *planeReconcileRepo, *planeRun){
		"desired generation": func(_ *ExecutionPlaneReconciler, repo *planeReconcileRepo, _ *planeRun) { repo.plane.Generation++ },
		"disabled": func(_ *ExecutionPlaneReconciler, repo *planeReconcileRepo, _ *planeRun) {
			repo.plane.Desired.State = domain.ExecutionPlaneDisabled
		},
		"session replaced": func(_ *ExecutionPlaneReconciler, repo *planeReconcileRepo, _ *planeRun) {
			repo.plane.ObservationCursor.SessionID = uuid.New()
		},
		"reservation lost": func(_ *ExecutionPlaneReconciler, repo *planeReconcileRepo, _ *planeRun) { repo.reservations = nil },
		"host disabled":    func(_ *ExecutionPlaneReconciler, repo *planeReconcileRepo, _ *planeRun) { repo.host.Enabled = false },
		"worker cordoned": func(r *ExecutionPlaneReconciler, _ *planeReconcileRepo, _ *planeRun) {
			r.workers.(*planeReconcileWorkers).worker.SchedulingState = domain.WorkerSchedulingCordoned
		},
		"plane full": func(r *ExecutionPlaneReconciler, _ *planeReconcileRepo, _ *planeRun) {
			r.workers.(*planeReconcileWorkers).worker.CurrentQueueDepth = 2
		},
		"persisted failure": func(_ *ExecutionPlaneReconciler, repo *planeReconcileRepo, _ *planeRun) {
			q := *repo.plane.Observation.Probe
			q.Successful = false
			q.Capabilities = nil
			repo.plane.Observation.Probe = &q
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, repo, c, run := planeReconcileFixture(t)
			verifyPlaneFixture(t, r, c, run)
			mutate(r, repo, run)
			caps, err := r.VerifiedCapabilities(t.Context(), run.plane.WorkerPubKey, r.now())
			require.NoError(t, err)
			require.Empty(t, caps)
		})
	}
}
func TestPlaneReconcileDisconnectDuringProbeCannotRestoreCapability(t *testing.T) {
	r, _, client, run := planeReconcileFixture(t)
	verifyPlaneFixture(t, r, client, run)
	observer := planeObserver{r: r, run: run, signals: make(chan planeSignal, 1)}
	client.onProbe = func(domain.ExecutionPlaneProbeRequest) {
		require.NoError(t, observer.OnUnavailable(t.Context(), domain.VMDiagnostic{Code: domain.VMErrorUnavailable}))
	}
	require.NoError(t, r.probe(t.Context(), run, client.state.Sequence))
	require.Empty(t, run.verified.Capabilities)
	require.Nil(t, run.confirmed)
}
func TestPlaneReconcileRejectsWindowsAndUnsolicitedSession(t *testing.T) {
	r, repo, client, run := planeReconcileFixture(t)
	verifyPlaneFixture(t, r, client, run)
	original := run.session
	unsolicited := client.state
	unsolicited.SessionID = uuid.New()
	q := *unsolicited.Probe
	q.SessionID = unsolicited.SessionID
	unsolicited.Probe = &q
	require.NoError(t, r.accept(t.Context(), run, unsolicited))
	require.Equal(t, original, repo.plane.ObservationCursor.SessionID)
	windows := client.state
	windows.Sequence++
	q = *windows.Probe
	q.Sequence = windows.Sequence
	q.Capabilities = []domain.ExecutionPlaneCapability{{LifecycleClass: domain.VMLifecycleLoomQEMU, OS: domain.VMOSWindows, Architecture: "amd64", AgentProtocolVersion: "3"}}
	windows.Probe = &q
	require.Error(t, r.accept(t.Context(), run, windows))
	require.Empty(t, run.verified.Capabilities)
}
func TestPlaneReconcileDisabledDrainReleasesOnlyPlaneReservation(t *testing.T) {
	r, repo, client, run := planeReconcileFixture(t)
	run.plane.Desired.State = domain.ExecutionPlaneDisabled
	repo.plane.Desired.State = domain.ExecutionPlaneDisabled
	client.state.State = domain.ExecutionPlaneDisabled
	require.NoError(t, r.rotate(t.Context(), run))
	client.state.SessionID = run.session
	client.state.Draining = true
	require.NoError(t, r.accept(t.Context(), run, client.state))
	require.Zero(t, repo.released)
	client.state.Sequence++
	client.state.Draining = false
	require.NoError(t, r.accept(t.Context(), run, client.state))
	require.Equal(t, 1, repo.released)
	require.Empty(t, run.verified.Capabilities)
	require.Empty(t, client.applyRequests)
}
func TestPlaneReconcileRunEventLifecycleAndShutdown(t *testing.T) {
	r, repo, client, run := planeReconcileFixture(t)
	r.runs = map[planeKey]*planeRun{}
	client.observations = make(chan domain.ExecutionPlaneObservation, 4)
	client.observeDone = make(chan struct{})
	verified := make(chan struct{}, 1)
	r.onChange = func(_ context.Context, c ExecutionPlaneChange) error {
		if c.Kind == "observed" && len(c.Verified.Capabilities) > 0 {
			verified <- struct{}{}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, run.plane.OrgID, run.plane.ID) }()
	<-verified
	caps, err := r.VerifiedCapabilities(t.Context(), run.plane.WorkerPubKey, r.now())
	require.NoError(t, err)
	require.Len(t, caps, 1)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	<-client.observeDone
	caps, err = r.VerifiedCapabilities(t.Context(), run.plane.WorkerPubKey, r.now())
	require.NoError(t, err)
	require.Empty(t, caps)
	repo.mu.Lock()
	require.Positive(t, repo.accepted)
	repo.mu.Unlock()
}
func TestPlaneReconcileReconnectReprobesAfterDeduplicatedHistory(t *testing.T) {
	r, _, client, run := planeReconcileFixture(t)
	r.runs = map[planeKey]*planeRun{}
	client.observations = make(chan domain.ExecutionPlaneObservation, 4)
	client.observeDone = make(chan struct{})
	client.controls = make(chan func(domain.ExecutionPlaneObserver) error)
	verified := make(chan uuid.UUID, 2)
	r.onChange = func(_ context.Context, change ExecutionPlaneChange) error {
		if change.Kind == "observed" && len(change.Verified.Capabilities) > 0 {
			verified <- change.Verified.SessionID
		}
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, run.plane.OrgID, run.plane.ID) }()
	first := <-verified
	client.controls <- func(observer domain.ExecutionPlaneObserver) error {
		if err := observer.OnUnavailable(ctx, domain.VMDiagnostic{Code: domain.VMErrorUnavailable}); err != nil {
			return err
		}
		// The relay's identical old snapshot is deduplicated. Only EOSE is new.
		return observer.OnEOSE(ctx)
	}
	second := <-verified
	require.NotEqual(t, first, second)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Len(t, client.probeRequests, 2, "reconnect must request new live evidence without polling")
}

func TestPlaneReconcileUnavailableEndpointHasNoApplyOrProbeFallback(t *testing.T) {
	r, _, client, run := planeReconcileFixture(t)
	client.discoverError = planeUnavailable(errors.New("endpoint not implemented"))
	require.NoError(t, r.rotate(t.Context(), run))
	require.Error(t, r.inspectAndConverge(t.Context(), run))
	require.Empty(t, client.applyRequests)
	require.Empty(t, client.probeRequests)
	require.Empty(t, run.verified.Capabilities)
}
