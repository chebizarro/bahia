package nostr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const KindCatalogVersion = "2026-05-26.item8"

const (
	KindNIP65RelayList = kinds.NIP65RelayList

	KindHiveCIWorkflowRun    = kinds.HiveCIWorkflowRun
	KindHiveCIWorkflowResult = kinds.HiveCIWorkflowResult

	KindLoomWorkerAdvertisement = kinds.LoomWorkerAdvertisement
	KindLoomJobStatusUpdate     = kinds.LoomJobStatusUpdate
	KindLoomJobResult           = kinds.LoomJobResult
	KindLoomJobCancellation     = kinds.LoomJobCancellation

	KindRelaySetDiscovery = kinds.RelaySetDiscovery

	KindBahiaIdentityDefinition = kinds.BahiaIdentityDefinition
	KindBahiaReplayCheckpoint   = kinds.BahiaReplayCheckpoint
	KindBahiaReadinessStatus    = kinds.BahiaReadinessStatus

	KindControlPlaneDeployRequest            = kinds.DeployRequest
	KindControlPlaneRollbackRequest          = kinds.RollbackRequest
	KindControlPlaneServiceAction            = kinds.ServiceAction
	KindControlPlaneServiceCreate            = kinds.ServiceCreate
	KindControlPlaneEnvironmentCreate        = kinds.EnvironmentCreate
	KindControlPlaneDeploymentApproval       = kinds.DeploymentApproval
	KindControlPlaneObservationSubmit        = kinds.ObservationSubmit
	KindControlPlaneDriftRemediate           = kinds.DriftRemediate
	KindControlPlaneLLMRouteCreate           = kinds.LLMRouteCreate
	KindControlPlaneLLMReleaseRegister       = kinds.LLMReleaseRegister
	KindControlPlaneLLMDeployRequest         = kinds.LLMDeployRequest
	KindControlPlaneLLMDeploymentApproval    = kinds.LLMDeploymentApproval
	KindControlPlaneLLMRollbackRequest       = kinds.LLMRollbackRequest
	KindControlPlaneToolProvisionRequest     = kinds.ToolProvisionRequest
	KindControlPlaneToolApprovalRequest      = kinds.ToolApprovalRequest
	KindControlPlaneAdoptionScanRequest      = kinds.AdoptionScanRequest
	KindControlPlaneAdoptionImportRequest    = kinds.AdoptionImportRequest
	KindControlPlaneServiceUpdate            = kinds.ServiceUpdate
	KindControlPlaneServiceDelete            = kinds.ServiceDelete
	KindControlPlaneEnvironmentUpdate        = kinds.EnvironmentUpdate
	KindControlPlaneEnvironmentDelete        = kinds.EnvironmentDelete
	KindControlPlaneArtifactRegister         = kinds.ArtifactRegister
	KindControlPlanePolicyCreate             = kinds.PolicyCreate
	KindControlPlanePolicyUpdate             = kinds.PolicyUpdate
	KindControlPlanePolicyDelete             = kinds.PolicyDelete
	KindControlPlanePolicyEvaluate           = kinds.PolicyEvaluate
	KindControlPlanePackageRepositoryApply   = kinds.PackageRepositoryApply
	KindControlPlanePackageRepositoryDelete  = kinds.PackageRepositoryDelete
	KindControlPlanePackagePublishIntent     = kinds.PackagePublishIntent
	KindControlPlanePackagePromotionRequest  = kinds.PackagePromotionRequest
	KindControlPlanePackageYankRequest       = kinds.PackageYankRequest
	KindControlPlanePackageDriftDetect       = kinds.PackageDriftDetect
	KindControlPlaneWorkerCordonRequest      = kinds.WorkerCordonRequest
	KindControlPlaneWorkerUncordonRequest    = kinds.WorkerUncordonRequest
	KindControlPlaneWorkerDrainRequest       = kinds.WorkerDrainRequest
	KindControlPlaneWorkerUndrainRequest     = kinds.WorkerUndrainRequest
	KindControlPlaneWorkerMaintenanceEnter   = kinds.WorkerMaintenanceEnter
	KindControlPlaneWorkerMaintenanceExit    = kinds.WorkerMaintenanceExit
	KindControlPlaneWorkerLabelsUpdate       = kinds.WorkerLabelsUpdate
	KindControlPlaneWorkerPolicyApplyRequest = kinds.WorkerPolicyApplyRequest
	KindControlPlaneWorkloadPinRequest       = kinds.WorkloadPinRequest
	KindControlPlaneWorkerCleanupRequest     = kinds.WorkerCleanupRequest

	KindMLRecipeRunRequest            = kinds.MLRecipeRunRequest
	KindMLInferenceDeployRequest      = kinds.MLInferenceDeployRequest
	KindMLInferenceDeploymentApproval = kinds.MLInferenceDeploymentApproval
	KindMLInferenceRollbackRequest    = kinds.MLInferenceRollbackRequest
	KindMLModelImportRequest          = kinds.MLModelImportRequest
	KindMLRecipeRunResult             = kinds.MLRecipeRunResult
	KindMLInferenceDeployResult       = kinds.MLInferenceDeployResult
	KindMLInferenceApprovalResult     = kinds.MLInferenceApprovalResult
	KindMLInferenceRollbackResult     = kinds.MLInferenceRollbackResult
	KindMLModelImportResult           = kinds.MLModelImportResult

	KindControlPlaneDeploymentStatus    = kinds.DeploymentStatus
	KindControlPlaneServiceStatus       = kinds.ServiceStatus
	KindControlPlaneActionStatus        = kinds.ActionStatus
	KindControlPlaneLLMDeploymentStatus = kinds.LLMDeploymentStatus
	KindControlPlaneToolProvisionStatus = kinds.ToolProvisionStatus
	KindControlPlaneAdoptionStatus      = kinds.AdoptionStatus
	KindControlPlanePackageStatus       = kinds.PackageStatus
	KindControlPlaneWorkerStatus        = kinds.WorkerStatus

	KindControlPlaneDeploymentResult         = kinds.DeploymentResult
	KindControlPlaneActionResult             = kinds.ActionResult
	KindControlPlaneServiceCreateResult      = kinds.ServiceCreateResult
	KindControlPlaneEnvironmentCreateResult  = kinds.EnvironmentCreateResult
	KindControlPlaneObservationResult        = kinds.ObservationResult
	KindControlPlaneRemediationResult        = kinds.RemediationResult
	KindControlPlaneLLMRouteCreateResult     = kinds.LLMRouteCreateResult
	KindControlPlaneLLMReleaseRegisterResult = kinds.LLMReleaseRegisterResult
	KindControlPlaneLLMDeploymentResult      = kinds.LLMDeploymentResult
	KindControlPlaneToolProvisionResult      = kinds.ToolProvisionResult
	KindControlPlaneToolApprovalResponse     = kinds.ToolApprovalResponse
	KindControlPlaneAdoptionScanResult       = kinds.AdoptionScanResult
	KindControlPlaneAdoptionImportResult     = kinds.AdoptionImportResult
	KindControlPlanePackageResult            = kinds.PackageResult
	KindControlPlanePackageDriftEvent        = kinds.PackageDriftEvent
	KindControlPlaneWorkerResult             = kinds.WorkerResult

	KindFIPSOverlayAdvert = kinds.FIPSOverlayAdvert
)

type ReplayGroup struct {
	Name     string
	Kinds    []int
	Tier     int
	Snapshot bool
	Required bool
}

type KindCatalog struct {
	Version  string
	Groups   []ReplayGroup
	decoders map[int]DecodeFunc
}

type DecodeFunc func(ev *gonostr.Event) (*DecodedProjectionEvent, error)

type ProjectionFamily string

const (
	FamilyService      ProjectionFamily = "service"
	FamilyEnvironment  ProjectionFamily = "environment"
	FamilyWorker       ProjectionFamily = "worker"
	FamilyBuild        ProjectionFamily = "build"
	FamilyArtifact     ProjectionFamily = "artifact"
	FamilyIntent       ProjectionFamily = "intent"
	FamilyRun          ProjectionFamily = "run"
	FamilyPolicy       ProjectionFamily = "policy"
	FamilyState        ProjectionFamily = "state"
	FamilyContinuity   ProjectionFamily = "continuity"
	FamilyBackup       ProjectionFamily = "backup"
	FamilyDNS          ProjectionFamily = "dns"
	FamilyLLM          ProjectionFamily = "llm"
	FamilyML           ProjectionFamily = "ml"
	FamilyPackage      ProjectionFamily = "package"
	FamilyHiveCI       ProjectionFamily = "hive_ci"
	FamilyLoom         ProjectionFamily = "loom"
	FamilyAssistant    ProjectionFamily = "assistant"
	FamilyTool         ProjectionFamily = "tool"
	FamilyAdoption     ProjectionFamily = "adoption"
	FamilySystem       ProjectionFamily = "system"
	FamilyFIPS         ProjectionFamily = "fips"
	FamilyControlPlane ProjectionFamily = "control_plane"
)

type DecodedProjectionEvent struct {
	Kind      int
	DTag      string
	Group     string
	Tier      int
	Timestamp time.Time
	SourceID  string
	Family    ProjectionFamily

	Service     *DecodedService
	Environment *DecodedEnvironment
	Worker      *DecodedWorker
	Build       *DecodedBuild
	Artifact    *DecodedArtifact
	Intent      *DecodedIntent
	Run         *DecodedRun
	Policy      *DecodedPolicy
	State       *DecodedState
	Continuity  *DecodedContinuity
	Backup      *DecodedBackup
	DNS         *DecodedDNS
	LLM         *DecodedLLM
	ML          *DecodedML
	Package     *DecodedPackage
	HiveCI      *DecodedHiveCI
	Loom        *DecodedLoom
	Assistant   *DecodedAssistant
	Tool        *DecodedTool
	Adoption    *DecodedAdoption
	System      *DecodedSystem
	FIPS        *DecodedFIPS

	Tombstone bool
}

type DecodedService struct {
	Deleted       bool               `json:"deleted"`
	ID            string             `json:"id"`
	Name          string             `json:"name,omitempty"`
	RepoURL       string             `json:"repo_url,omitempty"`
	ArtifactRepo  string             `json:"artifact_repo,omitempty"`
	DefaultBranch string             `json:"default_branch,omitempty"`
	RuntimeType   domain.RuntimeType `json:"runtime_type,omitempty"`
	CreatedAt     time.Time          `json:"created_at,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at,omitempty"`
}

type DecodedEnvironment struct {
	Deleted        bool                  `json:"deleted"`
	ID             string                `json:"id"`
	Name           string                `json:"name,omitempty"`
	Protected      bool                  `json:"protected"`
	DeployStrategy domain.DeployStrategy `json:"deploy_strategy,omitempty"`
	CreatedAt      time.Time             `json:"created_at,omitempty"`
	UpdatedAt      time.Time             `json:"updated_at,omitempty"`
}

type DecodedWorker struct {
	Worker             *domain.Worker                   `json:"worker,omitempty"`
	AssignmentState    *domain.WorkerAssignmentState    `json:"assignment_state,omitempty"`
	DrainStatus        *domain.WorkerDrainStatus        `json:"drain_status,omitempty"`
	EligibilityPreview *domain.WorkerEligibilityPreview `json:"eligibility_preview,omitempty"`
}

type DecodedBuild struct {
	Deleted       bool               `json:"deleted"`
	ID            string             `json:"id"`
	ServiceID     string             `json:"service_id"`
	GitSHA        string             `json:"git_sha,omitempty"`
	GitRef        string             `json:"git_ref,omitempty"`
	CISystem      string             `json:"ci_system,omitempty"`
	CIRunID       string             `json:"ci_run_id,omitempty"`
	LoomJobID     string             `json:"loom_job_id,omitempty"`
	Status        domain.BuildStatus `json:"status,omitempty"`
	SourceEventID string             `json:"source_event_id,omitempty"`
	StartedAt     *time.Time         `json:"started_at,omitempty"`
	FinishedAt    *time.Time         `json:"finished_at,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty"`
	CreatedAt     time.Time          `json:"created_at,omitempty"`
}

type DecodedArtifact struct {
	Deleted           bool              `json:"deleted"`
	ID                string            `json:"id"`
	BuildID           string            `json:"build_id"`
	ServiceID         string            `json:"service_id"`
	ImageRepo         string            `json:"image_repo,omitempty"`
	ImageTag          string            `json:"image_tag,omitempty"`
	ImageDigest       string            `json:"image_digest,omitempty"`
	ManifestMediaType string            `json:"manifest_media_type,omitempty"`
	SizeBytes         *int64            `json:"size_bytes,omitempty"`
	SBOMURL           string            `json:"sbom_url,omitempty"`
	SignatureRef      string            `json:"signature_ref,omitempty"`
	ScanStatus        domain.ScanStatus `json:"scan_status,omitempty"`
	Metadata          map[string]any    `json:"metadata,omitempty"`
	CreatedAt         time.Time         `json:"created_at,omitempty"`
}

type DecodedIntent struct {
	Deleted          bool                          `json:"deleted"`
	ID               string                        `json:"id"`
	ServiceID        string                        `json:"service_id"`
	EnvironmentID    string                        `json:"environment_id"`
	ArtifactID       string                        `json:"artifact_id"`
	RequestedBy      string                        `json:"requested_by,omitempty"`
	SourceKind       domain.SourceKind             `json:"source_kind,omitempty"`
	ApprovalStatus   domain.ApprovalStatus         `json:"approval_status,omitempty"`
	Status           domain.DeploymentIntentStatus `json:"status,omitempty"`
	DeploymentStatus string                        `json:"deployment_status,omitempty"`
	DesiredHash      string                        `json:"desired_hash,omitempty"`
	Renderer         string                        `json:"renderer,omitempty"`
	Target           string                        `json:"target,omitempty"`
	ApprovalMetadata map[string]any                `json:"approval_metadata,omitempty"`
	Metadata         map[string]any                `json:"metadata,omitempty"`
	CreatedAt        time.Time                     `json:"created_at,omitempty"`
	ApprovedAt       *time.Time                    `json:"approved_at,omitempty"`
	UpdatedAt        time.Time                     `json:"updated_at,omitempty"`
}

type DecodedRun struct {
	Deleted            bool                       `json:"deleted"`
	ID                 string                     `json:"id"`
	DeploymentIntentID string                     `json:"deployment_intent_id"`
	LoomJobID          string                     `json:"loom_job_id,omitempty"`
	WorkerPubkey       string                     `json:"worker_pubkey,omitempty"`
	WorkerName         string                     `json:"worker_name,omitempty"`
	Status             domain.DeploymentRunStatus `json:"status,omitempty"`
	ExitCode           *int                       `json:"exit_code,omitempty"`
	StdoutRef          string                     `json:"stdout_ref,omitempty"`
	StderrRef          string                     `json:"stderr_ref,omitempty"`
	Renderer           string                     `json:"renderer,omitempty"`
	DesiredHash        string                     `json:"desired_hash,omitempty"`
	RevisionHash       string                     `json:"revision_hash,omitempty"`
	Target             string                     `json:"target,omitempty"`
	ApplySummary       string                     `json:"apply_summary,omitempty"`
	ObservationID      string                     `json:"observation_id,omitempty"`
	StartedAt          *time.Time                 `json:"started_at,omitempty"`
	FinishedAt         *time.Time                 `json:"finished_at,omitempty"`
	Metadata           map[string]any             `json:"metadata,omitempty"`
	CreatedAt          time.Time                  `json:"created_at,omitempty"`
	UpdatedAt          time.Time                  `json:"updated_at,omitempty"`
}

type DecodedPolicy struct {
	Deleted       bool                     `json:"deleted"`
	ID            string                   `json:"id"`
	Name          string                   `json:"name,omitempty"`
	EnvironmentID *string                  `json:"environment_id,omitempty"`
	Rules         []domain.PolicyRule      `json:"rules,omitempty"`
	RuleCount     int                      `json:"rule_count,omitempty"`
	Enforcement   domain.PolicyEnforcement `json:"enforcement,omitempty"`
	Enabled       bool                     `json:"enabled"`
	CreatedAt     time.Time                `json:"created_at,omitempty"`
	UpdatedAt     time.Time                `json:"updated_at,omitempty"`
}

type DecodedState struct {
	Deleted              bool               `json:"deleted"`
	ServiceID            string             `json:"service_id"`
	EnvironmentID        string             `json:"environment_id"`
	DesiredArtifactID    *string            `json:"desired_artifact_id,omitempty"`
	DesiredIntentID      *string            `json:"desired_intent_id,omitempty"`
	LastSuccessfulRunID  *string            `json:"last_successful_run_id,omitempty"`
	CurrentObservationID *string            `json:"current_observation_id,omitempty"`
	DriftStatus          domain.DriftStatus `json:"drift_status,omitempty"`
	DesiredHash          string             `json:"desired_hash,omitempty"`
	ObservedHash         string             `json:"observed_hash,omitempty"`
	Renderer             string             `json:"renderer,omitempty"`
	Target               string             `json:"target,omitempty"`
	LastReconciledAt     *time.Time         `json:"last_reconciled_at,omitempty"`
	UpdatedAt            time.Time          `json:"updated_at,omitempty"`
}

type DecodedContinuity struct {
	Profile             *domain.ServiceContinuityProfile `json:"profile,omitempty"`
	Recipe              *domain.ContinuityRecipe         `json:"recipe,omitempty"`
	StandbyNode         *StandbyNodeDefinition           `json:"standby_node,omitempty"`
	ReplicationPolicy   *domain.ReplicationPolicy        `json:"replication_policy,omitempty"`
	Heartbeat           *domain.HeartbeatObservation     `json:"heartbeat,omitempty"`
	Command             *ContinuityCommandRequest        `json:"command,omitempty"`
	Status              *DecodedContinuityStatus         `json:"status,omitempty"`
	PreviousProfile     domain.ContinuityMode            `json:"previous_profile,omitempty"`
	RecoveryProgressKey string                           `json:"recovery_progress_key,omitempty"`
}

type DecodedContinuityStatus struct {
	ServiceKey          string                `json:"service_key"`
	ActiveProfile       domain.ContinuityMode `json:"active_profile"`
	OperationState      string                `json:"operation_state"`
	PrimaryWorkerPubKey string                `json:"primary_worker_pubkey,omitempty"`
	ActiveWorkerPubKey  string                `json:"active_worker_pubkey,omitempty"`
	StandbyWorkerPubKey string                `json:"standby_worker_pubkey,omitempty"`
	Reason              string                `json:"reason,omitempty"`
	ChangedAt           time.Time             `json:"changed_at,omitempty"`
	CurrentRunID        string                `json:"current_run_id,omitempty"`
	CurrentStepIndex    int                   `json:"current_step_index,omitempty"`
	CurrentStepCount    int                   `json:"current_step_count,omitempty"`
	CurrentStepAction   string                `json:"current_step_action,omitempty"`
}
type DecodedBackup struct{}
type DecodedDNS struct{}
type DecodedLLM struct{}
type DecodedML struct{}
type DecodedPackage struct{}
type DecodedHiveCI struct{}
type DecodedLoom struct{}
type DecodedAssistant struct{}
type DecodedTool struct{}
type DecodedAdoption struct{}
type DecodedSystem struct{}
type DecodedFIPS struct{}

func NewKindCatalog() *KindCatalog {
	groups := []ReplayGroup{
		{Name: "discovery_snapshot", Kinds: []int{KindRelaySetDiscovery, KindNIP65RelayList, kinds.ContextVMServerAnnouncement, kinds.ContextVMToolsList, kinds.ContextVMResourcesList, kinds.ContextVMResourceTemplatesList, kinds.ContextVMPromptsList, KindBahiaIdentityDefinition, KindBahiaReplayCheckpoint, KindBahiaReadinessStatus}, Tier: 0, Snapshot: true, Required: true},
		{Name: "state_snapshot", Kinds: []int{KindCASControlState}, Tier: 1, Snapshot: true, Required: true},
		{Name: "status_live", Kinds: []int{KindNIP38Status}, Tier: 1, Snapshot: false, Required: true},
		{Name: "audit_live", Kinds: []int{KindCASAudit}, Tier: 1, Snapshot: false, Required: true},
		{Name: "loom_live", Kinds: []int{KindLoomWorkerAdvertisement, KindLoomJobStatusUpdate, KindLoomJobResult, KindLoomJobCancellation}, Tier: 3, Snapshot: false, Required: false},
		{Name: "hive_ci_live", Kinds: []int{KindHiveCIWorkflowRun, KindHiveCIWorkflowResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "fips_snapshot", Kinds: []int{KindFIPSOverlayAdvert}, Tier: 3, Snapshot: true, Required: false},
	}

	catalog := &KindCatalog{
		Version:  KindCatalogVersion,
		Groups:   groups,
		decoders: make(map[int]DecodeFunc),
	}
	for _, kind := range catalog.AllKinds() {
		catalog.decoders[kind] = decoderNotImplemented(kind)
	}
	catalog.registerRequiredGroupNoopDecoders()
	catalog.registerOptionalProtocolDecoders()
	catalog.registerProjectionDecoders()
	return catalog
}

func (c *KindCatalog) GroupsForTier(tier int) []ReplayGroup {
	return filterReplayGroups(c.Groups, func(group ReplayGroup) bool { return group.Tier <= tier })
}

func (c *KindCatalog) SnapshotGroups() []ReplayGroup {
	return filterReplayGroups(c.Groups, func(group ReplayGroup) bool { return group.Snapshot })
}

func (c *KindCatalog) LiveGroups() []ReplayGroup {
	return filterReplayGroups(c.Groups, func(group ReplayGroup) bool { return !group.Snapshot })
}

func (c *KindCatalog) AllKinds() []int {
	seen := make(map[int]struct{})
	for _, group := range c.Groups {
		for _, kind := range group.Kinds {
			seen[kind] = struct{}{}
		}
	}
	kinds := make([]int, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	sort.Ints(kinds)
	return kinds
}

func (c *KindCatalog) KindsForTier(tier int) []int {
	return kindsFromGroups(c.GroupsForTier(tier))
}

func (c *KindCatalog) RequiredGroupsForTier(tier int) []ReplayGroup {
	return filterReplayGroups(c.Groups, func(group ReplayGroup) bool { return group.Tier <= tier && group.Required })
}

func (c *KindCatalog) Decoder(kind int) (DecodeFunc, bool) {
	decoder, ok := c.decoders[kind]
	return decoder, ok
}

func filterReplayGroups(groups []ReplayGroup, keep func(ReplayGroup) bool) []ReplayGroup {
	filtered := make([]ReplayGroup, 0, len(groups))
	for _, group := range groups {
		if keep(group) {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func kindsFromGroups(groups []ReplayGroup) []int {
	seen := make(map[int]struct{})
	for _, group := range groups {
		for _, kind := range group.Kinds {
			seen[kind] = struct{}{}
		}
	}
	kinds := make([]int, 0, len(seen))
	for kind := range seen {
		kinds = append(kinds, kind)
	}
	sort.Ints(kinds)
	return kinds
}

func (c *KindCatalog) registerRequiredGroupNoopDecoders() {
	for _, group := range c.Groups {
		if !group.Required {
			continue
		}
		family := noopProjectionFamily(group.Name)
		for _, kind := range group.Kinds {
			c.decoders[kind] = decodeNoopProjection(group.Name, group.Tier, family)
		}
	}
}

func (c *KindCatalog) registerOptionalProtocolDecoders() {
	c.decoders[KindLoomJobStatusUpdate] = decodeNoopProjection("loom_live", 3, FamilyLoom)
	c.decoders[KindLoomJobResult] = decodeNoopProjection("loom_live", 3, FamilyLoom)
	c.decoders[KindLoomJobCancellation] = decodeNoopProjection("loom_live", 3, FamilyLoom)
	c.decoders[KindHiveCIWorkflowRun] = decodeNoopProjection("hive_ci_live", 3, FamilyHiveCI)
	c.decoders[KindHiveCIWorkflowResult] = decodeNoopProjection("hive_ci_live", 3, FamilyHiveCI)
	c.decoders[KindFIPSOverlayAdvert] = decodeNoopProjection("fips_snapshot", 3, FamilyFIPS)
}

func (c *KindCatalog) registerProjectionDecoders() {
	c.decoders[KindServiceRegistry] = decodeServiceProjection
	c.decoders[KindEnvironmentRegistry] = decodeEnvironmentProjection
	c.decoders[KindBuildRegistry] = decodeBuildProjection
	c.decoders[KindArtifactRegistry] = decodeArtifactProjection
	c.decoders[KindDeploymentIntentRegistry] = decodeIntentProjection
	c.decoders[KindDeploymentRunRegistry] = decodeRunProjection
	c.decoders[KindPolicyRegistry] = decodePolicyProjection
	c.decoders[KindServiceState] = decodeStateProjection

	c.decoders[KindWorkerState] = decodeWorkerProjection
	c.decoders[KindWorkerAssignmentState] = decodeWorkerAssignmentProjection
	c.decoders[KindWorkerDrainStatus] = decodeWorkerDrainProjection
	c.decoders[KindWorkerEligibilityPreview] = decodeWorkerEligibilityProjection
	c.decoders[KindLoomWorkerAdvertisement] = decodeWorkerAdvertisementProjection

	c.decoders[KindContinuityProfile] = decodeContinuityProfileProjection
	c.decoders[KindFailoverPolicy] = decodeFailoverPolicyProjection
	c.decoders[KindRecoveryWorkflow] = decodeRecoveryWorkflowProjection
	c.decoders[KindStandbyNodeDefinition] = decodeStandbyNodeProjection
	c.decoders[KindReplicationPolicy] = decodeReplicationPolicyProjection
	c.decoders[KindHeartbeatObservation] = decodeNIP38StatusProjection
	c.decoders[KindFailoverRequest] = decodeContinuityCommandProjection
	c.decoders[KindRecoveryRequest] = decodeContinuityCommandProjection
	c.decoders[KindContinuityStatus] = decodeContinuityStatusProjection
	c.decoders[KindDegradedModeActivation] = decodeContinuityStatusProjection
	c.decoders[KindRecoveryProgress] = decodeContinuityStatusProjection
}

func decodeServiceProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedService
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyService, payload.ID, payload.UpdatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Service = &payload }), nil
}

func decodeEnvironmentProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedEnvironment
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyEnvironment, payload.ID, payload.UpdatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Environment = &payload }), nil
}

func decodeBuildProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedBuild
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyBuild, payload.ID, payload.CreatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Build = &payload }), nil
}

func decodeArtifactProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedArtifact
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyArtifact, payload.ID, payload.CreatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Artifact = &payload }), nil
}

func decodeIntentProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedIntent
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyIntent, payload.ID, payload.UpdatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Intent = &payload }), nil
}

func decodeRunProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedRun
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyRun, payload.ID, payload.UpdatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Run = &payload }), nil
}

func decodePolicyProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedPolicy
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyPolicy, payload.ID, payload.UpdatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.Policy = &payload }), nil
}

func decodeStateProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var payload DecodedState
	if err := decodeContent(ev, &payload); err != nil {
		return nil, err
	}
	key := payload.ServiceID + ":" + payload.EnvironmentID
	return baseDecoded(ev, FamilyState, key, payload.UpdatedAt, payload.Deleted, func(out *DecodedProjectionEvent) { out.State = &payload }), nil
}

func decodeWorkerProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var worker domain.Worker
	if err := decodeContent(ev, &worker); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyWorker, firstNonBlank(worker.PubKey, tagValueLocal(ev.Tags, "worker"), eventPubKeyHex(ev)), worker.UpdatedAt, false, func(out *DecodedProjectionEvent) {
		out.Worker = &DecodedWorker{Worker: &worker}
	}), nil
}

func decodeWorkerAssignmentProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var state domain.WorkerAssignmentState
	if err := decodeContent(ev, &state); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyWorker, firstNonBlank(state.WorkerPubKey, tagValueLocal(ev.Tags, "worker")), state.UpdatedAt, false, func(out *DecodedProjectionEvent) {
		out.Worker = &DecodedWorker{AssignmentState: &state}
	}), nil
}

func decodeWorkerDrainProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var status domain.WorkerDrainStatus
	if err := decodeContent(ev, &status); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyWorker, firstNonBlank(status.WorkerPubKey, tagValueLocal(ev.Tags, "worker")), status.UpdatedAt, false, func(out *DecodedProjectionEvent) {
		out.Worker = &DecodedWorker{DrainStatus: &status}
	}), nil
}

func decodeWorkerEligibilityProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var preview domain.WorkerEligibilityPreview
	if err := decodeContent(ev, &preview); err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyWorker, firstNonBlank(preview.PreviewID, tagValueLocal(ev.Tags, "d")), preview.UpdatedAt, false, func(out *DecodedProjectionEvent) {
		out.Worker = &DecodedWorker{EligibilityPreview: &preview}
	}), nil
}

func decodeWorkerAdvertisementProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var content struct {
		Name              string                      `json:"name"`
		Description       string                      `json:"description"`
		MaxConcurrentJobs int                         `json:"max_concurrent_jobs"`
		CurrentQueueDepth int                         `json:"current_queue_depth"`
		Resources         *domain.WorkerResources     `json:"resources,omitempty"`
		Accelerators      []domain.WorkerAccelerator  `json:"accelerators,omitempty"`
		RuntimeTarget     *domain.WorkerRuntimeTarget `json:"runtime_target,omitempty"`
		MLCapabilities    domain.WorkerMLCapabilities `json:"ml_capabilities,omitempty"`
	}
	if strings.TrimSpace(ev.Content) != "" {
		if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
			return nil, fmt.Errorf("decode worker advertisement content: %w", err)
		}
	}
	worker := &domain.Worker{PubKey: eventPubKeyHex(ev), Name: content.Name, Description: content.Description, MaxConcurrentJobs: content.MaxConcurrentJobs, CurrentQueueDepth: content.CurrentQueueDepth, Resources: content.Resources, Accelerators: content.Accelerators, RuntimeTarget: content.RuntimeTarget, MLCapabilities: content.MLCapabilities, LastAdvertisementAt: ev.CreatedAt.Time().UTC(), Status: domain.WorkerStatusOnline, CreatedAt: ev.CreatedAt.Time().UTC(), UpdatedAt: ev.CreatedAt.Time().UTC()}
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "S":
			sw := domain.WorkerSoftware{Name: tag[1]}
			if len(tag) >= 3 {
				sw.Version = tag[2]
			}
			if len(tag) >= 4 {
				sw.Path = tag[3]
			}
			worker.Software = append(worker.Software, sw)
		case "A":
			worker.Architecture = tag[1]
		case "g":
			worker.Geohash = tag[1]
		case "relay":
			worker.PreferredRelays = append(worker.PreferredRelays, tag[1])
		case "runtime":
			worker.MLCapabilities.Runtimes = append(worker.MLCapabilities.Runtimes, domain.MLRuntimeKind(tag[1]))
		case "artifact_format", "format":
			worker.MLCapabilities.ArtifactFormats = append(worker.MLCapabilities.ArtifactFormats, domain.MLArtifactFormat(tag[1]))
		case "task":
			worker.MLCapabilities.Tasks = append(worker.MLCapabilities.Tasks, domain.MLTaskKind(tag[1]))
		case "accelerator":
			worker.MLCapabilities.Accelerators = append(worker.MLCapabilities.Accelerators, tag[1])
		case "toolchain":
			worker.MLCapabilities.Toolchains = append(worker.MLCapabilities.Toolchains, tag[1])
		case "cached_artifact", "artifact":
			worker.MLCapabilities.CachedArtifacts = append(worker.MLCapabilities.CachedArtifacts, tag[1])
		}
	}
	worker.MLCapabilities = domain.NormalizeWorkerMLCapabilities(*worker)
	return baseDecoded(ev, FamilyWorker, worker.PubKey, worker.UpdatedAt, false, func(out *DecodedProjectionEvent) { out.Worker = &DecodedWorker{Worker: worker} }), nil
}

func decodeContinuityProfileProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	profile, err := DecodeContinuityProfileEvent(ev)
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), profile.UpdatedAt, false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{Profile: profile} }), nil
}

func decodeFailoverPolicyProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	recipe, err := DecodeFailoverPolicyEvent(ev)
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), recipe.UpdatedAt, false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{Recipe: recipe} }), nil
}

func decodeRecoveryWorkflowProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	recipe, err := DecodeRecoveryWorkflowEvent(ev)
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), recipe.UpdatedAt, false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{Recipe: recipe} }), nil
}

func decodeStandbyNodeProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	def, err := DecodeStandbyNodeDefinitionEvent(ev)
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), def.UpdatedAt, false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{StandbyNode: def} }), nil
}

func decodeReplicationPolicyProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	policy, err := DecodeReplicationPolicyEvent(ev)
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), policy.UpdatedAt, false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{ReplicationPolicy: policy} }), nil
}

func decodeNIP38StatusProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	if isContinuityHeartbeatStatusEvent(ev) {
		return decodeHeartbeatProjection(ev)
	}
	return decodeNoopProjection("status_live", 1, FamilyControlPlane)(ev)
}

func decodeHeartbeatProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	heartbeat, err := DecodeHeartbeatObservationEvent(ev)
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), heartbeat.ObservedAt, false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{Heartbeat: heartbeat} }), nil
}

func decodeContinuityCommandProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var command *ContinuityCommandRequest
	var err error
	if eventKindMatches(ev, KindFailoverRequest) {
		command, err = DecodeFailoverRequestEvent(ev)
	} else {
		command, err = DecodeRecoveryRequestEvent(ev)
	}
	if err != nil {
		return nil, err
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), ev.CreatedAt.Time().UTC(), false, func(out *DecodedProjectionEvent) { out.Continuity = &DecodedContinuity{Command: command} }), nil
}

func decodeContinuityStatusProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	var status DecodedContinuityStatus
	if err := decodeContent(ev, &status); err != nil {
		return nil, err
	}
	previous := domain.ContinuityMode("")
	if eventKindMatches(ev, KindDegradedModeActivation) {
		previous = domain.ContinuityMode(tagValueLocal(ev.Tags, "previous_profile"))
	}
	return baseDecoded(ev, FamilyContinuity, continuityDTag(ev), firstTime(status.ChangedAt, ev.CreatedAt.Time().UTC()), false, func(out *DecodedProjectionEvent) {
		out.Continuity = &DecodedContinuity{Status: &status, PreviousProfile: previous, RecoveryProgressKey: tagValueLocal(ev.Tags, "run")}
	}), nil
}

func decodeNoopProjection(group string, tier int, family ProjectionFamily) DecodeFunc {
	return func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
		if ev == nil {
			return nil, fmt.Errorf("projection event is nil")
		}
		return &DecodedProjectionEvent{
			Kind:      eventKindInt(ev),
			DTag:      noopProjectionDTag(ev),
			Group:     group,
			Tier:      tier,
			Timestamp: ev.CreatedAt.Time().UTC(),
			SourceID:  eventIDHex(ev),
			Family:    family,
		}, nil
	}
}

func noopProjectionFamily(group string) ProjectionFamily {
	switch {
	case strings.HasPrefix(group, "core_control_plane"):
		return FamilyControlPlane
	case strings.HasPrefix(group, "discovery") || strings.HasPrefix(group, "system"):
		return FamilySystem
	case strings.HasPrefix(group, "state"):
		return FamilyState
	case strings.HasPrefix(group, "status"):
		return FamilyControlPlane
	case strings.HasPrefix(group, "audit"):
		return FamilyControlPlane
	default:
		return ProjectionFamily("")
	}
}

func noopProjectionDTag(ev *gonostr.Event) string {
	return firstNonBlank(
		tagValueLocal(ev.Tags, "d"),
		tagValueLocal(ev.Tags, "service"),
		tagValueLocal(ev.Tags, "environment"),
		tagValueLocal(ev.Tags, "artifact"),
		tagValueLocal(ev.Tags, "intent"),
		tagValueLocal(ev.Tags, "run"),
		tagValueLocal(ev.Tags, "policy"),
		tagValueLocal(ev.Tags, "e"),
		eventIDHex(ev),
	)
}

func decodeContent(ev *gonostr.Event, out any) error {
	if ev == nil {
		return fmt.Errorf("projection event is nil")
	}
	if strings.TrimSpace(ev.Content) == "" {
		return fmt.Errorf("projection event kind %d content is required", ev.Kind)
	}
	if err := json.Unmarshal([]byte(ev.Content), out); err != nil {
		return fmt.Errorf("decode projection event kind %d content: %w", ev.Kind, err)
	}
	return nil
}

func baseDecoded(ev *gonostr.Event, family ProjectionFamily, entityKey string, updatedAt time.Time, tombstone bool, fill func(*DecodedProjectionEvent)) *DecodedProjectionEvent {
	if updatedAt.IsZero() {
		updatedAt = ev.CreatedAt.Time().UTC()
	}
	out := &DecodedProjectionEvent{Kind: eventKindInt(ev), DTag: firstNonBlank(tagValueLocal(ev.Tags, "d"), entityKey), Group: "", Tier: 0, Timestamp: updatedAt.UTC(), SourceID: eventIDHex(ev), Family: family, Tombstone: tombstone}
	if out.DTag == "" {
		out.DTag = entityKey
	}
	if fill != nil {
		fill(out)
	}
	return out
}

func continuityDTag(ev *gonostr.Event) string {
	return firstNonBlank(tagValueLocal(ev.Tags, "d"), tagValueLocal(ev.Tags, "service"), tagValueLocal(ev.Tags, "worker"), eventIDHex(ev))
}

func tagValueLocal(tags gonostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func decoderNotImplemented(kind int) DecodeFunc {
	return func(*gonostr.Event) (*DecodedProjectionEvent, error) {
		return nil, fmt.Errorf("decoder for kind %d not yet implemented", kind)
	}
}
