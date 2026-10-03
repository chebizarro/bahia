package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	defaultFleetReconcileConcurrency = 4
	maxFleetReconcileSouls           = 1000
)

// FleetConfigReconciler applies a trusted fleet-config revision to deployed
// OpenClaw souls. Revisions are serialized while soul work is bounded and
// independent, so a slow older rollout cannot overtake a newer revision.
//
// Each soul is reconciled holding it (soulOperationGate), the same hold
// lifecycle actions take, so a fleet reload and a lifecycle action never drive
// one soul's runtime or rewrite its kind:31951 at the same time. A soul held
// by other work is deferred: once that work finishes, the soul is re-driven to
// the latest revision, re-reading the soul first so the other work's changes
// are kept.
//
// An apply whose runtime result was not observed within the wait is neither
// applied nor failed: the soul is awaiting_terminal, nothing is rolled back,
// and the late result is reconciled when the reactor observes it (see
// runtimeResultWaiters). The apply keeps the soul meanwhile, so newer
// revisions and lifecycle actions wait behind it and a runtime never has two
// requests in flight; revisions reach it in order.
type FleetConfigReconciler struct {
	reactor     *Reactor
	concurrency int

	// mu serializes revisions.
	mu sync.Mutex

	latestMu sync.Mutex
	latest   *FleetConfigSnapshot
}

func NewFleetConfigReconciler(reactor *Reactor, concurrency int) *FleetConfigReconciler {
	if concurrency <= 0 {
		concurrency = defaultFleetReconcileConcurrency
	}
	return &FleetConfigReconciler{reactor: reactor, concurrency: concurrency}
}

// Reconcile fans one exact fleet revision out to every affected deployed soul.
// Per-soul failures are reported after all eligible souls have reached a
// terminal state; successful souls are not rolled back because another soul
// failed.
func (r *FleetConfigReconciler) Reconcile(ctx context.Context, snapshot *FleetConfigSnapshot) error {
	if r == nil || r.reactor == nil {
		return fmt.Errorf("fleet config reconciler is not configured")
	}
	if snapshot == nil || strings.TrimSpace(snapshot.EventID) == "" {
		return fmt.Errorf("fleet config revision is required")
	}

	// The fleet lock serializes revision admission: the latest-check, the
	// latest-update, and the soul listing are atomic. The lock is released
	// before per-soul fan-out so the fleet lock is not held across runtime
	// calls and deferred lifecycle work that the soul gate runs inline after
	// a fleet operation finishes (bahia-irsry.57). Per-soul ordering is
	// already guaranteed by soulOperationGate; revision ordering is preserved
	// because the re-drive reads latestRevision() and a newer revision's
	// per-soul work is deferred behind an older one's by the gate.
	r.mu.Lock()
	if latest := r.latestRevision(); latest != nil && fleetSnapshotBefore(snapshot, latest) {
		r.mu.Unlock()
		return nil
	}
	r.latestMu.Lock()
	r.latest = snapshot
	r.latestMu.Unlock()

	souls, err := r.reactor.listFleetReconcileSouls(ctx)
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("list fleet reconcile souls: %w", err)
	}
	eligible := make([]*domain.AgentSoul, 0, len(souls))
	for _, soul := range souls {
		if fleetReconcileEligible(soul, snapshot) {
			eligible = append(eligible, soul)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].AgentID < eligible[j].AgentID })
	if len(eligible) == 0 {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	// Per-soul work fans out below without holding the fleet lock.
	workers := min(r.concurrency, len(eligible))
	jobs := make(chan *domain.AgentSoul)
	errs := make(chan error, len(eligible))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for soul := range jobs {
				if err := r.reconcileGated(ctx, soul.AgentID, snapshot); err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, soul := range eligible {
		select {
		case jobs <- soul:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			close(errs)
			return errors.Join(ctx.Err(), errors.Join(drainFleetErrors(errs)...))
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	return errors.Join(drainFleetErrors(errs)...)
}

func fleetReconcileEligible(soul *domain.AgentSoul, snapshot *FleetConfigSnapshot) bool {
	return soul != nil &&
		soul.Status == domain.SoulStatusActive &&
		soul.Runtime.Target == domain.RuntimeTargetOpenClaw &&
		soul.AppliedFleetConfigRevision != snapshot.EventID
}

// latestRevision is the newest revision Reconcile has taken up, if any.
func (r *FleetConfigReconciler) latestRevision() *FleetConfigSnapshot {
	r.latestMu.Lock()
	defer r.latestMu.Unlock()
	return r.latest
}

// reconcileGated applies snapshot to agentID holding the soul. While other
// work holds the soul it defers a re-drive to the latest revision instead.
func (r *FleetConfigReconciler) reconcileGated(ctx context.Context, agentID string, snapshot *FleetConfigSnapshot) error {
	var reconcileErr error
	now := soulOperation{key: fleetOperationKey(snapshot.EventID), run: func(ctx context.Context, hold *soulHold) bool {
		parked, err := r.reconcileHeld(ctx, hold, agentID, snapshot)
		reconcileErr = err
		return parked
	}}
	if r.reactor.soulOperations().do(ctx, agentID, now, r.redriveOperation(agentID)) == soulOperationDeferred {
		r.reactor.logger.Info("fleet config revision deferred for soul held by another operation; it is re-driven to the latest revision once that finishes",
			"agent_id", agentID, "fleet_revision", snapshot.EventID)
	}
	return reconcileErr
}

// redriveOperation re-drives agentID to the latest fleet revision: deferred
// behind other work on the soul, or after a restart.
func (r *FleetConfigReconciler) redriveOperation(agentID string) soulOperation {
	return soulOperation{key: fleetRedriveOperationKey, run: func(ctx context.Context, hold *soulHold) bool {
		logger := r.reactor.logger.With("agent_id", agentID)
		latest := r.latestRevision()
		if latest == nil {
			var err error
			if latest, err = r.reactor.getProvisioningFleetConfig(ctx); err != nil {
				logger.Warn("cannot re-drive soul to the latest fleet revision; the next revision or a restart re-drives it", "error", err)
				return false
			}
		}
		if latest == nil {
			return false
		}
		parked, err := r.reconcileHeld(ctx, hold, agentID, latest)
		if err != nil {
			logger.Error("deferred fleet config reconciliation failed", "latest_revision", latest.EventID, "error", err)
		}
		return parked
	}}
}

// reconcileHeld applies snapshot to agentID, which hold holds. It re-reads the
// soul: work that held it before (a lifecycle action, an earlier revision) may
// have republished it since the soul was listed.
func (r *FleetConfigReconciler) reconcileHeld(ctx context.Context, hold *soulHold, agentID string, snapshot *FleetConfigSnapshot) (bool, error) {
	soul, err := r.reactor.GetSoul(ctx, agentID)
	if err != nil {
		return false, fmt.Errorf("reconcile fleet config for %s: read soul: %w", agentID, err)
	}
	if !fleetReconcileEligible(soul, snapshot) {
		return false, nil
	}
	return r.reconcileSoul(ctx, hold, soul, snapshot)
}

func fleetSnapshotBefore(candidate, current *FleetConfigSnapshot) bool {
	if candidate.CreatedAt != current.CreatedAt {
		return candidate.CreatedAt < current.CreatedAt
	}
	return candidate.EventID > current.EventID
}

func drainFleetErrors(errs <-chan error) []error {
	var out []error
	for err := range errs {
		out = append(out, err)
	}
	return out
}

// reconcileSoul applies next to soul, which hold holds. parked reports that
// the apply is awaiting its runtime terminal result and keeps the hold.
func (r *FleetConfigReconciler) reconcileSoul(ctx context.Context, hold *soulHold, soul *domain.AgentSoul, next *FleetConfigSnapshot) (parked bool, _ error) {
	action := r.fleetAction(soul, next)
	if err := r.publishProgress(ctx, action, next, "processing", "fleet config reconciliation started"); err != nil {
		return false, fmt.Errorf("reconcile fleet config for %s: %w", soul.AgentID, err)
	}

	var previous *FleetConfigSnapshot
	var err error
	if soul.AppliedFleetConfigRevision != "" {
		previous, err = r.reactor.getFleetConfigRevision(ctx, soul.AppliedFleetConfigRevision)
		if err != nil {
			return false, r.failSoul(ctx, action, next, soul, fmt.Errorf("load applied fleet revision %s: %w", soul.AppliedFleetConfigRevision, err), nil)
		}
		if previous == nil {
			return false, r.failSoul(ctx, action, next, soul, fmt.Errorf("applied fleet revision %s is unavailable", soul.AppliedFleetConfigRevision), nil)
		}
	}

	changed := diffFleetConfigDocuments(previous, next)
	if len(changed) == 0 {
		updated := *soul
		updated.AppliedFleetConfigRevision = next.EventID
		if err := r.reactor.PublishSoul(ctx, &updated); err != nil {
			return false, r.failSoul(ctx, action, next, soul, fmt.Errorf("record unchanged fleet revision: %w", err), nil)
		}
		return false, r.completeSoul(ctx, action, next, soul, changed, "unchanged")
	}

	adapter, err := r.openClawAdapter()
	if err != nil {
		return false, r.failSoul(ctx, action, next, soul, err, nil)
	}
	if err := r.publishProgress(ctx, action, next, "processing", "applying fleet config via soulfactory.config.reload"); err != nil {
		return false, fmt.Errorf("reconcile fleet config for %s: %w", soul.AgentID, err)
	}
	applyReq := r.runtimeRequest(soul, next, next, "apply")
	result, applyErr := adapter.Execute(ctx, applyReq)
	if pending, unknown := runtimeOutcomeUnknown(applyErr); unknown {
		return r.awaitApply(ctx, hold, adapter, action, soul, next, previous, changed, pending, applyErr)
	}
	_, err = r.finishApply(ctx, adapter, action, soul, next, previous, changed, result, applyErr)
	return false, err
}

// finishApply applies the apply request's terminal result, observed in time or
// late: success records the revision on the soul, an observed failure rolls
// the runtime back. It returns the soul as now recorded.
func (r *FleetConfigReconciler) finishApply(
	ctx context.Context,
	adapter RuntimeAdapter,
	action *domain.SoulAction,
	soul *domain.AgentSoul,
	next, previous *FleetConfigSnapshot,
	changed []string,
	result *RuntimeControlResultEnvelope,
	applyErr error,
) (*domain.AgentSoul, error) {
	if applyErr == nil {
		applyErr = fleetRuntimeResultError(result)
	}
	if applyErr != nil {
		rollbackErr := r.rollbackSoul(ctx, adapter, action, soul, next, previous)
		return soul, r.failSoul(ctx, action, next, soul, fmt.Errorf("apply fleet config: %w", applyErr), rollbackErr)
	}

	updated := *soul
	updated.AppliedFleetConfigRevision = next.EventID
	if err := r.reactor.PublishSoul(ctx, &updated); err != nil {
		rollbackErr := r.rollbackSoul(ctx, adapter, action, soul, next, previous)
		return soul, r.failSoul(ctx, action, next, soul, fmt.Errorf("publish applied fleet revision: %w", err), rollbackErr)
	}
	return &updated, r.completeSoul(ctx, action, next, soul, changed, "applied")
}

// awaitApply parks an apply whose runtime result was not observed: the soul is
// awaiting_terminal, with no rollback and no terminal result, until the
// reactor delivers the late result to resumeApply. The apply keeps its hold on
// the soul meanwhile. parked reports that it is awaiting.
func (r *FleetConfigReconciler) awaitApply(
	ctx context.Context,
	hold *soulHold,
	adapter RuntimeAdapter,
	action *domain.SoulAction,
	soul *domain.AgentSoul,
	next, previous *FleetConfigSnapshot,
	changed []string,
	pending *runtimeResultPending,
	cause error,
) (parked bool, _ error) {
	logger := r.reactor.logger.With("agent_id", soul.AgentID, "fleet_revision", next.EventID)
	message := fmt.Sprintf("%s: fleet config apply outcome unknown: %v; no rollback without an observed runtime failure", actionStatusAwaitingTerminal, cause)
	if err := r.publishProgress(ctx, action, next, actionStatusAwaitingTerminal, message, awaitingTerminalTags()...); err != nil {
		logger.Warn("failed to publish fleet awaiting_terminal progress", "error", err)
	}
	if pending == nil {
		logger.Warn("fleet config apply outcome unknown and not correlatable; the soul is re-driven by the next revision or restart", "error", cause)
		return false, nil
	}
	late, observed := r.reactor.resultWaiters().park(pending, parkedOperation{
		shardKey:      soul.AgentID,
		holdsSoul:     hold != nil,
		actionEventID: action.EventID,
		requestKind:   domain.KindSoulFleetConfig,
		resume: func(ctx context.Context, late *RuntimeControlResultEnvelope) {
			r.resumeApply(ctx, hold, adapter, action, soul, next, previous, changed, late)
		},
	})
	if !observed {
		logger.Info("fleet config apply awaiting runtime terminal result", "request_event", pending.requestID())
		return true, nil
	}
	_, err := r.finishApply(ctx, adapter, action, soul, next, previous, changed, late, nil)
	return false, err
}

// resumeApply reconciles a late apply result exactly as a timely one, then
// releases the soul. Work deferred behind the apply runs next; a newer
// revision deferred meanwhile re-drives the soul to the latest revision.
func (r *FleetConfigReconciler) resumeApply(
	ctx context.Context,
	hold *soulHold,
	adapter RuntimeAdapter,
	action *domain.SoulAction,
	soul *domain.AgentSoul,
	next, previous *FleetConfigSnapshot,
	changed []string,
	late *RuntimeControlResultEnvelope,
) {
	defer r.reactor.soulOperations().release(ctx, hold)
	if _, err := r.finishApply(ctx, adapter, action, soul, next, previous, changed, late, nil); err != nil {
		r.reactor.logger.Error("fleet config reconciliation failed after late runtime result",
			"agent_id", soul.AgentID, "fleet_revision", next.EventID, "error", err)
	}
}

func fleetRuntimeResultError(result *RuntimeControlResultEnvelope) error {
	if result == nil {
		return fmt.Errorf("runtime returned no config reload result")
	}
	if result.Status != "success" {
		if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
			return fmt.Errorf("runtime config reload status %s: %s", result.Status, result.Error.Message)
		}
		return fmt.Errorf("runtime config reload status %s", result.Status)
	}
	return nil
}

func (r *FleetConfigReconciler) rollbackSoul(
	ctx context.Context,
	adapter RuntimeAdapter,
	action *domain.SoulAction,
	soul *domain.AgentSoul,
	failed, previous *FleetConfigSnapshot,
) error {
	if err := r.publishProgress(ctx, action, failed, "processing", "rolling back fleet config via soulfactory.config.reload"); err != nil {
		return fmt.Errorf("publish rollback progress: %w", err)
	}
	result, err := adapter.Execute(ctx, r.runtimeRequest(soul, failed, previous, "rollback"))
	result, err = r.reactor.resultWaiters().observeLateRollback(soul.AgentID, result, err, func(ctx context.Context, late *RuntimeControlResultEnvelope) {
		status, message := lateRollbackProgress("rollback fleet config", late)
		if err := r.publishProgress(ctx, action, failed, actionStatusRollbackResolved, message, nostr.Tag{tagRollbackStatus, status}); err != nil {
			r.reactor.logger.Warn("failed to publish late fleet rollback progress", "agent_id", soul.AgentID, "fleet_revision", failed.EventID, "error", err)
		}
	})
	if err != nil {
		return rollbackStepError("rollback fleet config", err)
	}
	return fleetRuntimeResultError(result)
}

func (r *FleetConfigReconciler) runtimeRequest(
	soul *domain.AgentSoul,
	revision *FleetConfigSnapshot,
	value *FleetConfigSnapshot,
	phase string,
) RuntimeAdapterRequest {
	patch := map[string]interface{}{"fleet_config": value}
	return RuntimeAdapterRequest{
		Method: RuntimeMethodConfigReload,
		Operator: RuntimeOperatorRef{
			Pubkey:       revision.Author,
			RequestEvent: revision.EventID,
		},
		Soul: RuntimeSoulRef{
			ID:       soul.AgentID,
			Draft:    firstNonEmpty(soul.DraftEventID, soul.DraftRef),
			SpecHash: soul.SpecHash,
		},
		Target: RuntimeTargetRef{
			Runtime:       domain.RuntimeTargetOpenClaw,
			RuntimePubkey: soul.Runtime.RuntimePubkey,
			AgentID:       soul.AgentID,
		},
		Params: map[string]interface{}{
			"schema":             SoulFactoryConfigReloadSchema,
			"target_fields":      []string{"fleet_config"},
			"patch":              patch,
			"previous_spec_hash": soul.SpecHash,
			"new_spec_hash":      soul.SpecHash,
		},
		DraftPolicy: soul.RelayPolicy,
		RequestKind: domain.KindSoulFleetConfig,
		Action:      fleetRuntimeAction(phase),
		IdempotencyKey: runtimeIdempotencyKey(
			r.reactor.config.SoulFactoryPubkey,
			RuntimeMethodConfigReload,
			revision.EventID+":"+phase,
			soul.Runtime.RuntimePubkey,
			soul.AgentID,
			soul.SpecHash,
		),
	}
}

func fleetRuntimeAction(phase string) domain.SoulActionType {
	if phase == "rollback" {
		return domain.SoulActionRollback
	}
	return domain.SoulActionHotReload
}

func (r *FleetConfigReconciler) openClawAdapter() (RuntimeAdapter, error) {
	handler := r.reactor.lifecycle()
	adapters := handler.runtimeAdapters
	if len(adapters) == 0 {
		if full, ok := r.reactor.provisioner.(*FullProvisioner); ok && full != nil {
			adapters = full.runtimeAdapters
		}
	}
	adapter := adapters[domain.RuntimeTargetOpenClaw]
	if adapter == nil {
		return nil, fmt.Errorf("fleet config reconciliation requires an OpenClaw runtime adapter")
	}
	return adapter, nil
}

func (r *FleetConfigReconciler) fleetAction(soul *domain.AgentSoul, snapshot *FleetConfigSnapshot) *domain.SoulAction {
	return &domain.SoulAction{
		EventID:   snapshot.EventID,
		SoulRef:   parameterizedCoordinate(domain.KindAgentSoul, r.reactor.config.SoulFactoryPubkey, soul.AgentID),
		Action:    domain.SoulActionHotReload,
		Initiator: snapshot.Author,
	}
}

func (r *FleetConfigReconciler) publishProgress(
	ctx context.Context,
	action *domain.SoulAction,
	snapshot *FleetConfigSnapshot,
	status, message string,
	extra ...nostr.Tag,
) error {
	event := BuildActionStatusEvent(action, status, message, normalizeSoulLookupRef(action.SoulRef))
	setFleetReconcileTags(event, snapshot)
	event.Tags = append(event.Tags, extra...)
	if err := r.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign fleet reconciliation progress: %w", err)
	}
	return r.reactor.publish(ctx, event, r.reactor.provisioningPublicationRelays())
}

func (r *FleetConfigReconciler) completeSoul(
	ctx context.Context,
	action *domain.SoulAction,
	snapshot *FleetConfigSnapshot,
	soul *domain.AgentSoul,
	changed []string,
	status string,
) error {
	if err := r.publishProgress(ctx, action, snapshot, "completed", "fleet config reconciliation "+status); err != nil {
		return fmt.Errorf("publish fleet completion progress for %s: %w", soul.AgentID, err)
	}
	data := map[string]interface{}{
		"soul_ref":         action.SoulRef,
		"agent_id":         soul.AgentID,
		"fleet_revision":   snapshot.EventID,
		"fleet_status":     status,
		"changed_sections": changed,
	}
	return r.publishResult(ctx, action, snapshot, "completed", data, soul.AgentID)
}

func (r *FleetConfigReconciler) failSoul(
	ctx context.Context,
	action *domain.SoulAction,
	snapshot *FleetConfigSnapshot,
	soul *domain.AgentSoul,
	cause, rollbackErr error,
) error {
	rollbackStatus := "not-required"
	switch {
	case errors.Is(rollbackErr, ErrNoTerminalResult):
		rollbackStatus = rollbackStatusOutcomeUnknown
	case rollbackErr != nil:
		rollbackStatus = "failed"
	case strings.Contains(cause.Error(), "apply fleet config") || strings.Contains(cause.Error(), "publish applied fleet revision"):
		rollbackStatus = "completed"
	}
	data := map[string]interface{}{
		"soul_ref":        action.SoulRef,
		"agent_id":        soul.AgentID,
		"fleet_revision":  snapshot.EventID,
		"fleet_status":    "failed",
		"rollback_status": rollbackStatus,
		"error":           cause.Error(),
	}
	if rollbackErr != nil {
		data["rollback_error"] = rollbackErr.Error()
	}
	publishErr := r.publishResult(ctx, action, snapshot, "error", data, soul.AgentID)
	return fmt.Errorf("reconcile fleet config for %s: %w", soul.AgentID, errors.Join(cause, rollbackErr, publishErr))
}

func (r *FleetConfigReconciler) publishResult(
	ctx context.Context,
	action *domain.SoulAction,
	snapshot *FleetConfigSnapshot,
	status string,
	data map[string]interface{},
	agentID string,
) error {
	event, err := BuildActionResultEvent(action, status, data, ActionResultCanonical, agentID)
	if err != nil {
		return err
	}
	setFleetReconcileTags(event, snapshot)
	if err := r.reactor.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign fleet reconciliation result: %w", err)
	}
	return r.reactor.publish(ctx, event, r.reactor.provisioningPublicationRelays())
}

func setFleetReconcileTags(event *nostr.Event, snapshot *FleetConfigSnapshot) {
	if event == nil || snapshot == nil {
		return
	}
	setTagValue(&event.Tags, tagRequestKind, strconv.Itoa(domain.KindSoulFleetConfig))
	appendTag(&event.Tags, tagFleetRevision, snapshot.EventID)
	appendTag(&event.Tags, tagFleetConfig, snapshot.Coordinate)
	appendTag(&event.Tags, tagMethod, RuntimeMethodConfigReload)
}

func setTagValue(tags *nostr.Tags, key, value string) {
	for i, tag := range *tags {
		if len(tag) > 0 && tag[0] == key {
			(*tags)[i] = nostr.Tag{key, value}
			return
		}
	}
	appendTag(tags, key, value)
}

func diffFleetConfigDocuments(previous, next *FleetConfigSnapshot) []string {
	if next == nil {
		return nil
	}
	if previous == nil {
		sections := make([]string, 0, len(next.Document.Template)+1)
		for section := range next.Document.Template {
			sections = append(sections, section)
		}
		if !reflect.DeepEqual(next.Document.Defaults, FleetConfigDefaults{}) {
			sections = append(sections, "defaults")
		}
		sort.Strings(sections)
		return sections
	}
	sections := make(map[string]struct{})
	for section, nextValue := range next.Document.Template {
		if !reflect.DeepEqual(previous.Document.Template[section], nextValue) {
			sections[section] = struct{}{}
		}
	}
	for section := range previous.Document.Template {
		if _, exists := next.Document.Template[section]; !exists {
			sections[section] = struct{}{}
		}
	}
	if !reflect.DeepEqual(previous.Document.Defaults, next.Document.Defaults) {
		sections["defaults"] = struct{}{}
	}
	out := make([]string, 0, len(sections))
	for section := range sections {
		out = append(out, section)
	}
	sort.Strings(out)
	return out
}

func (r *Reactor) listFleetReconcileSouls(ctx context.Context) ([]*domain.AgentSoul, error) {
	if r.listSoulsFn != nil {
		return r.listSoulsFn(ctx)
	}
	if r.relayClient == nil {
		return nil, fmt.Errorf("soul Factory relay client is not configured")
	}
	factory, err := nostr.PubKeyFromHex(strings.TrimSpace(r.config.SoulFactoryPubkey))
	if err != nil {
		return nil, fmt.Errorf("invalid Soul Factory pubkey for fleet reconciliation: %w", err)
	}
	// Fail closed: reconcile republishes every soul it lists, so a missing or
	// stale soul would be skipped or overwritten. See RelayReadPolicy.
	read, err := r.relayClient.QueryWithPolicy(ctx, "reactor.fleet_reconcile_souls", RelayReadComplete(), []nostr.Filter{{
		Kinds:   []nostr.Kind{nostr.Kind(domain.KindAgentSoul)},
		Authors: []nostr.PubKey{factory},
		Limit:   maxFleetReconcileSouls,
	}})
	if err != nil {
		return nil, err
	}
	latest := make(map[string]*nostr.Event)
	for _, event := range read.Events {
		if event == nil {
			continue
		}
		agentID := tagValue(event.Tags, tagParameterizedD)
		latest[agentID] = newerRelayEvent(latest[agentID], event)
	}
	souls := make([]*domain.AgentSoul, 0, len(latest))
	for _, event := range latest {
		if soul := ParseAgentSoulEvent(event); soul != nil {
			souls = append(souls, soul)
		}
	}
	return souls, nil
}

func (r *Reactor) getFleetConfigRevision(ctx context.Context, eventID string) (*FleetConfigSnapshot, error) {
	if r.getFleetConfigRevisionFn != nil {
		return r.getFleetConfigRevisionFn(ctx, eventID)
	}
	if r.relayClient == nil {
		return nil, fmt.Errorf("soul Factory relay client is not configured")
	}
	id, err := nostr.IDFromHex(strings.TrimSpace(eventID))
	if err != nil {
		return nil, fmt.Errorf("invalid fleet config revision id: %w", err)
	}
	read, err := r.relayClient.QueryWithPolicy(ctx, "reactor.fleet_config_revision", RelayReadAllIDs(id), []nostr.Filter{{
		IDs:   []nostr.ID{id},
		Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulFleetConfig)},
		Limit: 1,
	}})
	if err != nil {
		return nil, err
	}
	for _, event := range read.Events {
		if event != nil && event.ID == id {
			return ParseFleetConfigEvent(event, r.config.AuthorizedPubkeys)
		}
	}
	return nil, nil
}
