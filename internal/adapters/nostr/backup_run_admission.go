package nostr

import (
	"context"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

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
	return record.StateEventID, true, record.Delivered && record.StatusOutcome == "accepted" && record.StatusEventID != "", nil
}

// StageRunAdmission signs and persists the first queued run state together
// with its immutable request keys. No relay attempt or SQL write precedes the
// durable outbox transaction; the normal outbox runner supplies delivery.
func (p *BackupCanonicalPublisher) StageRunAdmission(ctx context.Context, intentID, requestEventID string, run *domain.BackupRun) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p == nil || p.projector == nil || !p.projector.Enabled() || p.runAdmission == nil ||
		p.runAdmission.localOutbox == nil || p.runAdmission.target != repository.NostrPublishTargetControlPlane || run == nil {
		return "", fmt.Errorf("backup run admission signer or control-plane outbox is unavailable")
	}
	if run.ID == uuid.Nil || run.Status != domain.RunStatusQueued || run.RequestEventID != requestEventID ||
		run.RequestDTag != "backup-run:"+run.ID.String() || domain.ValidateBackupRun(run) != nil {
		return "", fmt.Errorf("backup run admission requires a valid queued run bound to its signed request")
	}
	tags, content := BackupRunStateRecord(run, nil)
	wireKind, envelope := controlStateEnvelope(kinds.BackupRunState, BackupRunDTag(run.ID), false)
	tags = append(envelope, tags...)
	key := projectionKeyOf(wireKind, tags)
	_, unlock := p.projector.lockProjectionKey(key)
	defer unlock()
	createdAt := p.projector.nextProjectionCreatedAt(key)
	event := gonostr.Event{Kind: gonostr.Kind(wireKind), CreatedAt: createdAt, Tags: tags, Content: content}
	if err := signEventWithPrivateKeyHex(&event, p.projector.privateKey); err != nil {
		return "", fmt.Errorf("sign backup run admission: %w", err)
	}
	stateID, inserted, err := p.runAdmission.stageBackupRun(ctx, event, intentID, run.RequestDTag, requestEventID, run.RequestedBy, run.ID)
	if err != nil {
		return "", err
	}
	if inserted {
		p.projector.rememberProjection(key, projectionFingerprint(wireKind, tags, content), createdAt)
	}
	return stateID, nil
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
