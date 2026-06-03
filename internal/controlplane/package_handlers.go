package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

func (r *Reactor) recoverPackageIntents(ctx context.Context) {
	if r.packageProjection == nil {
		return
	}
	intents, err := r.packageProjection.ListNonTerminalIntents(ctx, 100)
	if err != nil {
		r.logger.Warn("package intent recovery query failed", "error", err)
		return
	}
	for _, intent := range intents {
		r.logger.Warn("marking non-terminal package intent failed after restart; submit a fresh signed package intent to retry", "intent", intent.ID.String(), "request_event_id", intent.RequestEventID, "operation", intent.Operation)
		now := time.Now().UTC()
		intent.Status = domain.PackageIntentStatusFailed
		intent.ErrorMessage = "package intent was non-terminal during reactor startup; submit a fresh signed intent to retry"
		intent.UpdatedAt = now
		intent.CompletedAt = &now
		if intent.ResultPayload == nil {
			intent.ResultPayload = map[string]any{}
		}
		intent.ResultPayload["status"] = "failed"
		intent.ResultPayload["error"] = intent.ErrorMessage
		if upsertErr := r.packageProjection.UpsertIntent(ctx, &intent); upsertErr != nil {
			r.logger.Warn("failed to mark recovered package intent terminal", "intent", intent.ID.String(), "error", upsertErr)
		}
	}
}

func (r *Reactor) handlePackageRepositoryApply(ctx context.Context, event *nostr.Event) {
	var cmd PackageRepositoryApplyCommand
	if !r.decodePackageRequest(ctx, event, &cmd) {
		return
	}
	intent, ok := r.beginPackageIntent(ctx, event, domain.PackageOperationRepositoryApply, packageIntentFields{RepositoryID: cmd.RepositoryID, RepositoryName: cmd.Name}, cmd)
	if !ok {
		return
	}
	repo := &domain.PackageRepository{ID: cmd.RepositoryID, Name: cmd.Name, Format: cmd.Format, BackendRef: cmd.BackendRef, BackendType: cmd.BackendType, ExternalRepositoryName: cmd.ExternalRepositoryName, Description: cmd.Description, NamespacePrefix: cmd.NamespacePrefix, Policy: cmd.Policy, Metadata: cmd.Metadata}
	existing, _ := r.packageProjection.GetRepositoryByName(ctx, cmd.Name)
	if cmd.RepositoryID != uuid.Nil {
		if byID, err := r.packageProjection.GetRepository(ctx, cmd.RepositoryID); err == nil && byID != nil {
			existing = byID
		}
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "approved", "repository policy accepted")
	out, err := r.packageService.EnsureRepository(ctx, repo, existing)
	if err != nil {
		r.finishPackageIntent(ctx, event, intent, "repository_apply", nil, err)
		return
	}
	if pubErr := r.publishPackageRepositoryRegistry(ctx, out); pubErr != nil {
		r.logger.Warn("publish package repository registry failed", "error", pubErr)
	}
	result := map[string]any{"operation": "repository_apply", "status": "succeeded", "repository": out}
	r.finishPackageIntent(ctx, event, intent, "repository_apply", result, nil)
}

func (r *Reactor) handlePackageRepositoryDelete(ctx context.Context, event *nostr.Event) {
	var cmd PackageRepositoryDeleteCommand
	if !r.decodePackageRequest(ctx, event, &cmd) {
		return
	}
	repo, err := r.lookupPackageRepository(ctx, cmd.RepositoryID, cmd.RepositoryName)
	if err != nil {
		intent, ok := r.beginPackageIntent(ctx, event, domain.PackageOperationRepositoryDelete, packageIntentFields{RepositoryID: cmd.RepositoryID, RepositoryName: cmd.RepositoryName}, cmd)
		if ok {
			r.finishPackageIntent(ctx, event, intent, "repository_delete", map[string]any{"operation": "repository_delete", "status": "succeeded", "already_deleted": true}, nil)
		}
		return
	}
	intent, ok := r.beginPackageIntent(ctx, event, domain.PackageOperationRepositoryDelete, packageIntentFields{RepositoryID: repo.ID, RepositoryName: repo.Name}, cmd)
	if !ok {
		return
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "approved", "repository delete policy accepted")
	out, err := r.packageService.DeleteRepository(ctx, repo, cmd.Force)
	if err != nil {
		r.finishPackageIntent(ctx, event, intent, "repository_delete", nil, err)
		return
	}
	if pubErr := r.publishPackageRepositoryRegistry(ctx, out); pubErr != nil {
		r.logger.Warn("publish package repository registry failed", "error", pubErr)
	}
	r.finishPackageIntent(ctx, event, intent, "repository_delete", map[string]any{"operation": "repository_delete", "status": "succeeded", "repository": out}, nil)
}

func (r *Reactor) handlePackagePublishIntent(ctx context.Context, event *nostr.Event) {
	var cmd PackagePublishCommand
	if !r.decodePackageRequest(ctx, event, &cmd) {
		return
	}
	repo, err := r.lookupPackageRepository(ctx, cmd.RepositoryID, cmd.RepositoryName)
	if err != nil {
		r.publishPackageError(ctx, event, domain.PackageOperationArtifactPublish, "validation", err.Error())
		return
	}
	intent, ok := r.beginPackageIntent(ctx, event, domain.PackageOperationArtifactPublish, packageIntentFields{RepositoryID: repo.ID, RepositoryName: repo.Name, Namespace: cmd.Namespace, PackageName: cmd.PackageName, Version: cmd.Version, Filename: cmd.Filename}, cmd)
	if !ok {
		return
	}
	if repo.Policy.PublishRequiresApproval && strings.TrimSpace(cmd.ApprovedBy) == "" {
		_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "rejected", "publication requires approval")
		r.finishPackageIntent(ctx, event, intent, "artifact_publish", nil, fmt.Errorf("publication requires approval: %w", service.ErrPackageApprovalRequired))
		return
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "checking", "checking publication policy")
	existing, _ := r.packageProjection.GetArtifact(ctx, repo.ID, strings.Trim(cmd.Namespace, "/"), cmd.PackageName, cmd.Version, cmd.Filename)
	artifact, err := r.packageService.PublishPackage(ctx, repo, existing, service.PackagePublishRequest{Namespace: cmd.Namespace, PackageName: cmd.PackageName, Version: cmd.Version, Filename: cmd.Filename, SourceURL: cmd.SourceURL, SHA256: cmd.SHA256, SizeBytes: cmd.SizeBytes, ContentType: cmd.ContentType, Metadata: cmd.Metadata})
	if err != nil {
		r.finishPackageIntent(ctx, event, intent, "artifact_publish", nil, err)
		return
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "approved", "publication policy accepted")
	if pubErr := r.publishPackageArtifactRegistry(ctx, artifact); pubErr != nil {
		r.logger.Warn("publish package artifact registry failed", "error", pubErr)
	}
	now := time.Now().UTC()
	publication := &domain.PackagePublication{ID: uuid.New(), RepositoryID: repo.ID, ArtifactID: artifact.ID, Status: domain.PackagePublicationStatusSucceeded, PolicyDecision: domain.PackagePolicyDecisionAllowed, PolicyRef: cmd.PolicyRef, ApprovedBy: cmd.ApprovedBy, PublishedAt: &now, Metadata: map[string]any{"operation": "artifact_publish"}, CreatedAt: now, UpdatedAt: now}
	if pubErr := r.publishPackagePromotionRegistry(ctx, publication); pubErr != nil {
		r.logger.Warn("publish package publication registry failed", "error", pubErr)
	}
	r.finishPackageIntent(ctx, event, intent, "artifact_publish", map[string]any{"operation": "artifact_publish", "status": "succeeded", "artifact": artifact, "publication": publication}, nil)
}

func (r *Reactor) handlePackagePromotionRequest(ctx context.Context, event *nostr.Event) {
	var cmd PackagePromotionCommand
	if !r.decodePackageRequest(ctx, event, &cmd) {
		return
	}
	sourceRepo, err := r.lookupPackageRepository(ctx, cmd.SourceRepositoryID, cmd.SourceRepositoryName)
	if err != nil {
		r.publishPackageError(ctx, event, domain.PackageOperationPromote, "validation", err.Error())
		return
	}
	targetRepo, err := r.lookupPackageRepository(ctx, cmd.TargetRepositoryID, cmd.TargetRepositoryName)
	if err != nil {
		r.publishPackageError(ctx, event, domain.PackageOperationPromote, "validation", err.Error())
		return
	}
	intent, ok := r.beginPackageIntent(ctx, event, domain.PackageOperationPromote, packageIntentFields{RepositoryID: sourceRepo.ID, RepositoryName: sourceRepo.Name, Namespace: cmd.Namespace, PackageName: cmd.PackageName, Version: cmd.Version, Filename: cmd.Filename}, cmd)
	if !ok {
		return
	}
	artifact, err := r.packageProjection.GetArtifact(ctx, sourceRepo.ID, strings.Trim(cmd.Namespace, "/"), cmd.PackageName, cmd.Version, cmd.Filename)
	if err != nil || artifact == nil {
		r.finishPackageIntent(ctx, event, intent, "promote", nil, fmt.Errorf("source package artifact not found"))
		return
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "checking", "checking promotion policy")
	existingTarget, _ := r.packageProjection.GetArtifact(ctx, targetRepo.ID, artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
	target, publication, err := r.packageService.PromotePackage(ctx, sourceRepo, targetRepo, artifact, existingTarget, service.PackagePromotionRequest{Environment: cmd.Environment, Channel: cmd.Channel, ApprovedBy: cmd.ApprovedBy, PolicyRef: cmd.PolicyRef, Metadata: cmd.Metadata})
	if err != nil {
		status := "failed"
		if errors.Is(err, service.ErrPackageApprovalRequired) || errors.Is(err, service.ErrPackagePolicyDenied) {
			status = "rejected"
		}
		_ = r.publishPackageStatus(ctx, event, intent, "policy_check", status, err.Error())
		r.finishPackageIntent(ctx, event, intent, "promote", nil, err)
		return
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "approved", "promotion policy accepted")
	publication.Status = domain.PackagePublicationStatusSucceeded
	if pubErr := r.publishPackageArtifactRegistry(ctx, target); pubErr != nil {
		r.logger.Warn("publish promoted package artifact registry failed", "error", pubErr)
	}
	if pubErr := r.publishPackagePromotionRegistry(ctx, publication); pubErr != nil {
		r.logger.Warn("publish package promotion registry failed", "error", pubErr)
	}
	r.finishPackageIntent(ctx, event, intent, "promote", map[string]any{"operation": "promote", "status": "succeeded", "artifact": target, "promotion": publication}, nil)
}

func (r *Reactor) handlePackageYankRequest(ctx context.Context, event *nostr.Event) {
	var cmd PackageYankCommand
	if !r.decodePackageRequest(ctx, event, &cmd) {
		return
	}
	op := domain.PackageOperationYank
	if cmd.Deprecated {
		op = domain.PackageOperationDeprecate
	}
	repo, err := r.lookupPackageRepository(ctx, cmd.RepositoryID, cmd.RepositoryName)
	if err != nil {
		r.publishPackageError(ctx, event, op, "validation", err.Error())
		return
	}
	intent, ok := r.beginPackageIntent(ctx, event, op, packageIntentFields{RepositoryID: repo.ID, RepositoryName: repo.Name, Namespace: cmd.Namespace, PackageName: cmd.PackageName, Version: cmd.Version, Filename: cmd.Filename}, cmd)
	if !ok {
		return
	}
	_ = r.publishPackageStatus(ctx, event, intent, "policy_check", "approved", "yank policy accepted")
	existing, _ := r.packageProjection.GetArtifact(ctx, repo.ID, strings.Trim(cmd.Namespace, "/"), cmd.PackageName, cmd.Version, cmd.Filename)
	artifact, err := r.packageService.YankPackage(ctx, repo, existing, service.PackageYankRequest{Namespace: cmd.Namespace, PackageName: cmd.PackageName, Version: cmd.Version, Filename: cmd.Filename, Reason: cmd.Reason, Metadata: cmd.Metadata})
	if err != nil {
		r.finishPackageIntent(ctx, event, intent, string(op), nil, err)
		return
	}
	if cmd.Deprecated && artifact.Metadata == nil {
		artifact.Metadata = map[string]any{}
	}
	if cmd.Deprecated {
		artifact.Metadata["deprecated"] = true
		artifact.Metadata["deprecation_reason"] = cmd.Reason
	}
	if pubErr := r.publishPackageArtifactRegistry(ctx, artifact); pubErr != nil {
		r.logger.Warn("publish yanked package artifact registry failed", "error", pubErr)
	}
	r.finishPackageIntent(ctx, event, intent, string(op), map[string]any{"operation": op, "status": "succeeded", "artifact": artifact}, nil)
}

func (r *Reactor) handlePackageDriftDetect(ctx context.Context, event *nostr.Event) {
	var cmd PackageDriftDetectCommand
	if !r.decodePackageRequest(ctx, event, &cmd) {
		return
	}
	repo, err := r.lookupPackageRepository(ctx, cmd.RepositoryID, cmd.RepositoryName)
	if err != nil {
		r.publishPackageError(ctx, event, domain.PackageOperationDriftDetect, "validation", err.Error())
		return
	}
	intent, ok := r.beginPackageIntent(ctx, event, domain.PackageOperationDriftDetect, packageIntentFields{RepositoryID: repo.ID, RepositoryName: repo.Name}, cmd)
	if !ok {
		return
	}
	observations := []service.PackageDriftObservation{}
	repoObs, err := r.packageService.ObserveRepositoryDrift(ctx, repo)
	if err != nil {
		r.finishPackageIntent(ctx, event, intent, "drift_detect", nil, err)
		return
	}
	observations = append(observations, *repoObs)
	if cmd.IncludeArtifacts {
		const pageSize = 500
		for offset := 0; ; offset += pageSize {
			artifacts, listErr := r.packageProjection.ListArtifacts(ctx, repo.ID, pageSize, offset)
			if listErr != nil {
				r.finishPackageIntent(ctx, event, intent, "drift_detect", nil, listErr)
				return
			}
			for i := range artifacts {
				obs, obsErr := r.packageService.ObserveArtifactDrift(ctx, repo, &artifacts[i])
				if obsErr != nil {
					observations = append(observations, service.PackageDriftObservation{ResourceKind: "artifact", ResourceID: artifacts[i].ID.String(), Expected: true, Observed: false, Drifted: true, Reason: obsErr.Error()})
					continue
				}
				observations = append(observations, *obs)
			}
			if len(artifacts) < pageSize {
				break
			}
		}
	}
	drifted := false
	for _, obs := range observations {
		if obs.Drifted {
			drifted = true
			break
		}
	}
	_ = r.publishPackageDriftEvent(ctx, event, repo, observations, drifted)
	r.finishPackageIntent(ctx, event, intent, "drift_detect", map[string]any{"operation": "drift_detect", "status": "succeeded", "drifted": drifted, "observations": observations, "repository_last_event_id": repo.LastEventID}, nil)
}

type packageIntentFields struct {
	RepositoryID   uuid.UUID
	RepositoryName string
	Namespace      string
	PackageName    string
	Version        string
	Filename       string
}

func (r *Reactor) decodePackageRequest(ctx context.Context, event *nostr.Event, dest any) bool {
	if !r.isAuthorized(event.PubKey) {
		r.publishPackageError(ctx, event, "", "unauthorized", "requester not in authorized list")
		return false
	}
	if r.packageService == nil || r.packageProjection == nil {
		r.publishPackageError(ctx, event, "", "unavailable", "package control plane is not configured")
		return false
	}
	if err := json.Unmarshal([]byte(event.Content), dest); err != nil {
		r.publishPackageError(ctx, event, "", "parse_error", err.Error())
		return false
	}
	return true
}

func (r *Reactor) beginPackageIntent(ctx context.Context, event *nostr.Event, operation domain.PackageOperation, fields packageIntentFields, payload any) (*domain.PackageIntent, bool) {
	existing, _ := r.packageProjection.GetIntentByRequestEventID(ctx, event.ID)
	if existing != nil && existing.Status.Terminal() {
		_ = r.publishPackageResult(ctx, event, existing, string(operation), existing.ResultPayload, existing.ErrorMessage)
		return existing, false
	}
	if existing != nil && !existing.Status.Terminal() {
		_ = r.publishPackageStatus(ctx, event, existing, "idempotency", "already_processing", "request is already being processed")
		return existing, false
	}
	requestPayload := map[string]any{}
	if b, err := json.Marshal(payload); err == nil {
		_ = json.Unmarshal(b, &requestPayload)
	}
	now := time.Now().UTC()
	intent := &domain.PackageIntent{ID: uuid.New(), RequestEventID: event.ID, Operation: operation, RepositoryName: fields.RepositoryName, Namespace: strings.Trim(fields.Namespace, "/"), PackageName: fields.PackageName, Version: fields.Version, Filename: fields.Filename, RequesterPubkey: event.PubKey, RequestPayload: requestPayload, Status: domain.PackageIntentStatusAccepted, CreatedAt: now, UpdatedAt: now}
	if fields.RepositoryID != uuid.Nil {
		intent.RepositoryID = &fields.RepositoryID
	}
	if err := r.packageProjection.UpsertIntent(ctx, intent); err != nil {
		r.publishPackageError(ctx, event, operation, "projection_error", err.Error())
		return nil, false
	}
	_ = r.publishPackageStatus(ctx, event, intent, "accepted", "accepted", "package request accepted")
	intent.Status = domain.PackageIntentStatusExecuting
	intent.UpdatedAt = time.Now().UTC()
	_ = r.packageProjection.UpsertIntent(ctx, intent)
	_ = r.publishPackageStatus(ctx, event, intent, "executing", "running", "package request executing")
	return intent, true
}

func (r *Reactor) finishPackageIntent(ctx context.Context, event *nostr.Event, intent *domain.PackageIntent, operation string, result map[string]any, err error) {
	now := time.Now().UTC()
	if result == nil {
		result = map[string]any{"operation": operation}
	}
	if err != nil {
		result["status"] = "failed"
		result["error"] = err.Error()
		if errors.Is(err, service.ErrPackagePolicyDenied) || errors.Is(err, service.ErrPackageApprovalRequired) {
			result["status"] = "rejected"
		}
		intent.Status = domain.PackageIntentStatusFailed
		intent.ErrorMessage = err.Error()
	} else {
		result["status"] = "succeeded"
		intent.Status = domain.PackageIntentStatusSucceeded
	}
	intent.ResultPayload = result
	intent.UpdatedAt = now
	intent.CompletedAt = &now
	_ = r.packageProjection.UpsertIntent(ctx, intent)
	_ = r.publishPackageResult(ctx, event, intent, operation, result, intent.ErrorMessage)
}

func (r *Reactor) lookupPackageRepository(ctx context.Context, id uuid.UUID, name string) (*domain.PackageRepository, error) {
	if id != uuid.Nil {
		repo, err := r.packageProjection.GetRepository(ctx, id)
		if err != nil {
			return nil, err
		}
		if repo != nil {
			return repo, nil
		}
	}
	if strings.TrimSpace(name) != "" {
		repo, err := r.packageProjection.GetRepositoryByName(ctx, strings.TrimSpace(name))
		if err != nil {
			return nil, err
		}
		if repo != nil {
			return repo, nil
		}
	}
	return nil, fmt.Errorf("package repository not found")
}

func (r *Reactor) publishPackageStatus(ctx context.Context, requestEvent *nostr.Event, intent *domain.PackageIntent, step, status, message string) error {
	content := map[string]any{"intent_id": intent.ID.String(), "request_event_id": requestEvent.ID, "operation": string(intent.Operation), "step": step, "status": status, "message": message}
	tags := packageReplyTags(requestEvent, intent, step, status)
	tags = append(tags, nostr.Tag{"domain", "package"}, nostr.Tag{"schema", "bahia.status.package.v1"}, nostr.Tag{"legacy_kind", fmt.Sprintf("%d", KindPackageStatus)})
	if err := r.publishCanonicalStatus(ctx, requestEvent, tags, content); err != nil {
		return err
	}
	return nil
}

func (r *Reactor) publishPackageResult(ctx context.Context, requestEvent *nostr.Event, intent *domain.PackageIntent, operation string, result map[string]any, errorMessage string) error {
	if result == nil {
		result = map[string]any{}
	}
	result["intent_id"] = intent.ID.String()
	result["request_event_id"] = requestEvent.ID
	result["operation"] = operation
	status := stringFromAny(result["status"])
	if status == "" {
		status = "succeeded"
	}
	tags := packageReplyTags(requestEvent, intent, "result", status)
	if errorMessage != "" {
		tags = append(tags, nostr.Tag{"error", errorMessage})
	}
	tags = append(tags, nostr.Tag{"domain", "package"}, nostr.Tag{"schema", "bahia.result.package.v1"}, nostr.Tag{"legacy_kind", fmt.Sprintf("%d", KindPackageResult)})
	var rpcErr *JSONRPCError
	if errorMessage != "" || status == "failed" || status == "rejected" {
		rpcErr = &JSONRPCError{Code: -32000, Message: errorMessage}
		if rpcErr.Message == "" {
			rpcErr.Message = status
		}
	}
	if err := r.publishContextVMResult(ctx, requestEvent, result, tags, rpcErr); err != nil {
		return err
	}
	return nil
}

func (r *Reactor) publishPackageError(ctx context.Context, requestEvent *nostr.Event, operation domain.PackageOperation, step, message string) error {
	intent := &domain.PackageIntent{ID: uuid.New(), RequestEventID: requestEvent.ID, Operation: operation, RequesterPubkey: requestEvent.PubKey, Status: domain.PackageIntentStatusFailed, ErrorMessage: message}
	return r.publishPackageResult(ctx, requestEvent, intent, string(operation), map[string]any{"status": "failed", "step": step, "error": message}, message)
}

func packageReplyTags(requestEvent *nostr.Event, intent *domain.PackageIntent, step, status string) nostr.Tags {
	tags := nostr.Tags{{"e", requestEvent.ID, "", "reply"}, {"p", requestEvent.PubKey}, {"operation", string(intent.Operation)}, {"status", status}, {"step", step}, {"intent", intent.ID.String()}}
	if intent.RepositoryID != nil {
		tags = append(tags, nostr.Tag{"repository", intent.RepositoryID.String()})
	}
	if intent.RepositoryName != "" {
		tags = append(tags, nostr.Tag{"repository_name", intent.RepositoryName})
	}
	if intent.PackageName != "" {
		tags = append(tags, nostr.Tag{"package", intent.PackageName})
	}
	if intent.Version != "" {
		tags = append(tags, nostr.Tag{"version", intent.Version})
	}
	if intent.Filename != "" {
		tags = append(tags, nostr.Tag{"filename", intent.Filename})
	}
	return tags
}

func (r *Reactor) publishPackageRepositoryRegistry(ctx context.Context, repo *domain.PackageRepository) error {
	event := &nostr.Event{Kind: KindCASControlState, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "package:repository:" + repo.ID.String()}, {"domain", "package"}, {"entity", "repository"}, {"schema", "bahia.state.package-repository.v1"}, {"legacy_kind", fmt.Sprintf("%d", KindPackageRepositoryRegistry)}, {"repository", repo.ID.String()}, {"name", repo.Name}, {"backend_ref", repo.BackendRef}, {"format", string(repo.Format)}, {"status", string(repo.Status)}, {"deleted", fmt.Sprintf("%t", repo.Deleted)}}, Content: mustJSON(repo)}
	if err := r.signEvent(ctx, event); err != nil {
		return err
	}
	if _, err := r.publishEvent(ctx, event); err != nil {
		return err
	}
	repo.LastEventID = event.ID
	repo.LastEventCreatedAt = event.CreatedAt.Time()
	return r.packageProjection.UpsertRepository(ctx, repo)
}

func (r *Reactor) publishPackageArtifactRegistry(ctx context.Context, artifact *domain.PackageArtifact) error {
	d := fmt.Sprintf("%s:%s:%s:%s:%s", artifact.RepositoryID, artifact.Namespace, artifact.PackageName, artifact.Version, artifact.Filename)
	event := &nostr.Event{Kind: KindCASControlState, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "package:artifact:" + d}, {"domain", "package"}, {"entity", "artifact"}, {"schema", "bahia.state.package-artifact.v1"}, {"legacy_kind", fmt.Sprintf("%d", KindPackageArtifactRegistry)}, {"artifact", artifact.ID.String()}, {"repository", artifact.RepositoryID.String()}, {"repository_name", artifact.RepositoryName}, {"package", artifact.PackageName}, {"version", artifact.Version}, {"filename", artifact.Filename}, {"sha256", artifact.SHA256}, {"status", string(artifact.Status)}, {"deleted", fmt.Sprintf("%t", artifact.Deleted)}}, Content: mustJSON(artifact)}
	if err := r.signEvent(ctx, event); err != nil {
		return err
	}
	if _, err := r.publishEvent(ctx, event); err != nil {
		return err
	}
	artifact.LastEventID = event.ID
	artifact.LastEventCreatedAt = event.CreatedAt.Time()
	return r.packageProjection.UpsertArtifact(ctx, artifact)
}

func (r *Reactor) publishPackagePromotionRegistry(ctx context.Context, publication *domain.PackagePublication) error {
	event := &nostr.Event{Kind: KindCASControlState, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "package:promotion:" + publication.ID.String()}, {"domain", "package"}, {"entity", "promotion"}, {"schema", "bahia.state.package-promotion.v1"}, {"legacy_kind", fmt.Sprintf("%d", KindPackagePromotionRegistry)}, {"promotion", publication.ID.String()}, {"repository", publication.RepositoryID.String()}, {"artifact", publication.ArtifactID.String()}, {"status", string(publication.Status)}, {"policy_decision", string(publication.PolicyDecision)}}, Content: mustJSON(publication)}
	if publication.TargetRepositoryID != nil {
		event.Tags = append(event.Tags, nostr.Tag{"target_repository", publication.TargetRepositoryID.String()})
	}
	if err := r.signEvent(ctx, event); err != nil {
		return err
	}
	if _, err := r.publishEvent(ctx, event); err != nil {
		return err
	}
	publication.LastEventID = event.ID
	publication.LastEventCreatedAt = event.CreatedAt.Time()
	return r.packageProjection.UpsertPublication(ctx, publication)
}

func (r *Reactor) publishPackageDriftEvent(ctx context.Context, requestEvent *nostr.Event, repo *domain.PackageRepository, observations []service.PackageDriftObservation, drifted bool) error {
	status := "ok"
	if drifted {
		status = "drifted"
	}
	content := map[string]any{"repository_id": repo.ID.String(), "repository_name": repo.Name, "status": status, "drifted": drifted, "observations": observations, "repository_last_event_id": repo.LastEventID}
	tags := nostr.Tags{{"domain", "package"}, {"schema", "bahia.result.package-drift.v1"}, {"legacy_kind", fmt.Sprintf("%d", KindPackageDriftEvent)}, {"repository", repo.ID.String()}, {"repository_name", repo.Name}, {"status", status}}
	return r.publishContextVMResult(ctx, requestEvent, content, tags, nil)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
