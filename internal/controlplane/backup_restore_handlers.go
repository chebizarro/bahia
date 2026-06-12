package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type BackupRestoreControlPlaneExecutor interface {
	ProcessBackupRestore(ctx context.Context, restoreID uuid.UUID) error
}

type backupRestoreRegistry interface {
	CreateBackupRestoreIfAbsent(ctx context.Context, restore *domain.BackupRestoreRun) (*domain.BackupRestoreRun, bool, error)
	ApplyBackupRestoreApproval(ctx context.Context, restoreID uuid.UUID, approved bool, approvalEventID, approvedBy, message string, reasonParts ...any) (*domain.BackupRestoreRun, bool, error)
}

type backupRestoreApprovalResponder interface {
	service.BackupRestoreResponder
	PublishBackupRestoreApprovalResult(ctx context.Context, restore *domain.BackupRestoreRun, approved bool, changed bool, message string) error
}

type backupRestoreRequest struct {
	BackupRunID      string         `json:"backup_run_id,omitempty"`
	RestoreTargetRef string         `json:"restore_target_ref,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type backupRestoreApprovalRequest struct {
	RestoreID  string         `json:"restore_id,omitempty"`
	Approved   *bool          `json:"approved,omitempty"`
	Decision   string         `json:"decision,omitempty"`
	Message    string         `json:"message,omitempty"`
	ReasonCode string         `json:"reason_code,omitempty"`
	Reason     map[string]any `json:"reason,omitempty"`
}

func (r *Reactor) handleBackupRestoreRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_restore", KindBackupRestoreResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRestoreRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "backup_restore_unavailable", "backup restore registry is not configured")
		return
	}
	if r.backupRestoreExecutor == nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "backup_restore_coordinator_unavailable", "backup restore coordinator is not configured")
		return
	}
	req, err := parseBackupRestoreRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "parse_error", err.Error())
		return
	}
	backupRunID, err := uuid.Parse(req.BackupRunID)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "validation_error", "backup_run_id must be a UUID")
		return
	}
	restore := &domain.BackupRestoreRun{
		ID:               uuid.New(),
		BackupRunID:      backupRunID,
		RestoreTargetRef: req.RestoreTargetRef,
		RequestedBy:      event.PubKey.Hex(),
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
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreResult, "failed", "restore_create_error", err.Error())
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

func (r *Reactor) handleBackupRestoreApproval(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_restore_approval", KindBackupRestoreApprovalResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRestoreRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreApprovalResult, "failed", "backup_restore_unavailable", "backup restore registry is not configured")
		return
	}
	if r.backupRestoreExecutor == nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreApprovalResult, "failed", "backup_restore_coordinator_unavailable", "backup restore coordinator is not configured")
		return
	}
	req, err := parseBackupRestoreApprovalRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreApprovalResult, "failed", "parse_error", err.Error())
		return
	}
	restoreID, err := uuid.Parse(req.RestoreID)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreApprovalResult, "failed", "validation_error", "restore_id must be a UUID")
		return
	}
	restore, changed, err := registry.ApplyBackupRestoreApproval(ctx, restoreID, *req.Approved, event.ID.Hex(), event.PubKey.Hex(), req.Message, req.ReasonCode, req.Reason)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRestoreApprovalResult, "failed", "restore_approval_error", err.Error())
		return
	}
	if approvalResponder, ok := r.backupRestoreResponder.(backupRestoreApprovalResponder); ok {
		_ = approvalResponder.PublishBackupRestoreApprovalResult(ctx, restore, *req.Approved, changed, approvalMessage(req.Message, changed))
	}
	if !changed || !*req.Approved || restore == nil || restore.ApprovalStatus != domain.BackupApprovalApproved || backupRestoreTerminal(restore) {
		return
	}
	go func(restoreID uuid.UUID) {
		if err := r.backupRestoreExecutor.ProcessBackupRestore(ctx, restoreID); err != nil {
			r.logger.Warn("backup restore executor failed", "restore_id", restoreID.String(), "error", err)
		}
	}(restore.ID)
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

func parseBackupRestoreApprovalRequest(event *nostr.Event) (*backupRestoreApprovalRequest, error) {
	var req backupRestoreApprovalRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.RestoreID == "" {
		req.RestoreID = firstNonEmpty(tagValueNostr(event.Tags, "restore_id"), tagValueNostr(event.Tags, "restore"))
	}
	if req.Approved == nil {
		decision := strings.ToLower(strings.TrimSpace(firstNonEmpty(req.Decision, tagValueNostr(event.Tags, "decision"))))
		switch decision {
		case "approve", "approved", "true", "yes":
			approved := true
			req.Approved = &approved
		case "reject", "rejected", "false", "no":
			approved := false
			req.Approved = &approved
		}
	}
	if strings.TrimSpace(req.RestoreID) == "" {
		return nil, fmt.Errorf("restore_id is required")
	}
	if req.Approved == nil {
		return nil, fmt.Errorf("approved decision is required")
	}
	req.RestoreID = strings.TrimSpace(req.RestoreID)
	if req.Message == "" {
		req.Message = firstNonEmpty(tagValueNostr(event.Tags, "message"), tagValueNostr(event.Tags, "reason"))
	}
	if req.ReasonCode == "" {
		req.ReasonCode = firstNonEmpty(tagValueNostr(event.Tags, "reason_code"), tagValueNostr(event.Tags, "reason_kind"))
	}
	req.Message = strings.TrimSpace(req.Message)
	req.ReasonCode = strings.TrimSpace(req.ReasonCode)
	return &req, nil
}

func backupRestoreTerminal(restore *domain.BackupRestoreRun) bool {
	return restore != nil && (restore.Status == domain.RunStatusSucceeded || restore.Status == domain.RunStatusFailed || restore.Status == domain.RunStatusCancelled || restore.Status == domain.RunStatusTimeout)
}

func approvalMessage(message string, changed bool) string {
	message = strings.TrimSpace(message)
	if message != "" {
		return message
	}
	if changed {
		return "backup restore approval recorded"
	}
	return "backup restore approval already decided"
}
