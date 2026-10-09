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
	if !r.authorizeBackupPausedRequest(ctx, event) {
		return
	}
	req, err := parseBackupRetentionRequest(event)
	if err == nil {
		_, err = uuid.Parse(req.RepositoryID)
	}
	if err == nil {
		_, err = uuid.Parse(req.PolicyID)
	}
	if err != nil {
		r.zapLog.Warn("dropping invalid backup retention request without signing a refusal", zap.String("request_event_id", event.ID.Hex()), zap.Error(err))
		return
	}
	r.publishPausedBackupRequest(ctx, event, KindBackupRetentionResult, "backup retention request intake is paused until canonical acceptance receipts are available")
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
