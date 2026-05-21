package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type BackupRetentionControlPlaneExecutor interface {
	ProcessBackupRetentionRun(ctx context.Context, runID uuid.UUID) error
}

type backupRetentionRegistry interface {
	CreateBackupRetentionRunIfAbsent(ctx context.Context, run *domain.BackupRetentionRun) (*domain.BackupRetentionRun, bool, error)
}

type backupRetentionRequest struct {
	RepositoryID string         `json:"repository_id,omitempty"`
	PolicyID     string         `json:"policy_id,omitempty"`
	DryRun       bool           `json:"dry_run,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

func (r *Reactor) handleBackupRetentionRequest(ctx context.Context, event *nostr.Event) {
	if !r.authorizeBackupCommandRequest(ctx, event, "backup_retention", KindBackupRetentionResult) {
		return
	}
	registry, ok := r.backupRegistry.(backupRetentionRegistry)
	if !ok {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRetentionResult, "failed", "backup_retention_unavailable", "backup retention registry is not configured")
		return
	}
	if r.backupRetentionExecutor == nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRetentionResult, "failed", "backup_retention_coordinator_unavailable", "backup retention coordinator is not configured")
		return
	}
	req, err := parseBackupRetentionRequest(event)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRetentionResult, "failed", "parse_error", err.Error())
		return
	}
	repositoryID, err := uuid.Parse(req.RepositoryID)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRetentionResult, "failed", "validation_error", "repository_id must be a UUID")
		return
	}
	policyID, err := uuid.Parse(req.PolicyID)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRetentionResult, "failed", "validation_error", "policy_id must be a UUID")
		return
	}
	run := &domain.BackupRetentionRun{
		ID:             uuid.New(),
		RepositoryID:   repositoryID,
		PolicyID:       &policyID,
		RequestedBy:    event.PubKey,
		RequestEventID: event.ID,
		RequestKind:    event.Kind,
		RequestDTag:    tagValueNostr(event.Tags, "d"),
		Status:         domain.RunStatusQueued,
		DryRun:         req.DryRun,
		Metadata: backupNostrMetadata(event, req.Metadata, map[string]any{
			"nostr_request_command": "backup_retention",
			"nostr_repository_id":   repositoryID.String(),
			"nostr_policy_id":       policyID.String(),
			"nostr_dry_run":         req.DryRun,
		}),
	}
	createdRun, created, err := registry.CreateBackupRetentionRunIfAbsent(ctx, run)
	if err != nil {
		_ = r.publishBackupCommandFailure(ctx, event, KindBackupRetentionResult, "failed", "retention_create_error", err.Error())
		return
	}
	if r.backupRetentionResponder != nil {
		step := "queued"
		message := "backup retention enforcement queued"
		if !created {
			step = "duplicate"
			message = "backup retention request already accepted for this requester and d tag"
		}
		_ = r.backupRetentionResponder.PublishBackupRetentionStatus(ctx, createdRun, step, message)
	}
	if !created {
		if backupRetentionTerminal(createdRun) && r.backupRetentionResponder != nil {
			_ = r.backupRetentionResponder.PublishBackupRetentionResult(ctx, createdRun, "backup retention already completed")
		}
		return
	}
	go func(runID uuid.UUID) {
		if err := r.backupRetentionExecutor.ProcessBackupRetentionRun(ctx, runID); err != nil {
			r.logger.Warn("backup retention executor failed", "run_id", runID.String(), "error", err)
		}
	}(createdRun.ID)
}

func parseBackupRetentionRequest(event *nostr.Event) (*backupRetentionRequest, error) {
	var req backupRetentionRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, err
		}
	}
	if req.RepositoryID == "" {
		req.RepositoryID = tagValueNostr(event.Tags, "repository_id")
	}
	if req.PolicyID == "" {
		req.PolicyID = tagValueNostr(event.Tags, "policy_id")
	}
	if strings.TrimSpace(req.RepositoryID) == "" {
		return nil, fmt.Errorf("repository_id is required")
	}
	if strings.TrimSpace(req.PolicyID) == "" {
		return nil, fmt.Errorf("policy_id is required")
	}
	req.RepositoryID = strings.TrimSpace(req.RepositoryID)
	req.PolicyID = strings.TrimSpace(req.PolicyID)
	return &req, nil
}

func backupRetentionTerminal(run *domain.BackupRetentionRun) bool {
	return run != nil && (run.Status == domain.RunStatusSucceeded || run.Status == domain.RunStatusFailed || run.Status == domain.RunStatusCancelled || run.Status == domain.RunStatusTimeout)
}
