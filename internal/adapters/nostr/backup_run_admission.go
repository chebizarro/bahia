package nostr

import (
	"context"
	"fmt"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
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

// StageRunAdmission is intentionally disabled until every service-key signing
// path participates in a cross-process writer fence and complete history proof.
func (p *BackupCanonicalPublisher) StageRunAdmission(ctx context.Context, intentID, requestEventID string, run *domain.BackupRun) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("backup run admission paused: cross-process service-key signer fence unavailable")
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
