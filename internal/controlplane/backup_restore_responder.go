package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type backupRestorePublishRegistry interface {
	CreateOrUpdateBackupRestore(ctx context.Context, restore *domain.BackupRestoreRun) error
}

// BackupRestoreResponder publishes Nostr-native restore status, approval-result, and terminal result events.
type BackupRestoreResponder struct {
	publisher backupResultPublisher
	signer    canonicalnostr.Signer
	registry  backupRestorePublishRegistry
	eventRepo repository.NostrEventRepository
	logger    *zap.Logger
}

func NewBackupRestoreResponder(publisher backupResultPublisher, signer canonicalnostr.Signer, registry backupRestorePublishRegistry, eventRepo repository.NostrEventRepository, logger *zap.Logger) *BackupRestoreResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupRestoreResponder{publisher: publisher, signer: signer, registry: registry, eventRepo: eventRepo, logger: logger.Named("backup-restore-responder")}
}

func (r *BackupRestoreResponder) PublishBackupRestoreStatus(ctx context.Context, restore *domain.BackupRestoreRun, step, message string) error {
	if r == nil || restore == nil || strings.TrimSpace(restore.RequestEventID) == "" || strings.TrimSpace(restore.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRestore(restore)
	content := map[string]any{
		"request_event_id":    restore.RequestEventID,
		"restore_id":          restore.ID.String(),
		"backup_run_id":       restore.BackupRunID.String(),
		"snapshot_id":         restore.SnapshotID,
		"restore_target_ref":  restore.RestoreTargetRef,
		"approval_status":     string(restore.ApprovalStatus),
		"status":              status,
		"run_status":          string(restore.Status),
		"step":                strings.TrimSpace(step),
		"message":             strings.TrimSpace(message),
		"verification_status": string(restore.VerificationStatus),
		"created_at":          time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(content)
	tags := backupRestoreTags(restore, nostr.Tags{{"d", fmt.Sprintf("status:%s:%s", restore.ID, strings.TrimSpace(step))}, {"e", restore.RequestEventID, "", "reply"}, {"p", restore.RequestedBy}, {"status", status}, {"step", strings.TrimSpace(step)}, {"approval", string(restore.ApprovalStatus)}})
	summary, err := r.signPublishRecord(ctx, KindBackupRestoreStatus, tags, string(body), "backup.restore.status", &restore.ID)
	r.mergeRestorePublishSummary(ctx, restore, "backup_restore_status", summary)
	return err
}

func (r *BackupRestoreResponder) PublishBackupRestoreApprovalResult(ctx context.Context, restore *domain.BackupRestoreRun, approved bool, changed bool, message string) error {
	if r == nil || restore == nil || strings.TrimSpace(restore.RequestEventID) == "" || strings.TrimSpace(restore.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRestore(restore)
	content := map[string]any{
		"request_event_id":  restore.RequestEventID,
		"restore_id":        restore.ID.String(),
		"backup_run_id":     restore.BackupRunID.String(),
		"approved":          approved,
		"changed":           changed,
		"approval_status":   string(restore.ApprovalStatus),
		"approval_event_id": restore.ApprovalEventID,
		"approved_by":       restore.ApprovedBy,
		"approved_at":       restore.ApprovedAt,
		"message":           strings.TrimSpace(message),
		"status":            status,
		"run_status":        string(restore.Status),
		"created_at":        time.Now().UTC().Format(time.RFC3339),
	}
	if restore.Error != "" {
		content["error"] = map[string]any{"message": restore.Error}
	}
	body, _ := json.Marshal(content)
	dTag := "approval:" + restore.ID.String()
	if restore.ApprovalEventID != "" {
		dTag += ":" + restore.ApprovalEventID
	}
	tags := backupRestoreTags(restore, nostr.Tags{{"d", dTag}, {"e", restore.RequestEventID, "", "reply"}, {"p", restore.RequestedBy}, {"status", status}, {"approval", string(restore.ApprovalStatus)}, {"approved", fmt.Sprintf("%t", approved)}, {"changed", fmt.Sprintf("%t", changed)}})
	summary, err := r.signPublishRecord(ctx, KindBackupRestoreApprovalResult, tags, string(body), "backup.restore.approval_result", &restore.ID)
	r.mergeRestorePublishSummary(ctx, restore, "backup_restore_approval_result", summary)
	return err
}

func (r *BackupRestoreResponder) PublishBackupRestoreResult(ctx context.Context, restore *domain.BackupRestoreRun, message string) error {
	if r == nil || restore == nil || strings.TrimSpace(restore.RequestEventID) == "" || strings.TrimSpace(restore.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRestore(restore)
	content := map[string]any{
		"request_event_id":    restore.RequestEventID,
		"restore_id":          restore.ID.String(),
		"backup_run_id":       restore.BackupRunID.String(),
		"recipe_id":           restore.RecipeID.String(),
		"repository_id":       restore.RepositoryID.String(),
		"backend":             string(restore.Backend),
		"snapshot_id":         restore.SnapshotID,
		"restore_target_ref":  restore.RestoreTargetRef,
		"approval_status":     string(restore.ApprovalStatus),
		"approval_event_id":   restore.ApprovalEventID,
		"approved_by":         restore.ApprovedBy,
		"approved_at":         restore.ApprovedAt,
		"approval_message":    restore.ApprovalMessage,
		"status":              status,
		"run_status":          string(restore.Status),
		"verification_status": string(restore.VerificationStatus),
		"evidence":            restore.Evidence,
		"message":             strings.TrimSpace(message),
		"created_at":          time.Now().UTC().Format(time.RFC3339),
	}
	if restore.PolicyID != nil {
		content["policy_id"] = restore.PolicyID.String()
	}
	if restore.Error != "" {
		content["error"] = map[string]any{"message": restore.Error}
	}
	body, _ := json.Marshal(content)
	tags := backupRestoreTags(restore, nostr.Tags{{"d", "result:" + restore.RequestEventID}, {"e", restore.RequestEventID, "", "reply"}, {"p", restore.RequestedBy}, {"status", status}, {"approval", string(restore.ApprovalStatus)}, {"verification", string(restore.VerificationStatus)}})
	summary, err := r.signPublishRecord(ctx, KindBackupRestoreResult, tags, string(body), "backup.restore.result", &restore.ID)
	r.mergeRestorePublishSummary(ctx, restore, "backup_restore_result", summary)
	return err
}

func (r *BackupRestoreResponder) signPublishRecord(ctx context.Context, kind int, tags nostr.Tags, content, entityType string, entityID *uuid.UUID) (map[string]any, error) {
	if r.publisher == nil || r.signer == nil {
		return map[string]any{"kind": kind, "published": false, "error": "backup restore responder publisher or signer is not configured", "recorded_at": time.Now().UTC().Format(time.RFC3339)}, fmt.Errorf("backup restore responder publisher or signer is not configured")
	}
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: content}
	if err := SignGoNostrEvent(ctx, r.signer, event); err != nil {
		return map[string]any{"kind": kind, "published": false, "error": err.Error(), "recorded_at": time.Now().UTC().Format(time.RFC3339)}, err
	}
	results, err := r.publisher.PublishWithResults(ctx, *event)
	summary := backupPublishSummary(kind, event, results, err)
	r.record(ctx, event, entityType, entityID)
	return summary, err
}

func (r *BackupRestoreResponder) mergeRestorePublishSummary(ctx context.Context, restore *domain.BackupRestoreRun, key string, summary map[string]any) {
	if r == nil || r.registry == nil || restore == nil || len(summary) == 0 {
		return
	}
	if restore.PublishSummary == nil {
		restore.PublishSummary = map[string]any{}
	}
	restore.PublishSummary[key] = summary
	if err := r.registry.CreateOrUpdateBackupRestore(ctx, restore); err != nil {
		r.logger.Warn("failed to persist backup restore publish summary", zap.String("restore_id", restore.ID.String()), zap.String("summary_key", key), zap.Error(err))
	}
}

func (r *BackupRestoreResponder) record(ctx context.Context, ev *nostr.Event, entityType string, entityID *uuid.UUID) {
	if r == nil || r.eventRepo == nil || ev == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	if _, err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID, Kind: ev.Kind, PubKey: ev.PubKey, Content: ev.Content, Tags: tagsJSON, Sig: ev.Sig, CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: entityType, EntityID: entityID}); err != nil {
		r.logger.Warn("failed to record backup restore reply event", zap.String("event_id", ev.ID), zap.String("entity_type", entityType), zap.Error(err))
	}
}

func backupRestoreTags(restore *domain.BackupRestoreRun, tags nostr.Tags) nostr.Tags {
	tags = append(tags, nostr.Tag{"restore", restore.ID.String()}, nostr.Tag{"restore_id", restore.ID.String()}, nostr.Tag{"run", restore.BackupRunID.String()}, nostr.Tag{"backup_run_id", restore.BackupRunID.String()}, nostr.Tag{"recipe_id", restore.RecipeID.String()}, nostr.Tag{"repository_id", restore.RepositoryID.String()}, nostr.Tag{"backend", string(restore.Backend)}, nostr.Tag{"target", restore.RestoreTargetRef})
	if restore.PolicyID != nil {
		tags = append(tags, nostr.Tag{"policy", restore.PolicyID.String()}, nostr.Tag{"policy_id", restore.PolicyID.String()})
	}
	for _, key := range []string{"repository", "site", "environment", "worker"} {
		if value := backupMetadataString(restore.Metadata, "nostr_tag_"+key); value != "" {
			tags = append(tags, nostr.Tag{key, value})
		}
	}
	return dedupeTags(tags)
}

func backupStatusForRestore(restore *domain.BackupRestoreRun) string {
	if restore == nil {
		return "failed"
	}
	switch restore.Status {
	case domain.RunStatusSucceeded:
		return "succeeded"
	case domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return "failed"
	case domain.RunStatusQueued:
		return "queued"
	default:
		return "processing"
	}
}
