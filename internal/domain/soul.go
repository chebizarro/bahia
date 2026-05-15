package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// NewUUID generates a new UUID.
func NewUUID() uuid.UUID {
	return uuid.New()
}

// Soul Factory Event Kinds
const (
	KindSoulTemplate        = 31950
	KindAgentSoul           = 31951
	KindSoulDraft           = 31952
	KindProvisioningRequest = 5950
	KindProvisioningStatus  = 6950
	KindProvisioningResult  = 7950
	KindSoulAction          = 1950

	// Runtime-facing SoulFactory control kinds (Swarmstr/Metiq-compatible aliases).
	KindRuntimeCapability     = 30317
	KindRuntimeControlRequest = 38384
	KindRuntimeControlResult  = 38386

	// Legacy lifecycle result alias used by early callers. New lifecycle results use KindProvisioningResult.
	KindSoulActionLegacyResult = KindSoulAction + 1
)

// SoulStatus represents the lifecycle state of an agent soul.
type SoulStatus string

const (
	SoulStatusDraft        SoulStatus = "draft"
	SoulStatusProvisioning SoulStatus = "provisioning"
	SoulStatusActive       SoulStatus = "active"
	SoulStatusSuspended    SoulStatus = "suspended"
	SoulStatusRevoked      SoulStatus = "revoked"
)

// SoulTier represents the resource tier for an agent.
type SoulTier string

const (
	SoulTierLightweight SoulTier = "lightweight"
	SoulTierStandard    SoulTier = "standard"
	SoulTierHeavy       SoulTier = "heavy"
)

// ProvisioningStatus represents the state of a provisioning run.
type ProvisioningStatus string

const (
	ProvisioningStatusPending   ProvisioningStatus = "pending"
	ProvisioningStatusRunning   ProvisioningStatus = "running"
	ProvisioningStatusCompleted ProvisioningStatus = "completed"
	ProvisioningStatusFailed    ProvisioningStatus = "failed"
	ProvisioningStatusCancelled ProvisioningStatus = "cancelled"
)

// ProvisioningStep represents the ordered steps in provisioning.
type ProvisioningStep string

const (
	StepGenerate  ProvisioningStep = "generate"
	StepSignet    ProvisioningStep = "signet"
	StepAvatar    ProvisioningStep = "avatar"
	StepProfile   ProvisioningStep = "profile"
	StepQdrant    ProvisioningStep = "qdrant"
	StepMemory    ProvisioningStep = "memory"
	StepWorkspace ProvisioningStep = "workspace"
	StepDeploy    ProvisioningStep = "deploy"
)

// ProvisioningSteps is the ordered list of provisioning steps.
var ProvisioningSteps = []ProvisioningStep{
	StepGenerate,
	StepSignet,
	StepAvatar,
	StepProfile,
	StepQdrant,
	StepMemory,
	StepWorkspace,
	StepDeploy,
}

// StepStatusComplete is a soul-factory specific status (reuses StepStatus from rollout.go)
const StepStatusComplete StepStatus = "complete"

// SoulActionType represents lifecycle actions on souls.
type SoulActionType string

const (
	SoulActionSuspend    SoulActionType = "suspend"
	SoulActionResume     SoulActionType = "resume"
	SoulActionRevoke     SoulActionType = "revoke"
	SoulActionRegenerate SoulActionType = "regenerate"
	SoulActionRedeploy   SoulActionType = "redeploy"
	SoulActionUpdate     SoulActionType = "update"
)

const (
	SoulFactoryRuntimeControlSchema    = "soulfactory-runtime-control/v1"
	SoulFactoryRuntimeCapabilitySchema = "soulfactory-runtime-capability/v1"
)

// RuntimeTarget identifies a SoulFactory-managed runtime implementation.
type RuntimeTarget string

const (
	RuntimeTargetOpenClaw RuntimeTarget = "openclaw"
	RuntimeTargetMetiq    RuntimeTarget = "metiq"
)

// ToolGrant represents access to an MCP server with specific scopes.
type ToolGrant struct {
	MCPServer string   `json:"mcp_server"`
	Scopes    []string `json:"scopes"`
}

// SoulIdentitySpec captures editable identity fields in a kind:31952 draft.
type SoulIdentitySpec struct {
	Name    string   `json:"name,omitempty"`
	Purpose string   `json:"purpose,omitempty"`
	Tier    SoulTier `json:"tier,omitempty"`
	NIP05   string   `json:"nip05,omitempty"`
}

// SoulRuntimeSpec captures runtime targeting and observed binding metadata.
type SoulRuntimeSpec struct {
	Target         RuntimeTarget `json:"target,omitempty"`
	RuntimePubkey  string        `json:"runtime_pubkey,omitempty"`
	CapabilityRef  string        `json:"capability_ref,omitempty"`
	RuntimeBinding string        `json:"runtime_binding,omitempty"`
	State          string        `json:"state,omitempty"`
}

// SoulRelayPolicySpec captures read/write/control relay policy for a soul.
type SoulRelayPolicySpec struct {
	Read           []string `json:"read,omitempty"`
	Write          []string `json:"write,omitempty"`
	Control        []string `json:"control,omitempty"`
	NIP65Discovery bool     `json:"nip65_discovery,omitempty"`
}

// SoulPermissionSpec captures Nostr/tool approval policy for a soul.
type SoulPermissionSpec struct {
	AllowedKinds   []int       `json:"allowed_kinds,omitempty"`
	ToolGrants     []ToolGrant `json:"tool_grants,omitempty"`
	ApprovalPolicy string      `json:"approval_policy,omitempty"`
}

// SoulWorkspaceSpec captures repository/workspace fields for a soul.
type SoulWorkspaceSpec struct {
	Repo        string `json:"repo,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// SoulAssetRefs captures already-created avatar/voice references.
type SoulAssetRefs struct {
	AvatarRef string `json:"avatar_ref,omitempty"`
	VoiceRef  string `json:"voice_ref,omitempty"`
}

// SoulTemplate represents a prepared prompt template for generating agent souls.
type SoulTemplate struct {
	ID           uuid.UUID   `json:"id"`
	EventID      string      `json:"event_id"`
	Identifier   string      `json:"identifier"` // d-tag
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Tier         SoulTier    `json:"tier"`
	BasePrompt   string      `json:"base_prompt"`
	DefaultKinds []int       `json:"default_kinds"`
	DefaultTools []ToolGrant `json:"default_tools"`
	Tags         []string    `json:"tags"`
	Author       string      `json:"author"` // pubkey
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// AgentSoul represents a fully provisioned agent identity.
type AgentSoul struct {
	ID      uuid.UUID  `json:"id"`
	EventID string     `json:"event_id"`
	AgentID string     `json:"agent_id"` // d-tag
	Name    string     `json:"name"`
	Purpose string     `json:"purpose"`
	Tier    SoulTier   `json:"tier"`
	Status  SoulStatus `json:"status"`

	// Nostr identity
	NostrPubkey string `json:"nostr_pubkey"`
	NostrNpub   string `json:"nostr_npub"`
	BunkerURI   string `json:"bunker_uri"`
	NIP05       string `json:"nip05"`

	// Generated content
	SoulMD     string `json:"soul_md"`
	IdentityMD string `json:"identity_md"`

	// Permissions
	AllowedKinds []int       `json:"allowed_kinds"`
	ToolGrants   []ToolGrant `json:"tool_grants"`

	// Infrastructure references
	AvatarBlobHash   string `json:"avatar_blob_hash"`
	AvatarURL        string `json:"avatar_url"`
	SoulBlobHash     string `json:"soul_blob_hash"`
	QdrantCollection string `json:"qdrant_collection"`
	WorkspaceRepoURL string `json:"workspace_repo_url"`

	// Template reference
	TemplateID    *uuid.UUID `json:"template_id,omitempty"`
	TemplateRef   string     `json:"template_ref"`
	OriginalBrief string     `json:"original_brief"`

	// Bahia integration
	BahiaServiceID *uuid.UUID `json:"bahia_service_id,omitempty"`
	DeployStatus   string     `json:"deploy_status,omitempty"`

	// Draft/runtime reconciliation
	DraftRef         string              `json:"draft_ref,omitempty"`
	SpecHash         string              `json:"spec_hash,omitempty"`
	PreviousSpecHash string              `json:"previous_spec_hash,omitempty"`
	Runtime          SoulRuntimeSpec     `json:"runtime,omitempty"`
	RelayPolicy      SoulRelayPolicySpec `json:"relay_policy,omitempty"`
	PermissionSpec   SoulPermissionSpec  `json:"permissions,omitempty"`
	Workspace        SoulWorkspaceSpec   `json:"workspace,omitempty"`
	Assets           SoulAssetRefs       `json:"assets,omitempty"`
	CapabilityRef    string              `json:"capability_ref,omitempty"`
	LastResultRef    string              `json:"last_result_ref,omitempty"`

	// Lifecycle
	CreatedAt     time.Time  `json:"created_at"`
	ProvisionedAt *time.Time `json:"provisioned_at,omitempty"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// SoulDraftContent represents the JSON content of a kind:31952 draft event.
type SoulDraftContent struct {
	Brief        string      `json:"brief"`
	SoulMD       string      `json:"soul_md,omitempty"`
	IdentityMD   string      `json:"identity_md,omitempty"`
	AllowedKinds []int       `json:"allowed_kinds,omitempty"`
	ToolGrants   []ToolGrant `json:"tool_grants,omitempty"`
	AvatarPrompt string      `json:"avatar_prompt,omitempty"`
	GeneratedAt  *int64      `json:"generated_at,omitempty"`

	// Structured desired spec fields. Legacy fields above remain valid.
	Identity         SoulIdentitySpec    `json:"identity,omitempty"`
	Runtime          SoulRuntimeSpec     `json:"runtime,omitempty"`
	Permissions      SoulPermissionSpec  `json:"permissions,omitempty"`
	RelayPolicy      SoulRelayPolicySpec `json:"relay_policy,omitempty"`
	Workspace        SoulWorkspaceSpec   `json:"workspace,omitempty"`
	Assets           SoulAssetRefs       `json:"assets,omitempty"`
	SpecHash         string              `json:"spec_hash,omitempty"`
	PreviousSpecHash string              `json:"previous_spec_hash,omitempty"`
}

// SoulDraft represents a work-in-progress soul before provisioning.
type SoulDraft struct {
	ID          uuid.UUID `json:"id"`
	EventID     string    `json:"event_id"`
	AgentID     string    `json:"agent_id"` // d-tag
	Name        string    `json:"name"`
	Tier        SoulTier  `json:"tier"`
	TemplateRef string    `json:"template_ref"`

	// Content
	Content SoulDraftContent `json:"content"`

	// Metadata
	CreatedBy string    `json:"created_by"` // pubkey
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProvisioningRequest represents a request to provision a soul.
type ProvisioningRequest struct {
	EventID      string   `json:"event_id"`
	AgentID      string   `json:"agent_id"`
	Name         string   `json:"name"`
	Tier         SoulTier `json:"tier"`
	TemplateRef  string   `json:"template_ref"`
	DraftRef     string   `json:"draft_ref,omitempty"`
	DraftEventID string   `json:"draft_event_id,omitempty"`
	SpecHash     string   `json:"spec_hash,omitempty"`
	Brief        string   `json:"brief"`
	Requester    string   `json:"requester"` // pubkey
}

// ProvisioningRun tracks the multi-step provisioning workflow.
type ProvisioningRun struct {
	ID          uuid.UUID                `json:"id"`
	RequestID   string                   `json:"request_id"` // kind:5950 event ID
	SoulID      *uuid.UUID               `json:"soul_id,omitempty"`
	AgentID     string                   `json:"agent_id"`
	Status      ProvisioningStatus       `json:"status"`
	CurrentStep ProvisioningStep         `json:"current_step"`
	Steps       []ProvisioningStepResult `json:"steps"`
	Error       string                   `json:"error,omitempty"`

	// Request context
	RequesterPubkey string `json:"requester_pubkey"`
	DraftRef        string `json:"draft_ref,omitempty"`
	DraftEventID    string `json:"draft_event_id,omitempty"`
	SpecHash        string `json:"spec_hash,omitempty"`

	// Timing
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ProvisioningStepResult represents the result of a single provisioning step.
type ProvisioningStepResult struct {
	Name     ProvisioningStep       `json:"name"`
	Status   StepStatus             `json:"status"`
	Output   map[string]interface{} `json:"output,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Duration time.Duration          `json:"duration,omitempty"`
}

// SoulAction represents a lifecycle action on an existing soul.
type SoulAction struct {
	ID               uuid.UUID              `json:"id"`
	EventID          string                 `json:"event_id"`
	SoulRef          string                 `json:"soul_ref"`
	Action           SoulActionType         `json:"action"`
	Reason           string                 `json:"reason,omitempty"`
	Initiator        string                 `json:"initiator"`           // pubkey
	NewBrief         string                 `json:"new_brief,omitempty"` // for regenerate
	DraftRef         string                 `json:"draft_ref,omitempty"` // for update
	SpecHash         string                 `json:"spec_hash,omitempty"`
	PreviousSpecHash string                 `json:"previous_spec_hash,omitempty"`
	Patch            map[string]interface{} `json:"patch,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
}

// IsLifecycleResultKind reports whether kind is a supported lifecycle terminal result kind.
func IsLifecycleResultKind(kind int) bool {
	return kind == KindProvisioningResult || kind == KindSoulActionLegacyResult
}

// CanonicalLifecycleResultKind maps legacy lifecycle result aliases to the current result kind.
func CanonicalLifecycleResultKind(kind int) int {
	if kind == KindSoulActionLegacyResult {
		return KindProvisioningResult
	}
	return kind
}

// SoulGeneratorInput is the input to the LLM soul generator.
type SoulGeneratorInput struct {
	Template *SoulTemplate `json:"template,omitempty"`
	AgentID  string        `json:"agent_id"`
	Name     string        `json:"name"`
	Brief    string        `json:"brief"`
	Tier     SoulTier      `json:"tier"`
}

// SoulGeneratorOutput is the structured output from the LLM.
type SoulGeneratorOutput struct {
	SoulMD          string      `json:"soul_md"`
	IdentityMD      string      `json:"identity_md"`
	AllowedKinds    []int       `json:"allowed_kinds"`
	ToolGrants      []ToolGrant `json:"tool_grants"`
	AvatarPrompt    string      `json:"avatar_prompt"`
	PersonalityTags []string    `json:"personality_tags"`
}

// ParseDraftContent parses the JSON content of a draft event.
func ParseDraftContent(content string) (*SoulDraftContent, error) {
	var c SoulDraftContent
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ToJSON serializes draft content to JSON.
func (c *SoulDraftContent) ToJSON() (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
