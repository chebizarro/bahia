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

type backupRetentionPublishRegistry interface {
	CreateOrUpdateBackupRetentionRun(ctx context.Context, run *domain.BackupRetentionRun) error
}

// BackupRetentionResponder publishes retention progress observations and terminal results.
type BackupRetentionResponder struct {
	publisher backupResultPublisher
	signer    canonicalnostr.Signer
	registry  backupRetentionPublishRegistry
	eventRepo repository.NostrEventRepository
	logger    *zap.Logger
}

func NewBackupRetentionResponder(publisher backupResultPublisher, signer canonicalnostr.Signer, registry backupRetentionPublishRegistry, eventRepo repository.NostrEventRepository, logger *zap.Logger) *BackupRetentionResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupRetentionResponder{publisher: publisher, signer: signer, registry: registry, eventRepo: eventRepo, logger: logger.Named("backup-retention-responder")}
}

func (r *BackupRetentionResponder) PublishBackupRetentionStatus(ctx context.Context, run *domain.BackupRetentionRun, step, message string) error {
	if r == nil || run == nil || strings.TrimSpace(run.RequestEventID) == "" || strings.TrimSpace(run.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRetention(run)
	content := map[string]any{
		"request_event_id": run.RequestEventID,
		"retention_run_id": run.ID.String(),
		"repository_id":    run.RepositoryID.String(),
		"policy_id":        uuidString(run.PolicyID),
		"backend":          string(run.Backend),
		"dry_run":          run.DryRun,
		"status":           status,
		"run_status":       string(run.Status),
		"step":             strings.TrimSpace(step),
		"message":          strings.TrimSpace(message),
		"created_at":       time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(content)
	tags := backupRetentionTags(run, nostr.Tags{{"d", fmt.Sprintf("observation:retention:%s:%s", run.ID, strings.TrimSpace(step))}, {"e", run.RequestEventID, "", "reply"}, {"p", run.RequestedBy}, {"status", status}, {"step", strings.TrimSpace(step)}, {"observation", "backup_retention"}, {"dry_run", fmt.Sprintf("%t", run.DryRun)}})
	summary, err := r.signPublishRecord(ctx, KindBackupObservation, tags, string(body), "backup.retention.observation", &run.ID)
	r.mergeRetentionPublishSummary(ctx, run, "backup_retention_observation", summary)
	return err
}

func (r *BackupRetentionResponder) PublishBackupRetentionResult(ctx context.Context, run *domain.BackupRetentionRun, message string) error {
	if r == nil || run == nil || strings.TrimSpace(run.RequestEventID) == "" || strings.TrimSpace(run.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRetention(run)
	content := map[string]any{
		"request_event_id": run.RequestEventID,
		"retention_run_id": run.ID.String(),
		"repository_id":    run.RepositoryID.String(),
		"policy_id":        uuidString(run.PolicyID),
		"backend":          string(run.Backend),
		"dry_run":          run.DryRun,
		"status":           status,
		"run_status":       string(run.Status),
		"evidence":         run.Evidence,
		"message":          strings.TrimSpace(message),
		"created_at":       time.Now().UTC().Format(time.RFC3339),
	}
	if run.Error != "" {
		content["error"] = map[string]any{"message": run.Error}
	}
	body, _ := json.Marshal(content)
	tags := backupRetentionTags(run, nostr.Tags{{"d", "result:" + run.RequestEventID}, {"e", run.RequestEventID, "", "reply"}, {"p", run.RequestedBy}, {"status", status}, {"dry_run", fmt.Sprintf("%t", run.DryRun)}})
	summary, err := r.signPublishRecord(ctx, KindBackupRetentionResult, tags, string(body), "backup.retention.result", &run.ID)
	r.mergeRetentionPublishSummary(ctx, run, "backup_retention_result", summary)
	return err
}

func (r *BackupRetentionResponder) signPublishRecord(ctx context.Context, kind int, tags nostr.Tags, content, entityType string, entityID *uuid.UUID) (map[string]any, error) {
	if r.publisher == nil || r.signer == nil {
		return map[string]any{"kind": kind, "published": false, "error": "backup retention responder publisher or signer is not configured", "recorded_at": time.Now().UTC().Format(time.RFC3339)}, fmt.Errorf("backup retention responder publisher or signer is not configured")
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

func (r *BackupRetentionResponder) mergeRetentionPublishSummary(ctx context.Context, run *domain.BackupRetentionRun, key string, summary map[string]any) {
	if r == nil || r.registry == nil || run == nil || len(summary) == 0 {
		return
	}
	if run.PublishSummary == nil {
		run.PublishSummary = map[string]any{}
	}
	run.PublishSummary[key] = summary
	if err := r.registry.CreateOrUpdateBackupRetentionRun(ctx, run); err != nil {
		r.logger.Warn("failed to persist backup retention publish summary", zap.String("retention_run_id", run.ID.String()), zap.String("summary_key", key), zap.Error(err))
	}
}

func (r *BackupRetentionResponder) record(ctx context.Context, ev *nostr.Event, entityType string, entityID *uuid.UUID) {
	if r == nil || r.eventRepo == nil || ev == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	if _, err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID, Kind: ev.Kind, PubKey: ev.PubKey, Content: ev.Content, Tags: tagsJSON, Sig: ev.Sig, CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: entityType, EntityID: entityID}); err != nil {
		r.logger.Warn("failed to record backup retention reply event", zap.String("event_id", ev.ID), zap.String("entity_type", entityType), zap.Error(err))
	}
}

func backupRetentionTags(run *domain.BackupRetentionRun, tags nostr.Tags) nostr.Tags {
	tags = append(tags, nostr.Tag{"retention", run.ID.String()}, nostr.Tag{"retention_run_id", run.ID.String()}, nostr.Tag{"repository_id", run.RepositoryID.String()}, nostr.Tag{"backend", string(run.Backend)})
	if run.PolicyID != nil {
		tags = append(tags, nostr.Tag{"policy", run.PolicyID.String()}, nostr.Tag{"policy_id", run.PolicyID.String()})
	}
	for _, key := range []string{"repository", "site", "environment", "worker"} {
		if value := backupMetadataString(run.Metadata, "nostr_tag_"+key); value != "" {
			tags = append(tags, nostr.Tag{key, value})
		}
	}
	return dedupeTags(tags)
}

func backupStatusForRetention(run *domain.BackupRetentionRun) string {
	if run == nil {
		return "failed"
	}
	switch run.Status {
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

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
