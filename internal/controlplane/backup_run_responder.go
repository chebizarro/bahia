package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type backupPublishRegistry interface {
	CreateOrUpdateBackupRun(ctx context.Context, run *domain.BackupRun) error
	RecordBackupVerification(ctx context.Context, record *domain.BackupVerificationRecord) error
}

type backupResultPublisher interface {
	PublishWithResults(ctx context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error)
}

// BackupRunResponder publishes Nostr-native backup status, result, and attestation events.
type BackupRunResponder struct {
	publisher backupResultPublisher
	signer    canonicalnostr.Signer
	registry  backupPublishRegistry
	eventRepo repository.NostrEventRepository
	logger    *zap.Logger
}

func NewBackupRunResponder(publisher backupResultPublisher, signer canonicalnostr.Signer, registry backupPublishRegistry, eventRepo repository.NostrEventRepository, logger *zap.Logger) *BackupRunResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupRunResponder{publisher: publisher, signer: signer, registry: registry, eventRepo: eventRepo, logger: logger.Named("backup-run-responder")}
}

func (r *BackupRunResponder) PublishBackupRunStatus(ctx context.Context, run *domain.BackupRun, step, message string) error {
	if r == nil || run == nil || strings.TrimSpace(run.RequestEventID) == "" || strings.TrimSpace(run.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRun(run)
	content := map[string]any{
		"request_event_id":    run.RequestEventID,
		"run":                 run.ID.String(),
		"status":              status,
		"run_status":          string(run.Status),
		"step":                strings.TrimSpace(step),
		"message":             strings.TrimSpace(message),
		"verification_status": string(run.VerificationStatus),
		"restore_eligible":    domain.BackupRunRestoreEligible(run),
		"created_at":          time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(content)
	tags := backupRunTags(run, nostr.Tags{{"d", fmt.Sprintf("status:%s:%s", run.ID, strings.TrimSpace(step))}, {"e", run.RequestEventID, "", "reply"}, {"p", run.RequestedBy}, {"status", status}, {"step", strings.TrimSpace(step)}})
	summary, err := r.signPublishRecord(ctx, KindBackupRunStatus, tags, string(body), "backup.run.status", &run.ID)
	r.mergeRunPublishSummary(ctx, run, "backup_run_status", summary)
	return err
}

func (r *BackupRunResponder) PublishBackupRunResult(ctx context.Context, run *domain.BackupRun, verification *domain.BackupVerificationRecord, message string) error {
	if r == nil || run == nil || strings.TrimSpace(run.RequestEventID) == "" || strings.TrimSpace(run.RequestedBy) == "" {
		return nil
	}
	status := backupStatusForRun(run)
	content := map[string]any{
		"request_event_id":    run.RequestEventID,
		"status":              status,
		"message":             strings.TrimSpace(message),
		"run":                 run.ID.String(),
		"recipe_id":           run.RecipeID.String(),
		"repository_id":       run.RepositoryID.String(),
		"backend":             string(run.Backend),
		"target_ref":          run.TargetRef,
		"snapshot_created":    run.SnapshotCreated,
		"snapshot_id":         run.SnapshotID,
		"verification_status": string(run.VerificationStatus),
		"restore_eligible":    domain.BackupRunRestoreEligible(run),
		"created_at":          time.Now().UTC().Format(time.RFC3339),
	}
	if run.PolicyID != nil {
		content["policy_id"] = run.PolicyID.String()
	}
	if run.Error != "" {
		content["error"] = map[string]any{"message": run.Error}
	}
	if verification != nil {
		content["verification"] = map[string]any{"id": verification.ID.String(), "status": string(verification.Status), "verified": verification.Verified, "mode": string(verification.Mode), "error": verification.Error, "evidence": verification.Evidence}
	}
	body, _ := json.Marshal(content)
	tags := backupRunTags(run, nostr.Tags{{"d", "result:" + run.RequestEventID}, {"e", run.RequestEventID, "", "reply"}, {"p", run.RequestedBy}, {"status", status}, {"verification", string(run.VerificationStatus)}})
	resultSummary, resultErr := r.signPublishRecord(ctx, KindBackupRunResult, tags, string(body), "backup.run.result", &run.ID)
	r.mergeRunPublishSummary(ctx, run, "backup_run_result", resultSummary)

	runAttestation, attestErr := r.publishRunAttestation(ctx, run, status, resultSummary)
	r.mergeRunPublishSummary(ctx, run, "backup_run_attestation", runAttestation)

	var verifyErr error
	if verification != nil {
		verificationSummary, err := r.publishVerificationAttestation(ctx, run, verification)
		verifyErr = err
		r.mergeVerificationPublishSummary(ctx, verification, "backup_verification_attestation", verificationSummary)
	}
	return firstErr(resultErr, attestErr, verifyErr)
}

func (r *BackupRunResponder) publishRunAttestation(ctx context.Context, run *domain.BackupRun, status string, resultSummary map[string]any) (map[string]any, error) {
	content := map[string]any{
		"schema":              "bahia.backup.run.attestation.v1",
		"run":                 run.ID.String(),
		"request_event_id":    run.RequestEventID,
		"status":              status,
		"run_status":          string(run.Status),
		"snapshot_created":    run.SnapshotCreated,
		"snapshot_id":         run.SnapshotID,
		"verification_status": string(run.VerificationStatus),
		"restore_eligible":    domain.BackupRunRestoreEligible(run),
		"publish_result":      resultSummary,
		"attested_at":         time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(content)
	tags := backupRunTags(run, nostr.Tags{{"d", "backup-run:" + run.ID.String()}, {"e", run.RequestEventID, "", "reply"}, {"p", run.RequestedBy}, {"status", status}, {"attestation", "backup_run"}})
	return r.signPublishRecord(ctx, KindBackupRunAttestation, tags, string(body), "backup.run.attestation", &run.ID)
}

func (r *BackupRunResponder) publishVerificationAttestation(ctx context.Context, run *domain.BackupRun, verification *domain.BackupVerificationRecord) (map[string]any, error) {
	content := map[string]any{
		"schema":           "bahia.backup.verification.attestation.v1",
		"run":              run.ID.String(),
		"verification_id":  verification.ID.String(),
		"request_event_id": run.RequestEventID,
		"mode":             string(verification.Mode),
		"status":           string(verification.Status),
		"verified":         verification.Verified,
		"evidence":         verification.Evidence,
		"error":            verification.Error,
		"attested_at":      time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal(content)
	tags := backupRunTags(run, nostr.Tags{{"d", "backup-verification:" + run.ID.String()}, {"e", run.RequestEventID, "", "reply"}, {"p", run.RequestedBy}, {"status", string(verification.Status)}, {"verification", string(verification.Status)}, {"verification_id", verification.ID.String()}, {"attestation", "backup_verification"}})
	return r.signPublishRecord(ctx, KindBackupVerificationAttestation, tags, string(body), "backup.verification.attestation", &verification.ID)
}

func (r *BackupRunResponder) signPublishRecord(ctx context.Context, kind int, tags nostr.Tags, content, entityType string, entityID *uuid.UUID) (map[string]any, error) {
	if r.publisher == nil || r.signer == nil {
		return map[string]any{"kind": kind, "published": false, "error": "backup responder publisher or signer is not configured", "recorded_at": time.Now().UTC().Format(time.RFC3339)}, fmt.Errorf("backup responder publisher or signer is not configured")
	}
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: dedupeTags(tags), Content: content}
	if err := SignGoNostrEvent(ctx, r.signer, event); err != nil {
		return map[string]any{"kind": kind, "published": false, "error": err.Error(), "recorded_at": time.Now().UTC().Format(time.RFC3339)}, err
	}
	results, err := r.publisher.PublishWithResults(ctx, *event)
	summary := backupPublishSummary(kind, event, results, err)
	r.record(ctx, event, entityType, entityID)
	return summary, err
}

func backupRunTags(run *domain.BackupRun, tags nostr.Tags) nostr.Tags {
	tags = append(tags, nostr.Tag{"run", run.ID.String()}, nostr.Tag{"recipe_id", run.RecipeID.String()}, nostr.Tag{"repository_id", run.RepositoryID.String()}, nostr.Tag{"backend", string(run.Backend)}, nostr.Tag{"target", run.TargetRef})
	if run.PolicyID != nil {
		tags = append(tags, nostr.Tag{"policy_id", run.PolicyID.String()})
	}
	if value := firstNonEmpty(backupMetadataString(run.Metadata, "nostr_tag_recipe"), backupMetadataString(run.Metadata, "nostr_recipe_coord")); value != "" {
		tags = append(tags, nostr.Tag{"recipe", value})
	}
	if value := firstNonEmpty(backupMetadataString(run.Metadata, "nostr_tag_repository"), backupMetadataString(run.Metadata, "nostr_repository_name")); value != "" {
		tags = append(tags, nostr.Tag{"repository", value})
	}
	if value := firstNonEmpty(backupMetadataString(run.Metadata, "nostr_tag_policy"), backupMetadataString(run.Metadata, "nostr_policy_name")); value != "" {
		tags = append(tags, nostr.Tag{"policy", value})
	}
	for _, key := range []string{"recipe_id", "repository_id", "policy_id", "site", "environment", "worker", "verification"} {
		if value := backupMetadataString(run.Metadata, "nostr_tag_"+key); value != "" {
			tags = append(tags, nostr.Tag{key, value})
		}
	}
	return dedupeTags(tags)
}

func backupStatusForRun(run *domain.BackupRun) string {
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

func backupPublishSummary(kind int, event *nostr.Event, results []nostrpool.PublishResult, err error) map[string]any {
	outcomes := make([]map[string]any, 0, len(results))
	accepted := 0
	for _, result := range results {
		entry := map[string]any{"relay_url": result.RelayURL, "accepted": result.Accepted, "reason": result.Reason}
		if result.Error != nil {
			entry["error"] = result.Error.Error()
		}
		if result.Accepted || result.IsDuplicate() {
			accepted++
		}
		outcomes = append(outcomes, entry)
	}
	summary := map[string]any{"kind": kind, "event_id": event.ID, "accepted_relays": accepted, "relay_outcomes": outcomes, "recorded_at": time.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		summary["error"] = err.Error()
	}
	return summary
}

func (r *BackupRunResponder) mergeRunPublishSummary(ctx context.Context, run *domain.BackupRun, key string, summary map[string]any) {
	if r == nil || r.registry == nil || run == nil || len(summary) == 0 {
		return
	}
	if run.PublishSummary == nil {
		run.PublishSummary = map[string]any{}
	}
	run.PublishSummary[key] = summary
	if err := r.registry.CreateOrUpdateBackupRun(ctx, run); err != nil {
		r.logger.Warn("failed to persist backup run publish summary", zap.String("run_id", run.ID.String()), zap.String("summary_key", key), zap.Error(err))
	}
}

func (r *BackupRunResponder) mergeVerificationPublishSummary(ctx context.Context, verification *domain.BackupVerificationRecord, key string, summary map[string]any) {
	if r == nil || r.registry == nil || verification == nil || len(summary) == 0 {
		return
	}
	if verification.PublishSummary == nil {
		verification.PublishSummary = map[string]any{}
	}
	verification.PublishSummary[key] = summary
	if err := r.registry.RecordBackupVerification(ctx, verification); err != nil {
		r.logger.Warn("failed to persist backup verification publish summary", zap.String("verification_id", verification.ID.String()), zap.String("summary_key", key), zap.Error(err))
	}
}

func (r *BackupRunResponder) record(ctx context.Context, ev *nostr.Event, entityType string, entityID *uuid.UUID) {
	if r == nil || r.eventRepo == nil || ev == nil {
		return
	}
	tagsJSON, _ := json.Marshal(ev.Tags)
	if _, err := r.eventRepo.Record(ctx, &repository.NostrEventRecord{ID: ev.ID.Hex(), Kind: int(ev.Kind), PubKey: ev.PubKey.Hex(), Content: ev.Content, Tags: tagsJSON, Sig: nostr.HexEncodeToString(ev.Sig[:]), CreatedAt: ev.CreatedAt.Time(), ReceivedAt: time.Now().UTC(), EntityType: entityType, EntityID: entityID}); err != nil {
		r.logger.Warn("failed to record backup reply event", zap.String("event_id", ev.ID.Hex()), zap.String("entity_type", entityType), zap.Error(err))
	}
}

func backupMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	if value, ok := metadata[key]; ok && value != nil {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
