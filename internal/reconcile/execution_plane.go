package reconcile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// ExecutionPlaneAdmission is implemented by service.ExecutionPlaneService.
type ExecutionPlaneAdmission interface {
	ValidateDesired(context.Context, *domain.ExecutionPlaneDeployment) error
	ReserveDesiredCapacity(context.Context, *domain.ExecutionPlaneDeployment) error
}

type ExecutionPlaneChange struct {
	OrgID        uuid.UUID
	PlaneID      uuid.UUID
	WorkerPubKey string
	Generation   int64
	Kind         string
	Diagnostic   domain.VMDiagnostic
	Verified     domain.VerifiedExecutionPlaneCapabilities
}

// ExecutionPlaneChangeHook supplies E's metrics/in-process events. Durable state
// and audit projection use the repository change journal, not this callback.
type ExecutionPlaneChangeHook func(context.Context, ExecutionPlaneChange) error

type ExecutionPlaneReconciler struct {
	repo      repository.VirtualizationRepository
	client    domain.ExecutionPlaneClient
	admission ExecutionPlaneAdmission
	workers   repository.WorkerRepository
	onChange  ExecutionPlaneChangeHook
	now       func() time.Time
	mu        sync.RWMutex
	runs      map[planeKey]*planeRun
}

type planeKey struct{ org, id uuid.UUID }
type planeRun struct {
	plane          domain.ExecutionPlaneDeployment
	session        uuid.UUID
	sequence       int64
	failedSequence int64
	confirmed      *domain.ExecutionPlaneProbeEvidence
	verified       domain.VerifiedExecutionPlaneCapabilities
	connected      bool
	applied        bool
}

type planeSignal struct {
	observation *domain.ExecutionPlaneObservation
	eose        bool
	unavailable bool
}

type planeObserver struct {
	r       *ExecutionPlaneReconciler
	run     *planeRun
	signals chan<- planeSignal
}

func NewExecutionPlaneReconciler(repo repository.VirtualizationRepository, client domain.ExecutionPlaneClient, admission ExecutionPlaneAdmission, workers repository.WorkerRepository, onChange ExecutionPlaneChangeHook) *ExecutionPlaneReconciler {
	return &ExecutionPlaneReconciler{repo: repo, client: client, admission: admission, workers: workers, onChange: onChange, now: time.Now, runs: map[planeKey]*planeRun{}}
}

func planeUnavailable(cause error) error {
	return &domain.VMProviderError{Code: domain.VMErrorUnavailable, Retryable: true, Cause: cause}
}
func planeEndpoint(p *domain.ExecutionPlaneDeployment) domain.ExecutionPlaneEndpoint {
	return domain.ExecutionPlaneEndpoint{HostID: p.HostID, EndpointRef: p.ManagementEndpointRef, Author: p.ManagementAuthor}
}

func (r *ExecutionPlaneReconciler) emit(ctx context.Context, run *planeRun, kind string, diagnostic domain.VMDiagnostic) error {
	if r.onChange == nil {
		return nil
	}
	r.mu.RLock()
	change := ExecutionPlaneChange{OrgID: run.plane.OrgID, PlaneID: run.plane.ID, WorkerPubKey: run.plane.WorkerPubKey, Generation: run.plane.Generation, Kind: kind, Diagnostic: diagnostic, Verified: run.verified}
	change.Verified.Capabilities = append([]domain.ExecutionPlaneCapability(nil), change.Verified.Capabilities...)
	r.mu.RUnlock()
	return r.onChange(ctx, change)
}

func (r *ExecutionPlaneReconciler) retract(ctx context.Context, run *planeRun, kind string, diagnostic domain.VMDiagnostic) error {
	r.mu.Lock()
	run.verified = domain.VerifiedExecutionPlaneCapabilities{}
	run.confirmed = nil
	r.mu.Unlock()
	return r.emit(ctx, run, kind, diagnostic)
}

// rotate uses C's additive read-only ObservationCursor, including after a crash
// between the previous CAS rotation and its first accepted observation.
func (r *ExecutionPlaneReconciler) rotate(ctx context.Context, run *planeRun) error {
	p, err := r.repo.ExecutionPlanes().Get(ctx, run.plane.OrgID, run.plane.ID)
	if err != nil {
		return err
	}
	if p.Generation != run.plane.Generation {
		return repository.ErrConflict
	}
	expected := uuid.Nil
	if p.ObservationCursor != nil {
		expected = p.ObservationCursor.SessionID
	}
	next := uuid.New()
	ref := repository.VirtualizationResourceRef{OrgID: p.OrgID, Kind: domain.ExecutionPlaneResource, ID: p.ID}
	if err := r.repo.RotateObservationSession(ctx, ref, p.Generation, expected, next); err != nil {
		return err
	}
	r.mu.Lock()
	run.session, run.sequence, run.failedSequence, run.connected = next, 0, 0, true
	run.confirmed, run.verified = nil, domain.VerifiedExecutionPlaneCapabilities{}
	run.plane.Observation = nil
	run.plane.ObservationCursor = &domain.VMObservationCursor{SessionID: next}
	r.mu.Unlock()
	return r.emit(ctx, run, "session_changed", domain.VMDiagnostic{})
}

// Run owns one plane's subscription and live health probes until cancellation.
// E must cancel/restart it on desired-generation changes and application shutdown.
// The interval is a health probe, never a relay polling/completion mechanism.
func (r *ExecutionPlaneReconciler) Run(ctx context.Context, org, id uuid.UUID) error {
	if r == nil || r.repo == nil || r.client == nil || r.admission == nil || r.workers == nil {
		return planeUnavailable(nil)
	}
	p, err := r.repo.ExecutionPlanes().Get(ctx, org, id)
	if err != nil {
		return err
	}
	key := planeKey{org, id}
	run := &planeRun{plane: *p}
	r.mu.Lock()
	if _, exists := r.runs[key]; exists {
		r.mu.Unlock()
		return repository.ErrConflict
	}
	r.runs[key] = run
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.runs, key)
		run.verified = domain.VerifiedExecutionPlaneCapabilities{}
		run.confirmed = nil
		r.mu.Unlock()
	}()
	return r.repo.WithOperationLock(ctx, org, id, func(ctx context.Context) (result error) {
		defer func() {
			result = errors.Join(result, r.retract(context.WithoutCancel(ctx), run, "unavailable", domain.VMDiagnostic{Code: domain.VMErrorUnavailable}))
		}()
		if err := r.admission.ValidateDesired(ctx, p); err != nil {
			return err
		}
		if err := r.admission.ReserveDesiredCapacity(ctx, p); err != nil {
			return err
		}
		if err := r.rotate(ctx, run); err != nil {
			return err
		}
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		signals := make(chan planeSignal, 16)
		finished := make(chan error, 1)
		observer := &planeObserver{r: r, run: run, signals: signals}
		go func() { finished <- r.client.Observe(streamCtx, planeEndpoint(p), p.ID, observer) }()
		defer func() { cancel(); <-finished }()
		if err := r.inspectAndConverge(ctx, run); err != nil {
			return err
		}
		ticker := time.NewTicker(time.Duration(p.Desired.ProbePolicy.IntervalSeconds) * time.Second)
		defer ticker.Stop()
		recoverAfterEOSE := false
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-finished:
				// Preserve one completion for the joining defer.
				finished <- err
				if err == nil {
					return planeUnavailable(nil)
				}
				return err
			case signal := <-signals:
				if signal.unavailable {
					recoverAfterEOSE = true
					if err := r.rotate(ctx, run); err != nil {
						return err
					}
					continue
				}
				if signal.eose {
					if err := r.emit(ctx, run, "caught_up", domain.VMDiagnostic{}); err != nil {
						return err
					}
					// Replayed snapshots may already be deduplicated by the transport.
					// Recovery explicitly requests new evidence after reconnect; EOSE
					// itself never grants or refreshes a capability.
					if recoverAfterEOSE {
						recoverAfterEOSE = false
						r.mu.RLock()
						confirmed := run.confirmed
						r.mu.RUnlock()
						if confirmed == nil {
							if err := r.inspectAndConverge(ctx, run); err != nil {
								return err
							}
						}
					}
					continue
				}
				if err := r.accept(ctx, run, *signal.observation); err != nil {
					return err
				}
			case <-ticker.C:
				if err := r.expire(ctx, run); err != nil {
					return err
				}
				// Inspect is not repeated to check apply completion. Probe only the
				// latest subscribed matching state; drift awaits an EVENT.
				r.mu.RLock()
				observed := run.plane.Observation
				r.mu.RUnlock()
				if observed != nil && planeMatches(&run.plane, observed) {
					if err := r.probe(ctx, run, observed.Sequence); err != nil {
						return err
					}
				}
			}
		}
	})
}

func (o *planeObserver) send(ctx context.Context, signal planeSignal) error {
	select {
	case o.signals <- signal:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (o *planeObserver) OnObservation(ctx context.Context, observation domain.ExecutionPlaneObservation) error {
	// A failed probe revokes synchronously, even while Run is waiting for a
	// different administrative operation to finish. Stale failures cannot grant.
	if q := observation.Probe; q != nil && !q.Successful {
		o.r.mu.Lock()
		current := q.SessionID == o.run.session && q.ObservedGeneration == o.run.plane.Generation && q.Author == o.run.plane.ManagementAuthor &&
			q.Sequence > o.run.failedSequence && (o.run.confirmed == nil || q.Sequence >= o.run.confirmed.Sequence)
		if current {
			o.run.failedSequence = q.Sequence
			o.run.confirmed = nil
			o.run.verified = domain.VerifiedExecutionPlaneCapabilities{}
		}
		o.r.mu.Unlock()
		if current {
			if err := o.r.emit(ctx, o.run, "probe_failed", q.Diagnostic); err != nil {
				return err
			}
		}
	}
	return o.send(ctx, planeSignal{observation: &observation})
}
func (o *planeObserver) OnEOSE(ctx context.Context) error {
	return o.send(ctx, planeSignal{eose: true})
}
func (o *planeObserver) OnUnavailable(ctx context.Context, diagnostic domain.VMDiagnostic) error {
	o.r.mu.Lock()
	o.run.connected = false
	o.r.mu.Unlock()
	if err := o.r.retract(ctx, o.run, "unavailable", diagnostic); err != nil {
		return err
	}
	return o.send(ctx, planeSignal{unavailable: true})
}

func planeMatches(p *domain.ExecutionPlaneDeployment, o *domain.ExecutionPlaneObservation) bool {
	return o != nil && o.ObservedGeneration == p.Generation && o.PlaneID == p.ID && o.HostID == p.HostID && o.Author == p.ManagementAuthor &&
		o.Availability == domain.VMObservationAvailable && o.PackageDigest == p.Desired.Package.Digest && o.ConfigRevision == p.Desired.Configuration.Revision &&
		reflect.DeepEqual(o.LifecycleClasses, p.Desired.LifecycleClasses) && reflect.DeepEqual(o.ImagePins, p.Desired.ImagePins) &&
		o.ReservedCapacity == p.Desired.ReservedCapacity && o.Concurrency == p.Desired.Concurrency && o.State == p.Desired.State &&
		(p.Desired.State == domain.ExecutionPlaneDisabled || !o.Draining)
}

func (r *ExecutionPlaneReconciler) administrativeContext(ctx context.Context, run *planeRun) (context.Context, context.CancelFunc, error) {
	host, err := r.repo.Hosts().Get(ctx, run.plane.OrgID, run.plane.HostID)
	if err != nil {
		return nil, nil, err
	}
	if host == nil || host.OperationLimits.InspectSeconds <= 0 {
		return nil, nil, planeUnavailable(nil)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(host.OperationLimits.InspectSeconds)*time.Second)
	return bounded, cancel, nil
}

func (r *ExecutionPlaneReconciler) inspectAndConverge(ctx context.Context, run *planeRun) error {
	bounded, cancel, err := r.administrativeContext(ctx, run)
	if err != nil {
		return err
	}
	defer cancel()
	e := planeEndpoint(&run.plane)
	support, err := r.client.Discover(bounded, e)
	if err != nil {
		return err
	}
	if !loom.SupportsPlaneClasses(support, run.plane.Desired.LifecycleClasses) {
		return planeUnavailable(nil)
	}
	observation, err := r.client.Inspect(bounded, e, run.plane.ID)
	if err != nil {
		return err
	}
	if observation == nil {
		return planeUnavailable(nil)
	}
	if !planeMatches(&run.plane, observation) {
		return r.apply(ctx, run)
	}
	return r.probe(ctx, run, observation.Sequence)
}

func (r *ExecutionPlaneReconciler) apply(ctx context.Context, run *planeRun) error {
	if err := r.retract(ctx, run, "drifted", domain.VMDiagnostic{}); err != nil {
		return err
	}
	if run.applied {
		return nil
	}
	if err := r.admission.ValidateDesired(ctx, &run.plane); err != nil {
		return err
	}
	if err := r.admission.ReserveDesiredCapacity(ctx, &run.plane); err != nil {
		return err
	}
	bounded, cancel, err := r.administrativeContext(ctx, run)
	if err != nil {
		return err
	}
	defer cancel()
	operation := uuid.NewSHA1(run.plane.ID, []byte(fmt.Sprintf("execution-plane-apply:%d", run.plane.Generation)))
	ack, err := r.client.Apply(bounded, planeEndpoint(&run.plane), domain.ExecutionPlaneApplyRequest{SchemaVersion: domain.VirtualizationSchemaVersion, PlaneID: run.plane.ID, HostID: run.plane.HostID, Generation: run.plane.Generation, OperationID: operation, Desired: run.plane.Desired})
	if err != nil {
		return err
	}
	if ack == nil || !ack.Accepted || ack.OperationID != operation {
		return planeUnavailable(nil)
	}
	run.applied = true
	return r.emit(ctx, run, "apply_admitted", domain.VMDiagnostic{})
}

func (r *ExecutionPlaneReconciler) probe(ctx context.Context, run *planeRun, after int64) error {
	bounded, cancel, err := r.administrativeContext(ctx, run)
	if err != nil {
		return err
	}
	defer cancel()
	r.mu.Lock()
	if after > run.sequence {
		run.sequence = after
	}
	run.sequence++
	request := domain.ExecutionPlaneProbeRequest{PlaneID: run.plane.ID, Generation: run.plane.Generation, SessionID: run.session, Sequence: run.sequence, LifecycleClasses: run.plane.Desired.LifecycleClasses}
	r.mu.Unlock()
	started := r.now()
	evidence, err := r.client.Probe(bounded, planeEndpoint(&run.plane), request)
	if err != nil {
		return errors.Join(err, r.retract(ctx, run, "probe_failed", domain.VMDiagnostic{Code: domain.VMErrorUnavailable}))
	}
	if evidence == nil || evidence.PlaneID != request.PlaneID || evidence.Author != run.plane.ManagementAuthor || evidence.SessionID != request.SessionID || evidence.Sequence != request.Sequence || evidence.ObservedGeneration != request.Generation || evidence.ObservedAt.Before(started) || evidence.ObservedAt.After(r.now()) || !reflect.DeepEqual(evidence.LifecycleClasses, request.LifecycleClasses) {
		return errors.Join(planeUnavailable(nil), r.retract(ctx, run, "probe_failed", domain.VMDiagnostic{Code: domain.VMErrorIntegrity}))
	}
	if !evidence.Successful {
		return r.retract(ctx, run, "probe_failed", evidence.Diagnostic)
	}
	r.mu.Lock()
	// Disconnect during a probe cannot resurrect the previous session.
	if run.connected && run.session == evidence.SessionID && evidence.Sequence > run.failedSequence {
		copy := *evidence
		run.confirmed = &copy
	}
	r.mu.Unlock()
	return r.emit(ctx, run, "probe_observed", domain.VMDiagnostic{})
}

func (r *ExecutionPlaneReconciler) accept(ctx context.Context, run *planeRun, o domain.ExecutionPlaneObservation) error {
	p, err := r.repo.ExecutionPlanes().Get(ctx, run.plane.OrgID, run.plane.ID)
	if err != nil {
		return err
	}
	if p.Generation != run.plane.Generation {
		return repository.ErrConflict
	}
	if o.ObservedGeneration < p.Generation {
		return nil
	}
	if err := domain.ValidateExecutionPlaneObservation(p, &o); err != nil {
		return errors.Join(err, r.retract(ctx, run, "unavailable", domain.VMDiagnostic{Code: domain.VMErrorIntegrity}))
	}
	if o.ObservedAt.After(r.now()) {
		return domain.ErrInvalidValue
	}
	r.mu.RLock()
	session, connected, confirmed := run.session, run.connected, run.confirmed
	r.mu.RUnlock()
	if !connected {
		return nil
	}
	if o.SessionID != session {
		if confirmed != nil {
			return nil
		}
		// A matching old-session snapshot is only a reason to request a new
		// live probe, never something that can install its own session.
		if planeMatches(p, &o) && confirmed == nil {
			return r.probe(ctx, run, o.Sequence)
		}
		if !planeMatches(p, &o) {
			return r.apply(ctx, run)
		}
		return nil
	}
	if p.ObservationCursor == nil || p.ObservationCursor.SessionID != session {
		return repository.ErrConflict
	}
	if o.Sequence <= p.ObservationCursor.Sequence {
		return nil
	}
	matches := planeMatches(p, &o)
	if o.Availability == domain.VMObservationAvailable {
		if matches {
			o.Drift = domain.VMDriftInSync
		} else {
			o.Drift = domain.VMDriftDrifted
		}
	} else {
		o.Drift = domain.VMDriftUnknown
	}
	if err := r.repo.AcceptPlaneObservation(ctx, p.OrgID, p.ID, o); err != nil {
		return err
	}
	p.Observation = &o
	p.ObservationCursor = &domain.VMObservationCursor{SessionID: session, Sequence: o.Sequence}
	var verified domain.VerifiedExecutionPlaneCapabilities
	if matches && confirmed != nil && o.Probe != nil && reflect.DeepEqual(*o.Probe, *confirmed) {
		verified = domain.EffectiveExecutionPlaneCapabilities(p, session, r.now())
	}
	r.mu.Lock()
	if !run.connected || (verified.ProbeSequence > 0 && verified.ProbeSequence <= run.failedSequence) {
		verified = domain.VerifiedExecutionPlaneCapabilities{}
	}
	run.plane = *p
	run.verified = verified
	r.mu.Unlock()
	if err := r.emit(ctx, run, "observed", o.Diagnostic); err != nil {
		return err
	}
	if p.Desired.State == domain.ExecutionPlaneDisabled {
		if matches && !o.Draining {
			return r.repo.ReleaseCapacity(ctx, p.OrgID, uuid.NewSHA1(p.ID, []byte("execution-plane-capacity")))
		}
		return nil
	}
	if o.Availability == domain.VMObservationUnavailable {
		return r.retract(ctx, run, "unavailable", o.Diagnostic)
	}
	if !matches {
		return r.apply(ctx, run)
	}
	if confirmed == nil && o.Probe == nil {
		return r.probe(ctx, run, o.Sequence)
	}
	return nil
}

func (r *ExecutionPlaneReconciler) expire(ctx context.Context, run *planeRun) error {
	r.mu.RLock()
	expires := run.verified.ExpiresAt
	r.mu.RUnlock()
	if !expires.IsZero() && !r.now().Before(expires) {
		return r.retract(ctx, run, "probe_expired", domain.VMDiagnostic{Code: domain.VMErrorUnavailable})
	}
	return nil
}

// VerifiedCapabilities is the sole Loom selection contribution. Recheck current
// desired generation/session, reservation, host and worker admission at selection,
// so a stale in-memory run cannot outlive disablement or a policy update.
func (r *ExecutionPlaneReconciler) VerifiedCapabilities(ctx context.Context, worker string, now time.Time) ([]domain.VerifiedExecutionPlaneCapabilities, error) {
	if r == nil || r.repo == nil || r.workers == nil {
		return nil, planeUnavailable(nil)
	}
	type candidate struct {
		key      planeKey
		verified domain.VerifiedExecutionPlaneCapabilities
	}
	var candidates []candidate
	r.mu.RLock()
	for key, run := range r.runs {
		if run.connected && run.plane.WorkerPubKey == worker && len(run.verified.Capabilities) > 0 && now.Before(run.verified.ExpiresAt) {
			copy := run.verified
			copy.Capabilities = append([]domain.ExecutionPlaneCapability(nil), copy.Capabilities...)
			candidates = append(candidates, candidate{key, copy})
		}
	}
	r.mu.RUnlock()
	if len(candidates) == 0 {
		return nil, nil
	}
	w, err := r.workers.GetByPubKey(ctx, worker)
	if err != nil {
		return nil, err
	}
	if w == nil || w.PubKey != worker {
		return nil, nil
	}
	var out []domain.VerifiedExecutionPlaneCapabilities
	for _, candidate := range candidates {
		p, err := r.repo.ExecutionPlanes().Get(ctx, candidate.key.org, candidate.key.id)
		if err != nil {
			return nil, err
		}
		v := candidate.verified
		if p.WorkerPubKey != worker || p.Generation != v.Generation || p.Desired.State != domain.ExecutionPlaneEnabled || p.ObservationCursor == nil || p.ObservationCursor.SessionID != v.SessionID || w.CurrentQueueDepth >= p.Desired.Concurrency {
			continue
		}
		host, err := r.repo.Hosts().Get(ctx, p.OrgID, p.HostID)
		if err != nil {
			return nil, err
		}
		if host == nil || !host.Enabled {
			continue
		}
		reservations, err := r.repo.ListReservations(ctx, p.OrgID, p.HostID)
		if err != nil {
			return nil, err
		}
		reserved := false
		for _, reservation := range reservations {
			if reservation.ResourceKind == domain.ExecutionPlaneResource && reservation.ResourceID == p.ID && reservation.Capacity == p.Desired.ReservedCapacity {
				reserved = true
			}
		}
		if !reserved || !reflect.DeepEqual(domain.EffectiveExecutionPlaneCapabilities(p, v.SessionID, now), v) {
			continue
		}
		r.mu.RLock()
		live := r.runs[candidate.key]
		stillLive := live != nil && live.connected && reflect.DeepEqual(live.verified, v)
		r.mu.RUnlock()
		if !stillLive {
			continue
		}
		w.VerifiedExecutionPlanes = []domain.VerifiedExecutionPlaneCapabilities{v}
		for _, capability := range v.Capabilities {
			if domain.HasVerifiedExecutionPlaneCapability(*w, capability, now) {
				out = append(out, v)
				break
			}
		}
	}
	return out, nil
}
