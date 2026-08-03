// Package repository defines data access interfaces for the Bahia domain.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// AdoptedRuntimeIdentityRepository manages stable workload fingerprints for adoption matching.
type AdoptedRuntimeIdentityRepository interface {
	UpsertMany(ctx context.Context, identities []domain.AdoptedRuntimeIdentity) error
	FindByFingerprints(ctx context.Context, orgID uuid.UUID, fingerprints []string) ([]domain.AdoptedRuntimeIdentity, error)
}

// ServiceRepository manages service records.
type ServiceRepository interface {
	Create(ctx context.Context, svc *domain.Service) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error)
	GetByName(ctx context.Context, name string) (*domain.Service, error)
	List(ctx context.Context) ([]domain.Service, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Service, error)
	Update(ctx context.Context, svc *domain.Service) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// EnvironmentRepository manages environment records.
type EnvironmentRepository interface {
	Create(ctx context.Context, env *domain.Environment) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Environment, error)
	GetByName(ctx context.Context, name string) (*domain.Environment, error)
	List(ctx context.Context) ([]domain.Environment, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Environment, error)
	Update(ctx context.Context, env *domain.Environment) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// BuildRepository manages build records.
type BuildRepository interface {
	Create(ctx context.Context, b *domain.Build) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Build, error)
	GetByCISystemRunID(ctx context.Context, ciSystem, ciRunID string) (*domain.Build, error)
	ListByService(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error
}

// ArtifactRepository manages artifact records.
type ArtifactRepository interface {
	Create(ctx context.Context, a *domain.Artifact) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error)
	GetByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error)
	GetByImageRepoDigest(ctx context.Context, imageRepo, imageDigest string) (*domain.Artifact, error)
	ListByService(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error)
	ListByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error)
}

// DeploymentUnitRepository manages runtime ownership boundaries inside environments.
type DeploymentUnitRepository interface {
	Create(ctx context.Context, unit *domain.DeploymentUnit) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentUnit, error)
	GetByEnvironmentKey(ctx context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error)
	ListByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error)
	ResolveDefault(ctx context.Context, env *domain.Environment) (*domain.DeploymentUnit, error)
}

// DeploymentIntentRepository manages deployment intent records.
type DeploymentIntentRepository interface {
	Create(ctx context.Context, di *domain.DeploymentIntent) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentIntent, error)
	GetByHiveResultEventID(ctx context.Context, eventID string) (*domain.DeploymentIntent, error)
	ListByServiceEnv(ctx context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error
	UpdateApproval(ctx context.Context, id uuid.UUID, status domain.ApprovalStatus) error
	UpdateDesiredState(ctx context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error
}

// DeploymentRunRepository manages deployment run records.
type DeploymentRunRepository interface {
	Create(ctx context.Context, dr *domain.DeploymentRun) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error)
	ListByIntent(ctx context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error
}

// RuntimeObservationRepository manages runtime observation records.
type RuntimeObservationRepository interface {
	Create(ctx context.Context, obs *domain.RuntimeObservation) error
	GetLatest(ctx context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error)
	ListByServiceEnv(ctx context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error)
}

// DeploymentPolicyRepository manages deployment policy records.
type DeploymentPolicyRepository interface {
	Create(ctx context.Context, p *domain.DeploymentPolicy) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error)
	GetByName(ctx context.Context, name string) (*domain.DeploymentPolicy, error)
	List(ctx context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error)
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.DeploymentPolicy, error)
	ListGlobal(ctx context.Context) ([]domain.DeploymentPolicy, error)
	Update(ctx context.Context, p *domain.DeploymentPolicy) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// SBOMRepository manages artifact SBOM records.
type SBOMRepository interface {
	CreateSBOM(ctx context.Context, sbom *domain.ArtifactSBOM) error
	GetSBOMByID(ctx context.Context, id uuid.UUID) (*domain.ArtifactSBOM, error)
	GetSBOMByArtifact(ctx context.Context, artifactID uuid.UUID) (*domain.ArtifactSBOM, error)
	GetSBOMByHash(ctx context.Context, rawHash string) (*domain.ArtifactSBOM, error)
	CreatePackages(ctx context.Context, packages []domain.SBOMPackage) error
	ListPackagesBySBOM(ctx context.Context, sbomID uuid.UUID) ([]domain.SBOMPackage, error)
	SearchPackagesByName(ctx context.Context, name string, limit int) ([]domain.SBOMPackage, error)
}

// SBOMManifestRepository manages subject-neutral SBOM manifest projections.
type SBOMManifestRepository interface {
	CreateManifest(ctx context.Context, manifest *domain.SBOMManifest) error
	ProjectManifest(ctx context.Context, manifest *domain.SBOMManifest, packages []domain.SBOMManifestPackage) error
	UpdateCompatibilityVulnerabilityCounts(ctx context.Context, artifactID uuid.UUID, payloadSHA256 string, counts domain.SecuritySeverityCounts, total int) error
	GetManifestByID(ctx context.Context, id uuid.UUID) (*domain.SBOMManifest, error)
	ListManifestsBySubject(ctx context.Context, subject domain.SBOMSubject, limit int) ([]domain.SBOMManifest, error)
	UpdateManifestPublishState(ctx context.Context, id uuid.UUID, state domain.SBOMPublishState, referenceEventID, availabilityEventID, publishError string) error
	CreateManifestPackages(ctx context.Context, packages []domain.SBOMManifestPackage) error
	ListPackagesByManifest(ctx context.Context, manifestID uuid.UUID) ([]domain.SBOMManifestPackage, error)
	SearchManifestPackagesByName(ctx context.Context, name string, limit int) ([]domain.SBOMManifestPackage, error)
	ListPublishedManifests(ctx context.Context, limit int) ([]domain.SBOMManifest, error)
}

// SecurityFindingFilter constrains Security finding read surfaces.
type SecurityFindingFilter struct {
	RunID         *uuid.UUID
	TargetKeyHash string
	Severity      domain.SecuritySeverity
	OSVID         string
	Limit         int
	Offset        int
}

// SecurityScheduleFilter constrains Security schedule read surfaces.
type SecurityScheduleFilter struct {
	PolicyID      *uuid.UUID
	TargetKeyHash string
	EnabledOnly   bool
	Limit         int
	Offset        int
}

// SecurityRepository manages durable Security target, scan, finding, schedule, breach,
// OSV cache, and observable-publication state. It does not execute scans.
type SecurityRepository interface {
	UpsertSecurityTarget(ctx context.Context, target *domain.SecurityTarget) (*domain.SecurityTarget, error)
	GetSecurityTargetByHash(ctx context.Context, targetKeyHash string) (*domain.SecurityTarget, error)
	ListSecurityTargets(ctx context.Context, targetType domain.SecurityTargetType, limit int) ([]domain.SecurityTarget, error)

	CreateSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error
	GetSecurityScanRun(ctx context.Context, id uuid.UUID) (*domain.SecurityScanRun, error)
	GetActiveSecurityScanRunByTargetHash(ctx context.Context, targetKeyHash string) (*domain.SecurityScanRun, error)
	ListSecurityScanRuns(ctx context.Context, targetKeyHash string, limit int) ([]domain.SecurityScanRun, error)
	ListSecurityScanRunsByStatus(ctx context.Context, statuses []domain.SecurityScanStatus, limit int) ([]domain.SecurityScanRun, error)
	MarkSecurityScanRunStarted(ctx context.Context, id uuid.UUID, startedAt time.Time) error
	CompleteSecurityScanRun(ctx context.Context, run *domain.SecurityScanRun) error
	UpdateSecurityScanRunStatus(ctx context.Context, id uuid.UUID, status domain.SecurityScanStatus, errorMessage string, finishedAt *time.Time) error
	UpsertSecurityTargetLatest(ctx context.Context, latest *domain.SecurityTargetLatest) error
	GetSecurityTargetLatestByHash(ctx context.Context, targetKeyHash string) (*domain.SecurityTargetLatest, error)
	GetLatestSecurityTargetLatestForArtifact(ctx context.Context, artifactID uuid.UUID) (*domain.SecurityTargetLatest, error)

	UpsertSecurityFindings(ctx context.Context, findings []domain.SecurityOSVFinding) error
	ListSecurityFindings(ctx context.Context, runID uuid.UUID) ([]domain.SecurityOSVFinding, error)
	ListSecurityFindingsFiltered(ctx context.Context, filter SecurityFindingFilter) ([]domain.SecurityOSVFinding, error)

	UpsertSecurityScanSchedule(ctx context.Context, schedule *domain.SecurityScanSchedule) error
	ListSecurityScanSchedulesFiltered(ctx context.Context, filter SecurityScheduleFilter) ([]domain.SecurityScanSchedule, error)
	ClaimDueSecurityScanSchedules(ctx context.Context, now time.Time, limit int, leasedBy string, leaseUntil time.Time) ([]domain.SecurityScanSchedule, error)
	MarkSecurityScheduleDispatched(ctx context.Context, id uuid.UUID, runID uuid.UUID, dispatchedAt time.Time, nextDueAt time.Time) error
	DisableSecurityScanSchedulesForPolicy(ctx context.Context, policyID uuid.UUID, disabledAt time.Time) error

	RecordSecurityPolicyBreach(ctx context.Context, breach *domain.SecurityPolicyBreach) (domain.SecurityBreachRecordResult, error)
	ResolveSecurityPolicyBreach(ctx context.Context, policyID uuid.UUID, targetKeyHash string, resolvedAt time.Time) error
	GetActiveSecurityPolicyBreach(ctx context.Context, policyID uuid.UUID, targetKeyHash string) (*domain.SecurityPolicyBreach, error)

	UpsertOSVVulnerabilityCache(ctx context.Context, cache *domain.OSVVulnerabilityCache) error
	GetOSVVulnerabilityCache(ctx context.Context, osvID string, now time.Time) (*domain.OSVVulnerabilityCache, error)
	PruneExpiredOSVVulnerabilityCache(ctx context.Context, now time.Time) (int64, error)

	UpsertSecurityPublication(ctx context.Context, publication *domain.SecurityObservablePublication) error
	UpdateSecurityPublicationState(ctx context.Context, id uuid.UUID, state domain.SecurityPublicationState, eventID, lastError string, nextRetryAt *time.Time, publishedAt *time.Time) error
	ListRetryableSecurityPublications(ctx context.Context, now time.Time, limit int) ([]domain.SecurityObservablePublication, error)
}

// PaymentRecordRepository manages Cashu payment records.
type PaymentRecordRepository interface {
	Create(ctx context.Context, rec *domain.PaymentRecord) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.PaymentRecord, error)
	ListByRun(ctx context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error)
	ListByWorker(ctx context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.PaymentStatus, errMsg string) error
	GetByTokenHash(ctx context.Context, tokenHash string) (*domain.PaymentRecord, error)
}

// ArtifactSignatureRepository manages artifact signature records.
type ArtifactSignatureRepository interface {
	Create(ctx context.Context, sig *domain.ArtifactSignature) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.ArtifactSignature, error)
	ListByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error)
	ListVerifiedByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error)
	HasVerifiedSignature(ctx context.Context, artifactID uuid.UUID) (bool, error)
}

// SecretRepository manages service secret records.
type SecretRepository interface {
	Create(ctx context.Context, s *domain.ServiceSecret) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.ServiceSecret, error)
	GetCurrentVersion(ctx context.Context, secretID uuid.UUID) (*domain.SecretVersion, error)
	ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error)
	ListByServiceAndEnv(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error)
	ListEffective(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error)
	Update(ctx context.Context, s *domain.ServiceSecret) error
	RecordSecretAccessAudit(ctx context.Context, audit *domain.SecretAccessAudit) error
	Delete(ctx context.Context, id uuid.UUID) error
	DeleteByName(ctx context.Context, serviceID uuid.UUID, envID *uuid.UUID, name string) error
}

// RolloutPlanRepository manages rollout plan and step records.
type RolloutPlanRepository interface {
	CreatePlan(ctx context.Context, plan *domain.RolloutPlan) error
	GetPlanByID(ctx context.Context, id uuid.UUID) (*domain.RolloutPlan, error)
	GetPlanByIntent(ctx context.Context, intentID uuid.UUID) (*domain.RolloutPlan, error)
	UpdatePlan(ctx context.Context, plan *domain.RolloutPlan) error
	ListActivePlans(ctx context.Context) ([]domain.RolloutPlan, error)

	CreateStep(ctx context.Context, step *domain.RolloutStep) error
	CreateSteps(ctx context.Context, steps []domain.RolloutStep) error
	GetStepByID(ctx context.Context, id uuid.UUID) (*domain.RolloutStep, error)
	ListStepsByPlan(ctx context.Context, planID uuid.UUID) ([]domain.RolloutStep, error)
	UpdateStep(ctx context.Context, step *domain.RolloutStep) error
}

// NotificationRepository manages notification channels and log records.
type NotificationRepository interface {
	CreateChannel(ctx context.Context, ch *domain.NotificationChannel) error
	GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.NotificationChannel, error)
	ListChannels(ctx context.Context, enabledOnly bool) ([]domain.NotificationChannel, error)
	UpdateChannel(ctx context.Context, ch *domain.NotificationChannel) error
	DeleteChannel(ctx context.Context, id uuid.UUID) error

	CreateLog(ctx context.Context, log *domain.NotificationLog) error
	UpdateLog(ctx context.Context, log *domain.NotificationLog) error
	ListLogsByChannel(ctx context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error)
	ListRecentLogs(ctx context.Context, limit int) ([]domain.NotificationLog, error)
	ListRetryable(ctx context.Context, maxAttempts int) ([]domain.NotificationLog, error)
}

// EnvironmentServiceStateRepository manages the denormalized state view.
type EnvironmentServiceStateRepository interface {
	Upsert(ctx context.Context, state *domain.EnvironmentServiceState) error
	Get(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error)
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error)
	ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error)
	ListDrifted(ctx context.Context) ([]domain.EnvironmentServiceState, error)
	ListDueForObservation(ctx context.Context, dueBefore time.Time) ([]domain.EnvironmentServiceState, error)
	ListAll(ctx context.Context) ([]domain.EnvironmentServiceState, error)
}

// LLMRouteRepository manages LLM route records.
type LLMRouteRepository interface {
	Create(ctx context.Context, route *domain.LLMRoute) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error)
	GetByName(ctx context.Context, name string) (*domain.LLMRoute, error)
	List(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error)
	Update(ctx context.Context, route *domain.LLMRoute) error
	Delete(ctx context.Context, id uuid.UUID) error
}

// LLMReleaseRepository manages immutable LLM release records.
type LLMReleaseRepository interface {
	Create(ctx context.Context, release *domain.LLMRelease) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMRelease, error)
	GetByRouteVersion(ctx context.Context, routeID uuid.UUID, version string) (*domain.LLMRelease, error)
	ListByRoute(ctx context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error)
}

// LLMDeploymentIntentRepository manages LLM deployment intents.
type LLMDeploymentIntentRepository interface {
	Create(ctx context.Context, intent *domain.LLMDeploymentIntent) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error)
	ListByRouteEnv(ctx context.Context, routeID, envID uuid.UUID, limit, offset int) ([]domain.LLMDeploymentIntent, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error
	UpdateApproval(ctx context.Context, id uuid.UUID, status domain.ApprovalStatus) error
}

// LLMDeploymentRunRepository manages LLM deployment runs and queue operations.
type LLMDeploymentRunRepository interface {
	Create(ctx context.Context, run *domain.LLMDeploymentRun) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error)
	ListByIntent(ctx context.Context, intentID uuid.UUID) ([]domain.LLMDeploymentRun, error)
	EnsureQueuedRunForNextReadyIntent(ctx context.Context) (*domain.LLMDeploymentRun, error)
	ClaimNextQueuedRun(ctx context.Context) (*domain.LLMDeploymentRun, error)
	RequeueStaleRunning(ctx context.Context, olderThan time.Duration) (int, error)
	Update(ctx context.Context, run *domain.LLMDeploymentRun) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error
}

// LLMRouteObservationRepository manages LLM route observations.
type LLMRouteObservationRepository interface {
	Create(ctx context.Context, observation *domain.LLMRouteObservation) error
	GetLatest(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteObservation, error)
	ListByRouteEnv(ctx context.Context, routeID, envID uuid.UUID, limit int) ([]domain.LLMRouteObservation, error)
}

// LLMRouteStateRepository manages denormalized LLM route state.
type LLMRouteStateRepository interface {
	Upsert(ctx context.Context, state *domain.LLMRouteState) error
	Get(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteState, error)
	ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.LLMRouteState, error)
	ListByRoute(ctx context.Context, routeID uuid.UUID) ([]domain.LLMRouteState, error)
	ListDrifted(ctx context.Context) ([]domain.LLMRouteState, error)
	ListAll(ctx context.Context) ([]domain.LLMRouteState, error)
}

// MLRegistryRepository manages canonical generic AI/ML registry and state records.
type MLRegistryRepository interface {
	UpsertModel(ctx context.Context, model *domain.MLModel) error
	GetModel(ctx context.Context, id uuid.UUID) (*domain.MLModel, error)
	GetModelBySlug(ctx context.Context, slug string) (*domain.MLModel, error)
	ListModels(ctx context.Context, taskKind domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error)

	UpsertModelVersion(ctx context.Context, version *domain.MLModelVersion) error
	GetModelVersion(ctx context.Context, id uuid.UUID) (*domain.MLModelVersion, error)
	GetModelVersionByModelVersion(ctx context.Context, modelID uuid.UUID, version string) (*domain.MLModelVersion, error)
	ListModelVersions(ctx context.Context, modelID uuid.UUID, limit, offset int) ([]domain.MLModelVersion, error)

	UpsertArtifactRef(ctx context.Context, artifact *domain.MLArtifactRef) error
	GetArtifactRef(ctx context.Context, id uuid.UUID) (*domain.MLArtifactRef, error)
	ListArtifactRefsByModelVersion(ctx context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error)
	UpsertProvenanceEdge(ctx context.Context, edge *domain.MLProvenanceEdge) error
	ListProvenanceEdgesByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.MLProvenanceEdge, error)

	UpsertRecipe(ctx context.Context, recipe *domain.MLRecipe) error
	GetRecipe(ctx context.Context, id uuid.UUID) (*domain.MLRecipe, error)
	GetRecipeByNameVersion(ctx context.Context, name, version string) (*domain.MLRecipe, error)
	UpsertRecipeRun(ctx context.Context, run *domain.MLRecipeRun) error
	GetRecipeRun(ctx context.Context, id uuid.UUID) (*domain.MLRecipeRun, error)

	UpsertInferenceEndpoint(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error
	GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error)
	GetInferenceEndpointByNameEnv(ctx context.Context, name string, envID uuid.UUID) (*domain.MLInferenceEndpoint, error)
	ListInferenceEndpoints(ctx context.Context, envID uuid.UUID, limit, offset int) ([]domain.MLInferenceEndpoint, error)

	UpsertDeploymentIntent(ctx context.Context, intent *domain.MLDeploymentIntent) error
	GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error)
	ListDeploymentIntents(ctx context.Context, endpointID, envID uuid.UUID, limit, offset int) ([]domain.MLDeploymentIntent, error)
	UpsertDeploymentRun(ctx context.Context, run *domain.MLDeploymentRun) error
	GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error)
	ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.MLDeploymentRun, error)

	UpsertInferenceObservation(ctx context.Context, obs *domain.MLInferenceObservation) error
	GetLatestInferenceObservation(ctx context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceObservation, error)
	UpsertInferenceState(ctx context.Context, state *domain.MLInferenceState) error
	GetInferenceState(ctx context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error)
	ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error)
}

// PackageControlPlaneRepository manages Nostr-derived package repository projections and request idempotency caches.
type PackageControlPlaneRepository interface {
	UpsertRepository(ctx context.Context, repo *domain.PackageRepository) error
	GetRepository(ctx context.Context, id uuid.UUID) (*domain.PackageRepository, error)
	GetRepositoryByName(ctx context.Context, name string) (*domain.PackageRepository, error)
	ListRepositories(ctx context.Context, includeDeleted bool) ([]domain.PackageRepository, error)

	UpsertArtifact(ctx context.Context, artifact *domain.PackageArtifact) error
	GetArtifact(ctx context.Context, repositoryID uuid.UUID, namespace, packageName, version, filename string) (*domain.PackageArtifact, error)
	ListArtifacts(ctx context.Context, repositoryID uuid.UUID, limit, offset int) ([]domain.PackageArtifact, error)

	UpsertPublication(ctx context.Context, publication *domain.PackagePublication) error
	GetPublication(ctx context.Context, id uuid.UUID) (*domain.PackagePublication, error)
	ListPublicationsByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.PackagePublication, error)
	ListPublicationsByRepository(ctx context.Context, repositoryID uuid.UUID, includeTerminal bool) ([]domain.PackagePublication, error)

	UpsertIntent(ctx context.Context, intent *domain.PackageIntent) error
	GetIntent(ctx context.Context, id uuid.UUID) (*domain.PackageIntent, error)
	GetIntentByRequestEventID(ctx context.Context, requestEventID string) (*domain.PackageIntent, error)
	ListNonTerminalIntents(ctx context.Context, limit int) ([]domain.PackageIntent, error)
}

// ToolProvisioningRepository manages tool provisioning intents, runs, state, and approvals.
type ToolProvisioningRepository interface {
	// Intents
	CreateIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error
	GetIntent(ctx context.Context, id uuid.UUID) (*domain.ToolProvisionIntent, error)
	UpdateIntent(ctx context.Context, intent *domain.ToolProvisionIntent) error
	ListPendingApprovalIntents(ctx context.Context) ([]domain.ToolProvisionIntent, error)
	ListIntentsByStatus(ctx context.Context, statuses ...domain.ToolProvisionStatus) ([]domain.ToolProvisionIntent, error)

	// Runs
	CreateRun(ctx context.Context, run *domain.ToolProvisionRun) error
	GetRun(ctx context.Context, id uuid.UUID) (*domain.ToolProvisionRun, error)
	UpdateRun(ctx context.Context, run *domain.ToolProvisionRun) error

	// State
	GetProfileState(ctx context.Context, serviceID, envID uuid.UUID) (*domain.ToolProfileState, error)
	UpsertProfileState(ctx context.Context, state *domain.ToolProfileState) error

	// Denylist
	AddToDenylist(ctx context.Context, entry *domain.ToolDenylistEntry) error
	RemoveFromDenylist(ctx context.Context, packageName, manager string) error
	IsDenylisted(ctx context.Context, packageName, manager string) (bool, error)
	ListDenylist(ctx context.Context) ([]domain.ToolDenylistEntry, error)

	// Approval log
	LogApproval(ctx context.Context, intentID uuid.UUID, action, actorPubkey, reason string) error
}

// OCIRegistryRepository manages OCI manifest/blob/tag metadata.
type OCIRegistryRepository interface {
	EnsureRepository(ctx context.Context, name string) (*domain.OCIRepository, error)
	GetRepository(ctx context.Context, name string) (*domain.OCIRepository, error)
	GetManifestByDigest(ctx context.Context, repoName, digest string) (*domain.OCIManifest, error)
	GetManifestByTag(ctx context.Context, repoName, tag string) (*domain.OCIManifest, error)
	PutManifest(ctx context.Context, manifest domain.OCIManifest, tag string) error
	GetBlob(ctx context.Context, digest string) (*domain.OCIBlob, error)
	BlobExistsInRepo(ctx context.Context, repoName, digest string) (bool, error)
	FinalizeBlob(ctx context.Context, upload domain.OCIBlobUpload) error
	LinkBlobToRepo(ctx context.Context, repoName, digest string) error
	UpsertBlob(ctx context.Context, repoName, digest, mediaType, storageRef string, sizeBytes int64) error
	ListTags(ctx context.Context, repoName, lastTag string, limit int) ([]string, error)
	ListReferrers(ctx context.Context, repoName, subjectDigest, artifactType string) ([]domain.OCIReferrerDescriptor, error)
	// For checking manifest existence (used by bridge)
	GetManifest(ctx context.Context, repoName, reference string) (*domain.OCIManifest, error)
}

// UploadSessionRepository manages OCI blob upload sessions.
type UploadSessionRepository interface {
	Create(ctx context.Context, uploadID, repoName, spoolPath string, expiresAt time.Time) error
	Get(ctx context.Context, uploadID string) (repoName, spoolPath, state string, offsetBytes int64, expiresAt time.Time, err error)
	UpdateOffset(ctx context.Context, uploadID string, offsetBytes int64, expiresAt time.Time) error
	UpdateState(ctx context.Context, uploadID, state string) error
	Delete(ctx context.Context, uploadID string) error
}

// HiveCIRepository manages Hive-CI workflow runs/results and pipeline policies.
type HiveCIRepository interface {
	UpsertWorkflowRun(ctx context.Context, run domain.HiveCIWorkflowRun) error
	UpsertWorkflowResult(ctx context.Context, result domain.HiveCIWorkflowResult) error
	GetRunByEventID(ctx context.Context, eventID string) (*domain.HiveCIWorkflowRun, error)
	GetResultByEventID(ctx context.Context, eventID string) (*domain.HiveCIWorkflowResult, error)
	GetLatestResultByRunEventID(ctx context.Context, runEventID string) (*domain.HiveCIWorkflowResult, error)
	ListPendingResults(ctx context.Context) ([]domain.HiveCIWorkflowResult, error)
	ListOrphanedResultsByRun(ctx context.Context, runEventID string) ([]domain.HiveCIWorkflowResult, error)
	UpdateResultState(ctx context.Context, eventID string, newState domain.HiveCIProcessingState) error
	IncrementResultRetry(ctx context.Context, eventID string, at time.Time) (int, error)
	MarkResultFailed(ctx context.Context, eventID, reason string) error
	ListPolicies(ctx context.Context) ([]domain.HiveCIPipelinePolicy, error)
	GetPolicyByRepoAndWorkflow(ctx context.Context, repo, workflow string) (*domain.HiveCIPipelinePolicy, error)
	EnsurePipelinePolicy(ctx context.Context, policy domain.HiveCIPipelinePolicy) error
	// LookupRepositoryCI returns CI status for the given repo coordinates.
	// Returns one result per unique requested coordinate, preserving request order.
	// Coordinates with no CI data get empty result entries (not errors).
	LookupRepositoryCI(ctx context.Context, repoCoordinates []string, includeDisabledPolicies bool) ([]domain.RepositoryCILookup, error)
}

// ServiceAccountRepository manages OCI registry service accounts.
type ServiceAccountRepository interface {
	Create(ctx context.Context, accountID, name, secretHash string, scopes []string, enabled bool) error
	GetByName(ctx context.Context, name string) (accountID, secretHash string, scopes []string, enabled bool, err error)
	UpdateScopes(ctx context.Context, accountID string, scopes []string) error
	UpdateEnabled(ctx context.Context, accountID string, enabled bool) error
	Delete(ctx context.Context, accountID string) error
}
