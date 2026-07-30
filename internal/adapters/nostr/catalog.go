package nostr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const KindCatalogVersion = "2026-05-26.item8"

const (
	KindNIP65RelayList = kinds.NIP65RelayList

	KindLoomWorkerAdvertisement = kinds.LoomWorkerAdvertisement
	KindLoomJobResult           = kinds.LoomJobResult

	KindRelaySetDiscovery = kinds.RelaySetDiscovery

	KindBahiaIdentityDefinition = kinds.BahiaIdentityDefinition
	KindBahiaReplayCheckpoint   = kinds.BahiaReplayCheckpoint
	KindBahiaReadinessStatus    = kinds.BahiaReadinessStatus

	KindControlPlaneWorkerCleanupRequest = kinds.WorkerCleanupRequest

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

type DecodedHiveCI struct {
	WorkflowRun    *domain.HiveCIWorkflowRun    `json:"workflow_run,omitempty"`
	WorkflowResult *domain.HiveCIWorkflowResult `json:"workflow_result,omitempty"`
	QualityGate    *DecodedQualityGate          `json:"quality_gate,omitempty"`
}

type DecodedQualityGate struct {
	ID            string         `json:"id"`
	System        string         `json:"system"`
	RunID         string         `json:"run_id,omitempty"`
	Project       string         `json:"project,omitempty"`
	Status        string         `json:"status"`
	Result        string         `json:"result,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	BlocksMerge   bool           `json:"blocks_merge"`
	LogURL        string         `json:"log_url,omitempty"`
	SourceEventID string         `json:"source_event_id"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	ObservedAt    time.Time      `json:"observed_at"`
}

type DecodedLoom struct {
	JobStatus       *DecodedLoomJobStatus       `json:"job_status,omitempty"`
	JobResult       *DecodedLoomJobResult       `json:"job_result,omitempty"`
	JobCancellation *DecodedLoomJobCancellation `json:"job_cancellation,omitempty"`
}

type DecodedLoomJobStatus struct {
	JobID         string    `json:"job_id"`
	Status        string    `json:"status"`
	Message       string    `json:"message,omitempty"`
	Progress      *int      `json:"progress,omitempty"`
	WorkerPubkey  string    `json:"worker_pubkey,omitempty"`
	SourceEventID string    `json:"source_event_id"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type DecodedLoomJobResult struct {
	JobID         string    `json:"job_id"`
	Status        string    `json:"status"`
	Success       bool      `json:"success"`
	ExitCode      int       `json:"exit_code"`
	Duration      int       `json:"duration"`
	StdoutURL     string    `json:"stdout_url,omitempty"`
	StderrURL     string    `json:"stderr_url,omitempty"`
	ChangeToken   string    `json:"change_token,omitempty"`
	Error         string    `json:"error,omitempty"`
	WorkerPubkey  string    `json:"worker_pubkey,omitempty"`
	SourceEventID string    `json:"source_event_id"`
	FinishedAt    time.Time `json:"finished_at"`
}

type DecodedLoomJobCancellation struct {
	JobID           string    `json:"job_id"`
	WorkerPubkey    string    `json:"worker_pubkey,omitempty"`
	RequesterPubkey string    `json:"requester_pubkey,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	SourceEventID   string    `json:"source_event_id"`
	RequestedAt     time.Time `json:"requested_at"`
}

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
	c.decoders[KindLoomJobResult] = decodeLoomJobResultProjection
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

func decodeLoomJobStatusProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	jobID, err := requiredTagLocal(ev.Tags, "d")
	if err != nil {
		jobID, err = requiredTagLocal(ev.Tags, "e")
	}
	if err != nil {
		return nil, fmt.Errorf("decode Loom job status: %w", err)
	}
	status, err := requiredTagLocal(ev.Tags, "status")
	if err != nil {
		return nil, fmt.Errorf("decode Loom job status: %w", err)
	}
	if !isLoomJobStatus(status) {
		return nil, fmt.Errorf("decode Loom job status: invalid status %q", status)
	}
	var progress *int
	if raw := tagValueLocal(ev.Tags, "progress"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("decode Loom job status progress %q: %w", raw, err)
		}
		progress = &parsed
	}
	payload := DecodedLoomJobStatus{
		JobID:         jobID,
		Status:        status,
		Message:       strings.TrimSpace(ev.Content),
		Progress:      progress,
		WorkerPubkey:  eventPubKeyHex(ev),
		SourceEventID: eventIDHex(ev),
		UpdatedAt:     ev.CreatedAt.Time().UTC(),
	}
	return baseDecoded(ev, FamilyLoom, jobID, payload.UpdatedAt, false, func(out *DecodedProjectionEvent) {
		out.Group = "loom_live"
		out.Tier = 3
		out.Loom = &DecodedLoom{JobStatus: &payload}
	}), nil
}

func decodeLoomJobResultProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	jobID, err := requiredTagLocal(ev.Tags, "e")
	if err != nil {
		return nil, fmt.Errorf("decode Loom job result: %w", err)
	}
	success, err := parseBoolTag(ev.Tags, "success")
	if err != nil {
		return nil, fmt.Errorf("decode Loom job result: %w", err)
	}
	exitCode, err := parseIntTag(ev.Tags, "exit_code")
	if err != nil {
		return nil, fmt.Errorf("decode Loom job result: %w", err)
	}
	duration, err := parseIntTag(ev.Tags, "duration")
	if err != nil {
		return nil, fmt.Errorf("decode Loom job result: %w", err)
	}
	status := "failed"
	if success {
		status = "completed"
	}
	payload := DecodedLoomJobResult{
		JobID:         jobID,
		Status:        status,
		Success:       success,
		ExitCode:      exitCode,
		Duration:      duration,
		StdoutURL:     tagValueLocal(ev.Tags, "stdout"),
		StderrURL:     tagValueLocal(ev.Tags, "stderr"),
		ChangeToken:   tagValueLocal(ev.Tags, "change"),
		Error:         tagValueLocal(ev.Tags, "error"),
		WorkerPubkey:  eventPubKeyHex(ev),
		SourceEventID: eventIDHex(ev),
		FinishedAt:    ev.CreatedAt.Time().UTC(),
	}
	return baseDecoded(ev, FamilyLoom, jobID, payload.FinishedAt, false, func(out *DecodedProjectionEvent) {
		out.Group = "loom_live"
		out.Tier = 3
		out.Loom = &DecodedLoom{JobResult: &payload}
	}), nil
}

func decodeLoomJobCancellationProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	jobID, err := requiredTagLocal(ev.Tags, "e")
	if err != nil {
		return nil, fmt.Errorf("decode Loom job cancellation: %w", err)
	}
	payload := DecodedLoomJobCancellation{
		JobID:           jobID,
		WorkerPubkey:    tagValueLocal(ev.Tags, "p"),
		RequesterPubkey: eventPubKeyHex(ev),
		Reason:          strings.TrimSpace(ev.Content),
		SourceEventID:   eventIDHex(ev),
		RequestedAt:     ev.CreatedAt.Time().UTC(),
	}
	return baseDecoded(ev, FamilyLoom, jobID, payload.RequestedAt, false, func(out *DecodedProjectionEvent) {
		out.Group = "loom_live"
		out.Tier = 3
		out.Loom = &DecodedLoom{JobCancellation: &payload}
	}), nil
}

func decodeHiveCIWorkflowRunProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	repoCoordinate, err := requiredTagLocal(ev.Tags, "a")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow run: %w", err)
	}
	commit, err := requiredTagLocal(ev.Tags, "commit")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow run: %w", err)
	}
	branch, err := requiredTagLocal(ev.Tags, "branch")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow run: %w", err)
	}
	workflow, err := requiredTagLocal(ev.Tags, "workflow")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow run: %w", err)
	}
	triggeredBy, err := requiredTagLocal(ev.Tags, "triggered-by")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow run: %w", err)
	}
	publisher, err := requiredTagLocal(ev.Tags, "publisher")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow run: %w", err)
	}
	run := domain.HiveCIWorkflowRun{
		RunEventID:      eventIDHex(ev),
		RepoCoordinate:  repoCoordinate,
		CommitSHA:       commit,
		Branch:          branch,
		WorkflowPath:    workflow,
		TriggerType:     tagValueLocal(ev.Tags, "trigger"),
		TriggeredBy:     triggeredBy,
		PublisherPubkey: publisher,
		EventCreatedAt:  ev.CreatedAt.Time().UTC(),
		ProcessingState: domain.HiveCIProcessingStatePendingResult,
	}
	gate := &DecodedQualityGate{
		ID:            "hiveci:" + run.RunEventID,
		System:        "hiveci",
		RunID:         run.RunEventID,
		Project:       repoCoordinate,
		Status:        "running",
		SourceEventID: run.RunEventID,
		ObservedAt:    run.EventCreatedAt,
		Metadata: map[string]any{
			"commit_sha":    commit,
			"branch":        branch,
			"workflow_path": workflow,
			"triggered_by":  triggeredBy,
		},
	}
	return baseDecoded(ev, FamilyHiveCI, run.RunEventID, run.EventCreatedAt, false, func(out *DecodedProjectionEvent) {
		out.Group = "hive_ci_live"
		out.Tier = 3
		out.HiveCI = &DecodedHiveCI{WorkflowRun: &run, QualityGate: gate}
	}), nil
}

func decodeHiveCIWorkflowResultProjection(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
	runEventID, err := requiredTagLocal(ev.Tags, "e")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow result: %w", err)
	}
	logURL, err := requiredTagLocal(ev.Tags, "log_url")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow result: %w", err)
	}
	status, err := requiredTagLocal(ev.Tags, "status")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow result: %w", err)
	}
	if status != "success" && status != "failure" {
		return nil, fmt.Errorf("decode HiveCI workflow result: invalid status %q", status)
	}
	exitCode, err := parseIntTag(ev.Tags, "exit_code")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow result: %w", err)
	}
	duration, err := parseIntTag(ev.Tags, "duration")
	if err != nil {
		return nil, fmt.Errorf("decode HiveCI workflow result: %w", err)
	}
	type workflowResultContent struct {
		ImageRepo   string `json:"image_repo"`
		ImageTag    string `json:"image_tag"`
		ImageDigest string `json:"image_digest"`
	}
	var content workflowResultContent
	if strings.TrimSpace(ev.Content) != "" {
		if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
			return nil, fmt.Errorf("decode HiveCI workflow result content: %w", err)
		}
	}
	imageRepo := firstNonBlank(tagValueLocal(ev.Tags, "image_repo"), content.ImageRepo)
	imageTag := firstNonBlank(tagValueLocal(ev.Tags, "image_tag"), content.ImageTag)
	imageDigest := firstNonBlank(tagValueLocal(ev.Tags, "image_digest"), content.ImageDigest)
	result := domain.HiveCIWorkflowResult{
		ResultEventID:   eventIDHex(ev),
		RunEventID:      runEventID,
		Status:          status,
		ExitCode:        exitCode,
		DurationSeconds: duration,
		LogURL:          logURL,
		Error:           tagValueLocal(ev.Tags, "error"),
		ImageRepo:       imageRepo,
		ImageTag:        imageTag,
		ImageDigest:     imageDigest,
		PublisherPubkey: eventPubKeyHex(ev),
		EventCreatedAt:  ev.CreatedAt.Time().UTC(),
		ProcessingState: domain.HiveCIProcessingStatePendingResult,
	}
	gateResult := "fail"
	if status == "success" {
		gateResult = "pass"
	}
	reason := firstNonBlank(result.Error, fmt.Sprintf("HiveCI workflow result %s", status))
	gate := &DecodedQualityGate{
		ID:            "hiveci:" + result.RunEventID,
		System:        "hiveci",
		RunID:         result.RunEventID,
		Status:        "completed",
		Result:        gateResult,
		Reason:        reason,
		BlocksMerge:   gateResult == "fail",
		LogURL:        logURL,
		SourceEventID: result.ResultEventID,
		ObservedAt:    result.EventCreatedAt,
		Metadata: map[string]any{
			"exit_code":        exitCode,
			"duration_seconds": duration,
			"image_repo":       imageRepo,
			"image_tag":        imageTag,
			"image_digest":     imageDigest,
		},
	}
	return baseDecoded(ev, FamilyHiveCI, result.RunEventID, result.EventCreatedAt, false, func(out *DecodedProjectionEvent) {
		out.Group = "hive_ci_live"
		out.Tier = 3
		out.HiveCI = &DecodedHiveCI{WorkflowResult: &result, QualityGate: gate}
	}), nil
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

func requiredTagLocal(tags gonostr.Tags, key string) (string, error) {
	value := strings.TrimSpace(tagValueLocal(tags, key))
	if value == "" {
		return "", fmt.Errorf("missing required tag %q", key)
	}
	return value, nil
}

func parseIntTag(tags gonostr.Tags, key string) (int, error) {
	raw, err := requiredTagLocal(tags, key)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("tag %q must be an integer: %w", key, err)
	}
	return parsed, nil
}

func parseBoolTag(tags gonostr.Tags, key string) (bool, error) {
	raw, err := requiredTagLocal(tags, key)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("tag %q must be true or false", key)
	}
}

func isLoomJobStatus(status string) bool {
	switch status {
	case "queued", "running", "completed", "failed", "cancelled", "timeout":
		return true
	default:
		return false
	}
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
