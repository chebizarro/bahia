package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

// SoulFactory event tag names. Keeping them in one file prevents reactor,
// lifecycle, provisioning, and clients from silently drifting on wire shape.
const (
	tagAction             = "action"
	tagAgentID            = "agent-id"
	tagAgentPubkey        = "agent-pubkey"
	tagAllowedKind        = "allowed-kind"
	tagFleetConfig        = "fleet-config"
	tagFleetRevision      = "fleet-revision"
	tagApprovalPolicy     = "approval-policy"
	tagAvatar             = "avatar"
	tagAvatarRef          = "avatar-ref"
	tagBunker             = "bunker"
	tagCapability         = "capability"
	tagDeployStatus       = "deploy-status"
	tagDraft              = "draft"
	tagDraftEvent         = "draft-event"
	tagEvent              = "e"
	tagIdempotencyKey     = "idempotency-key"
	tagMethod             = "method"
	tagName               = "name"
	tagNIP05              = "nip05"
	tagNpub               = "npub"
	tagParameterizedD     = "d"
	tagPreviousDraft      = "previous-draft"
	tagPreviousDraftEvent = "previous-draft-event"
	tagPreviousSpecHash   = "previous-spec-hash"
	tagProgress           = "progress"
	tagPubkey             = "p"
	tagPurpose            = "purpose"
	tagQdrant             = "qdrant"
	tagReason             = "reason"
	tagRelayControl       = "relay-control"
	tagRelayRead          = "relay-read"
	tagRelayWrite         = "relay-write"
	tagRequestKind        = "request-kind"
	tagRuntime            = "runtime"
	tagRuntimeBinding     = "runtime-binding"
	tagRuntimePubkey      = "runtime-pubkey"
	tagRuntimeState       = "runtime-state"
	tagSchema             = "schema"
	tagService            = "service"
	tagSoul               = "soul"
	tagSoulBlob           = "soul-blob"
	tagSpecHash           = "spec-hash"
	tagStatus             = "status"
	tagStep               = "step"
	tagTemplate           = "template"
	tagTier               = "tier"
	tagTool               = "tool"
	tagVoiceRef           = "voice-ref"
	tagWorkspace          = "workspace"
)

// ParseProvisioningRequestEvent extracts a domain provisioning request from a
// kind:5950 event. It accepts both the legacy tag + {brief} shape and additive
// draft/spec-hash fields introduced for runtime-aware provisioning.
func ParseProvisioningRequestEvent(event *nostr.Event) (*domain.ProvisioningRequest, error) {
	if event == nil {
		return nil, fmt.Errorf("nil provisioning event")
	}
	req := &domain.ProvisioningRequest{EventID: event.ID.Hex(), Requester: event.PubKey.Hex()}

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case tagAgentID:
			req.AgentID = strings.TrimSpace(tag[1])
		case tagName:
			req.Name = strings.TrimSpace(tag[1])
		case tagTier:
			req.Tier = domain.SoulTier(strings.TrimSpace(tag[1]))
		case tagTemplate:
			req.TemplateRef = strings.TrimSpace(tag[1])
		case tagDraft:
			req.DraftRef = strings.TrimSpace(tag[1])
		case tagDraftEvent:
			req.DraftEventID = strings.TrimSpace(tag[1])
		case tagSpecHash:
			req.SpecHash = strings.TrimSpace(tag[1])
		case tagRuntime:
			req.Runtime.Target = domain.RuntimeTarget(strings.TrimSpace(tag[1]))
		case tagRuntimePubkey:
			req.Runtime.RuntimePubkey = strings.TrimSpace(tag[1])
		case tagCapability:
			req.Runtime.CapabilityRef = strings.TrimSpace(tag[1])
		case tagEvent:
			if isDraftEventTag(tag) && req.DraftEventID == "" {
				req.DraftEventID = strings.TrimSpace(tag[1])
			}
		}
	}

	if strings.TrimSpace(event.Content) != "" {
		var content struct {
			AgentID      string                 `json:"agent_id"`
			Name         string                 `json:"name"`
			Tier         domain.SoulTier        `json:"tier"`
			TemplateRef  string                 `json:"template_ref"`
			DraftRef     string                 `json:"draft_ref"`
			DraftEventID string                 `json:"draft_event_id"`
			SpecHash     string                 `json:"spec_hash"`
			Runtime      domain.SoulRuntimeSpec `json:"runtime"`
			Brief        string                 `json:"brief"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
			return nil, fmt.Errorf("parse provisioning content: %w", err)
		}
		req.AgentID = firstNonEmpty(req.AgentID, content.AgentID)
		req.Name = firstNonEmpty(req.Name, content.Name)
		if req.Tier == "" {
			req.Tier = content.Tier
		}
		req.TemplateRef = firstNonEmpty(req.TemplateRef, content.TemplateRef)
		req.DraftRef = firstNonEmpty(req.DraftRef, content.DraftRef)
		req.DraftEventID = firstNonEmpty(req.DraftEventID, content.DraftEventID)
		req.SpecHash = firstNonEmpty(req.SpecHash, content.SpecHash)
		if req.Runtime.Target == "" {
			req.Runtime.Target = content.Runtime.Target
		}
		req.Runtime.RuntimePubkey = firstNonEmpty(req.Runtime.RuntimePubkey, content.Runtime.RuntimePubkey)
		req.Runtime.CapabilityRef = firstNonEmpty(req.Runtime.CapabilityRef, content.Runtime.CapabilityRef)
		req.Runtime.RuntimeBinding = firstNonEmpty(req.Runtime.RuntimeBinding, content.Runtime.RuntimeBinding)
		req.Runtime.State = firstNonEmpty(req.Runtime.State, content.Runtime.State)
		req.Brief = strings.TrimSpace(content.Brief)
	}

	if req.AgentID == "" {
		return nil, fmt.Errorf("missing agent-id tag")
	}
	if req.Brief == "" && req.DraftRef == "" && req.DraftEventID == "" && req.TemplateRef == "" {
		return nil, fmt.Errorf("must provide brief, draft, or template")
	}
	if req.Tier == "" {
		req.Tier = domain.SoulTierStandard
	}
	return req, nil
}

// ParseSoulActionEvent extracts a lifecycle/customization action from a
// kind:1950 event. It accepts legacy {brief} regenerate content and the newer
// structured {new_brief, draft_ref, spec_hash, previous_spec_hash, patch} shape.
func ParseSoulActionEvent(event *nostr.Event) (*domain.SoulAction, error) {
	if event == nil {
		return nil, fmt.Errorf("nil soul action event")
	}
	action := &domain.SoulAction{
		ID:        domain.NewUUID(),
		EventID:   event.ID.Hex(),
		Initiator: event.PubKey.Hex(),
		CreatedAt: event.CreatedAt.Time(),
	}

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case tagSoul:
			action.SoulRef = strings.TrimSpace(tag[1])
		case tagAction:
			action.Action = domain.SoulActionType(strings.TrimSpace(tag[1]))
		case tagReason:
			action.Reason = strings.TrimSpace(tag[1])
		case tagDraft:
			action.DraftRef = strings.TrimSpace(tag[1])
		case tagDraftEvent:
			action.DraftEventID = strings.TrimSpace(tag[1])
		case tagEvent:
			if isDraftEventTag(tag) && action.DraftEventID == "" {
				action.DraftEventID = strings.TrimSpace(tag[1])
			}
		case tagSpecHash:
			action.SpecHash = strings.TrimSpace(tag[1])
		case tagPreviousSpecHash:
			action.PreviousSpecHash = strings.TrimSpace(tag[1])
		}
	}

	if strings.TrimSpace(event.Content) != "" {
		var content struct {
			Brief            string                 `json:"brief"`
			NewBrief         string                 `json:"new_brief"`
			Reason           string                 `json:"reason"`
			DraftRef         string                 `json:"draft_ref"`
			DraftEventID     string                 `json:"draft_event_id"`
			SpecHash         string                 `json:"spec_hash"`
			PreviousSpecHash string                 `json:"previous_spec_hash"`
			Patch            map[string]interface{} `json:"patch"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
			return nil, fmt.Errorf("parse action content: %w", err)
		}
		action.NewBrief = firstNonEmpty(content.NewBrief, content.Brief, action.NewBrief)
		action.Reason = firstNonEmpty(action.Reason, content.Reason)
		action.DraftRef = firstNonEmpty(action.DraftRef, content.DraftRef)
		action.DraftEventID = firstNonEmpty(action.DraftEventID, content.DraftEventID)
		action.SpecHash = firstNonEmpty(action.SpecHash, content.SpecHash)
		action.PreviousSpecHash = firstNonEmpty(action.PreviousSpecHash, content.PreviousSpecHash)
		if len(content.Patch) > 0 {
			action.Patch = content.Patch
		}
	}

	if action.SoulRef == "" {
		return nil, fmt.Errorf("missing soul reference")
	}
	if action.Action == "" {
		return nil, fmt.Errorf("missing action type")
	}
	return action, nil
}

// ParseAgentSoulEvent converts a kind:31951 read-model event into an AgentSoul.
func ParseAgentSoulEvent(event *nostr.Event) *domain.AgentSoul {
	if event == nil {
		return nil
	}
	soul := &domain.AgentSoul{EventID: event.ID.Hex(), SoulMD: event.Content, CreatedAt: event.CreatedAt.Time()}

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		value := strings.TrimSpace(tag[1])
		switch tag[0] {
		case tagParameterizedD:
			soul.AgentID = value
		case tagName:
			soul.Name = value
		case tagPurpose:
			soul.Purpose = value
		case tagTier:
			soul.Tier = domain.SoulTier(value)
		case tagStatus:
			soul.Status = domain.SoulStatus(value)
		case tagPubkey:
			if len(tag) > 2 && tag[2] == "agent" {
				soul.NostrPubkey = value
			}
		case tagNpub:
			soul.NostrNpub = value
		case tagNIP05:
			soul.NIP05 = value
		case tagBunker:
			soul.BunkerURI = value
		case tagAvatar:
			soul.AvatarURL = value
		case tagSoulBlob:
			soul.SoulBlobHash = value
		case tagQdrant:
			soul.QdrantCollection = value
		case tagWorkspace:
			soul.WorkspaceRepoURL = value
			soul.Workspace.Repo = firstNonEmpty(soul.Workspace.Repo, value)
		case tagTemplate:
			soul.TemplateRef = value
		case tagDeployStatus:
			soul.DeployStatus = value
		case tagService:
			if id, err := uuid.Parse(value); err == nil {
				soul.BahiaServiceID = &id
			}
		case tagAllowedKind:
			if kind, err := strconv.Atoi(value); err == nil {
				soul.AllowedKinds = append(soul.AllowedKinds, kind)
			}
		case tagTool:
			grant := domain.ToolGrant{MCPServer: value}
			if len(tag) > 2 {
				grant.Scopes = tag[2:]
			}
			soul.ToolGrants = append(soul.ToolGrants, grant)
		case tagDraft:
			soul.DraftRef = value
		case tagDraftEvent:
			soul.DraftEventID = value
		case tagPreviousDraft:
			soul.PreviousDraftRef = value
		case tagPreviousDraftEvent:
			soul.PreviousDraftEventID = value
		case tagSpecHash:
			soul.SpecHash = value
		case tagPreviousSpecHash:
			soul.PreviousSpecHash = value
		case tagFleetRevision:
			soul.AppliedFleetConfigRevision = value
		case tagRuntime:
			soul.Runtime.Target = domain.RuntimeTarget(value)
		case tagRuntimePubkey:
			soul.Runtime.RuntimePubkey = value
		case tagRuntimeBinding:
			soul.Runtime.RuntimeBinding = value
		case tagRuntimeState:
			soul.Runtime.State = value
		case "provider":
			soul.Runtime.Provider = value
		case "model":
			soul.Runtime.Model = value
		case "run-id":
			if soul.Readiness == nil {
				soul.Readiness = &domain.SoulReadinessEvidence{}
			}
			soul.Readiness.RunID = value
		case "request-id":
			if soul.Readiness == nil {
				soul.Readiness = &domain.SoulReadinessEvidence{}
			}
			soul.Readiness.RequestID = value
		case "readiness":
			if value == "verified" && soul.Readiness == nil {
				soul.Readiness = &domain.SoulReadinessEvidence{}
			}
		case "readiness-ms":
			if duration, err := strconv.ParseInt(value, 10, 64); err == nil {
				if soul.Readiness == nil {
					soul.Readiness = &domain.SoulReadinessEvidence{}
				}
				soul.Readiness.TotalDurationMS = duration
			}
		case "probe-event":
			if soul.Readiness == nil {
				soul.Readiness = &domain.SoulReadinessEvidence{}
			}
			soul.Readiness.ProbeEventIDs = append(soul.Readiness.ProbeEventIDs, value)
		case tagCapability:
			soul.CapabilityRef = value
			soul.Runtime.CapabilityRef = firstNonEmpty(soul.Runtime.CapabilityRef, value)
		case tagRelayRead:
			soul.RelayPolicy.Read = append(soul.RelayPolicy.Read, value)
		case tagRelayWrite:
			soul.RelayPolicy.Write = append(soul.RelayPolicy.Write, value)
		case tagRelayControl:
			soul.RelayPolicy.Control = append(soul.RelayPolicy.Control, value)
		case tagApprovalPolicy:
			soul.PermissionSpec.ApprovalPolicy = value
		case tagAvatarRef:
			soul.Assets.AvatarRef = value
		case tagVoiceRef:
			soul.Assets.VoiceRef = value
		}
	}
	return soul
}

// ParseSoulDraftEvent converts a kind:31952 draft event into a SoulDraft.
func ParseSoulDraftEvent(event *nostr.Event) (*domain.SoulDraft, error) {
	if event == nil {
		return nil, fmt.Errorf("nil soul draft event")
	}
	draft := &domain.SoulDraft{ID: domain.NewUUID(), EventID: event.ID.Hex(), CreatedBy: event.PubKey.Hex(), CreatedAt: event.CreatedAt.Time(), UpdatedAt: event.CreatedAt.Time()}
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case tagParameterizedD:
			draft.AgentID = strings.TrimSpace(tag[1])
		case tagName:
			draft.Name = strings.TrimSpace(tag[1])
		case tagTier:
			draft.Tier = domain.SoulTier(strings.TrimSpace(tag[1]))
		case tagTemplate:
			draft.TemplateRef = strings.TrimSpace(tag[1])
		}
	}
	if strings.TrimSpace(event.Content) != "" {
		content, err := domain.ParseDraftContent(event.Content)
		if err != nil {
			return nil, fmt.Errorf("parse draft content: %w", err)
		}
		if strings.TrimSpace(content.Schema) == "" && content.HasV2CustomizationSpecs() {
			content.Schema = domain.SoulFactoryDraftSchemaV2
		}
		if err := ValidateSoulDraftContent(*content); err != nil {
			return nil, fmt.Errorf("validate draft content: %w", err)
		}
		draft.Content = *content
	}
	if draft.Tier == "" {
		draft.Tier = draft.Content.Identity.Tier
	}
	if draft.Name == "" {
		draft.Name = draft.Content.Identity.Name
	}
	return draft, nil
}

// BuildSoulDraftEvent builds a kind:31952 draft event. The caller is
// responsible for signing and publishing it.
func BuildSoulDraftEvent(draft *domain.SoulDraft) (*nostr.Event, error) {
	if draft == nil {
		return nil, fmt.Errorf("nil soul draft")
	}
	if err := ValidateSoulDraftContent(draft.Content); err != nil {
		return nil, fmt.Errorf("validate draft content: %w", err)
	}
	content, err := draft.Content.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal draft content: %w", err)
	}
	tags := nostr.Tags{{tagParameterizedD, strings.TrimSpace(draft.AgentID)}}
	appendTag(&tags, tagName, draft.Name)
	appendTag(&tags, tagTier, string(draft.Tier))
	appendTag(&tags, tagTemplate, draft.TemplateRef)
	appendTag(&tags, tagSpecHash, draft.Content.SpecHash)
	return &nostr.Event{Kind: domain.KindSoulDraft, CreatedAt: nostr.Now(), Tags: tags, Content: content}, nil
}

// DraftContentValidationError reports all invalid v2 draft customization fields at once.
type DraftContentValidationError struct {
	Violations []string
}

func (e *DraftContentValidationError) Error() string {
	if e == nil || len(e.Violations) == 0 {
		return "invalid draft content"
	}
	return "invalid draft content: " + strings.Join(e.Violations, "; ")
}

const (
	avatarDimensionMin  = 64
	avatarDimensionMax  = 2048
	avatarDimensionStep = 8
)

var supportedAvatarProviders = map[string]struct{}{
	"flux-comfyui": {},
	"fal":          {},
	"replicate":    {},
}

var supportedMemoryEmbeddingModels = map[string]map[string]struct{}{
	MemoryEmbeddingProviderVoyage: {
		"voyage-3":       {},
		"voyage-3-lite":  {},
		"voyage-3-large": {},
		"voyage-code-3":  {},
	},
	MemoryEmbeddingProviderOpenAI: {
		"text-embedding-3-small": {},
		"text-embedding-3-large": {},
		"text-embedding-ada-002": {},
	},
	MemoryEmbeddingProviderCohere: {
		"embed-v4.0":              {},
		"embed-english-v3.0":      {},
		"embed-multilingual-v3.0": {},
	},
}

var supportedMemoryRerankModels = map[string]struct{}{
	"cohere-rerank-v3":         {},
	"rerank-v3.5":              {},
	"rerank-english-v3.0":      {},
	"rerank-multilingual-v3.0": {},
}

// ValidateSoulDraftContent validates v2 customization sections that the event
// codec accepts on kind:31952 drafts. Legacy/no-schema v1 drafts without v2
// customization sections pass unchanged for backward compatibility.
func ValidateSoulDraftContent(content domain.SoulDraftContent) error {
	var violations []string
	schema := content.SchemaVersion()
	explicitSchema := strings.TrimSpace(content.Schema)
	if explicitSchema != "" && schema != domain.SoulFactoryDraftSchemaV1 && schema != domain.SoulFactoryDraftSchemaV2 {
		violations = append(violations, fmt.Sprintf("schema %q is unsupported", explicitSchema))
	}
	if explicitSchema == domain.SoulFactoryDraftSchemaV1 && content.HasV2CustomizationSpecs() {
		violations = append(violations, "v2 customization specs require schema soulfactory-draft/v2")
	}
	if !content.HasV2CustomizationSpecs() {
		return draftValidationErr(violations)
	}
	validateDraftAvatarSpec(content.Avatar, &violations)
	validateDraftVoiceSpec(content.Voice, &violations)
	validateDraftMemorySpec(content.Memory, &violations)
	if err := ValidateSoulPersonaSpec(content.Persona); err != nil {
		violations = append(violations, err.Error())
	}
	return draftValidationErr(violations)
}

// MergeSoulDraftContent applies a hot-reload style partial update to a draft.
// Object fields merge recursively; arrays and scalar values replace; null values
// delete fields. A patch may be either the content object itself or {"content":{...}}.
func MergeSoulDraftContent(base domain.SoulDraftContent, patch map[string]interface{}) (domain.SoulDraftContent, error) {
	if len(patch) == 0 {
		if err := ValidateSoulDraftContent(base); err != nil {
			return domain.SoulDraftContent{}, err
		}
		return base, nil
	}
	patch = draftContentPatchMap(patch)
	baseMap, err := draftContentToMap(base)
	if err != nil {
		return domain.SoulDraftContent{}, err
	}
	mergedMap := mergeDraftJSONMaps(baseMap, patch)
	data, err := json.Marshal(mergedMap)
	if err != nil {
		return domain.SoulDraftContent{}, fmt.Errorf("marshal merged draft content: %w", err)
	}
	var merged domain.SoulDraftContent
	if err := json.Unmarshal(data, &merged); err != nil {
		return domain.SoulDraftContent{}, fmt.Errorf("parse merged draft content: %w", err)
	}
	if strings.TrimSpace(merged.Schema) == "" && (base.IsV2() || merged.HasV2CustomizationSpecs()) {
		merged.Schema = domain.SoulFactoryDraftSchemaV2
	}
	if err := ValidateSoulDraftContent(merged); err != nil {
		return domain.SoulDraftContent{}, err
	}
	return merged, nil
}

func validateDraftAvatarSpec(spec domain.SoulAvatarSpec, violations *[]string) {
	current := strings.ToLower(strings.TrimSpace(spec.Current))
	if current != "" && current != "generated" && current != "uploaded" {
		*violations = append(*violations, fmt.Sprintf("avatar.current %q is unsupported; use generated or uploaded", spec.Current))
	}
	if spec.Generation == nil {
		return
	}
	generation := spec.Generation
	provider := strings.ToLower(strings.TrimSpace(generation.Provider))
	if provider != "" {
		if _, ok := supportedAvatarProviders[provider]; !ok {
			*violations = append(*violations, fmt.Sprintf("avatar.generation.provider %q is unsupported; supported providers are flux-comfyui, fal, replicate", generation.Provider))
		}
	}
	if strings.TrimSpace(generation.Prompt) == "" {
		*violations = append(*violations, "avatar.generation.prompt is required")
	}
	stylePreset := strings.ToLower(strings.TrimSpace(generation.StylePreset))
	if stylePreset != "" && !isSupportedAvatarStylePreset(stylePreset) {
		*violations = append(*violations, fmt.Sprintf("avatar.generation.style_preset %q is unsupported", generation.StylePreset))
	}
	validateAvatarDimension("avatar.generation.width", generation.Width, violations)
	validateAvatarDimension("avatar.generation.height", generation.Height, violations)
}

func validateDraftVoiceSpec(spec domain.SoulVoiceSpec, violations *[]string) {
	if !hasVoiceSpec(spec) {
		return
	}
	providerID := NormalizeVoiceProviderID(spec.Provider)
	registry := NewDefaultVoiceProviderRegistry()
	ctx := context.Background()
	if providerID != "" {
		capabilities, err := registry.Capabilities(ctx, providerID)
		if err != nil {
			*violations = append(*violations, fmt.Sprintf("voice.provider %q is unsupported", spec.Provider))
		} else {
			validateVoiceProviderConfigModel(providerID, providerConfigFor(spec, providerID), capabilities, violations)
		}
	} else if only := onlyProviderFromSpec(spec); only != "" {
		providerID = only
	}
	for rawProvider, config := range spec.Providers {
		id := NormalizeVoiceProviderID(rawProvider)
		capabilities, err := registry.Capabilities(ctx, id)
		if err != nil {
			*violations = append(*violations, fmt.Sprintf("voice.providers.%s is unsupported", rawProvider))
			continue
		}
		validateVoiceProviderConfigModel(id, config, capabilities, violations)
	}
	if providerID != "" {
		if _, err := registry.ResolveVoiceSpec(ctx, spec); err != nil {
			*violations = append(*violations, err.Error())
		}
	}
	switch strings.ToLower(strings.TrimSpace(spec.AutoMode)) {
	case "", "off", "always", "tagged":
	default:
		*violations = append(*violations, fmt.Sprintf("voice.auto_mode %q is unsupported; use off, always, or tagged", spec.AutoMode))
	}
}

func validateDraftMemorySpec(spec domain.SoulMemorySpec, violations *[]string) {
	if !hasMemorySpec(spec) {
		return
	}
	if err := ValidateSoulMemorySpec(spec); err != nil {
		*violations = append(*violations, err.Error())
	}
	provider, ok := normalizeMemoryProvider(spec.EmbeddingProvider)
	model := strings.TrimSpace(spec.EmbeddingModel)
	if model != "" {
		if !ok || provider == MemoryEmbeddingProviderAuto {
			*violations = append(*violations, "memory.embedding_model requires an explicit supported embedding_provider")
		} else if supportedModels, constrained := supportedMemoryEmbeddingModels[provider]; constrained {
			if _, modelOK := supportedModels[model]; !modelOK {
				*violations = append(*violations, fmt.Sprintf("memory.embedding_model %q is unsupported for provider %s", model, provider))
			}
		}
	}
	if spec.Search != nil && strings.TrimSpace(spec.Search.RerankModel) != "" {
		rerankModel := strings.TrimSpace(spec.Search.RerankModel)
		if _, ok := supportedMemoryRerankModels[rerankModel]; !ok {
			*violations = append(*violations, fmt.Sprintf("memory.search.rerank_model %q is unsupported", rerankModel))
		}
	}
}

func draftValidationErr(violations []string) error {
	if len(violations) == 0 {
		return nil
	}
	return &DraftContentValidationError{Violations: violations}
}

func hasVoiceSpec(spec domain.SoulVoiceSpec) bool {
	return strings.TrimSpace(spec.Provider) != "" || strings.TrimSpace(spec.PersonaID) != "" || spec.Persona != nil || strings.TrimSpace(spec.AutoMode) != "" || strings.TrimSpace(spec.SampleText) != "" || len(spec.Providers) > 0
}

func hasMemorySpec(spec domain.SoulMemorySpec) bool {
	return strings.TrimSpace(spec.EmbeddingProvider) != "" || strings.TrimSpace(spec.EmbeddingModel) != "" || spec.Search != nil || strings.TrimSpace(spec.Strategy) != "" || spec.AutoIndex || spec.RetentionDays != 0
}

func validateAvatarDimension(field string, value int, violations *[]string) {
	if value == 0 {
		return
	}
	if value < avatarDimensionMin || value > avatarDimensionMax {
		*violations = append(*violations, fmt.Sprintf("%s must be between %d and %d", field, avatarDimensionMin, avatarDimensionMax))
	}
	if value%avatarDimensionStep != 0 {
		*violations = append(*violations, fmt.Sprintf("%s must be a multiple of %d", field, avatarDimensionStep))
	}
}

func isSupportedAvatarStylePreset(preset string) bool {
	switch preset {
	case "pixel-art", "anime", "realistic", "abstract", "corporate":
		return true
	default:
		return false
	}
}

func validateVoiceProviderConfigModel(providerID string, config map[string]any, capabilities VoiceProviderCapabilities, violations *[]string) {
	model := stringConfigValue(config, "model", "model_id", "modelId")
	if model == "" {
		return
	}
	for _, supported := range capabilities.Models {
		if strings.EqualFold(model, supported) {
			return
		}
	}
	*violations = append(*violations, fmt.Sprintf("voice.providers.%s.model %q is unsupported", providerID, model))
}

func stringConfigValue(config map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := config[key]; ok {
			if str, ok := value.(string); ok {
				return strings.TrimSpace(str)
			}
		}
	}
	return ""
}

func draftContentPatchMap(patch map[string]interface{}) map[string]interface{} {
	contentPatch, ok := patch["content"].(map[string]interface{})
	if !ok {
		return patch
	}
	if len(contentPatch) == 0 {
		return patch
	}
	return contentPatch
}

func draftContentToMap(content domain.SoulDraftContent) (map[string]interface{}, error) {
	data, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshal base draft content: %w", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse base draft content: %w", err)
	}
	return out, nil
}

func mergeDraftJSONMaps(base, patch map[string]interface{}) map[string]interface{} {
	merged := cloneDraftJSONMap(base)
	for key, patchValue := range patch {
		if patchValue == nil {
			delete(merged, key)
			continue
		}
		baseObject, baseOK := merged[key].(map[string]interface{})
		patchObject, patchOK := patchValue.(map[string]interface{})
		if baseOK && patchOK {
			merged[key] = mergeDraftJSONMaps(baseObject, patchObject)
			continue
		}
		merged[key] = patchValue
	}
	return merged
}

func cloneDraftJSONMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneDraftJSONValue(value)
	}
	return out
}

func cloneDraftJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneDraftJSONMap(typed)
	case []interface{}:
		clone := make([]interface{}, len(typed))
		for i, item := range typed {
			clone[i] = cloneDraftJSONValue(item)
		}
		return clone
	default:
		return value
	}
}

// BuildProvisioningStatusEvent builds a kind:6950 progress event.
func BuildProvisioningStatusEvent(requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string, runID ...string) *nostr.Event {
	tags := nostr.Tags{
		{tagEvent, requestEvent.ID.Hex(), "", "reply"},
		{tagPubkey, requestEvent.PubKey.Hex()},
		{tagStatus, "processing"},
		{tagStep, string(step)},
		{tagProgress, strconv.Itoa(current), strconv.Itoa(total)},
	}
	if len(runID) > 0 {
		appendTag(&tags, "run-id", runID[0])
	}
	return &nostr.Event{
		Kind:      domain.KindProvisioningStatus,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   message,
	}
}

// BuildProvisioningSuccessResultEvent builds a kind:7950 provisioning success event.
func BuildProvisioningSuccessResultEvent(requestEvent *nostr.Event, soul *domain.AgentSoul, factoryPubkey string) (*nostr.Event, error) {
	factoryPubkey = strings.TrimSpace(factoryPubkey)
	if factoryPubkey == "" {
		return nil, fmt.Errorf("SoulFactory pubkey is required for provisioning success result")
	}
	content, err := json.Marshal(map[string]interface{}{
		"soul_id":           soul.AgentID,
		"npub":              soul.NostrNpub,
		"pubkey":            soul.NostrPubkey,
		"avatar_url":        soul.AvatarURL,
		"workspace_url":     soul.WorkspaceRepoURL,
		"qdrant_collection": soul.QdrantCollection,
		"bahia_service_id":  soul.BahiaServiceID,
		"provider":          soul.Runtime.Provider,
		"model":             soul.Runtime.Model,
		"readiness":         soul.Readiness,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal provisioning result content: %w", err)
	}

	tags := nostr.Tags{
		{tagEvent, requestEvent.ID.Hex(), "", "reply"},
		{tagPubkey, requestEvent.PubKey.Hex()},
		{tagStatus, "success"},
		{tagSoul, soulCoordinate(factoryPubkey, soul.AgentID)},
		{tagAgentPubkey, soul.NostrPubkey},
		{tagNpub, soul.NostrNpub},
	}
	appendResultContextTags(&tags, soul)
	return &nostr.Event{Kind: domain.KindProvisioningResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}, nil
}

// BuildProvisioningErrorResultEvent builds a kind:7950 error result event.
func BuildProvisioningErrorResultEvent(requestEvent *nostr.Event, step, message string, runID ...string) *nostr.Event {
	tags := nostr.Tags{
		{tagEvent, requestEvent.ID.Hex(), "", "reply"},
		{tagPubkey, requestEvent.PubKey.Hex()},
		{tagStatus, "error"},
		{tagStep, step},
		{"error", step},
	}
	if len(runID) > 0 {
		appendTag(&tags, "run-id", runID[0])
	}
	return &nostr.Event{
		Kind:      domain.KindProvisioningResult,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   message,
	}
}

// BuildAgentSoulEvent builds a kind:31951 authoritative soul read-model event.
func BuildAgentSoulEvent(soul *domain.AgentSoul) *nostr.Event {
	tags := nostr.Tags{
		{tagParameterizedD, soul.AgentID},
		{tagName, soul.Name},
		{tagPurpose, soul.Purpose},
		{tagTier, string(soul.Tier)},
		{tagStatus, string(soul.Status)},
		{tagPubkey, soul.NostrPubkey, "agent"},
		{tagNpub, soul.NostrNpub},
	}
	appendTag(&tags, tagNIP05, soul.NIP05)
	// Bunker URIs contain one-time connection secrets. They belong only in
	// the agent's private runtime handoff and must never enter a public Soul
	// read-model event.
	appendTag(&tags, tagAvatar, soul.AvatarURL)
	appendTag(&tags, tagSoulBlob, soul.SoulBlobHash)
	appendTag(&tags, tagQdrant, soul.QdrantCollection)
	appendTag(&tags, tagWorkspace, soul.WorkspaceRepoURL)
	for _, kind := range soul.AllowedKinds {
		tags = append(tags, nostr.Tag{tagAllowedKind, strconv.Itoa(kind)})
	}
	for _, grant := range soul.ToolGrants {
		tag := nostr.Tag{tagTool, grant.MCPServer}
		tag = append(tag, grant.Scopes...)
		tags = append(tags, tag)
	}
	appendResultContextTags(&tags, soul)
	appendTag(&tags, tagFleetRevision, soul.AppliedFleetConfigRevision)
	return &nostr.Event{Kind: domain.KindAgentSoul, CreatedAt: nostr.Now(), Tags: tags, Content: soul.SoulMD}
}

// BuildActionStatusEvent builds a lifecycle progress event. Lifecycle actions
// reuse kind:6950 and are distinguished from provisioning by request-kind,
// soul, and action tags correlated to the original kind:1950 event via #e.
func BuildActionStatusEvent(action *domain.SoulAction, status, message string, agentID ...string) *nostr.Event {
	tags := nostr.Tags{
		{tagEvent, action.EventID, "", "reply"},
		{tagPubkey, action.Initiator},
		{tagRequestKind, strconv.Itoa(domain.KindSoulAction)},
		{tagSoul, action.SoulRef},
		{tagAction, string(action.Action)},
		{tagStatus, status},
		{tagStep, "lifecycle"},
	}
	if len(agentID) > 0 {
		appendTag(&tags, tagAgentID, agentID[0])
	}
	return &nostr.Event{
		Kind:      domain.KindProvisioningStatus,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   message,
	}
}

// ActionResultShape selects the migration shape for lifecycle action terminal events.
type ActionResultShape int

const (
	ActionResultCanonical ActionResultShape = iota
	ActionResultLegacy
)

// BuildActionResultEvent builds a lifecycle terminal event. Canonical lifecycle
// results are kind:7950; ActionResultLegacy preserves the early kind:1951 alias.
func BuildActionResultEvent(action *domain.SoulAction, status string, data map[string]interface{}, shape ActionResultShape, agentID ...string) (*nostr.Event, error) {
	if action == nil {
		return nil, fmt.Errorf("nil soul action")
	}
	content := ""
	if data != nil {
		contentBytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("marshal action result content: %w", err)
		}
		content = string(contentBytes)
	}

	kind := domain.KindProvisioningResult
	tags := nostr.Tags{
		{tagEvent, action.EventID, "", "reply"},
		{tagPubkey, action.Initiator},
		{tagRequestKind, strconv.Itoa(domain.KindSoulAction)},
		{tagSoul, action.SoulRef},
		{tagAction, string(action.Action)},
		{tagStatus, status},
	}
	if len(agentID) > 0 {
		appendTag(&tags, tagAgentID, agentID[0])
	}
	if shape == ActionResultLegacy {
		kind = domain.KindSoulActionLegacyResult
	}
	return &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: tags, Content: content}, nil
}

// RuntimeControlEnvelope is the JSON content of a kind:38384 runtime control request.
type RuntimeControlEnvelope struct {
	Schema         string                 `json:"schema"`
	Method         string                 `json:"method"`
	IdempotencyKey string                 `json:"idempotency_key"`
	RequestedAt    int64                  `json:"requested_at"`
	Operator       RuntimeOperatorRef     `json:"operator"`
	Controller     RuntimeControllerRef   `json:"controller"`
	Target         RuntimeTargetRef       `json:"target"`
	Soul           RuntimeSoulRef         `json:"soul"`
	Params         map[string]interface{} `json:"params"`
}

type RuntimeOperatorRef struct {
	Pubkey       string `json:"pubkey"`
	RequestEvent string `json:"request_event"`
}

type RuntimeControllerRef struct {
	Pubkey string `json:"pubkey"`
}

type RuntimeTargetRef struct {
	Runtime       domain.RuntimeTarget `json:"runtime"`
	RuntimePubkey string               `json:"runtime_pubkey"`
	AgentID       string               `json:"agent_id"`
}

type RuntimeSoulRef struct {
	ID       string `json:"id"`
	Event    string `json:"event,omitempty"`
	Draft    string `json:"draft,omitempty"`
	SpecHash string `json:"spec_hash"`
}

// BuildRuntimeControlRequestEvent builds an unsigned kind:38384 request.
func BuildRuntimeControlRequestEvent(envelope RuntimeControlEnvelope) (*nostr.Event, error) {
	if envelope.Schema == "" {
		envelope.Schema = domain.SoulFactoryRuntimeControlSchema
	}
	if envelope.Params == nil {
		envelope.Params = map[string]interface{}{}
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal runtime control request: %w", err)
	}
	tags := nostr.Tags{
		{tagPubkey, envelope.Target.RuntimePubkey},
		{tagMethod, envelope.Method},
		{tagEvent, envelope.Operator.RequestEvent},
		{tagSoul, envelope.Soul.ID},
		{tagAgentID, envelope.Target.AgentID},
		{"controller", envelope.Controller.Pubkey},
		{tagIdempotencyKey, envelope.IdempotencyKey},
		{tagSpecHash, envelope.Soul.SpecHash},
		{tagSchema, envelope.Schema},
	}
	appendTag(&tags, tagDraft, envelope.Soul.Draft)
	appendTag(&tags, tagRuntime, string(envelope.Target.Runtime))
	return &nostr.Event{Kind: domain.KindRuntimeControlRequest, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}, nil
}

// ParseRuntimeControlRequestEvent decodes a kind:38384 runtime control request envelope.
func ParseRuntimeControlRequestEvent(event *nostr.Event) (*RuntimeControlEnvelope, error) {
	if event == nil {
		return nil, fmt.Errorf("nil runtime control request")
	}
	if event.Kind != domain.KindRuntimeControlRequest {
		return nil, fmt.Errorf("unexpected runtime control kind: %d", event.Kind)
	}
	var envelope RuntimeControlEnvelope
	if err := json.Unmarshal([]byte(event.Content), &envelope); err != nil {
		return nil, fmt.Errorf("parse runtime control request: %w", err)
	}
	if envelope.Schema == "" {
		envelope.Schema = tagValue(event.Tags, tagSchema)
	}
	if envelope.Method == "" {
		envelope.Method = tagValue(event.Tags, tagMethod)
	}
	if envelope.IdempotencyKey == "" {
		envelope.IdempotencyKey = tagValue(event.Tags, tagIdempotencyKey)
	}
	if envelope.Operator.RequestEvent == "" {
		envelope.Operator.RequestEvent = tagValue(event.Tags, tagEvent)
	}
	if envelope.Controller.Pubkey == "" {
		envelope.Controller.Pubkey = tagValue(event.Tags, "controller")
	}
	if envelope.Target.RuntimePubkey == "" {
		envelope.Target.RuntimePubkey = tagValue(event.Tags, tagPubkey)
	}
	if envelope.Target.AgentID == "" {
		envelope.Target.AgentID = tagValue(event.Tags, tagAgentID)
	}
	if envelope.Target.Runtime == "" {
		envelope.Target.Runtime = domain.RuntimeTarget(tagValue(event.Tags, tagRuntime))
	}
	if envelope.Soul.ID == "" {
		envelope.Soul.ID = tagValue(event.Tags, tagSoul)
	}
	if envelope.Soul.Draft == "" {
		envelope.Soul.Draft = tagValue(event.Tags, tagDraft)
	}
	if envelope.Soul.SpecHash == "" {
		envelope.Soul.SpecHash = tagValue(event.Tags, tagSpecHash)
	}
	return &envelope, nil
}

func appendResultContextTags(tags *nostr.Tags, soul *domain.AgentSoul) {
	if soul == nil {
		return
	}
	if soul.BahiaServiceID != nil {
		appendTag(tags, tagService, soul.BahiaServiceID.String())
	}
	appendTag(tags, tagTemplate, soul.TemplateRef)
	appendTag(tags, tagDeployStatus, soul.DeployStatus)
	appendTag(tags, tagDraft, soul.DraftRef)
	appendTag(tags, tagDraftEvent, soul.DraftEventID)
	appendTag(tags, tagPreviousDraft, soul.PreviousDraftRef)
	appendTag(tags, tagPreviousDraftEvent, soul.PreviousDraftEventID)
	appendTag(tags, tagSpecHash, soul.SpecHash)
	appendTag(tags, tagPreviousSpecHash, soul.PreviousSpecHash)
	appendTag(tags, tagRuntime, string(soul.Runtime.Target))
	appendTag(tags, tagRuntimePubkey, soul.Runtime.RuntimePubkey)
	appendTag(tags, tagRuntimeBinding, soul.Runtime.RuntimeBinding)
	appendTag(tags, tagRuntimeState, soul.Runtime.State)
	appendTag(tags, "provider", soul.Runtime.Provider)
	appendTag(tags, "model", soul.Runtime.Model)
	if soul.Readiness != nil {
		appendTag(tags, "request-id", soul.Readiness.RequestID)
		appendTag(tags, "run-id", soul.Readiness.RunID)
		appendTag(tags, "readiness", "verified")
		appendTag(tags, "readiness-ms", strconv.FormatInt(soul.Readiness.TotalDurationMS, 10))
		for _, eventID := range soul.Readiness.ProbeEventIDs {
			appendTag(tags, "probe-event", eventID)
		}
	}
	appendTag(tags, tagCapability, firstNonEmpty(soul.CapabilityRef, soul.Runtime.CapabilityRef))
	for _, relay := range soul.RelayPolicy.Read {
		appendTag(tags, tagRelayRead, relay)
	}
	for _, relay := range soul.RelayPolicy.Write {
		appendTag(tags, tagRelayWrite, relay)
	}
	for _, relay := range soul.RelayPolicy.Control {
		appendTag(tags, tagRelayControl, relay)
	}
	appendTag(tags, tagApprovalPolicy, soul.PermissionSpec.ApprovalPolicy)
	appendTag(tags, tagAvatarRef, soul.Assets.AvatarRef)
	appendTag(tags, tagVoiceRef, soul.Assets.VoiceRef)
}

func appendTag(tags *nostr.Tags, name, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		*tags = append(*tags, nostr.Tag{name, value})
	}
}

func tagValue(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return strings.TrimSpace(tag[1])
		}
	}
	return ""
}

func isDraftEventTag(tag nostr.Tag) bool {
	if len(tag) < 2 {
		return false
	}
	for _, marker := range tag[2:] {
		if marker == "draft" || marker == "root:draft" {
			return true
		}
	}
	return false
}

func soulCoordinate(factoryPubkey, agentID string) string {
	return fmt.Sprintf("%d:%s:%s", domain.KindAgentSoul, factoryPubkey, agentID)
}
