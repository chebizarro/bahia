package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const autoApplyReceiptFamily = "canonical-auto-apply-v1"

var ErrFreshDecisionRequired = errors.New("canonical auto-apply snapshot changed; fresh decision required")
var ErrAutoApplyRecoveryRequired = errors.New("canonical auto-apply effect cannot be safely classified; operator recovery required")

// ApplySnapshot is the exact, secret-value-free input selected under the effect fence.
// Empty optional route/release revisions mean that no such effect was selected.
// Every populated event ID identifies a specific locally read canonical winner.
type ApplySnapshot struct {
	IntentID           uuid.UUID                 `json:"intent_id"`
	DecisionEventID    string                    `json:"decision_event_id"`
	DecisionCoordinate string                    `json:"decision_coordinate"`
	DecisionActor      string                    `json:"decision_actor"`
	ServiceEventID     string                    `json:"service_event_id"`
	StateEventID       string                    `json:"state_event_id"`
	IntentEventID      string                    `json:"intent_event_id"`
	EnvironmentEventID string                    `json:"environment_event_id"`
	ArtifactEventID    string                    `json:"artifact_event_id"`
	RouteEventID       string                    `json:"route_event_id,omitempty"`
	RouteUpdatedAt     time.Time                 `json:"route_updated_at,omitzero"`
	ReleaseEventID     string                    `json:"release_event_id,omitempty"`
	ReleaseUpdatedAt   time.Time                 `json:"release_updated_at,omitzero"`
	Service            domain.Service            `json:"service"`
	Environment        domain.Environment        `json:"environment"`
	Unit               domain.DeploymentUnit     `json:"unit"`
	Spec               domain.DesiredServiceSpec `json:"spec"`
	SecretPins         []ApplySecretPin          `json:"secret_pins,omitempty"`
	Tool               *ToolEffectInput          `json:"tool,omitempty"`
	LLM                *LLMEffectInput           `json:"llm,omitempty"`
}

type ApplySecretPin struct {
	SecretID     uuid.UUID `json:"secret_id"`
	EnvVar       string    `json:"env_var"`
	CredentialID string    `json:"credential_id"`
	Version      int       `json:"version"`
}

func (s ApplySnapshot) digest() (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// AutoApplySnapshotSource must read and validate only local signed canonical
// records. It is called after the environment lock is acquired, including on
// replay and immediately before the external effect.
type AutoApplySnapshotSource interface {
	LoadAutoApplySnapshot(context.Context, uuid.UUID, uuid.UUID) (ApplySnapshot, error)
	ResolvePinnedSecrets(context.Context, ApplySnapshot) (map[string]string, func(), error)
}

// AutoApplyEffect is deliberately one revision-fenced runtime step. Observe
// must be authoritative: an uncertain or partial outcome is an error, never
// an invitation to blindly repeat Apply.
type AutoApplyEffect interface {
	Observe(context.Context, ApplySnapshot) (bool, *domain.RuntimeObservation, error)
	Apply(context.Context, ApplySnapshot, map[string]string) error
}

type autoApplyReceipt struct {
	Snapshot    ApplySnapshot `json:"snapshot"`
	Digest      string        `json:"digest"`
	Phase       string        `json:"phase"`
	Sequence    int           `json:"sequence"`
	Checkpoint  nostr.Event   `json:"checkpoint"`
	Observation string        `json:"observation_id,omitempty"`
}

// CanonicalAutoApplyExecutor persists a non-PG decision/effect journal beside
// the outbox. A signed checkpoint is retained in that journal before enqueue;
// replay always re-enqueues the same event and never signs a duplicate.
type CanonicalAutoApplyExecutor struct {
	Source  AutoApplySnapshotSource
	Effect  AutoApplyEffect
	Lock    service.EnvironmentApplyTryLocker
	Outbox  *localstore.Outbox
	Signer  nostr.Signer
	Enqueue func(context.Context, nostr.Event) error
	Ready   <-chan struct{}
	// Family and StatusScope isolate other canonical, revision-fenced steps
	// from Docker auto-apply. Empty values retain the original lifecycle.
	Family      string
	StatusScope string
}

func (e *CanonicalAutoApplyExecutor) family() string {
	if e != nil && e.Family != "" {
		return e.Family
	}
	return autoApplyReceiptFamily
}

func (e *CanonicalAutoApplyExecutor) statusScope() string {
	if e != nil && e.StatusScope != "" {
		return e.StatusScope
	}
	return "auto-apply"
}

func (e *CanonicalAutoApplyExecutor) Name() string { return "canonical-auto-apply-recovery" }

// Run repairs a crash-stranded signed checkpoint and then resumes incomplete
// effects after local canonical catch-up. Relay delivery itself is owned by
// the outbox runner, not by a timer or another runtime apply.
func (e *CanonicalAutoApplyExecutor) Run(ctx context.Context) error {
	if e.Ready != nil {
		select {
		case <-ctx.Done():
			return nil
		case <-e.Ready:
		}
	}
	return e.ReconcileReceipts(ctx)
}

func (e *CanonicalAutoApplyExecutor) ReconcileReceipts(ctx context.Context) error {
	if e == nil || e.Outbox == nil {
		return fmt.Errorf("canonical auto-apply receipt store is unavailable")
	}
	rows, err := e.Outbox.ListControlRecords(e.family())
	if err != nil {
		return err
	}
	var failures []error
	for _, raw := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		var record autoApplyReceipt
		if err := json.Unmarshal(raw, &record); err != nil || record.Snapshot.IntentID == uuid.Nil ||
			!record.Checkpoint.CheckID() || !record.Checkpoint.VerifySignature() {
			failures = append(failures, fmt.Errorf("invalid canonical auto-apply recovery receipt"))
			continue
		}
		if err := e.RecoverReceipt(ctx, record.Snapshot.IntentID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// RecoverReceipt re-enqueues the exact signed checkpoint before attempting to
// resume one nonterminal effect. Callers with multiple step-specific sources
// can select the source from the receipt ID, then invoke this method once.
func (e *CanonicalAutoApplyExecutor) RecoverReceipt(ctx context.Context, id uuid.UUID) error {
	record, err := e.readReceipt(id)
	if err != nil {
		return fmt.Errorf("read canonical effect receipt %s: %w", id, err)
	}
	if record == nil {
		return fmt.Errorf("canonical effect receipt %s is absent", id)
	}
	if err := e.deliverCheckpoint(ctx, record.Checkpoint); err != nil {
		return fmt.Errorf("receipt %s delivery: %w", id, err)
	}
	switch record.Phase {
	case "terminal", "refused", "superseded", "recovery_required":
		return nil
	}
	_, err = e.AutoRemediateDesiredState(ctx, record.Snapshot.Spec.ServiceID,
		record.Snapshot.Spec.EnvironmentID, nil)
	if err != nil {
		return fmt.Errorf("receipt %s resume: %w", id, err)
	}
	return nil
}

func (e *CanonicalAutoApplyExecutor) AutoRemediateDesiredState(ctx context.Context, serviceID, envID uuid.UUID, statusFn service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	if e == nil || e.Source == nil || e.Effect == nil || e.Lock == nil || e.Outbox == nil || e.Signer == nil || e.Enqueue == nil {
		return nil, fmt.Errorf("bahia-4f10q.2: canonical auto-apply executor is not configured")
	}
	unlock, acquired, err := e.Lock.TryLock(ctx, envID)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, service.ErrEnvironmentApplyLockContended
	}
	defer unlock()
	if statusFn != nil {
		statusFn(ctx, service.DeployStepLockingEnvironment, "Canonical apply fence acquired")
	}
	snapshot, err := e.Source.LoadAutoApplySnapshot(ctx, serviceID, envID)
	if err != nil {
		if errors.Is(err, ErrFreshDecisionRequired) {
			return nil, errors.Join(err, e.refuseStaleReceipts(ctx, serviceID, envID))
		}
		return nil, err
	}
	if snapshot.IntentID == uuid.Nil || snapshot.DecisionEventID == "" || snapshot.DecisionActor == "" ||
		snapshot.Spec.ServiceID != serviceID || snapshot.Spec.EnvironmentID != envID {
		return nil, fmt.Errorf("bahia-4f10q.2: incomplete signed decision snapshot")
	}
	digest, err := snapshot.digest()
	if err != nil {
		return nil, err
	}
	receipt, err := e.readReceipt(snapshot.IntentID)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		receipt, err = e.checkpoint(ctx, snapshot, digest, "", "accepted", "")
		if err != nil {
			return nil, err
		}
	} else if receipt.Digest != digest || receipt.Snapshot.DecisionEventID != snapshot.DecisionEventID {
		if receipt.Phase == "accepted" {
			return nil, e.refuse(ctx, receipt, ErrFreshDecisionRequired)
		}
		return nil, ErrFreshDecisionRequired
	}
	if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
		return nil, err
	}
	if receipt.Phase == "terminal" {
		return nil, nil
	}
	if receipt.Phase == "refused" {
		return nil, ErrFreshDecisionRequired
	}
	if receipt.Phase == "recovery_required" {
		return nil, ErrAutoApplyRecoveryRequired
	}
	if receipt.Phase == "superseded" {
		return nil, ErrFreshDecisionRequired
	}
	applied, observation, err := e.Effect.Observe(ctx, snapshot)
	if err != nil {
		if receipt.Phase == "effect_started" {
			return nil, e.refuse(ctx, receipt, ErrAutoApplyRecoveryRequired)
		}
		if errors.Is(err, ErrFreshDecisionRequired) {
			return nil, e.refuse(ctx, receipt, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrAutoApplyRecoveryRequired, err)
	}
	if receipt.Phase == "effect_observed" {
		if !applied {
			return nil, ErrAutoApplyRecoveryRequired
		}
		return e.finish(ctx, snapshot, digest, observation)
	}
	if applied {
		receipt, err = e.checkpoint(ctx, snapshot, digest, receipt.Phase, "effect_observed", observationID(observation))
		if err != nil {
			return nil, err
		}
		if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
			return nil, err
		}
		return e.finish(ctx, snapshot, digest, observation)
	}
	// An effect_started receipt may have crossed an external boundary before
	// a crash. Missing/partial resources cannot prove that no step ran.
	if receipt.Phase == "effect_started" {
		return nil, e.refuse(ctx, receipt, ErrAutoApplyRecoveryRequired)
	}
	secrets, release, err := e.Source.ResolvePinnedSecrets(ctx, snapshot)
	if err != nil {
		if errors.Is(err, ErrFreshDecisionRequired) {
			return nil, e.refuse(ctx, receipt, err)
		}
		return nil, err
	}
	defer release()
	// Re-read every observed winner and secret version under the same fence.
	current, err := e.Source.LoadAutoApplySnapshot(ctx, serviceID, envID)
	if err != nil {
		if errors.Is(err, ErrFreshDecisionRequired) {
			return nil, e.refuse(ctx, receipt, err)
		}
		return nil, err
	}
	currentDigest, err := current.digest()
	if err != nil {
		return nil, err
	}
	if currentDigest != digest {
		return nil, e.refuse(ctx, receipt, ErrFreshDecisionRequired)
	}
	receipt, err = e.checkpoint(ctx, snapshot, digest, receipt.Phase, "effect_started", "")
	if err != nil {
		return nil, err
	}
	if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
		return nil, err
	}
	if statusFn != nil {
		statusFn(ctx, service.DeployStepApplying, "Applying the signed canonical revision")
	}
	if err := e.Effect.Apply(ctx, snapshot, secrets); err != nil {
		return nil, e.refuse(ctx, receipt, fmt.Errorf("%w: %v", ErrAutoApplyRecoveryRequired, err))
	}
	applied, observation, err = e.Effect.Observe(ctx, snapshot)
	if err != nil || !applied {
		return nil, e.refuse(ctx, receipt, ErrAutoApplyRecoveryRequired)
	}
	receipt, err = e.checkpoint(ctx, snapshot, digest, "effect_started", "effect_observed", observationID(observation))
	if err != nil {
		return nil, err
	}
	if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
		return nil, err
	}
	return e.finish(ctx, snapshot, digest, observation)
}

// A source can refuse a moved winner before it can construct a new snapshot.
// An already-admitted decision still deserves a signed fresh-decision status;
// no receipt means there was never an accepted executor snapshot to report.
func (e *CanonicalAutoApplyExecutor) refuseStaleReceipts(ctx context.Context, serviceID, envID uuid.UUID) error {
	rows, err := e.Outbox.ListControlRecords(e.family())
	if err != nil {
		return err
	}
	var failures []error
	for _, raw := range rows {
		var receipt autoApplyReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			failures = append(failures, fmt.Errorf("invalid auto-apply receipt while refusing stale snapshot: %w", err))
			continue
		}
		if receipt.Snapshot.Spec.ServiceID != serviceID || receipt.Snapshot.Spec.EnvironmentID != envID {
			continue
		}
		switch receipt.Phase {
		case "terminal", "refused", "superseded", "recovery_required":
			continue
		}
		if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := e.refuse(ctx, &receipt, ErrFreshDecisionRequired); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func observationID(obs *domain.RuntimeObservation) string {
	if obs == nil {
		return ""
	}
	return obs.ID.String()
}

func (e *CanonicalAutoApplyExecutor) finish(ctx context.Context, snapshot ApplySnapshot, digest string, obs *domain.RuntimeObservation) (*domain.RuntimeObservation, error) {
	current, loadErr := e.Source.LoadAutoApplySnapshot(ctx, snapshot.Spec.ServiceID, snapshot.Spec.EnvironmentID)
	if loadErr != nil {
		return nil, e.refuse(ctx, &autoApplyReceipt{Snapshot: snapshot, Digest: digest, Phase: "effect_observed"}, ErrFreshDecisionRequired)
	}
	currentDigest, err := current.digest()
	if err != nil {
		return nil, err
	}
	if currentDigest != digest {
		return nil, e.refuse(ctx, &autoApplyReceipt{Snapshot: snapshot, Digest: digest, Phase: "effect_observed"}, ErrFreshDecisionRequired)
	}
	receipt, err := e.checkpoint(ctx, snapshot, digest, "effect_observed", "terminal", observationID(obs))
	if err != nil {
		return nil, err
	}
	if err := e.deliverCheckpoint(ctx, receipt.Checkpoint); err != nil {
		return nil, err
	}
	return obs, nil
}

func (e *CanonicalAutoApplyExecutor) refuse(ctx context.Context, receipt *autoApplyReceipt, cause error) error {
	phase := "recovery_required"
	if errors.Is(cause, ErrFreshDecisionRequired) {
		if receipt.Phase == "accepted" {
			phase = "refused"
		} else if receipt.Phase == "effect_observed" {
			phase = "superseded"
		}
	}
	next, err := e.checkpoint(ctx, receipt.Snapshot, receipt.Digest, receipt.Phase, phase, receipt.Observation)
	if err != nil {
		return errors.Join(cause, err)
	}
	if err := e.deliverCheckpoint(ctx, next.Checkpoint); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (e *CanonicalAutoApplyExecutor) readReceipt(id uuid.UUID) (*autoApplyReceipt, error) {
	raw, err := e.Outbox.GetControlRecord(e.family(), id.String())
	if err != nil || raw == nil {
		return nil, err
	}
	var receipt autoApplyReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("decode auto-apply receipt: %w", err)
	}
	if receipt.Snapshot.IntentID != id || !receipt.Checkpoint.CheckID() || !receipt.Checkpoint.VerifySignature() {
		return nil, fmt.Errorf("auto-apply receipt is corrupt")
	}
	return &receipt, nil
}

func (e *CanonicalAutoApplyExecutor) checkpoint(ctx context.Context, snapshot ApplySnapshot, digest, prior, phase, observation string) (*autoApplyReceipt, error) {
	current, err := e.readReceipt(snapshot.IntentID)
	if err != nil {
		return nil, err
	}
	if current != nil && (current.Phase != prior || current.Digest != digest) {
		return nil, fmt.Errorf("auto-apply receipt phase or snapshot conflict")
	}
	sequence := 1
	floor := nostr.Timestamp(0)
	if current != nil {
		sequence = current.Sequence + 1
		floor = current.Checkpoint.CreatedAt
	}
	now := nostr.Now()
	at := max(now, floor+1)
	if at > now+30 {
		return nil, fmt.Errorf("auto-apply checkpoint waits for prior coordinate timestamp %d", floor)
	}
	status := "pending"
	switch phase {
	case "accepted":
		status = "accepted"
	case "terminal":
		status = "success"
	case "refused":
		status = "rejected"
	case "recovery_required", "superseded":
		status = "error"
	}
	content, err := json.Marshal(map[string]any{
		"intent_id": snapshot.IntentID.String(), "decision_event_id": snapshot.DecisionEventID,
		"snapshot_hash": digest, "phase": phase, "result": phase, "sequence": sequence, "observation_id": observation,
	})
	if err != nil {
		return nil, err
	}
	event := nostr.Event{Kind: 30315, CreatedAt: at, Tags: nostr.Tags{
		{"d", "intent-status:" + snapshot.DecisionActor + ":" + e.statusScope() + ":" + snapshot.IntentID.String()},
		{"domain", "intent"}, {"t", "intent-status"}, {"p", snapshot.DecisionActor},
		{"e", snapshot.DecisionEventID}, {"intent_id", snapshot.IntentID.String()},
		{"status", status},
	}, Content: string(content)}
	if err := e.Signer.SignEvent(ctx, &event); err != nil {
		return nil, err
	}
	next := autoApplyReceipt{Snapshot: snapshot, Digest: digest, Phase: phase, Sequence: sequence,
		Checkpoint: event, Observation: observation}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	_, err = e.Outbox.UpdateControlRecord(e.family(), snapshot.IntentID.String(), func(previous []byte) ([]byte, error) {
		if current == nil {
			if previous != nil {
				return nil, fmt.Errorf("auto-apply admission raced another decision")
			}
		} else {
			var actual autoApplyReceipt
			if json.Unmarshal(previous, &actual) != nil || actual.Sequence != current.Sequence || actual.Checkpoint.ID != current.Checkpoint.ID {
				return nil, fmt.Errorf("auto-apply checkpoint raced another writer")
			}
		}
		return raw, nil
	})
	if err != nil {
		return nil, err
	}
	return &next, nil
}

func (e *CanonicalAutoApplyExecutor) deliverCheckpoint(ctx context.Context, event nostr.Event) error {
	if !event.CheckID() || !event.VerifySignature() {
		return fmt.Errorf("auto-apply checkpoint signature is invalid")
	}
	if proof, ok, err := e.Outbox.GetDeliveryProof(event.ID); err != nil {
		return err
	} else if ok {
		if proof.Event.ID != event.ID || !proof.ValidFor(event, proof.Target) {
			return fmt.Errorf("auto-apply checkpoint has conflicting relay proof")
		}
		return nil
	}
	if entry, ok, err := e.Outbox.Get(event.ID); err != nil {
		return err
	} else if ok {
		if entry.Event.ID != event.ID || !entry.Event.CheckID() || !entry.Event.VerifySignature() {
			return fmt.Errorf("auto-apply checkpoint has conflicting outbox entry")
		}
		if entry.State == localstore.OutboxFailed {
			return fmt.Errorf("auto-apply checkpoint relay quorum failed")
		}
		return nil
	}
	return e.Enqueue(ctx, event)
}
