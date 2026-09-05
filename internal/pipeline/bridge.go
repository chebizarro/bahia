package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	registryadapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

var immutableManifestDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var errPromotionGateBlocked = errors.New("promotion gate blocked")

type ReleaseRegistrationAuditor interface {
	AuditReleaseRegistration(context.Context, domain.HiveCIAcceptedRelease, *domain.Artifact, string, error) error
	PrepareReleaseRegistrationAudit(context.Context, domain.HiveCIAcceptedRelease, *domain.Artifact, string, error) (*repository.NostrEventRecord, error)
}

type artifactRegistry interface {
	RegisterBuild(context.Context, *domain.Build) error
	UpdateBuildStatus(context.Context, uuid.UUID, domain.BuildStatus) error
	RegisterVerifiedArtifact(context.Context, *domain.Artifact, service.ArtifactVerificationProof) error
	CreateDeploymentIntent(context.Context, *domain.DeploymentIntent) error
}

type desiredStateBuilder interface {
	BuildDesiredStateSnapshot(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID) (*domain.DesiredServiceSpec, error)
}

// Bridge orchestrates trusted Hive-CI result -> Bahia build/artifact
// registration. Automatic processing and the signer-first build-result action
// share this implementation, so retries cannot create parallel artifacts.
type Bridge struct {
	hiveRepo          repository.HiveCIRepository
	serviceRepo       repository.ServiceRepository
	buildRepo         repository.BuildRepository
	artifactRepo      repository.ArtifactRepository
	intentRepo        repository.DeploymentIntentRepository
	envRepo           repository.EnvironmentRepository
	ociRegistry       repository.OCIRegistryRepository
	registryInspector registryadapter.ImageInspector
	registry          artifactRegistry
	desiredState      desiredStateBuilder
	logger            *zap.Logger
	autoRegister      bool
	releaseAuditor    ReleaseRegistrationAuditor
	trustedResults    map[string]struct{}
	decisionCount     atomic.Uint64
}

func NewBridge(
	hiveRepo repository.HiveCIRepository,
	serviceRepo repository.ServiceRepository,
	buildRepo repository.BuildRepository,
	artifactRepo repository.ArtifactRepository,
	intentRepo repository.DeploymentIntentRepository,
	envRepo repository.EnvironmentRepository,
	ociRegistry repository.OCIRegistryRepository,
	registryInspector registryadapter.ImageInspector,
	registry artifactRegistry,
	trustedCIPubkeys []string,
	autoRegister bool,
	logger *zap.Logger,
) *Bridge {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Bridge{
		hiveRepo: hiveRepo, serviceRepo: serviceRepo, buildRepo: buildRepo,
		artifactRepo: artifactRepo, intentRepo: intentRepo, envRepo: envRepo,
		ociRegistry: ociRegistry, registryInspector: registryInspector,
		registry: registry, logger: logger.Named("pipeline-bridge"),
		autoRegister: autoRegister,
	}
}

// SetTrustedResultPubkeys admits worker-signed 5402 results produced by
// Bahia-dispatched ci/workflow-run jobs.
func (b *Bridge) SetTrustedResultPubkeys(pubkeys []string) {
	b.trustedResults = make(map[string]struct{}, len(pubkeys))
	for _, pubkey := range pubkeys {
		pubkey = strings.ToLower(strings.TrimSpace(pubkey))
		if pubkey != "" {
			b.trustedResults[pubkey] = struct{}{}
		}
	}
}

func (b *Bridge) SetDesiredStateBuilder(builder desiredStateBuilder) {
	if b != nil {
		b.desiredState = builder
	}
}

func (b *Bridge) SetReleaseRegistrationAuditor(auditor ReleaseRegistrationAuditor) {
	b.releaseAuditor = auditor
}

func (b *Bridge) warnCandidate(message, reason string, fields ...zap.Field) {
	count := b.decisionCount.Add(1)
	fields = append(fields, zap.String("reason", reason), zap.Uint64("decision_count", count))
	b.logger.Warn(message, fields...)
}

func buildResultRejectionReason(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "publisher") || strings.Contains(message, "signer"):
		return "unauthorized_signer"
	case strings.Contains(message, "pipeline policy") || strings.Contains(message, "repository does not match") || strings.Contains(message, "service") && strings.Contains(message, "not found"):
		return "unmapped_repository"
	case strings.Contains(message, "digest") || strings.Contains(message, "repository, tag"):
		return "missing_or_invalid_digest"
	case strings.Contains(message, "manifest") || strings.Contains(message, "registry"):
		return "artifact_verification_failed"
	default:
		return "artifact_registration_rejected"
	}
}

// RegisterAcceptedRelease converts the validated release boundary into one
// digest-only Bahia artifact. It deliberately has no deployment-intent or
// desired-state dependency.
func (b *Bridge) RegisterAcceptedRelease(ctx context.Context, release domain.HiveCIAcceptedRelease) (artifact *domain.Artifact, err error) {
	defer func() {
		if err == nil || b == nil || b.releaseAuditor == nil {
			return
		}
		if auditErr := b.releaseAuditor.AuditReleaseRegistration(ctx, release, artifact, "rejected", err); auditErr != nil {
			err = fmt.Errorf("%v; persist rejection audit: %w", err, auditErr)
		}
	}()
	if b == nil || b.registry == nil || b.buildRepo == nil || b.serviceRepo == nil || b.artifactRepo == nil {
		return nil, fmt.Errorf("Hive-CI release artifact registration is not configured")
	}
	policy := release.Policy
	if policy.ID == uuid.Nil || policy.ID.String() != release.PolicyID || policy.ServiceID == uuid.Nil ||
		policy.RepoCoordinate != release.Result.Lineage.RepoAddress || policy.WorkflowPath != release.Workflow {
		return nil, fmt.Errorf("accepted release policy snapshot is incomplete or conflicts with lineage")
	}
	svc, err := b.serviceRepo.GetByID(ctx, policy.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("load release service: %w", err)
	}
	if svc == nil || strings.TrimSpace(svc.ArtifactRepo) != release.Result.Manifest.Repository {
		return nil, fmt.Errorf("release repository does not match the policy service artifact repository")
	}
	build, err := b.buildRepo.GetByCISystemRunID(ctx, domain.CISystemHiveCI, release.Result.Lineage.WorkflowRunEventID)
	if err != nil {
		return nil, fmt.Errorf("lookup release build: %w", err)
	}
	if build == nil {
		build = &domain.Build{
			ServiceID: policy.ServiceID, GitSHA: release.Result.Lineage.Commit, GitRef: release.Branch,
			CISystem: domain.CISystemHiveCI, CIRunID: release.Result.Lineage.WorkflowRunEventID,
			SourceEventID: release.Result.Lineage.WorkflowRunEventID, Status: domain.BuildStatusSucceeded,
			Metadata: map[string]any{
				"source": "hiveci_release", "release_identity": release.Result.ReleaseIdentity,
				"release_result_event_id": release.ResultEventID, "lineage": release.Result.Lineage,
			},
		}
	} else if build.ServiceID != policy.ServiceID || build.Status != domain.BuildStatusSucceeded ||
		!strings.EqualFold(build.GitSHA, release.Result.Lineage.Commit) {
		return nil, fmt.Errorf("existing release build conflicts with accepted lineage")
	}
	artifact = &domain.Artifact{
		BuildID: build.ID, ServiceID: policy.ServiceID,
		ImageRepo:   release.Result.Manifest.Repository,
		ImageDigest: release.Result.Manifest.Digest,
		ImageTag:    "",
	}
	if b.releaseAuditor == nil {
		return nil, fmt.Errorf("release registration audit is not configured")
	}
	atomicRegistry, ok := b.registry.(interface {
		RegisterReleaseArtifactWithAudit(
			context.Context,
			*domain.Build,
			*domain.Artifact,
			service.ReleaseArtifactVerificationProof,
			service.ReleaseArtifactAuditPreparer,
		) error
	})
	if !ok {
		return nil, fmt.Errorf("atomic digest-only release artifact registry is not configured")
	}
	if err := atomicRegistry.RegisterReleaseArtifactWithAudit(
		ctx, build, artifact,
		service.ReleaseArtifactVerificationProof{Release: release, VerifiedAt: time.Now().UTC()},
		func(committed *domain.Artifact) (*repository.NostrEventRecord, error) {
			return b.releaseAuditor.PrepareReleaseRegistrationAudit(ctx, release, committed, "accepted", nil)
		},
	); err != nil {
		return nil, fmt.Errorf("register accepted release artifact with audit: %w", err)
	}
	return artifact, nil
}

// ProcessResult consumes a newly persisted signed Hive-CI result.
func (b *Bridge) ProcessResult(ctx context.Context, resultEventID string) error {
	_, err := b.processResult(ctx, resultEventID, nil, false)
	return err
}

// RegisterBuildResult is the signer-first recovery/action path. The browser
// supplies only the Bahia build ID; every artifact identifier comes from the
// trusted persisted Hive-CI result and verified OCI manifest.
func (b *Bridge) RegisterBuildResult(ctx context.Context, buildID uuid.UUID) (*domain.Artifact, error) {
	if b == nil || b.buildRepo == nil || b.hiveRepo == nil {
		return nil, fmt.Errorf("HiveCI build-result registration is not configured")
	}
	build, err := b.buildRepo.GetByID(ctx, buildID)
	if err != nil {
		return nil, fmt.Errorf("load build: %w", err)
	}
	if build == nil {
		return nil, fmt.Errorf("build %s not found", buildID)
	}
	result, err := b.hiveRepo.GetLatestResultByRunEventID(ctx, build.CIRunID)
	if err != nil {
		return nil, fmt.Errorf("load HiveCI result for build: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("build has no signed HiveCI result")
	}
	return b.processResult(ctx, result.ResultEventID, build, true)
}

func (b *Bridge) processResult(ctx context.Context, resultEventID string, expectedBuild *domain.Build, explicit bool) (artifact *domain.Artifact, err error) {
	result, err := b.hiveRepo.GetResultByEventID(ctx, resultEventID)
	if err != nil {
		return nil, fmt.Errorf("load hiveci result: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("HiveCI result %s not found", resultEventID)
	}
	var run *domain.HiveCIWorkflowRun
	if result.Status == "success" {
		defer func() {
			if err == nil {
				return
			}
			fields := []zap.Field{
				zap.String("result_event_id", result.ResultEventID),
				zap.String("run_event_id", result.RunEventID),
				zap.String("image_repo", result.ImageRepo),
				zap.String("image_digest", result.ImageDigest),
				zap.Error(err),
			}
			if run != nil {
				fields = append(fields, zap.String("repo_coordinate", run.RepoCoordinate), zap.String("workflow", run.WorkflowPath))
			}
			b.warnCandidate("Hive-CI artifact candidate rejected", buildResultRejectionReason(err), fields...)
		}()
	}
	run, err = b.hiveRepo.GetRunByEventID(ctx, result.RunEventID)
	if err != nil {
		return nil, fmt.Errorf("load hiveci run: %w", err)
	}
	if run == nil {
		return nil, fmt.Errorf("HiveCI run %s not found", result.RunEventID)
	}
	policy, err := b.hiveRepo.GetPolicyByRepoAndWorkflow(ctx, run.RepoCoordinate, run.WorkflowPath)
	if err != nil {
		return nil, fmt.Errorf("load pipeline policy: %w", err)
	}
	if policy == nil {
		if explicit {
			return nil, fmt.Errorf("no enabled pipeline policy binds this build result to a Bahia service")
		}
		b.warnCandidate("no enabled pipeline policy match; ignoring Hive-CI artifact candidate", "unmapped_repository",
			zap.String("result_event_id", result.ResultEventID),
			zap.String("run_event_id", run.RunEventID),
			zap.String("repo_coordinate", run.RepoCoordinate),
			zap.String("workflow", run.WorkflowPath))
		return nil, nil
	}
	if result.PublisherPubkey != run.PublisherPubkey {
		if _, trustedWorker := b.trustedResults[strings.ToLower(strings.TrimSpace(result.PublisherPubkey))]; !trustedWorker {
			_ = b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateRejected)
			return nil, fmt.Errorf("HiveCI result publisher does not match run publisher or trusted Loom worker allowlist")
		}
	}

	build := expectedBuild
	if build != nil {
		if (build.CISystem != domain.CISystemHiveCI && build.CISystem != domain.CISystemHiveCILegacy) || build.CIRunID != run.RunEventID {
			return nil, fmt.Errorf("build is not bound to the selected HiveCI run")
		}
		if build.ServiceID != policy.ServiceID {
			return nil, fmt.Errorf("build service does not match the pipeline policy service")
		}
	} else {
		build, err = b.buildRepo.GetByCISystemRunID(ctx, domain.CISystemHiveCI, run.RunEventID)
		if err != nil {
			return nil, fmt.Errorf("lookup existing build: %w", err)
		}
		if build == nil {
			build, err = b.buildRepo.GetByCISystemRunID(ctx, domain.CISystemHiveCILegacy, run.RunEventID)
			if err != nil {
				return nil, fmt.Errorf("lookup legacy HiveCI build: %w", err)
			}
		}
	}
	status := domain.BuildStatusFailed
	if result.Status == "success" {
		status = domain.BuildStatusSucceeded
	}
	if build == nil {
		if b.registry == nil {
			return nil, fmt.Errorf("canonical build registry is not configured")
		}
		build = &domain.Build{
			ServiceID: policy.ServiceID, GitSHA: strings.ToLower(strings.TrimSpace(run.CommitSHA)),
			GitRef: run.Branch, CISystem: domain.CISystemHiveCI, CIRunID: run.RunEventID,
			SourceEventID: run.RunEventID, Status: status,
			Metadata: resultEvidenceMetadata(run, result),
		}
		if err := b.registry.RegisterBuild(ctx, build); err != nil {
			return nil, fmt.Errorf("create build: %w", err)
		}
	} else {
		if build.GitSHA != "" && !strings.EqualFold(strings.TrimSpace(build.GitSHA), strings.TrimSpace(run.CommitSHA)) {
			return nil, fmt.Errorf("build commit does not match the signed HiveCI run")
		}
		if build.Status != status {
			if b.registry == nil {
				return nil, fmt.Errorf("canonical build registry is not configured")
			}
			if err := b.registry.UpdateBuildStatus(ctx, build.ID, status); err != nil {
				return nil, fmt.Errorf("update build status: %w", err)
			}
			build.Status = status
		}
	}
	if result.Status != "success" {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateProcessed); err != nil {
			return nil, fmt.Errorf("mark result processed: %w", err)
		}
		if explicit {
			return nil, fmt.Errorf("only a successful HiveCI build result can register an artifact")
		}
		return nil, nil
	}
	if !explicit && !b.autoRegister {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateArtifactPending); err != nil {
			return nil, fmt.Errorf("mark result artifact pending: %w", err)
		}
		b.warnCandidate("automatic Hive-CI artifact registration is disabled; retaining candidate", "automatic_registration_disabled",
			zap.String("result_event_id", result.ResultEventID), zap.String("run_event_id", run.RunEventID),
			zap.String("repo_coordinate", run.RepoCoordinate), zap.String("workflow", run.WorkflowPath))
		return nil, nil
	}

	imageRepo := strings.TrimSpace(result.ImageRepo)
	imageTag := strings.TrimSpace(result.ImageTag)
	imageDigest := strings.ToLower(strings.TrimSpace(result.ImageDigest))
	if imageRepo == "" || imageTag == "" || !immutableManifestDigest.MatchString(imageDigest) {
		_ = b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateRejected)
		candidateErr := fmt.Errorf("successful build result must include repository, tag, and immutable sha256 manifest digest")
		if explicit {
			return nil, candidateErr
		}
		b.warnCandidate("rejecting Hive-CI artifact candidate with incomplete image identity", "missing_or_invalid_digest",
			zap.String("result_event_id", result.ResultEventID), zap.String("run_event_id", run.RunEventID),
			zap.String("repo_coordinate", run.RepoCoordinate), zap.String("workflow", run.WorkflowPath),
			zap.String("image_repo", imageRepo), zap.String("image_tag", imageTag), zap.String("image_digest", imageDigest))
		return nil, nil
	}
	if b.serviceRepo == nil || b.artifactRepo == nil || b.registry == nil {
		return nil, fmt.Errorf("artifact registration dependencies are not configured")
	}
	svc, err := b.serviceRepo.GetByID(ctx, policy.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("load pipeline service: %w", err)
	}
	if svc == nil {
		return nil, fmt.Errorf("pipeline service %s not found", policy.ServiceID)
	}
	if strings.TrimSpace(svc.ArtifactRepo) != imageRepo {
		return nil, fmt.Errorf("build result repository does not match the service artifact repository")
	}

	verified, err := b.resolveManifest(ctx, imageRepo, imageTag, imageDigest)
	if err != nil {
		return nil, fmt.Errorf("verify OCI manifest: %w", err)
	}
	if verified == nil {
		_ = b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateArtifactPending)
		if explicit {
			return nil, fmt.Errorf("immutable OCI manifest could not be verified")
		}
		b.warnCandidate("Hive-CI artifact candidate is waiting for immutable OCI verification", "artifact_unavailable",
			zap.String("result_event_id", result.ResultEventID), zap.String("run_event_id", run.RunEventID),
			zap.String("repo_coordinate", run.RepoCoordinate), zap.String("workflow", run.WorkflowPath),
			zap.String("image_repo", imageRepo), zap.String("image_digest", imageDigest))
		return nil, nil
	}
	artifact = &domain.Artifact{
		BuildID: build.ID, ServiceID: build.ServiceID, ImageRepo: imageRepo,
		ImageTag: imageTag, ImageDigest: imageDigest, ScanStatus: domain.ScanStatusUnknown,
		Metadata: map[string]any{
			"source":                  "hiveci",
			"repository_coordinate":   run.RepoCoordinate,
			"hive_ci_run_event_id":    run.RunEventID,
			"hive_ci_result_event_id": result.ResultEventID,
			"workflow_path":           run.WorkflowPath,
			"git_sha":                 run.CommitSHA,
		},
	}
	proof := service.ArtifactVerificationProof{
		Source: verified.source, ManifestDigest: verified.digest, TagResolvedDigest: verified.digest,
		MediaType: verified.mediaType, SizeBytes: verified.size, ScanStatus: verified.scanStatus,
		Signatures: verified.signatures, SBOMRef: verified.sbomRef,
		ProvenanceRef: verified.provenanceRef, PolicyState: "matched", PolicyID: policy.ID,
		CIPublisher: run.PublisherPubkey, ReferrerDiscoveryState: verified.referrerDiscoveryState,
		VerifiedAt: time.Now().UTC(), Annotations: verified.annotations,
	}
	if err := b.registry.RegisterVerifiedArtifact(ctx, artifact, proof); err != nil {
		return nil, fmt.Errorf("register verified artifact: %w", err)
	}
	if err := b.autoCreateStagingIntent(ctx, policy, run, result, artifact); errors.Is(err, errPromotionGateBlocked) {
		return artifact, nil
	} else if err != nil {
		return nil, err
	}
	if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateProcessed); err != nil {
		return nil, fmt.Errorf("mark result processed: %w", err)
	}
	return artifact, nil
}

type verifiedManifest struct {
	source, digest, mediaType, scanStatus, sbomRef, provenanceRef, referrerDiscoveryState string
	size                                                                                  int64
	signatures                                                                            []string
	annotations                                                                           map[string]string
}

func (b *Bridge) resolveManifest(ctx context.Context, imageRepo, imageTag, imageDigest string) (*verifiedManifest, error) {
	if b.ociRegistry != nil {
		manifest, err := b.ociRegistry.GetManifest(ctx, imageRepo, imageTag)
		if err != nil && b.registryInspector == nil {
			return nil, err
		}
		if err == nil && manifest != nil {
			digest := strings.ToLower(strings.TrimSpace(manifest.Digest))
			if digest != imageDigest || !immutableManifestDigest.MatchString(digest) {
				return nil, fmt.Errorf("embedded OCI manifest digest mismatch")
			}
			verified := &verifiedManifest{
				source: "embedded_oci_layout", digest: digest, mediaType: manifest.MediaType,
				size: manifest.SizeBytes, annotations: manifest.Annotations,
			}
			referrers, refErr := b.ociRegistry.ListReferrers(ctx, imageRepo, imageDigest, "")
			if refErr != nil {
				return nil, fmt.Errorf("list embedded OCI referrers: %w", refErr)
			}
			enrichEmbeddedReferrers(verified, referrers)
			verified.referrerDiscoveryState = "complete"
			return verified, nil
		}
	}
	if b.registryInspector == nil {
		return nil, nil
	}
	repoForInspector := stripRegistryHost(imageRepo)
	inspection, err := b.registryInspector.InspectImage(ctx, repoForInspector, imageTag)
	if err != nil {
		return nil, err
	}
	if inspection == nil || !inspection.Exists {
		return nil, nil
	}
	digest := strings.ToLower(strings.TrimSpace(inspection.Digest))
	if digest != imageDigest || !immutableManifestDigest.MatchString(digest) {
		return nil, fmt.Errorf("registry manifest digest mismatch")
	}
	return &verifiedManifest{
		source: "registry_manifest", digest: digest, mediaType: inspection.MediaType,
		size: inspection.Size, scanStatus: inspection.ScanStatus,
		signatures: append([]string(nil), inspection.Signatures...),
		sbomRef:    inspection.SBOMRef, provenanceRef: inspection.ProvenanceRef,
		referrerDiscoveryState: "best_effort", annotations: inspection.Annotations,
	}, nil
}

func enrichEmbeddedReferrers(verified *verifiedManifest, refs []domain.OCIReferrerDescriptor) {
	for _, ref := range refs {
		kind := strings.ToLower(strings.TrimSpace(ref.ArtifactType))
		switch {
		case strings.Contains(kind, "signature") || strings.Contains(kind, "sigstore") || strings.Contains(kind, "cosign"):
			verified.signatures = append(verified.signatures, ref.Digest)
		case strings.Contains(kind, "sbom") || strings.Contains(kind, "spdx") || strings.Contains(kind, "cyclonedx"):
			if verified.sbomRef == "" {
				verified.sbomRef = ref.Digest
			}
		case strings.Contains(kind, "provenance") || strings.Contains(kind, "in-toto"):
			if verified.provenanceRef == "" {
				verified.provenanceRef = ref.Digest
			}
		}
	}
}

func resultEvidenceMetadata(run *domain.HiveCIWorkflowRun, result *domain.HiveCIWorkflowResult) map[string]any {
	return map[string]any{
		"log_url":               result.LogURL,
		"repository_coordinate": run.RepoCoordinate,
		"evidence": map[string]any{
			"run_event_id":    run.RunEventID,
			"result_event_id": result.ResultEventID,
		},
	}
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
	if policy == nil || policy.Metadata == nil || b.intentRepo == nil || b.envRepo == nil || b.registry == nil || artifact == nil {
		return nil
	}
	autoDeploy, _ := policy.Metadata["auto_deploy_staging"].(bool)
	if !autoDeploy {
		return nil
	}
	if requiresPSTFGreen(policy.Metadata) && !pstfGateGreen(result) {
		if err := b.hiveRepo.UpdateResultState(ctx, result.ResultEventID, domain.HiveCIProcessingStateRejected); err != nil {
			return fmt.Errorf("mark result rejected after failed PSTF gate: %w", err)
		}
		b.logger.Info("blocking promotion because PSTF gate is not green",
			zap.String("result_event_id", result.ResultEventID),
			zap.String("pstf_gate_name", result.PSTFGateName),
			zap.String("pstf_gate_status", result.PSTFGateStatus),
		)
		return errPromotionGateBlocked
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
	var deploymentUnitID *uuid.UUID
	var desiredState *domain.DesiredServiceSpec
	var desiredHash string
	if b.desiredState != nil {
		snapshot, err := b.desiredState.BuildDesiredStateSnapshot(ctx, policy.ServiceID, env.ID, artifact.ID, nil)
		if err != nil {
			return fmt.Errorf("build staging desired state: %w", err)
		}
		if snapshot == nil {
			return fmt.Errorf("build staging desired state: empty snapshot")
		}
		deploymentUnitID = snapshot.DeploymentUnitID
		desiredState = snapshot
		desiredHash = snapshot.DesiredHash
	}
	intent := &domain.DeploymentIntent{
		ServiceID: policy.ServiceID, EnvironmentID: env.ID, ArtifactID: artifact.ID,
		DeploymentUnitID: deploymentUnitID,
		DesiredState:     desiredState,
		DesiredHash:      desiredHash,
		RequestedBy:      "hive-ci-bridge", SourceKind: domain.SourceKindEventTriggered,
		ApprovalStatus: approval, Status: status,
		Metadata: map[string]any{"hive_ci_result_event_id": result.ResultEventID},
	}
	if err := b.registry.CreateDeploymentIntent(ctx, intent); err != nil {
		return fmt.Errorf("create staging deployment intent: %w", err)
	}
	return nil
}

func requiresPSTFGreen(metadata map[string]any) bool {
	for _, key := range []string{"require_pstf_green", "promotion_requires_pstf_green", "pstf_gate_required"} {
		if value, ok := metadata[key].(bool); ok && value {
			return true
		}
	}
	return false
}

func pstfGateGreen(result *domain.HiveCIWorkflowResult) bool {
	if result == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(result.PSTFGateStatus)) {
	case "green", "passed", "pass", "success", "succeeded", "ok":
		return true
	default:
		return false
	}
}
