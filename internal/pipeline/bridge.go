package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	registryadapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const ciSystemHiveCI = "hive-ci"

// Bridge orchestrates Hive-CI result -> Bahia build registration for CI-5/CI-6.
type Bridge struct {
	hiveRepo          repository.HiveCIRepository
	buildRepo         repository.BuildRepository
	artifactRepo      repository.ArtifactRepository
	intentRepo        repository.DeploymentIntentRepository
	envRepo           repository.EnvironmentRepository
	ociRegistry       repository.OCIRegistryRepository
	registryInspector registryadapter.ImageInspector
	logger            *zap.Logger
	trustedCI         map[string]struct{}
}

func NewBridge(
	hiveRepo repository.HiveCIRepository,
	buildRepo repository.BuildRepository,
	artifactRepo repository.ArtifactRepository,
	intentRepo repository.DeploymentIntentRepository,
	envRepo repository.EnvironmentRepository,
	ociRegistry repository.OCIRegistryRepository,
	registryInspector registryadapter.ImageInspector,
	trustedCIPubkeys []string,
	logger *zap.Logger,
) *Bridge {
	trusted := make(map[string]struct{}, len(trustedCIPubkeys))
	for _, pk := range trustedCIPubkeys {
		pk = strings.TrimSpace(pk)
		if pk != "" {
			trusted[pk] = struct{}{}
		}
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Bridge{
		hiveRepo: hiveRepo, buildRepo: buildRepo, artifactRepo: artifactRepo,
		intentRepo: intentRepo, envRepo: envRepo, ociRegistry: ociRegistry,
		registryInspector: registryInspector,
		logger:            logger.Named("pipeline-bridge"), trustedCI: trusted,
	}
}

// ProcessResult maps a verified Hive-CI result into a Bahia build/artifact, idempotently.
func (b *Bridge) ProcessResult(ctx context.Context, resultEventID string) error {
	result, err := b.hiveRepo.GetResultByEventID(ctx, resultEventID)
	if err != nil {
		return fmt.Errorf("load hiveci result: %w", err)
	}
	if result == nil {
		return nil
	}

	run, err := b.hiveRepo.GetRunByEventID(ctx, result.RunEventID)
	if err != nil {
		return fmt.Errorf("load hiveci run: %w", err)
	}
	if run == nil {
		return nil
	}

	policy, err := b.hiveRepo.GetPolicyByRepoAndWorkflow(ctx, run.RepoCoordinate, run.WorkflowPath)
	if err != nil {
		return fmt.Errorf("load pipeline policy: %w", err)
	}
	if policy == nil {
		b.logger.Info("no pipeline policy match; skipping",
			zap.String("run_event_id", run.RunEventID),
			zap.String("repo", run.RepoCoordinate),
			zap.String("workflow", run.WorkflowPath),
		)
		return nil
	}

	if _, ok := b.trustedCI[run.PublisherPubkey]; !ok {
		_ = b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateRejected)
		b.logger.Warn("rejecting untrusted dispatcher",
			zap.String("run_event_id", run.RunEventID),
			zap.String("dispatcher_pubkey", run.PublisherPubkey),
		)
		return nil
	}

	if run.PublisherPubkey != result.PublisherPubkey {
		_ = b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateRejected)
		b.logger.Warn("rejecting publisher mismatch",
			zap.String("run_event_id", run.RunEventID),
			zap.String("expected_publisher", run.PublisherPubkey),
			zap.String("actual_publisher", result.PublisherPubkey),
		)
		return nil
	}

	existingBuild, err := b.buildRepo.GetByCISystemRunID(ctx, ciSystemHiveCI, run.RunEventID)
	if err != nil {
		return fmt.Errorf("lookup existing build: %w", err)
	}
	if existingBuild == nil {
		status := domain.BuildStatusFailed
		if result.Status == "success" {
			status = domain.BuildStatusSucceeded
		}
		build := &domain.Build{
			ServiceID:     policy.ServiceID,
			GitSHA:        run.CommitSHA,
			GitRef:        run.Branch,
			CISystem:      ciSystemHiveCI,
			CIRunID:       run.RunEventID,
			SourceEventID: run.RunEventID,
			Status:        status,
		}
		if err := b.buildRepo.Create(ctx, build); err != nil {
			return fmt.Errorf("create build: %w", err)
		}
		existingBuild = build
	}

	if result.Status != "success" {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateProcessed); err != nil {
			return fmt.Errorf("mark result processed: %w", err)
		}
		return nil
	}

	imageRepo := strings.TrimSpace(result.ImageRepo)
	imageTag := strings.TrimSpace(result.ImageTag)
	imageDigest := strings.TrimSpace(result.ImageDigest)
	if imageRepo == "" || imageTag == "" || imageDigest == "" {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateArtifactPending); err != nil {
			return fmt.Errorf("mark result artifact pending: %w", err)
		}
		return nil
	}

	if b.artifactRepo == nil || (b.ociRegistry == nil && b.registryInspector == nil) {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateArtifactPending); err != nil {
			return fmt.Errorf("mark result artifact pending: %w", err)
		}
		return nil
	}

	manifest, err := b.resolveManifest(ctx, imageRepo, imageDigest)
	if err != nil {
		return fmt.Errorf("lookup registry manifest: %w", err)
	}
	if manifest == nil {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateArtifactPending); err != nil {
			return fmt.Errorf("mark result artifact pending: %w", err)
		}
		return nil
	}

	artifact, err := b.artifactRepo.GetByImageRepoDigest(ctx, imageRepo, imageDigest)
	if err != nil {
		return fmt.Errorf("lookup artifact by image repo/digest: %w", err)
	}
	if artifact == nil {
		size := manifest.SizeBytes
		artifact = &domain.Artifact{
			BuildID:           existingBuild.ID,
			ServiceID:         policy.ServiceID,
			ImageRepo:         imageRepo,
			ImageTag:          imageTag,
			ImageDigest:       imageDigest,
			ManifestMediaType: manifest.MediaType,
			SizeBytes:         &size,
			ScanStatus:        domain.ScanStatusUnknown,
			Metadata: map[string]any{
				"source":                  "hive-ci",
				"hive_ci_run_event_id":    run.RunEventID,
				"hive_ci_result_event_id": result.ResultEventID,
				"workflow_path":           run.WorkflowPath,
			},
		}
		if err := b.artifactRepo.Create(ctx, artifact); err != nil {
			return fmt.Errorf("create artifact: %w", err)
		}
	}

	if err := b.autoCreateStagingIntent(ctx, policy, run, result, artifact); err != nil {
		return err
	}

	if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateProcessed); err != nil {
		return fmt.Errorf("mark result processed: %w", err)
	}

	return nil
}

func (b *Bridge) resolveManifest(ctx context.Context, imageRepo, imageDigest string) (*domain.OCIManifest, error) {
	if b.ociRegistry != nil {
		manifest, err := b.ociRegistry.GetManifest(ctx, imageRepo, imageDigest)
		if err == nil && manifest != nil {
			return manifest, nil
		}
		if err != nil && b.registryInspector == nil {
			return nil, err
		}
	}
	if b.registryInspector == nil {
		return nil, nil
	}
	repoForInspector := stripRegistryHost(imageRepo)
	inspection, err := b.registryInspector.InspectImage(ctx, repoForInspector, imageDigest)
	if err != nil {
		return nil, err
	}
	if inspection == nil || !inspection.Exists {
		return nil, nil
	}
	manifest := &domain.OCIManifest{
		Digest:    inspection.Digest,
		MediaType: inspection.MediaType,
		SizeBytes: inspection.Size,
	}
	if manifest.Digest == "" {
		manifest.Digest = imageDigest
	}
	return manifest, nil
}

func stripRegistryHost(imageRepo string) string {
	imageRepo = strings.TrimSpace(imageRepo)
	if imageRepo == "" {
		return imageRepo
	}
	parts := strings.Split(imageRepo, "/")
	if len(parts) < 2 {
		return imageRepo
	}
	first := parts[0]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return strings.Join(parts[1:], "/")
	}
	if u, err := url.Parse(imageRepo); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Path, "/")
	}
	return imageRepo
}

func (b *Bridge) autoCreateStagingIntent(ctx context.Context, policy *domain.HiveCIPipelinePolicy, _ *domain.HiveCIWorkflowRun, result *domain.HiveCIWorkflowResult, artifact *domain.Artifact) error {
	if policy == nil || policy.Metadata == nil || b.intentRepo == nil || b.envRepo == nil || artifact == nil {
		return nil
	}
	autoDeploy, _ := policy.Metadata["auto_deploy_staging"].(bool)
	if !autoDeploy {
		return nil
	}

	targetEnvName, _ := policy.Metadata["staging_environment"].(string)
	var env *domain.Environment
	var err error
	if strings.TrimSpace(targetEnvName) != "" {
		env, err = b.envRepo.GetByName(ctx, strings.TrimSpace(targetEnvName))
		if err != nil {
			return fmt.Errorf("resolve staging environment by name: %w", err)
		}
	} else {
		env, err = b.envRepo.GetByID(ctx, policy.EnvironmentID)
		if err != nil {
			return fmt.Errorf("resolve staging environment by id: %w", err)
		}
	}
	if env == nil {
		return nil
	}

	existing, err := b.intentRepo.GetByHiveResultEventID(ctx, result.ResultEventID)
	if err != nil {
		return fmt.Errorf("lookup existing deployment intent: %w", err)
	}
	if existing != nil {
		return nil
	}

	approval := domain.ApprovalStatusNotRequired
	status := domain.IntentStatusApproved
	if env.Protected {
		approval = domain.ApprovalStatusPending
		status = domain.IntentStatusPending
	}

	intent := &domain.DeploymentIntent{
		ServiceID:      policy.ServiceID,
		EnvironmentID:  env.ID,
		ArtifactID:     artifact.ID,
		RequestedBy:    "hive-ci-bridge",
		SourceKind:     domain.SourceKindEventTriggered,
		ApprovalStatus: approval,
		Status:         status,
		Metadata: map[string]any{
			"hive_ci_result_event_id": result.ResultEventID,
		},
	}
	if err := b.intentRepo.Create(ctx, intent); err != nil {
		return fmt.Errorf("create staging deployment intent: %w", err)
	}
	return nil
}
