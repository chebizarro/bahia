package nostr

import (
	"fmt"
	"sort"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
)

const KindCatalogVersion = "2026-05-23.item4"

const (
	KindNIP65RelayList = 10002

	KindHiveCIWorkflowRun    = 5401
	KindHiveCIWorkflowResult = 5402

	KindLoomWorkerAdvertisement = 10100
	KindLoomJobStatusUpdate     = 30100
	KindLoomJobResult           = 5101
	KindLoomJobCancellation     = 5102

	KindRelaySetDiscovery = 30002

	KindBahiaIdentityDefinition = 31410
	KindBahiaReplayCheckpoint   = 31411
	KindBahiaReadinessStatus    = 30360

	KindControlPlaneDeployRequest            = 5961
	KindControlPlaneRollbackRequest          = 5962
	KindControlPlaneServiceAction            = 5963
	KindControlPlaneServiceCreate            = 5964
	KindControlPlaneEnvironmentCreate        = 5965
	KindControlPlaneDeploymentApproval       = 5966
	KindControlPlaneObservationSubmit        = 5967
	KindControlPlaneDriftRemediate           = 5968
	KindControlPlaneLLMRouteCreate           = 5971
	KindControlPlaneLLMReleaseRegister       = 5972
	KindControlPlaneLLMDeployRequest         = 5973
	KindControlPlaneLLMDeploymentApproval    = 5974
	KindControlPlaneLLMRollbackRequest       = 5975
	KindControlPlaneToolProvisionRequest     = 5976
	KindControlPlaneToolApprovalRequest      = 5977
	KindControlPlaneAdoptionScanRequest      = 5978
	KindControlPlaneAdoptionImportRequest    = 5979
	KindControlPlaneServiceUpdate            = 5981
	KindControlPlaneServiceDelete            = 5982
	KindControlPlaneEnvironmentUpdate        = 5983
	KindControlPlaneEnvironmentDelete        = 5984
	KindControlPlaneArtifactRegister         = 5985
	KindControlPlanePolicyCreate             = 5986
	KindControlPlanePolicyUpdate             = 5987
	KindControlPlanePolicyDelete             = 5988
	KindControlPlanePolicyEvaluate           = 5989
	KindControlPlanePackageRepositoryApply   = 5991
	KindControlPlanePackageRepositoryDelete  = 5992
	KindControlPlanePackagePublishIntent     = 5993
	KindControlPlanePackagePromotionRequest  = 5994
	KindControlPlanePackageYankRequest       = 5995
	KindControlPlanePackageDriftDetect       = 5996
	KindControlPlaneWorkerCordonRequest      = 5997
	KindControlPlaneWorkerUncordonRequest    = 5998
	KindControlPlaneWorkerDrainRequest       = 5999
	KindControlPlaneWorkerUndrainRequest     = 6000
	KindControlPlaneWorkerMaintenanceEnter   = 6001
	KindControlPlaneWorkerMaintenanceExit    = 6002
	KindControlPlaneWorkerLabelsUpdate       = 6003
	KindControlPlaneWorkerPolicyApplyRequest = 6004
	KindControlPlaneWorkloadPinRequest       = 6005

	KindMLRecipeRunRequest            = 38390
	KindMLInferenceDeployRequest      = 38391
	KindMLInferenceDeploymentApproval = 38392
	KindMLInferenceRollbackRequest    = 38393
	KindMLModelImportRequest          = 38394
	KindMLRecipeRunResult             = 38395
	KindMLInferenceDeployResult       = 38396
	KindMLInferenceApprovalResult     = 38397
	KindMLInferenceRollbackResult     = 38398
	KindMLModelImportResult           = 38399

	KindControlPlaneDeploymentStatus    = 6961
	KindControlPlaneServiceStatus       = 6962
	KindControlPlaneActionStatus        = 6963
	KindControlPlaneLLMDeploymentStatus = 6973
	KindControlPlaneToolProvisionStatus = 6976
	KindControlPlaneAdoptionStatus      = 6978
	KindControlPlanePackageStatus       = 6991
	KindControlPlaneWorkerStatus        = 6997

	KindControlPlaneDeploymentResult         = 7961
	KindControlPlaneActionResult             = 7962
	KindControlPlaneServiceCreateResult      = 7963
	KindControlPlaneEnvironmentCreateResult  = 7964
	KindControlPlaneObservationResult        = 7965
	KindControlPlaneRemediationResult        = 7966
	KindControlPlaneLLMRouteCreateResult     = 7971
	KindControlPlaneLLMReleaseRegisterResult = 7972
	KindControlPlaneLLMDeploymentResult      = 7973
	KindControlPlaneToolProvisionResult      = 7976
	KindControlPlaneToolApprovalResponse     = 7977
	KindControlPlaneAdoptionScanResult       = 7978
	KindControlPlaneAdoptionImportResult     = 7979
	KindControlPlanePackageResult            = 7991
	KindControlPlanePackageDriftEvent        = 7992
	KindControlPlaneWorkerResult             = 7997

	KindFIPSOverlayAdvert = 37195
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

type DecodedService struct{}
type DecodedEnvironment struct{}
type DecodedWorker struct{}
type DecodedBuild struct{}
type DecodedArtifact struct{}
type DecodedIntent struct{}
type DecodedRun struct{}
type DecodedPolicy struct{}
type DecodedContinuity struct{}
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
		{Name: "system_snapshot", Kinds: []int{KindRelaySetDiscovery, KindNIP65RelayList, KindSystemDiscovery, KindBahiaIdentityDefinition, KindBahiaReplayCheckpoint, KindBahiaReadinessStatus}, Tier: 0, Snapshot: true, Required: true},
		{Name: "continuity_snapshot", Kinds: []int{KindContinuityProfile, KindFailoverPolicy, KindStandbyNodeDefinition, KindReplicationPolicy, KindRecoveryWorkflow}, Tier: 1, Snapshot: true, Required: true},
		{Name: "worker_snapshot", Kinds: []int{KindWorkerState, KindWorkerAssignmentState, KindWorkerDrainStatus, KindWorkerEligibilityPreview, KindLegacyWorkerState, KindLegacyWorkerAssignmentState, KindLegacyWorkerDrainStatus, KindLegacyWorkerEligibilityPreview}, Tier: 1, Snapshot: true, Required: true},
		{Name: "continuity_live", Kinds: []int{KindHeartbeatObservation, KindContinuityStatus, KindDegradedModeActivation, KindRecoveryProgress, KindFailoverRequest, KindRecoveryRequest}, Tier: 1, Snapshot: false, Required: true},
		{Name: "core_registry_snapshot", Kinds: []int{KindServiceState, KindServiceRegistry, KindEnvironmentRegistry, KindArtifactRegistry, KindDeploymentIntentRegistry, KindDeploymentRunRegistry, KindBuildRegistry, KindPolicyRegistry}, Tier: 2, Snapshot: true, Required: true},
		{Name: "core_control_plane_live", Kinds: []int{KindCmdBuildRegister, KindCmdArtifactRegister, KindCmdIntentCreate, KindCmdIntentApprove, KindCmdIntentReject, KindCmdRollbackRequest, KindControlPlaneDeployRequest, KindControlPlaneRollbackRequest, KindControlPlaneServiceAction, KindControlPlaneServiceCreate, KindControlPlaneEnvironmentCreate, KindControlPlaneDeploymentApproval, KindControlPlaneObservationSubmit, KindControlPlaneDriftRemediate, KindControlPlaneServiceUpdate, KindControlPlaneServiceDelete, KindControlPlaneEnvironmentUpdate, KindControlPlaneEnvironmentDelete, KindControlPlaneArtifactRegister, KindControlPlanePolicyCreate, KindControlPlanePolicyUpdate, KindControlPlanePolicyDelete, KindControlPlanePolicyEvaluate, KindControlPlaneDeploymentStatus, KindControlPlaneServiceStatus, KindControlPlaneActionStatus, KindControlPlaneDeploymentResult, KindControlPlaneActionResult, KindControlPlaneServiceCreateResult, KindControlPlaneEnvironmentCreateResult, KindControlPlaneObservationResult, KindControlPlaneRemediationResult}, Tier: 2, Snapshot: false, Required: true},
		{Name: "loom_live", Kinds: []int{KindLoomWorkerAdvertisement, KindLoomJobStatusUpdate, KindLoomJobResult, KindLoomJobCancellation}, Tier: 3, Snapshot: false, Required: false},
		{Name: "hive_ci_live", Kinds: []int{KindHiveCIWorkflowRun, KindHiveCIWorkflowResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "llm_snapshot", Kinds: []int{KindLLMRouteRegistry, KindLLMRouteState}, Tier: 3, Snapshot: true, Required: false},
		{Name: "llm_live", Kinds: []int{KindControlPlaneLLMRouteCreate, KindControlPlaneLLMReleaseRegister, KindControlPlaneLLMDeployRequest, KindControlPlaneLLMDeploymentApproval, KindControlPlaneLLMRollbackRequest, KindControlPlaneLLMDeploymentStatus, KindControlPlaneLLMRouteCreateResult, KindControlPlaneLLMReleaseRegisterResult, KindControlPlaneLLMDeploymentResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "ml_snapshot", Kinds: []int{KindMLModelRegistry, KindMLModelVersionRegistry, KindMLDatasetRegistry, KindMLRecipeRegistry, KindMLRecipeRunState, KindMLInferenceEndpointRegistry, KindMLInferenceEndpointState, KindMLEvaluationExperimentState, KindMLArtifactProvenanceGraph, KindMLRuntimeCapabilityProfile}, Tier: 3, Snapshot: true, Required: false},
		{Name: "ml_live", Kinds: []int{KindMLRecipeRunRequest, KindMLInferenceDeployRequest, KindMLInferenceDeploymentApproval, KindMLInferenceRollbackRequest, KindMLModelImportRequest, KindMLRecipeRunResult, KindMLInferenceDeployResult, KindMLInferenceApprovalResult, KindMLInferenceRollbackResult, KindMLModelImportResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "package_snapshot", Kinds: []int{KindPackageRepositoryRegistry, KindPackageArtifactRegistry, KindPackagePromotionRegistry}, Tier: 3, Snapshot: true, Required: false},
		{Name: "package_live", Kinds: []int{KindControlPlanePackageRepositoryApply, KindControlPlanePackageRepositoryDelete, KindControlPlanePackagePublishIntent, KindControlPlanePackagePromotionRequest, KindControlPlanePackageYankRequest, KindControlPlanePackageDriftDetect, KindControlPlanePackageStatus, KindControlPlanePackageResult, KindControlPlanePackageDriftEvent}, Tier: 3, Snapshot: false, Required: false},
		{Name: "backup_snapshot", Kinds: []int{KindBackupDefinitionRegistry, KindBackupPolicyRegistry, KindBackupRepositoryRegistry, KindBackupRetentionRegistry, KindBackupRecipeRegistry, KindBackupRunState, KindBackupVerificationState, KindBackupRestoreState, KindBackupRuntimeObservationState}, Tier: 3, Snapshot: true, Required: false},
		{Name: "dns_snapshot", Kinds: []int{KindDNSZoneState, KindDNSEndpointState, KindDNSPolicyState, KindDNSBackendState}, Tier: 3, Snapshot: true, Required: false},
		{Name: "assistant_snapshot", Kinds: []int{KindAssistantSession}, Tier: 3, Snapshot: true, Required: false},
		{Name: "assistant_live", Kinds: []int{KindAssistantPromptRequest, KindAssistantApproval, KindAssistantStatus, KindAssistantResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "tool_live", Kinds: []int{KindControlPlaneToolProvisionRequest, KindControlPlaneToolApprovalRequest, KindControlPlaneToolProvisionStatus, KindControlPlaneToolProvisionResult, KindControlPlaneToolApprovalResponse}, Tier: 3, Snapshot: false, Required: false},
		{Name: "adoption_live", Kinds: []int{KindControlPlaneAdoptionScanRequest, KindControlPlaneAdoptionImportRequest, KindControlPlaneAdoptionStatus, KindControlPlaneAdoptionScanResult, KindControlPlaneAdoptionImportResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "worker_control_live", Kinds: []int{KindControlPlaneWorkerCordonRequest, KindControlPlaneWorkerUncordonRequest, KindControlPlaneWorkerDrainRequest, KindControlPlaneWorkerUndrainRequest, KindControlPlaneWorkerMaintenanceEnter, KindControlPlaneWorkerMaintenanceExit, KindControlPlaneWorkerLabelsUpdate, KindControlPlaneWorkerPolicyApplyRequest, KindControlPlaneWorkloadPinRequest, KindControlPlaneWorkerStatus, KindControlPlaneWorkerResult}, Tier: 3, Snapshot: false, Required: false},
		{Name: "fips_snapshot", Kinds: []int{KindFIPSOverlayAdvert}, Tier: 3, Snapshot: true, Required: false},
		{Name: "audit_live", Kinds: []int{KindBuildRegistered, KindArtifactRegistered, KindDeploymentCreated, KindDeploymentComplete, KindDriftDetected, KindObservation, KindServiceRegistryAudit, KindEnvironmentRegistryAudit, KindStateChangedAudit, KindRuntimeActionAudit, KindReconcileAudit, KindAdoptionAudit, KindDeploymentApprovalAudit, KindDeploymentRunAudit, KindLLMRouteRegistryAudit, KindLLMReleaseRegisteredAudit, KindLLMDeploymentAudit, KindLLMRunAudit, KindLLMRouteStateAudit, KindLLMGatewayAudit, KindDNSZoneSyncedAudit, KindDNSRecordChangedAudit, KindDNSDriftDetectedAudit, KindDNSEndpointRegisteredAudit, KindDNSEndpointDeregisteredAudit}, Tier: 3, Snapshot: false, Required: false},
	}

	catalog := &KindCatalog{
		Version:  KindCatalogVersion,
		Groups:   groups,
		decoders: make(map[int]DecodeFunc),
	}
	for _, kind := range catalog.AllKinds() {
		catalog.decoders[kind] = decoderNotImplemented(kind)
	}
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

func decoderNotImplemented(kind int) DecodeFunc {
	return func(*gonostr.Event) (*DecodedProjectionEvent, error) {
		return nil, fmt.Errorf("decoder for kind %d not yet implemented", kind)
	}
}
