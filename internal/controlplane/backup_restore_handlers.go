package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
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
	if !r.authorizeBackupPausedRequest(ctx, event) {
		return
	}
	req, err := parseBackupRestoreRequest(event)
	if err == nil {
		_, err = uuid.Parse(req.BackupRunID)
	}
	if err != nil {
		r.zapLog.Warn("dropping invalid backup restore request without signing a refusal", zap.String("request_event_id", event.ID.Hex()), zap.Error(err))
		return
	}
	r.publishPausedBackupRequest(ctx, event, KindBackupRestoreResult, "backup restore request intake is paused until canonical acceptance receipts are available")
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
