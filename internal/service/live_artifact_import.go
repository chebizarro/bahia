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

// liveImportCISystem marks build lineage created by the operator live-import
// path so it is never mistaken for CI-attested provenance.
const liveImportCISystem = "operator-live-import"

// maxLiveImportObservationAge bounds how stale the proving observation may be.
// Importing against an ancient observation would assert that something is
// running when it may long since have been replaced or removed.
const maxLiveImportObservationAge = time.Hour

// Bahia-managed container labels used to prove that an observed runtime really
// belongs to the service/environment/unit an operator claims it does.
const (
	labelServiceID        = "bahia.service_id"
	labelEnvironmentID    = "bahia.environment_id"
	labelDeploymentUnitID = "bahia.deployment_unit_id"
)

// ImportObservedArtifactInput describes an already-running image an authorized
// operator wants Bahia to govern as a first-class artifact.
type ImportObservedArtifactInput struct {
	ServiceID        uuid.UUID
	EnvironmentID    uuid.UUID
	DeploymentUnitID *uuid.UUID
	ImageRepo        string
	ImageTag         string
	ImageDigest      string
	GitSHA           string
	GitRef           string
	RequestedBy      string
}

// ImportObservedArtifactResult reports the governed lineage for an imported
// runtime image. It never carries desired-state changes: importing provenance
// is deliberately separate from promoting it.
type ImportObservedArtifactResult struct {
	Status           string           `json:"status"`
	Build            *domain.Build    `json:"build"`
	Artifact         *domain.Artifact `json:"artifact"`
	ObservedDigest   string           `json:"observed_digest"`
	ObservationID    uuid.UUID        `json:"observation_id"`
	RegistryVerified bool             `json:"registry_verified"`
	VerifiedLabels   []string         `json:"verified_labels,omitempty"`
	DesiredStateNote string           `json:"desired_state_note"`
}

// ImportObservedArtifact records an already-running, observation-verified image
// as a governed build/artifact lineage.
//
// This exists so that reconciling live reality with Bahia never requires direct
// database mutation. It is deliberately narrower than RegisterArtifact: the
// operator cannot assert an arbitrary digest, because the digest must match
// what Bahia itself observes running for the service and environment.
//
// It never touches desired state. Aligning desired state remains a separate,
// explicitly reviewed deployment (preview -> deploy with the expected
// desired-state hash), preserving the production promotion boundary.
func (s *RegistryService) ImportObservedArtifact(ctx context.Context, in ImportObservedArtifactInput) (*ImportObservedArtifactResult, error) {
	// Policy is evaluated before anything is read or written so a denied import
	// can never leave partial build/artifact state behind.
	if !s.allowLiveArtifactImport {
		return nil, fmt.Errorf(
			"live artifact import is disabled by policy: prefer the Hive CI path, where a signed kind 5402 carrying " +
				"BAHIA_ARTIFACT (image_repo, image_tag, image_digest) registers a digest-pinned artifact automatically; " +
				"set hiveci.allow_live_artifact_import=true to permit authorized operators to import an already-running " +
				"image. Never edit the database directly to bridge missing artifact state")
	}
	if in.ServiceID == uuid.Nil || in.EnvironmentID == uuid.Nil {
		return nil, fmt.Errorf("service_id and environment_id are required")
	}
	imageRepo := strings.TrimSpace(in.ImageRepo)
	imageTag := strings.TrimSpace(in.ImageTag)
	if imageRepo == "" || imageTag == "" {
		return nil, fmt.Errorf("image_repo and image_tag are required")
	}
	requestedDigest := domain.NormalizeImageDigest(in.ImageDigest)
	if !immutableArtifactDigest.MatchString(requestedDigest) {
		return nil, fmt.Errorf("image_digest must be an immutable sha256 manifest digest")
	}
	if s.txExecutor == nil {
		return nil, fmt.Errorf("live artifact import requires transactional repositories")
	}

	svc, err := s.services.GetByID(ctx, in.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("loading service: %w", err)
	}
	if svc == nil {
		return nil, fmt.Errorf("service %s not found", in.ServiceID)
	}
	env, err := s.environments.GetByID(ctx, in.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("loading environment: %w", err)
	}
	if env == nil {
		return nil, fmt.Errorf("environment %s not found", in.EnvironmentID)
	}

	// The running system is the authority for what digest exists. An operator
	// cannot import a digest Bahia has not actually observed.
	observation, err := s.observations.GetLatest(ctx, in.ServiceID, in.EnvironmentID)
	if err != nil {
		return nil, fmt.Errorf("loading latest runtime observation: %w", err)
	}
	if observation == nil {
		return nil, fmt.Errorf("no runtime observation exists for service %s in environment %s; Bahia can only import an image it observes running", in.ServiceID, in.EnvironmentID)
	}
	observedDigest := domain.NormalizeImageDigest(observation.ObservedImageDigest)
	if observedDigest == "" {
		return nil, fmt.Errorf("latest runtime observation for service %s carries no observed image digest", in.ServiceID)
	}
	if observedDigest != requestedDigest {
		return nil, fmt.Errorf("image_digest %s does not match the observed running digest %s", requestedDigest, observedDigest)
	}
	if in.DeploymentUnitID != nil && observation.DeploymentUnitID != nil && *in.DeploymentUnitID != *observation.DeploymentUnitID {
		return nil, fmt.Errorf("deployment_unit_id %s does not match the observed deployment unit %s", *in.DeploymentUnitID, *observation.DeploymentUnitID)
	}
	// Pinning a real digest to a repository Bahia never observed would make the
	// artifact unpullable and the provenance false.
	if observedRepo := strings.TrimSpace(observation.ObservedImageRepo); observedRepo != "" && !strings.EqualFold(observedRepo, imageRepo) {
		return nil, fmt.Errorf("image_repo %q does not match the observed running repository %q", imageRepo, observedRepo)
	}
	// A stale observation of a container that is no longer running must not
	// authorize an "observation-verified" import.
	if observation.HealthStatus == domain.HealthStatusStopped {
		return nil, fmt.Errorf("latest runtime observation reports the container stopped; Bahia only imports an image it observes running")
	}
	if age := time.Since(observation.ObservedAt); age > maxLiveImportObservationAge {
		return nil, fmt.Errorf("latest runtime observation is %s old (limit %s); re-observe the service before importing", age.Truncate(time.Second), maxLiveImportObservationAge)
	}

	verifiedLabels, err := verifyObservedBahiaLabels(observation, in)
	if err != nil {
		return nil, err
	}

	// Registry verification is opportunistic: an already-running image may be
	// local to its host and absent from any registry, which is precisely the
	// case this path exists to govern. A registry that DOES know the image must
	// agree with the running digest.
	registryVerified := false
	if s.verifier != nil {
		verification, verifyErr := s.verifier.VerifyImage(ctx, imageRepo, requestedDigest)
		switch {
		case verifyErr != nil:
			s.logger.Warn("live artifact import proceeding without registry verification",
				zap.String("image_repo", imageRepo), zap.Error(verifyErr))
		case verification != nil && verification.Exists:
			verifiedDigest := domain.NormalizeImageDigest(verification.Digest)
			if verifiedDigest != "" && verifiedDigest != requestedDigest {
				return nil, fmt.Errorf("registry reports digest %s for %s but the running image is %s", verifiedDigest, imageRepo, requestedDigest)
			}
			registryVerified = verifiedDigest != ""
		}
	}

	// Only bahia.* labels survive observation normalization, so there is no
	// OCI revision label to recover a commit from: the operator supplies it or
	// the lineage is explicitly marked as having no known source commit.
	gitSHA := strings.TrimSpace(in.GitSHA)
	if gitSHA == "" {
		gitSHA = "live-import"
	}
	gitRef := strings.TrimSpace(in.GitRef)
	if gitRef == "" {
		gitRef = imageTag
	}

	evidence := map[string]any{
		"import_source":     "operator-live-import",
		"observation_id":    observation.ID.String(),
		"observed_digest":   observedDigest,
		"observed_at":       observation.ObservedAt.UTC().Format(time.RFC3339Nano),
		"observed_health":   string(observation.HealthStatus),
		"observation_src":   observation.Source,
		"environment_id":    in.EnvironmentID.String(),
		"registry_verified": registryVerified,
		"verified_labels":   verifiedLabels,
	}
	if in.RequestedBy != "" {
		evidence["requested_by"] = in.RequestedBy
	}
	if observation.ObservedContainerID != "" {
		evidence["container_id"] = observation.ObservedContainerID
	}

	var (
		build    *domain.Build
		artifact *domain.Artifact
		status   = "imported"
	)
	// One transaction: either the whole lineage exists afterwards or none of it
	// does. A failure mid-way must not leave a build without its artifact.
	if err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Builds == nil || repos.Artifacts == nil {
			return fmt.Errorf("transactional build and artifact repositories are required")
		}
		existingArtifact, lookupErr := repos.Artifacts.GetByImageRepoDigest(ctx, imageRepo, requestedDigest)
		if lookupErr != nil {
			return fmt.Errorf("looking up existing artifact: %w", lookupErr)
		}
		if existingArtifact != nil {
			if existingArtifact.ServiceID != in.ServiceID {
				return fmt.Errorf("artifact %s@%s already belongs to service %s", imageRepo, requestedDigest, existingArtifact.ServiceID)
			}
			existingBuild, buildErr := repos.Builds.GetByID(ctx, existingArtifact.BuildID)
			if buildErr != nil {
				return fmt.Errorf("loading existing build lineage: %w", buildErr)
			}
			artifact, build, status = existingArtifact, existingBuild, "already_imported"
			return nil
		}

		runID := liveImportRunID(in.ServiceID, requestedDigest)
		existingBuild, buildErr := repos.Builds.GetByCISystemRunID(ctx, liveImportCISystem, runID)
		if buildErr != nil {
			return fmt.Errorf("looking up live-import build: %w", buildErr)
		}
		if existingBuild != nil {
			if existingBuild.ServiceID != in.ServiceID {
				return fmt.Errorf("live-import build %q already belongs to service %s", runID, existingBuild.ServiceID)
			}
			build = existingBuild
		} else {
			now := time.Now().UTC()
			buildMetadata := map[string]any{}
			for k, v := range evidence {
				buildMetadata[k] = v
			}
			build = &domain.Build{
				ServiceID:  in.ServiceID,
				GitSHA:     gitSHA,
				GitRef:     gitRef,
				CISystem:   liveImportCISystem,
				CIRunID:    runID,
				Status:     domain.BuildStatusSucceeded,
				Metadata:   buildMetadata,
				StartedAt:  &now,
				FinishedAt: &now,
			}
			if createErr := repos.Builds.Create(ctx, build); createErr != nil {
				return fmt.Errorf("creating live-import build: %w", createErr)
			}
		}

		artifactMetadata := map[string]any{}
		for k, v := range evidence {
			artifactMetadata[k] = v
		}
		artifact = &domain.Artifact{
			BuildID:     build.ID,
			ServiceID:   in.ServiceID,
			ImageRepo:   imageRepo,
			ImageTag:    imageTag,
			ImageDigest: requestedDigest,
			ScanStatus:  domain.ScanStatusUnknown,
			Metadata:    artifactMetadata,
		}
		if createErr := repos.Artifacts.Create(ctx, artifact); createErr != nil {
			return fmt.Errorf("creating live-import artifact: %w", createErr)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if status == "imported" {
		if build != nil {
			s.publisher.Publish(ctx, events.Event{Type: events.EventBuildRegistered, EntityID: build.ID.String(), Data: build})
		}
		s.publisher.Publish(ctx, events.Event{Type: events.EventArtifactRegistered, EntityID: artifact.ID.String(), Data: artifact})
		s.logger.Info("imported observed runtime artifact",
			zap.String("service_id", in.ServiceID.String()),
			zap.String("environment_id", in.EnvironmentID.String()),
			zap.String("artifact_id", artifact.ID.String()),
			zap.String("image_digest", requestedDigest),
			zap.Bool("registry_verified", registryVerified),
			zap.Strings("verified_labels", verifiedLabels),
			zap.String("requested_by", in.RequestedBy),
		)
	}

	return &ImportObservedArtifactResult{
		Status:           status,
		Build:            build,
		Artifact:         artifact,
		ObservedDigest:   observedDigest,
		ObservationID:    observation.ID,
		RegistryVerified: registryVerified,
		VerifiedLabels:   verifiedLabels,
		DesiredStateNote: "desired state unchanged; run a reviewed deployment preview and deploy with the expected desired-state hash to align it",
	}, nil
}

// verifyObservedBahiaLabels rejects an import whose observed container claims a
// different identity than the operator asserts. Labels are checked only when
// present: an adopted container may legitimately carry none.
func verifyObservedBahiaLabels(observation *domain.RuntimeObservation, in ImportObservedArtifactInput) ([]string, error) {
	if observation.NormalizedState == nil || len(observation.NormalizedState.BahiaLabels) == 0 {
		return nil, nil
	}
	labels := observation.NormalizedState.BahiaLabels
	verified := make([]string, 0, 3)
	checks := []struct {
		key      string
		expected string
	}{
		{labelServiceID, in.ServiceID.String()},
		{labelEnvironmentID, in.EnvironmentID.String()},
	}
	if in.DeploymentUnitID != nil {
		checks = append(checks, struct {
			key      string
			expected string
		}{labelDeploymentUnitID, in.DeploymentUnitID.String()})
	}
	for _, check := range checks {
		actual := strings.TrimSpace(labels[check.key])
		if actual == "" {
			continue
		}
		if !strings.EqualFold(actual, check.expected) {
			return nil, fmt.Errorf("observed container label %s is %s, not %s", check.key, actual, check.expected)
		}
		verified = append(verified, check.key)
	}
	return verified, nil
}

func liveImportRunID(serviceID uuid.UUID, digest string) string {
	return fmt.Sprintf("%s:%s", serviceID, digest)
}
