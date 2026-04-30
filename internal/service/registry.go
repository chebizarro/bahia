// Package service implements the core business logic for the Bahia Deployment Registry.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// ImageVerifier validates that an artifact image exists in the container registry.
// Implementations include the Harbor adapter (production) and NoopImageVerifier (dev/test).
type ImageVerifier interface {
	// VerifyImage checks that the given image reference exists and returns its digest and scan status.
	// imageRepo is the full repository path (e.g. "myproject/myimage"), reference is a tag or digest.
	VerifyImage(ctx context.Context, imageRepo, reference string) (*ImageVerification, error)
}

// ImageVerification holds the result of an image verification check.
type ImageVerification struct {
	Exists     bool
	Digest     string
	ScanStatus string
}

// NoopImageVerifier skips image verification. Used when no registry is configured.
type NoopImageVerifier struct{}

// VerifyImage always returns a successful verification with no digest or scan info.
func (n *NoopImageVerifier) VerifyImage(_ context.Context, _, _ string) (*ImageVerification, error) {
	return &ImageVerification{Exists: true}, nil
}

// RegistryService orchestrates the core deployment registry operations.
type RegistryService struct {
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	builds       repository.BuildRepository
	artifacts    repository.ArtifactRepository
	intents      repository.DeploymentIntentRepository
	runs         repository.DeploymentRunRepository
	observations repository.RuntimeObservationRepository
	state        repository.EnvironmentServiceStateRepository
	verifier     ImageVerifier
	publisher    events.Publisher
	logger       *zap.Logger
}

// NewRegistryService creates a new RegistryService.
// The verifier parameter is optional: pass nil to skip image verification (equivalent to NoopImageVerifier).
func NewRegistryService(
	services repository.ServiceRepository,
	environments repository.EnvironmentRepository,
	builds repository.BuildRepository,
	artifacts repository.ArtifactRepository,
	intents repository.DeploymentIntentRepository,
	runs repository.DeploymentRunRepository,
	observations repository.RuntimeObservationRepository,
	state repository.EnvironmentServiceStateRepository,
	verifier ImageVerifier,
	publisher events.Publisher,
	logger *zap.Logger,
) *RegistryService {
	if verifier == nil {
		verifier = &NoopImageVerifier{}
	}
	return &RegistryService{
		services:     services,
		environments: environments,
		builds:       builds,
		artifacts:    artifacts,
		intents:      intents,
		runs:         runs,
		observations: observations,
		state:        state,
		verifier:     verifier,
		publisher:    publisher,
		logger:       logger,
	}
}

// --- Service CRUD ---

func (s *RegistryService) CreateService(ctx context.Context, svc *domain.Service) error {
	if svc.RuntimeType == "" {
		svc.RuntimeType = domain.RuntimeTypeDocker
	}
	if svc.DefaultBranch == "" {
		svc.DefaultBranch = "main"
	}
	normalizeServiceRepositoryForWrite(svc)
	return s.services.Create(ctx, svc)
}

func (s *RegistryService) GetService(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	svc, err := s.services.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	normalizeServiceRepositoryForRead(svc)
	return svc, nil
}

func (s *RegistryService) GetServiceByName(ctx context.Context, name string) (*domain.Service, error) {
	svc, err := s.services.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	normalizeServiceRepositoryForRead(svc)
	return svc, nil
}

func (s *RegistryService) ListServices(ctx context.Context) ([]domain.Service, error) {
	svcs, err := s.services.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range svcs {
		normalizeServiceRepositoryForRead(&svcs[i])
	}
	return svcs, nil
}

func (s *RegistryService) UpdateService(ctx context.Context, svc *domain.Service) error {
	normalizeServiceRepositoryForWrite(svc)
	return s.services.Update(ctx, svc)
}

// DeleteService deletes a service after checking for dependent resources.
// If force is false and dependents exist, returns an error describing what would be cascaded.
func (s *RegistryService) DeleteService(ctx context.Context, id uuid.UUID, force bool) error {
	if !force {
		if pgRepo, ok := s.services.(*repository.PgServiceRepository); ok {
			builds, artifacts, intents, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			total := builds + artifacts + intents
			if total > 0 {
				return fmt.Errorf(
					"service has dependent resources (%d builds, %d artifacts, %d deployment intents); "+
						"use force=true to cascade delete or remove dependents first",
					builds, artifacts, intents,
				)
			}
		}
	}
	return s.services.Delete(ctx, id)
}

func normalizeServiceRepositoryForWrite(svc *domain.Service) {
	if svc == nil {
		return
	}

	svc.RepoURL = strings.TrimSpace(svc.RepoURL)

	if svc.Repository != nil {
		svc.Repository.CloneURL = strings.TrimSpace(svc.Repository.CloneURL)
		svc.Repository.WebURL = strings.TrimSpace(svc.Repository.WebURL)
		svc.Repository.Source = strings.TrimSpace(svc.Repository.Source)
		svc.Repository.RepoCoordinate = strings.TrimSpace(svc.Repository.RepoCoordinate)

		if svc.Repository.CI != nil {
			svc.Repository.CI.Provider = strings.TrimSpace(svc.Repository.CI.Provider)
			if svc.Repository.CI.Provider == "" {
				svc.Repository.CI.Provider = "hiveci"
			}
		}

		if len(svc.Repository.RelayURLs) > 0 {
			seen := make(map[string]struct{}, len(svc.Repository.RelayURLs))
			relays := make([]string, 0, len(svc.Repository.RelayURLs))
			for _, relay := range svc.Repository.RelayURLs {
				trimmed := strings.TrimSpace(relay)
				if trimmed == "" {
					continue
				}
				if _, ok := seen[trimmed]; ok {
					continue
				}
				seen[trimmed] = struct{}{}
				relays = append(relays, trimmed)
			}
			svc.Repository.RelayURLs = relays
		}

		if svc.Repository.CloneURL != "" {
			svc.RepoURL = svc.Repository.CloneURL
		}

		if svc.Repository.Source == "" {
			if svc.Repository.RepoCoordinate != "" {
				svc.Repository.Source = "nip34"
			} else {
				svc.Repository.Source = "manual"
			}
		}
	}

	if svc.Repository == nil && svc.RepoURL != "" {
		svc.Repository = &domain.RepositoryRef{
			Source:   "manual",
			CloneURL: svc.RepoURL,
		}
	}
}

func normalizeServiceRepositoryForRead(svc *domain.Service) {
	if svc == nil {
		return
	}

	svc.RepoURL = strings.TrimSpace(svc.RepoURL)

	if svc.Repository == nil && svc.RepoURL != "" {
		svc.Repository = &domain.RepositoryRef{
			Source:   "manual",
			CloneURL: svc.RepoURL,
		}
		return
	}

	if svc.Repository == nil {
		return
	}

	svc.Repository.CloneURL = strings.TrimSpace(svc.Repository.CloneURL)
	if svc.RepoURL == "" || svc.RepoURL != svc.Repository.CloneURL {
		svc.RepoURL = svc.Repository.CloneURL
	}
}

// --- Environment CRUD ---

func (s *RegistryService) CreateEnvironment(ctx context.Context, env *domain.Environment) error {
	if env.DeployStrategy == "" {
		env.DeployStrategy = domain.DeployStrategyReplace
	}
	return s.environments.Create(ctx, env)
}

func (s *RegistryService) GetEnvironment(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return s.environments.GetByID(ctx, id)
}

func (s *RegistryService) GetEnvironmentByName(ctx context.Context, name string) (*domain.Environment, error) {
	return s.environments.GetByName(ctx, name)
}

func (s *RegistryService) ListEnvironments(ctx context.Context) ([]domain.Environment, error) {
	return s.environments.List(ctx)
}

func (s *RegistryService) UpdateEnvironment(ctx context.Context, env *domain.Environment) error {
	return s.environments.Update(ctx, env)
}

// DeleteEnvironment deletes an environment after checking for dependent resources.
// If force is false and dependents exist, returns an error describing what would be cascaded.
func (s *RegistryService) DeleteEnvironment(ctx context.Context, id uuid.UUID, force bool) error {
	if !force {
		if pgRepo, ok := s.environments.(*repository.PgEnvironmentRepository); ok {
			intents, states, err := pgRepo.CountDependents(ctx, id)
			if err != nil {
				return fmt.Errorf("checking dependents: %w", err)
			}
			total := intents + states
			if total > 0 {
				return fmt.Errorf(
					"environment has dependent resources (%d deployment intents, %d state records); "+
						"use force=true to cascade delete or remove dependents first",
					intents, states,
				)
			}
		}
	}
	return s.environments.Delete(ctx, id)
}

// --- Build operations ---

func (s *RegistryService) RegisterBuild(ctx context.Context, b *domain.Build) error {
	if b.Status == "" {
		b.Status = domain.BuildStatusQueued
	}
	if err := s.builds.Create(ctx, b); err != nil {
		return err
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventBuildRegistered,
		EntityID: b.ID.String(),
		Data:     b,
	})

	s.logger.Info("build registered",
		zap.String("build_id", b.ID.String()),
		zap.String("service_id", b.ServiceID.String()),
		zap.String("git_sha", b.GitSHA),
	)
	return nil
}

func (s *RegistryService) GetBuild(ctx context.Context, id uuid.UUID) (*domain.Build, error) {
	return s.builds.GetByID(ctx, id)
}

func (s *RegistryService) ListBuilds(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.builds.ListByService(ctx, serviceID, limit, offset)
}

func (s *RegistryService) UpdateBuildStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error {
	if err := s.builds.UpdateStatus(ctx, id, status); err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventBuildStatusChanged,
		EntityID: id.String(),
		Data:     map[string]string{"status": string(status)},
	})
	return nil
}

// --- Artifact operations ---

func (s *RegistryService) RegisterArtifact(ctx context.Context, a *domain.Artifact) error {
	if a.ScanStatus == "" {
		a.ScanStatus = domain.ScanStatusUnknown
	}

	// Verify the image exists in the container registry before accepting it.
	// The reference is the digest if available, otherwise the tag.
	reference := a.ImageDigest
	if reference == "" {
		reference = a.ImageTag
	}
	verification, err := s.verifier.VerifyImage(ctx, a.ImageRepo, reference)
	if err != nil {
		s.logger.Error("image verification failed",
			zap.String("image_repo", a.ImageRepo),
			zap.String("reference", reference),
			zap.Error(err),
		)
		return fmt.Errorf("verifying image in registry: %w", err)
	}
	if !verification.Exists {
		return fmt.Errorf("image %s:%s not found in container registry", a.ImageRepo, reference)
	}

	// If the registry returned a digest and the artifact has one, cross-check them.
	if verification.Digest != "" && a.ImageDigest != "" && verification.Digest != a.ImageDigest {
		return fmt.Errorf("digest mismatch: artifact claims %s but registry reports %s", a.ImageDigest, verification.Digest)
	}

	// If the registry provided scan status and the artifact doesn't have one, adopt it.
	if verification.ScanStatus != "" && a.ScanStatus == domain.ScanStatusUnknown {
		if scanStatus := domain.ScanStatus(verification.ScanStatus); scanStatus != "" {
			a.ScanStatus = scanStatus
		}
	}

	if err := s.artifacts.Create(ctx, a); err != nil {
		return err
	}
	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventArtifactRegistered,
		EntityID: a.ID.String(),
		Data:     a,
	})
	s.logger.Info("artifact registered",
		zap.String("artifact_id", a.ID.String()),
		zap.String("image", a.ImageRepo+":"+a.ImageTag),
		zap.String("digest", a.ImageDigest),
	)
	return nil
}

func (s *RegistryService) GetArtifact(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return s.artifacts.GetByID(ctx, id)
}

func (s *RegistryService) GetArtifactByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error) {
	return s.artifacts.GetByDigest(ctx, repo, digest)
}

func (s *RegistryService) ListArtifacts(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.artifacts.ListByService(ctx, serviceID, limit, offset)
}

func (s *RegistryService) ListArtifactsByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	return s.artifacts.ListByBuild(ctx, buildID)
}

// --- Deployment Intent operations ---

// CreateDeploymentIntent creates a new deployment intent and updates the environment service state.
func (s *RegistryService) CreateDeploymentIntent(ctx context.Context, di *domain.DeploymentIntent) error {
	// Validate referenced entities exist.
	svc, err := s.services.GetByID(ctx, di.ServiceID)
	if err != nil {
		return fmt.Errorf("looking up service: %w", err)
	}
	if svc == nil {
		return fmt.Errorf("service %s not found", di.ServiceID)
	}

	env, err := s.environments.GetByID(ctx, di.EnvironmentID)
	if err != nil {
		return fmt.Errorf("looking up environment: %w", err)
	}
	if env == nil {
		return fmt.Errorf("environment %s not found", di.EnvironmentID)
	}

	artifact, err := s.artifacts.GetByID(ctx, di.ArtifactID)
	if err != nil {
		return fmt.Errorf("looking up artifact: %w", err)
	}
	if artifact == nil {
		return fmt.Errorf("artifact %s not found", di.ArtifactID)
	}

	// Auto-set approval for non-protected environments.
	if !env.Protected {
		di.ApprovalStatus = domain.ApprovalStatusNotRequired
	} else if di.ApprovalStatus == "" {
		di.ApprovalStatus = domain.ApprovalStatusPending
	}

	if di.Status == "" {
		if di.ApprovalStatus == domain.ApprovalStatusNotRequired || di.ApprovalStatus == domain.ApprovalStatusApproved {
			di.Status = domain.IntentStatusApproved
		} else {
			di.Status = domain.IntentStatusPending
		}
	}

	if err := s.intents.Create(ctx, di); err != nil {
		return err
	}

	// Update the environment service state desired artifact.
	state := &domain.EnvironmentServiceState{
		ServiceID:       di.ServiceID,
		EnvironmentID:   di.EnvironmentID,
		DesiredArtifactID: &di.ArtifactID,
		DesiredIntentID: &di.ID,
		DriftStatus:     domain.DriftStatusDeploying,
	}
	if err := s.state.Upsert(ctx, state); err != nil {
		s.logger.Error("failed to update environment service state", zap.Error(err))
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentIntentCreated,
		EntityID: di.ID.String(),
		Data:     di,
	})

	s.logger.Info("deployment intent created",
		zap.String("intent_id", di.ID.String()),
		zap.String("service", svc.Name),
		zap.String("environment", env.Name),
		zap.String("artifact", artifact.ImageDigest),
	)
	return nil
}

func (s *RegistryService) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return s.intents.GetByID(ctx, id)
}

func (s *RegistryService) ListDeploymentIntents(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.intents.ListByServiceEnv(ctx, serviceID, envID, limit, offset)
}

func (s *RegistryService) ApproveDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	di, err := s.intents.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if di == nil {
		return fmt.Errorf("deployment intent %s not found", id)
	}

	// Guard: only pending-approval intents can be approved.
	if di.ApprovalStatus != domain.ApprovalStatusPending {
		return fmt.Errorf("cannot approve intent %s: approval status is %s, expected %s",
			id, di.ApprovalStatus, domain.ApprovalStatusPending)
	}
	if di.Status != domain.IntentStatusPending {
		return fmt.Errorf("cannot approve intent %s: status is %s, expected %s",
			id, di.Status, domain.IntentStatusPending)
	}

	if err := s.intents.UpdateApproval(ctx, id, domain.ApprovalStatusApproved); err != nil {
		return err
	}
	if err := s.intents.UpdateStatus(ctx, id, domain.IntentStatusApproved); err != nil {
		return err
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentIntentApproved,
		EntityID: id.String(),
	})
	return nil
}

func (s *RegistryService) RejectDeploymentIntent(ctx context.Context, id uuid.UUID) error {
	di, err := s.intents.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if di == nil {
		return fmt.Errorf("deployment intent %s not found", id)
	}

	// Guard: only pending-approval intents can be rejected.
	if di.ApprovalStatus != domain.ApprovalStatusPending {
		return fmt.Errorf("cannot reject intent %s: approval status is %s, expected %s",
			id, di.ApprovalStatus, domain.ApprovalStatusPending)
	}
	if di.Status != domain.IntentStatusPending {
		return fmt.Errorf("cannot reject intent %s: status is %s, expected %s",
			id, di.Status, domain.IntentStatusPending)
	}

	if err := s.intents.UpdateApproval(ctx, id, domain.ApprovalStatusRejected); err != nil {
		return err
	}
	return s.intents.UpdateStatus(ctx, id, domain.IntentStatusRejected)
}

// --- Deployment Run operations ---

func (s *RegistryService) CreateDeploymentRun(ctx context.Context, dr *domain.DeploymentRun) error {
	// Guard: verify the intent exists and is in an approved state.
	intent, err := s.intents.GetByID(ctx, dr.DeploymentIntentID)
	if err != nil {
		return fmt.Errorf("looking up intent for run: %w", err)
	}
	if intent == nil {
		return fmt.Errorf("deployment intent %s not found", dr.DeploymentIntentID)
	}
	if intent.Status != domain.IntentStatusApproved && intent.Status != domain.IntentStatusDeploying {
		return fmt.Errorf("cannot create run for intent %s: status is %s, must be %s or %s",
			dr.DeploymentIntentID, intent.Status, domain.IntentStatusApproved, domain.IntentStatusDeploying)
	}

	if dr.Status == "" {
		dr.Status = domain.RunStatusQueued
	}
	if err := s.runs.Create(ctx, dr); err != nil {
		return err
	}

	// Update intent to deploying.
	if err := s.intents.UpdateStatus(ctx, dr.DeploymentIntentID, domain.IntentStatusDeploying); err != nil {
		s.logger.Error("failed to update intent status to deploying",
			zap.String("intent_id", dr.DeploymentIntentID.String()),
			zap.Error(err),
		)
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunCreated,
		EntityID: dr.ID.String(),
		Data:     dr,
	})
	return nil
}

func (s *RegistryService) GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return s.runs.GetByID(ctx, id)
}

func (s *RegistryService) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	return s.runs.ListByIntent(ctx, intentID)
}

// CompleteDeploymentRun marks a deployment run as completed and updates related state.
func (s *RegistryService) CompleteDeploymentRun(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	// Validate the status is a terminal status.
	if !isTerminalRunStatus(status) {
		return fmt.Errorf("cannot complete run with non-terminal status: %s", status)
	}

	dr, err := s.runs.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if dr == nil {
		return fmt.Errorf("deployment run %s not found", id)
	}

	// Guard: only running or queued runs can be completed.
	if dr.Status != domain.RunStatusQueued && dr.Status != domain.RunStatusRunning {
		return fmt.Errorf("cannot complete run %s: status is already %s", id, dr.Status)
	}

	if err := s.runs.UpdateStatus(ctx, id, status, exitCode); err != nil {
		return err
	}

	// Update the parent intent status based on run outcome.
	var intentStatus domain.DeploymentIntentStatus
	switch status {
	case domain.RunStatusSucceeded:
		intentStatus = domain.IntentStatusDeployed
	case domain.RunStatusFailed, domain.RunStatusTimeout:
		intentStatus = domain.IntentStatusFailed
	case domain.RunStatusCancelled:
		intentStatus = domain.IntentStatusFailed
	default:
		return nil
	}

	if err := s.intents.UpdateStatus(ctx, dr.DeploymentIntentID, intentStatus); err != nil {
		s.logger.Error("failed to update intent status after run completion",
			zap.String("intent_id", dr.DeploymentIntentID.String()),
			zap.String("intent_status", string(intentStatus)),
			zap.Error(err),
		)
		return fmt.Errorf("updating intent status after run completion: %w", err)
	}

	// If succeeded, update environment service state.
	if status == domain.RunStatusSucceeded {
		intent, err := s.intents.GetByID(ctx, dr.DeploymentIntentID)
		if err != nil {
			s.logger.Error("failed to fetch intent for state update after successful run",
				zap.String("intent_id", dr.DeploymentIntentID.String()),
				zap.Error(err),
			)
			return fmt.Errorf("fetching intent for state update: %w", err)
		}
		if intent != nil {
			now := time.Now().UTC()
			state := &domain.EnvironmentServiceState{
				ServiceID:           intent.ServiceID,
				EnvironmentID:       intent.EnvironmentID,
				DesiredArtifactID:   &intent.ArtifactID,
				DesiredIntentID:     &intent.ID,
				LastSuccessfulRunID: &id,
				DriftStatus:         domain.DriftStatusUnknown,
				LastReconciledAt:    &now,
			}
			if err := s.state.Upsert(ctx, state); err != nil {
				s.logger.Error("failed to upsert environment service state after successful run",
					zap.String("service_id", intent.ServiceID.String()),
					zap.String("environment_id", intent.EnvironmentID.String()),
					zap.Error(err),
				)
				return fmt.Errorf("upserting environment state after successful run: %w", err)
			}
		}
	}

	s.publisher.Publish(ctx, events.Event{
		Type:     events.EventDeploymentRunCompleted,
		EntityID: id.String(),
		Data:     map[string]string{"status": string(status)},
	})
	return nil
}

// --- Rollback ---

// Rollback creates a new deployment intent that reverts to the previous successfully deployed artifact.
// It traces back from the current desired intent to find the prior successful deployment's artifact.
func (s *RegistryService) Rollback(ctx context.Context, serviceID, envID uuid.UUID, requestedBy string) (*domain.DeploymentIntent, error) {
	// Get current state to find what's currently deployed.
	currentState, err := s.state.Get(ctx, serviceID, envID)
	if err != nil {
		return nil, fmt.Errorf("getting current state: %w", err)
	}
	if currentState == nil {
		return nil, fmt.Errorf("no deployment state exists for this service/environment")
	}

	// We need to find the artifact from the PREVIOUS successful deployment.
	// Walk the deployment intent history to find two distinct successfully-deployed artifacts.
	// The first is the current one, the second is the rollback target.
	intents, err := s.intents.ListByServiceEnv(ctx, serviceID, envID, 50, 0)
	if err != nil {
		return nil, fmt.Errorf("listing deployment intents: %w", err)
	}

	var currentArtifactID *uuid.UUID
	if currentState.DesiredArtifactID != nil {
		currentArtifactID = currentState.DesiredArtifactID
	}

	// Find the most recent successfully-deployed intent whose artifact differs
	// from the current desired artifact.
	var rollbackTargetArtifactID *uuid.UUID
	var supersedesIntentID *uuid.UUID

	for i := range intents {
		intent := &intents[i]
		// Only consider intents that were successfully deployed.
		if intent.Status != domain.IntentStatusDeployed {
			continue
		}

		// Skip the intent that matches the current desired artifact.
		if currentArtifactID != nil && intent.ArtifactID == *currentArtifactID {
			if supersedesIntentID == nil {
				supersedesIntentID = &intent.ID
			}
			continue
		}

		// This is a previously successful deployment with a different artifact.
		rollbackTargetArtifactID = &intent.ArtifactID
		if supersedesIntentID == nil {
			supersedesIntentID = &intent.ID
		}
		break
	}

	if rollbackTargetArtifactID == nil {
		return nil, fmt.Errorf("no previous successfully deployed artifact to roll back to")
	}

	// Verify the rollback target artifact still exists.
	artifact, err := s.artifacts.GetByID(ctx, *rollbackTargetArtifactID)
	if err != nil {
		return nil, fmt.Errorf("looking up rollback target artifact: %w", err)
	}
	if artifact == nil {
		return nil, fmt.Errorf("rollback target artifact %s no longer exists", *rollbackTargetArtifactID)
	}

	rollbackIntent := &domain.DeploymentIntent{
		ServiceID:          serviceID,
		EnvironmentID:      envID,
		ArtifactID:         *rollbackTargetArtifactID,
		RequestedBy:        requestedBy,
		SourceKind:         domain.SourceKindRollback,
		ApprovalStatus:     domain.ApprovalStatusNotRequired,
		Status:             domain.IntentStatusApproved,
		SupersedesIntentID: supersedesIntentID,
	}

	if err := s.CreateDeploymentIntent(ctx, rollbackIntent); err != nil {
		return nil, fmt.Errorf("creating rollback intent: %w", err)
	}

	return rollbackIntent, nil
}

// --- Runtime Observation ---

func (s *RegistryService) RecordObservation(ctx context.Context, obs *domain.RuntimeObservation) error {
	if err := s.observations.Create(ctx, obs); err != nil {
		return err
	}

	// Update environment service state with new observation.
	state, err := s.state.Get(ctx, obs.ServiceID, obs.EnvironmentID)
	if err != nil {
		s.logger.Error("failed to get environment service state for observation",
			zap.String("service_id", obs.ServiceID.String()),
			zap.String("environment_id", obs.EnvironmentID.String()),
			zap.Error(err),
		)
		return fmt.Errorf("getting environment service state: %w", err)
	}
	if state == nil {
		state = &domain.EnvironmentServiceState{
			ServiceID:     obs.ServiceID,
			EnvironmentID: obs.EnvironmentID,
		}
	}
	state.CurrentObservationID = &obs.ID

	// Determine drift.
	if state.DesiredArtifactID != nil {
		desired, err := s.artifacts.GetByID(ctx, *state.DesiredArtifactID)
		if err != nil {
			s.logger.Error("failed to fetch desired artifact for drift check",
				zap.String("artifact_id", state.DesiredArtifactID.String()),
				zap.Error(err),
			)
			// Don't fail the observation; mark drift as unknown and continue.
			state.DriftStatus = domain.DriftStatusUnknown
		} else if desired != nil && desired.ImageDigest == obs.ObservedImageDigest {
			state.DriftStatus = domain.DriftStatusInSync
		} else {
			state.DriftStatus = domain.DriftStatusDrifted
		}
	}

	now := time.Now().UTC()
	state.LastReconciledAt = &now
	return s.state.Upsert(ctx, state)
}

func (s *RegistryService) GetLatestObservation(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return s.observations.GetLatest(ctx, serviceID, envID)
}

// --- State queries ---

func (s *RegistryService) GetEnvironmentServiceState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return s.state.Get(ctx, serviceID, envID)
}

func (s *RegistryService) ListEnvironmentStates(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return s.state.ListByEnvironment(ctx, envID)
}

func (s *RegistryService) ListDriftedStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return s.state.ListDrifted(ctx)
}

func (s *RegistryService) ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return s.state.ListAll(ctx)
}

// isTerminalRunStatus returns true if the status represents a completed deployment run.
func isTerminalRunStatus(s domain.DeploymentRunStatus) bool {
	switch s {
	case domain.RunStatusSucceeded, domain.RunStatusFailed, domain.RunStatusCancelled, domain.RunStatusTimeout:
		return true
	default:
		return false
	}
}
