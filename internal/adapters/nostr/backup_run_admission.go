package nostr

import (
	"context"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

func (p *BackupCanonicalPublisher) LookupPendingRun(ctx context.Context, intentID, coordinate, requestEventID string) (*localstore.BackupRunPending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.runAdmission == nil || p.runAdmission.localOutbox == nil {
		return nil, fmt.Errorf("backup run pending inbox is unavailable")
	}
	return p.runAdmission.localOutbox.GetBackupRunPending(intentID, coordinate, requestEventID)
}

func (p *BackupCanonicalPublisher) StagePendingRun(ctx context.Context, intentID, coordinate string, event gonostr.Event, actor string, expiresAt time.Time) (*localstore.BackupRunPending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil || p.projector == nil || !p.projector.Enabled() || p.runAdmission == nil ||
		p.runAdmission.localOutbox == nil || p.runAdmission.target != repository.NostrPublishTargetControlPlane {
		return nil, fmt.Errorf("backup run pending inbox or service identity is unavailable")
	}
	servicePubkey := p.projector.servicePubkey
	record, inserted, err := p.runAdmission.localOutbox.PutBackupRunPending(localstore.BackupRunPending{
		IntentID: intentID, Coordinate: coordinate, RequestEvent: event, Actor: actor,
		ServicePubkey: servicePubkey, ReceivedAt: time.Now().UTC(), ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, err
	}
	if inserted && p.runPendingWake != nil {
		p.runPendingWake()
	}
	return record, nil
}

// LookupRunAdmission reads the durable request-to-state binding, including
// after the settled delivery row has been pruned. Its absence is not inferred
// from the rebuildable inbound event cache.
func (p *BackupCanonicalPublisher) LookupRunAdmission(ctx context.Context, intentID, coordinate, requestEventID string) (string, bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, false, err
	}
	if p == nil || p.runAdmission == nil || p.runAdmission.localOutbox == nil ||
		p.runAdmission.target != repository.NostrPublishTargetControlPlane {
		return "", false, false, fmt.Errorf("backup run admission outbox is unavailable")
	}
	record, err := p.runAdmission.localOutbox.GetBackupRunAdmission(intentID, coordinate, requestEventID)
	if err != nil || record == nil {
		return "", false, false, err
	}
	return record.StateEventID, true, record.Delivered && record.StatusDelivered && record.StatusOutcome == "accepted", nil
}

// StageRunAdmission remains disabled while canonical credential recovery and
// exactly-once execution are unavailable. Writer policy belongs to the signer.
func (p *BackupCanonicalPublisher) StageRunAdmission(ctx context.Context, intentID, requestEventID string, run *domain.BackupRun) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("backup run admission paused: credential recovery and exactly-once execution unavailable")
}

// StageRunAdmissionWithHistory is the only enabled admission path: the relay
// observation stays live until the signed run state and request keys are in
// the same local outbox transaction. The signer enforces single-writer policy.
func (p *BackupCanonicalPublisher) StageRunAdmissionWithHistory(ctx context.Context, intentID, requestEventID string, run *domain.BackupRun, proof *BackupRunHistoryProof) (string, error) {
	if p == nil || p.projector == nil || !p.projector.Enabled() || p.runAdmission == nil ||
		p.runAdmission.localOutbox == nil || p.runAdmission.target != repository.NostrPublishTargetControlPlane || run == nil || proof == nil {
		return "", fmt.Errorf("backup run admission requires service signer, outbox and live history proof")
	}
	coordinate := BackupRunDTag(run.ID)
	if run.ID.Version() != 7 || domain.ValidateBackupRun(run) != nil || run.ExecutionSnapshot == nil ||
		run.ExecutionSnapshot.CredentialRef == nil || secretstore.ValidateRef(*run.ExecutionSnapshot.CredentialRef) != nil {
		return "", fmt.Errorf("backup run admission requires a validated UUIDv7 run and pinned Signet snapshot")
	}
	pending, err := p.runAdmission.localOutbox.GetBackupRunPending(intentID, coordinate, requestEventID)
	if err != nil || pending == nil || pending.State != localstore.BackupRunPendingState ||
		pending.Actor != run.RequestedBy || int(pending.RequestEvent.Kind) != run.RequestKind || !time.Now().UTC().Before(pending.ExpiresAt) {
		return "", fmt.Errorf("backup run admission requires the exact unexpired signed pending request: %w", err)
	}
	if proof.Coordinate != coordinate || proof.ServicePubkey.Hex() != p.projector.servicePubkey || run.RequestDTag != coordinate || run.RequestEventID != requestEventID {
		return "", fmt.Errorf("backup run admission history or signed request identity does not match")
	}
	if err := proof.StillCurrent(); err != nil {
		return "", err
	}
	tags, content := BackupRunStateRecord(run, nil)
	kind, envelope := controlStateEnvelope(kinds.BackupRunState, coordinate, false)
	event := gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: gonostr.Now(), Tags: append(envelope, tags...), Content: content}
	if err := p.projector.signEvent(ctx, &event); err != nil {
		return "", err
	}
	if err := proof.StillCurrent(); err != nil {
		return "", err
	}
	stateID, _, err := p.runAdmission.stageBackupRun(ctx, event, intentID, coordinate, requestEventID, run.RequestedBy, run.ID)
	return stateID, err
}

// SignBackupRunState signs, but does not publish, a terminal state. The
// executor first stores these exact bytes in its bbolt receipt for replay.
func (p *BackupCanonicalPublisher) SignBackupRunState(ctx context.Context, run *domain.BackupRun, createdAt gonostr.Timestamp) (gonostr.Event, error) {
	if p == nil || p.projector == nil || !p.projector.Enabled() || run == nil {
		return gonostr.Event{}, fmt.Errorf("backup run state signer unavailable")
	}
	tags, content := BackupRunStateRecord(run, nil)
	kind, envelope := controlStateEnvelope(kinds.BackupRunState, BackupRunDTag(run.ID), false)
	event := gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: createdAt, Tags: append(envelope, tags...), Content: content}
	if err := p.projector.signEvent(ctx, &event); err != nil {
		return gonostr.Event{}, err
	}
	return event, nil
}

func (p *Publisher) stageBackupRun(ctx context.Context, event gonostr.Event, intentID, coordinate, requestEventID, actor string, runID uuid.UUID) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	record, inserted, err := p.localOutbox.EnqueueBackupRun(localstore.OutboxEntry{
		Event: event, Target: p.target, EntityType: "backup_run.admission", EntityID: runID.String(), EnqueuedAt: p.now(),
	}, intentID, coordinate, requestEventID, actor)
	if err != nil {
		return "", false, err
	}
	if inserted {
		p.keepOwnEvent(event)
		p.nudge()
	}
	return record.StateEventID, inserted, nil
}
