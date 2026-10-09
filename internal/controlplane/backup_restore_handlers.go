package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type BackupRestoreControlPlaneExecutor interface {
	ProcessBackupRestore(ctx context.Context, restoreID uuid.UUID) error
}

type backupRestoreRegistry interface {
	CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error)
}

type backupRestoreRequest struct {
	BackupRunID      string         `json:"backup_run_id,omitempty"`
	RestoreTargetRef string         `json:"restore_target_ref,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

func (r *Reactor) handleBackupRestoreRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_restore", KindBackupRestoreResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRestoreRegistry)
	if !ok {
		r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "backup_restore_unavailable", "backup restore registry is not configured")
		return
	}
	if r.backupRestoreExecutor == nil {
		r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "backup_restore_coordinator_unavailable", "backup restore coordinator is not configured")
		return
	}
	req, err := parseBackupRestoreRequest(event)
	if err != nil {
		r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "parse_error", err.Error())
		return
	}
	backupRunID, err := uuid.Parse(req.BackupRunID)
	if err != nil {
		r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "validation_error", "backup_run_id must be a UUID")
		return
	}
	restore := &domain.BackupRestoreRun{
		ID:               uuid.New(),
		BackupRunID:      backupRunID,
		RestoreTargetRef: req.RestoreTargetRef,
		RequestedBy:      backupRequestActor(event),
		RequestEventID:   event.ID.Hex(),
		RequestKind:      int(event.Kind),
		RequestDTag:      tagValueNostr(event.Tags, "d"),
		Status:           domain.RunStatusQueued,
		Metadata: backupNostrMetadata(event, req.Metadata, map[string]any{
			"nostr_request_command": "backup_restore",
			"nostr_backup_run_id":   backupRunID.String(),
			"nostr_restore_target":  req.RestoreTargetRef,
		}),
	}
	createdRestore, created, err := registry.CreateBackupRestoreIfAbsent(ctx, restore)
	if err != nil {
		r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "restore_create_error", err.Error())
		return
	}
	if r.backupRestoreResponder != nil {
		step := "queued"
		message := "backup restore queued"
		if createdRestore.ApprovalStatus == domain.BackupApprovalPending {
			step = "pending_approval"
			message = "backup restore pending approval"
		}
		if !created {
			step = "duplicate"
			message = "backup restore request already accepted for this requester and d tag"
		}
		_ = r.backupRestoreResponder.PublishBackupRestoreStatus(ctx, createdRestore, step, message)
	}
	if !created {
		if backupRestoreTerminal(createdRestore) && r.backupRestoreResponder != nil {
			_ = r.backupRestoreResponder.PublishBackupRestoreResult(ctx, createdRestore, "backup restore already completed")
		}
		return
	}
	if createdRestore.ApprovalStatus == domain.BackupApprovalPending {
		return
	}
	go func(restoreID uuid.UUID) {
		if err := r.backupRestoreExecutor.ProcessBackupRestore(ctx, restoreID); err != nil {
			r.logger.Warn("backup restore executor failed", "restore_id", restoreID.String(), "error", err)
		}
	}(createdRestore.ID)
}

func (r *Reactor) handleBackupRestoreApproval(_ context.Context, _ *nostr.Event) {
	r.logger.Warn("backup restore approval paused: SQL-derived restore inputs cannot be bound atomically before publication")
}

func parseBackupRestoreRequest(event *nostr.Event) (*backupRestoreRequest, error) {
	var req backupRestoreRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.BackupRunID == "" {
		req.BackupRunID = firstNonEmpty(tagValueNostr(event.Tags, "backup_run_id"), tagValueNostr(event.Tags, "run"))
	}
	if req.RestoreTargetRef == "" {
		req.RestoreTargetRef = tagValueNostr(event.Tags, "target")
	}
	if strings.TrimSpace(req.BackupRunID) == "" {
		return nil, fmt.Errorf("backup_run_id is required")
	}
	req.BackupRunID = strings.TrimSpace(req.BackupRunID)
	req.RestoreTargetRef = strings.TrimSpace(req.RestoreTargetRef)
	if req.RestoreTargetRef == "" {
		return nil, fmt.Errorf("restore_target_ref is required")
	}
	return &req, nil
}

func backupRestoreTerminal(restore *domain.BackupRestoreRun) bool {
	return restore != nil && (restore.Status == domain.RunStatusSucceeded || restore.Status == domain.RunStatusFailed || restore.Status == domain.RunStatusCancelled || restore.Status == domain.RunStatusTimeout)
}
