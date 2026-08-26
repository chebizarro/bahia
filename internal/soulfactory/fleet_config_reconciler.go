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
type FleetConfigReconciler struct {
	reactor     *Reactor
	concurrency int

	mu     sync.Mutex
	latest *FleetConfigSnapshot
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

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.latest != nil && fleetSnapshotBefore(snapshot, r.latest) {
		return nil
	}
	r.latest = snapshot

	souls, err := r.reactor.listFleetReconcileSouls(ctx)
	if err != nil {
		return fmt.Errorf("list fleet reconcile souls: %w", err)
	}
	eligible := make([]*domain.AgentSoul, 0, len(souls))
	for _, soul := range souls {
		if soul == nil ||
			soul.Status != domain.SoulStatusActive ||
			soul.Runtime.Target != domain.RuntimeTargetOpenClaw ||
			soul.AppliedFleetConfigRevision == snapshot.EventID {
			continue
		}
		eligible = append(eligible, soul)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].AgentID < eligible[j].AgentID })
	if len(eligible) == 0 {
		return nil
	}

	workers := min(r.concurrency, len(eligible))
	jobs := make(chan *domain.AgentSoul)
	errs := make(chan error, len(eligible))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for soul := range jobs {
				if err := r.reconcileSoul(ctx, soul, snapshot); err != nil {
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

func fleetSnapshotBefore(candidate, current *FleetConfigSnapshot) bool {
	if candidate.CreatedAt != current.CreatedAt {
		return candidate.CreatedAt < current.CreatedAt
	}
	return candidate.EventID < current.EventID
}

func drainFleetErrors(errs <-chan error) []error {
	var out []error
	for err := range errs {
		out = append(out, err)
	}
	return out
}

func (r *FleetConfigReconciler) reconcileSoul(ctx context.Context, soul *domain.AgentSoul, next *FleetConfigSnapshot) error {
	action := r.fleetAction(soul, next)
	if err := r.publishProgress(ctx, action, next, "processing", "fleet config reconciliation started"); err != nil {
		return fmt.Errorf("reconcile fleet config for %s: %w", soul.AgentID, err)
	}

	var previous *FleetConfigSnapshot
	var err error
	if soul.AppliedFleetConfigRevision != "" {
		previous, err = r.reactor.getFleetConfigRevision(ctx, soul.AppliedFleetConfigRevision)
		if err != nil {
			return r.failSoul(ctx, action, next, soul, fmt.Errorf("load applied fleet revision %s: %w", soul.AppliedFleetConfigRevision, err), nil)
		}
		if previous == nil {
			return r.failSoul(ctx, action, next, soul, fmt.Errorf("applied fleet revision %s is unavailable", soul.AppliedFleetConfigRevision), nil)
		}
	}

	changed := diffFleetConfigDocuments(previous, next)
	if len(changed) == 0 {
		updated := *soul
		updated.AppliedFleetConfigRevision = next.EventID
		if err := r.reactor.PublishSoul(ctx, &updated); err != nil {
			return r.failSoul(ctx, action, next, soul, fmt.Errorf("record unchanged fleet revision: %w", err), nil)
		}
		return r.completeSoul(ctx, action, next, soul, changed, "unchanged")
	}

	adapter, err := r.openClawAdapter()
	if err != nil {
		return r.failSoul(ctx, action, next, soul, err, nil)
	}
	if err := r.publishProgress(ctx, action, next, "processing", "applying fleet config via soulfactory.config.reload"); err != nil {
		return fmt.Errorf("reconcile fleet config for %s: %w", soul.AgentID, err)
	}
	applyReq := r.runtimeRequest(soul, next, next, "apply")
	result, applyErr := adapter.Execute(ctx, applyReq)
	if applyErr == nil {
		applyErr = fleetRuntimeResultError(result)
	}
	if applyErr != nil {
		rollbackErr := r.rollbackSoul(ctx, adapter, action, soul, next, previous)
		return r.failSoul(ctx, action, next, soul, fmt.Errorf("apply fleet config: %w", applyErr), rollbackErr)
	}

	updated := *soul
	updated.AppliedFleetConfigRevision = next.EventID
	if err := r.reactor.PublishSoul(ctx, &updated); err != nil {
		rollbackErr := r.rollbackSoul(ctx, adapter, action, soul, next, previous)
		return r.failSoul(ctx, action, next, soul, fmt.Errorf("publish applied fleet revision: %w", err), rollbackErr)
	}
	return r.completeSoul(ctx, action, next, soul, changed, "applied")
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
	if err != nil {
		return err
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
) error {
	event := BuildActionStatusEvent(action, status, message, normalizeSoulLookupRef(action.SoulRef))
	setFleetReconcileTags(event, snapshot)
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
	if rollbackErr == nil && (strings.Contains(cause.Error(), "apply fleet config") || strings.Contains(cause.Error(), "publish applied fleet revision")) {
		rollbackStatus = "completed"
	} else if rollbackErr != nil {
		rollbackStatus = "failed"
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
	if r.relayBus == nil {
		return nil, fmt.Errorf("Soul Factory relay bus is not configured")
	}
	factory, err := nostr.PubKeyFromHex(strings.TrimSpace(r.config.SoulFactoryPubkey))
	if err != nil {
		return nil, fmt.Errorf("invalid Soul Factory pubkey for fleet reconciliation: %w", err)
	}
	events, err := r.relayBus.Query(ctx, []nostr.Filter{{
		Kinds:   []nostr.Kind{nostr.Kind(domain.KindAgentSoul)},
		Authors: []nostr.PubKey{factory},
		Limit:   maxFleetReconcileSouls,
	}})
	if err != nil {
		return nil, err
	}
	latest := make(map[string]*nostr.Event)
	for _, event := range events {
		if event == nil {
			continue
		}
		agentID := tagValue(event.Tags, tagParameterizedD)
		current := latest[agentID]
		if current == nil || event.CreatedAt > current.CreatedAt ||
			(event.CreatedAt == current.CreatedAt && event.ID.Hex() > current.ID.Hex()) {
			latest[agentID] = event
		}
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
	if r.relayBus == nil {
		return nil, fmt.Errorf("Soul Factory relay bus is not configured")
	}
	id, err := nostr.IDFromHex(strings.TrimSpace(eventID))
	if err != nil {
		return nil, fmt.Errorf("invalid fleet config revision id: %w", err)
	}
	events, err := r.relayBus.Query(ctx, []nostr.Filter{{
		IDs:   []nostr.ID{id},
		Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulFleetConfig)},
		Limit: 1,
	}})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	return ParseFleetConfigEvent(events[0], r.config.AuthorizedPubkeys)
}
