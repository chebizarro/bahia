package soulfactory

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// SoulFactory event tag names. Keeping them in one file prevents reactor,
// lifecycle, provisioning, and clients from silently drifting on wire shape.
const (
	tagAction           = "action"
	tagAgentID          = "agent-id"
	tagAgentPubkey      = "agent-pubkey"
	tagAllowedKind      = "allowed-kind"
	tagApprovalPolicy   = "approval-policy"
	tagAvatar           = "avatar"
	tagAvatarRef        = "avatar-ref"
	tagBunker           = "bunker"
	tagCapability       = "capability"
	tagDeployStatus     = "deploy-status"
	tagDraft            = "draft"
	tagDraftEvent       = "draft-event"
	tagEvent            = "e"
	tagName             = "name"
	tagNIP05            = "nip05"
	tagNpub             = "npub"
	tagParameterizedD   = "d"
	tagPreviousSpecHash = "previous-spec-hash"
	tagProgress         = "progress"
	tagPubkey           = "p"
	tagPurpose          = "purpose"
	tagQdrant           = "qdrant"
	tagReason           = "reason"
	tagRelayControl     = "relay-control"
	tagRelayRead        = "relay-read"
	tagRelayWrite       = "relay-write"
	tagRequestKind      = "request-kind"
	tagRuntime          = "runtime"
	tagRuntimeBinding   = "runtime-binding"
	tagRuntimePubkey    = "runtime-pubkey"
	tagRuntimeState     = "runtime-state"
	tagSchema           = "schema"
	tagService          = "service"
	tagSoul             = "soul"
	tagSoulBlob         = "soul-blob"
	tagSpecHash         = "spec-hash"
	tagStatus           = "status"
	tagStep             = "step"
	tagTemplate         = "template"
	tagTier             = "tier"
	tagTool             = "tool"
	tagVoiceRef         = "voice-ref"
	tagWorkspace        = "workspace"
)

// ParseProvisioningRequestEvent extracts a domain provisioning request from a
// kind:5950 event. It accepts both the legacy tag + {brief} shape and additive
// draft/spec-hash fields introduced for runtime-aware provisioning.
func ParseProvisioningRequestEvent(event *nostr.Event) (*domain.ProvisioningRequest, error) {
	if event == nil {
		return nil, fmt.Errorf("nil provisioning event")
	}
	req := &domain.ProvisioningRequest{EventID: event.ID, Requester: event.PubKey}

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
		case tagEvent:
			if isDraftEventTag(tag) && req.DraftEventID == "" {
				req.DraftEventID = strings.TrimSpace(tag[1])
			}
		}
	}

	if strings.TrimSpace(event.Content) != "" {
		var content struct {
			AgentID      string          `json:"agent_id"`
			Name         string          `json:"name"`
			Tier         domain.SoulTier `json:"tier"`
			TemplateRef  string          `json:"template_ref"`
			DraftRef     string          `json:"draft_ref"`
			DraftEventID string          `json:"draft_event_id"`
			SpecHash     string          `json:"spec_hash"`
			Brief        string          `json:"brief"`
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
		EventID:   event.ID,
		Initiator: event.PubKey,
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
	soul := &domain.AgentSoul{EventID: event.ID, SoulMD: event.Content, CreatedAt: event.CreatedAt.Time()}

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
		case tagSpecHash:
			soul.SpecHash = value
		case tagPreviousSpecHash:
			soul.PreviousSpecHash = value
		case tagRuntime:
			soul.Runtime.Target = domain.RuntimeTarget(value)
		case tagRuntimePubkey:
			soul.Runtime.RuntimePubkey = value
		case tagRuntimeBinding:
			soul.Runtime.RuntimeBinding = value
		case tagRuntimeState:
			soul.Runtime.State = value
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
	draft := &domain.SoulDraft{ID: domain.NewUUID(), EventID: event.ID, CreatedBy: event.PubKey, CreatedAt: event.CreatedAt.Time(), UpdatedAt: event.CreatedAt.Time()}
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

// BuildProvisioningStatusEvent builds a kind:6950 progress event.
func BuildProvisioningStatusEvent(requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string) *nostr.Event {
	return &nostr.Event{
		Kind:      domain.KindProvisioningStatus,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{tagEvent, requestEvent.ID, "", "reply"},
			{tagPubkey, requestEvent.PubKey},
			{tagStatus, "processing"},
			{tagStep, string(step)},
			{tagProgress, strconv.Itoa(current), strconv.Itoa(total)},
		},
		Content: message,
	}
}

// BuildProvisioningSuccessResultEvent builds a kind:7950 provisioning success event.
func BuildProvisioningSuccessResultEvent(requestEvent *nostr.Event, soul *domain.AgentSoul, factoryPubkey string) (*nostr.Event, error) {
	content, err := json.Marshal(map[string]interface{}{
		"soul_id":           soul.AgentID,
		"npub":              soul.NostrNpub,
		"pubkey":            soul.NostrPubkey,
		"avatar_url":        soul.AvatarURL,
		"workspace_url":     soul.WorkspaceRepoURL,
		"qdrant_collection": soul.QdrantCollection,
		"bahia_service_id":  soul.BahiaServiceID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal provisioning result content: %w", err)
	}

	tags := nostr.Tags{
		{tagEvent, requestEvent.ID, "", "reply"},
		{tagPubkey, requestEvent.PubKey},
		{tagStatus, "success"},
		{tagSoul, soulCoordinate(firstNonEmpty(factoryPubkey, SoulFactoryPubkey), soul.AgentID)},
		{tagAgentPubkey, soul.NostrPubkey},
		{tagNpub, soul.NostrNpub},
	}
	appendResultContextTags(&tags, soul)
	return &nostr.Event{Kind: domain.KindProvisioningResult, CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}, nil
}

// BuildProvisioningErrorResultEvent builds a kind:7950 error result event.
func BuildProvisioningErrorResultEvent(requestEvent *nostr.Event, step, message string) *nostr.Event {
	return &nostr.Event{
		Kind:      domain.KindProvisioningResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{tagEvent, requestEvent.ID, "", "reply"},
			{tagPubkey, requestEvent.PubKey},
			{tagStatus, "error"},
			{tagStep, step},
			{"error", step},
		},
		Content: message,
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
	appendTag(&tags, tagBunker, soul.BunkerURI)
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
	return &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags, Content: content}, nil
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
		{"method", envelope.Method},
		{tagEvent, envelope.Operator.RequestEvent},
		{tagSoul, envelope.Soul.ID},
		{tagAgentID, envelope.Target.AgentID},
		{"controller", envelope.Controller.Pubkey},
		{"idempotency-key", envelope.IdempotencyKey},
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
		envelope.Method = tagValue(event.Tags, "method")
	}
	if envelope.IdempotencyKey == "" {
		envelope.IdempotencyKey = tagValue(event.Tags, "idempotency-key")
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
	appendTag(tags, tagSpecHash, soul.SpecHash)
	appendTag(tags, tagPreviousSpecHash, soul.PreviousSpecHash)
	appendTag(tags, tagRuntime, string(soul.Runtime.Target))
	appendTag(tags, tagRuntimePubkey, soul.Runtime.RuntimePubkey)
	appendTag(tags, tagRuntimeBinding, soul.Runtime.RuntimeBinding)
	appendTag(tags, tagRuntimeState, soul.Runtime.State)
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
