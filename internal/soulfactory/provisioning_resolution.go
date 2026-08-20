package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

type resolvedProvisioningSpec struct {
	Template *domain.SoulTemplate
	Draft    *domain.SoulDraft

	AgentID      string
	Name         string
	Brief        string
	Tier         domain.SoulTier
	TemplateRef  string
	DraftRef     string
	DraftEventID string
	SpecHash     string

	Identity       domain.SoulIdentitySpec
	Persona        domain.SoulPersonaSpec
	Avatar         domain.SoulAvatarSpec
	Voice          domain.SoulVoiceSpec
	Memory         domain.SoulMemorySpec
	Runtime        domain.SoulRuntimeSpec
	Permissions    domain.SoulPermissionSpec
	RelayPolicy    domain.SoulRelayPolicySpec
	Workspace      domain.SoulWorkspaceSpec
	Assets         domain.SoulAssetRefs
	SignetIdentity *OpenClawSignetIdentityContract
}

func (p *FullProvisioner) resolveProvisioningSpec(ctx context.Context, req *domain.ProvisioningRequest) (*resolvedProvisioningSpec, error) {
	if req == nil {
		return nil, fmt.Errorf("nil provisioning request")
	}
	resolved := &resolvedProvisioningSpec{AgentID: strings.TrimSpace(req.AgentID)}

	if req.DraftRef != "" || req.DraftEventID != "" {
		draft, err := p.reactor.getProvisioningDraft(ctx, req.DraftRef, req.DraftEventID)
		if err != nil {
			return nil, err
		}
		if draft == nil {
			return nil, fmt.Errorf("soul draft not found")
		}
		if draft.AgentID != "" && resolved.AgentID != "" && draft.AgentID != resolved.AgentID {
			return nil, fmt.Errorf("draft agent id %q does not match request agent id %q", draft.AgentID, resolved.AgentID)
		}
		if req.DraftEventID != "" && draft.EventID != req.DraftEventID {
			return nil, fmt.Errorf("resolved draft event %q does not match requested draft-event %q", draft.EventID, req.DraftEventID)
		}
		resolved.Draft = draft
		resolved.DraftRef = firstNonEmpty(req.DraftRef, parameterizedCoordinate(domain.KindSoulDraft, draft.CreatedBy, draft.AgentID))
		resolved.DraftEventID = draft.EventID
	}

	resolved.TemplateRef = firstNonEmpty(req.TemplateRef, draftTemplateRef(resolved.Draft))
	if resolved.TemplateRef != "" {
		template, err := p.reactor.getProvisioningTemplate(ctx, resolved.TemplateRef)
		if err != nil {
			return nil, err
		}
		if template == nil {
			return nil, fmt.Errorf("soul template not found: %s", resolved.TemplateRef)
		}
		resolved.Template = template
	}

	resolved.applyTemplateDefaults()
	resolved.applyDraftSnapshot()
	resolved.applyInlineOverrides(req)
	if err := resolved.validate(req); err != nil {
		return nil, err
	}
	return resolved, nil
}

func (s *resolvedProvisioningSpec) applyTemplateDefaults() {
	if s.Template == nil {
		return
	}
	s.TemplateRef = firstNonEmpty(s.TemplateRef, s.Template.Identifier, s.Template.EventID)
	s.Name = firstNonEmpty(s.Name, s.Template.Name)
	s.Brief = firstNonEmpty(s.Brief, s.Template.BasePrompt, s.Template.Description)
	if s.Tier == "" {
		s.Tier = s.Template.Tier
	}
	s.Permissions.AllowedKinds = append([]int{}, s.Template.DefaultKinds...)
	s.Permissions.ToolGrants = append([]domain.ToolGrant{}, s.Template.DefaultTools...)
}

func (s *resolvedProvisioningSpec) applyDraftSnapshot() {
	if s.Draft == nil {
		return
	}
	content := s.Draft.Content.MigrateToLatest()
	s.AgentID = firstNonEmpty(s.AgentID, s.Draft.AgentID)
	s.Name = firstNonEmpty(content.Identity.Name, s.Draft.Name, s.Name)
	s.Brief = firstNonEmpty(content.Brief, content.Identity.Purpose, s.Brief)
	if content.Identity.Tier != "" {
		s.Tier = content.Identity.Tier
	} else if s.Draft.Tier != "" {
		s.Tier = s.Draft.Tier
	}
	s.TemplateRef = firstNonEmpty(s.Draft.TemplateRef, s.TemplateRef)
	s.Identity = content.Identity
	s.Persona = content.Persona
	s.Avatar = content.Avatar
	s.Voice = content.Voice
	s.Memory = content.Memory
	s.Runtime = content.Runtime
	s.RelayPolicy = content.RelayPolicy
	s.Workspace = content.Workspace
	s.Assets = content.Assets
	s.Permissions = content.Permissions
	if len(content.AllowedKinds) > 0 {
		s.Permissions.AllowedKinds = append([]int{}, content.AllowedKinds...)
	}
	if len(content.ToolGrants) > 0 {
		s.Permissions.ToolGrants = append([]domain.ToolGrant{}, content.ToolGrants...)
	}
	s.SpecHash = firstNonEmpty(content.SpecHash, s.SpecHash)
}

func (s *resolvedProvisioningSpec) applyInlineOverrides(req *domain.ProvisioningRequest) {
	s.AgentID = firstNonEmpty(req.AgentID, s.AgentID)
	s.Name = firstNonEmpty(req.Name, s.Name, s.AgentID)
	s.Brief = firstNonEmpty(req.Brief, s.Brief)
	if req.Tier != "" {
		s.Tier = req.Tier
	}
	s.TemplateRef = firstNonEmpty(req.TemplateRef, s.TemplateRef)
	if req.DraftRef != "" {
		s.DraftRef = req.DraftRef
	}
	if req.DraftEventID != "" {
		s.DraftEventID = req.DraftEventID
	}
}

func (s *resolvedProvisioningSpec) validate(req *domain.ProvisioningRequest) error {
	if s.AgentID == "" {
		return fmt.Errorf("missing agent id")
	}
	if s.Tier == "" {
		s.Tier = domain.SoulTierStandard
	}
	if s.Brief == "" {
		return fmt.Errorf("resolved provisioning spec has no brief")
	}
	if s.Draft != nil {
		authoritativeHash := s.SpecHash
		if authoritativeHash == "" {
			hash, err := hashDraftContent(s.Draft.Content)
			if err != nil {
				return err
			}
			authoritativeHash = hash
		}
		if req.SpecHash != "" && req.SpecHash != authoritativeHash {
			return fmt.Errorf("request spec hash %s does not match resolved draft spec hash %s", req.SpecHash, authoritativeHash)
		}
		s.SpecHash = authoritativeHash
	} else if req.SpecHash != "" {
		s.SpecHash = req.SpecHash
	}
	if s.Runtime.Target != "" && s.SpecHash == "" {
		return fmt.Errorf("runtime provisioning requires a spec hash")
	}
	return nil
}

func (s *resolvedProvisioningSpec) applyToSoul(soul *domain.AgentSoul) {
	if soul == nil {
		return
	}
	soul.AgentID = s.AgentID
	soul.Name = s.Name
	soul.Purpose = s.Brief
	soul.Tier = s.Tier
	soul.TemplateRef = s.TemplateRef
	soul.OriginalBrief = s.Brief
	soul.DraftRef = s.DraftRef
	soul.DraftEventID = s.DraftEventID
	soul.SpecHash = s.SpecHash
	soul.Runtime = s.Runtime
	soul.RelayPolicy = s.RelayPolicy
	soul.PermissionSpec = s.Permissions
	soul.Workspace = s.Workspace
	soul.Assets = s.Assets
	soul.CapabilityRef = firstNonEmpty(soul.CapabilityRef, s.Runtime.CapabilityRef)
	if s.Workspace.Repo != "" && soul.WorkspaceRepoURL == "" {
		soul.WorkspaceRepoURL = s.Workspace.Repo
	}
}

func (s *resolvedProvisioningSpec) provisionRuntimeParams(soul *domain.AgentSoul) map[string]interface{} {
	content := domain.SoulDraftContent{
		Schema:      domain.SoulFactoryDraftSchemaLatest,
		Brief:       s.Brief,
		Identity:    s.Identity,
		Persona:     s.Persona,
		Avatar:      s.Avatar,
		Voice:       s.Voice,
		Memory:      s.Memory,
		Runtime:     s.Runtime,
		Permissions: s.Permissions,
		RelayPolicy: s.RelayPolicy,
		Workspace:   s.Workspace,
		Assets:      s.Assets,
		SpecHash:    s.SpecHash,
	}
	content.Identity.Name = s.Name
	content.Identity.Purpose = s.Brief
	content.Identity.Tier = s.Tier
	params := BuildProvisionRuntimeParamsFromDraft(content)
	checkpoint := *soul
	// Runtime-control events are relay-visible durable workflow state. Preserve
	// everything needed to project a late successful runtime result, but never
	// copy the one-time Signet bunker connection secret into that event.
	checkpoint.BunkerURI = ""
	params["bahia"] = map[string]interface{}{
		"agent_id":        soul.AgentID,
		"soul_id":         soul.ID.String(),
		"nostr_pubkey":    soul.NostrPubkey,
		"nostr_npub":      soul.NostrNpub,
		"workspace_repo":  soul.WorkspaceRepoURL,
		"soul_checkpoint": checkpoint,
	}
	if s.SignetIdentity != nil {
		params["bahia"].(map[string]interface{})["signet_identity"] = s.SignetIdentity
	}
	return params
}

func (r *Reactor) getProvisioningDraft(ctx context.Context, draftRef, draftEventID string) (*domain.SoulDraft, error) {
	if r.getDraftFn != nil {
		return r.getDraftFn(ctx, draftRef, draftEventID)
	}
	bus := r.relayBus
	if bus == nil {
		return nil, fmt.Errorf("soul draft lookup requires a relay bus")
	}
	filters := draftLookupFilters(draftRef, draftEventID)
	if len(filters) == 0 {
		return nil, nil
	}
	events, err := bus.Query(ctx, filters)
	if err != nil {
		return nil, err
	}
	var latest *domain.SoulDraft
	for _, event := range events {
		if event == nil || event.Kind != domain.KindSoulDraft {
			continue
		}
		draft, err := ParseSoulDraftEvent(event)
		if err != nil {
			return nil, err
		}
		if draftEventID != "" && draft.EventID != draftEventID {
			continue
		}
		if draftRef != "" && !draftMatchesRef(draft, draftRef) {
			continue
		}
		if latest == nil || draft.UpdatedAt.After(latest.UpdatedAt) || (draft.UpdatedAt.Equal(latest.UpdatedAt) && draft.EventID > latest.EventID) {
			latest = draft
		}
	}
	return latest, nil
}

func (r *Reactor) getProvisioningTemplate(ctx context.Context, templateRef string) (*domain.SoulTemplate, error) {
	if r.getTemplateFn != nil {
		return r.getTemplateFn(ctx, templateRef)
	}
	bus := r.relayBus
	if bus == nil {
		return nil, fmt.Errorf("soul template lookup requires a relay bus")
	}
	filters := templateLookupFilters(templateRef)
	if len(filters) == 0 {
		return nil, nil
	}
	events, err := bus.Query(ctx, filters)
	if err != nil {
		return nil, err
	}
	var latest *domain.SoulTemplate
	for _, event := range events {
		if event == nil || event.Kind != domain.KindSoulTemplate {
			continue
		}
		template := ParseTemplateEvent(event)
		if template == nil || !templateMatchesRef(template, templateRef) {
			continue
		}
		if latest == nil || template.UpdatedAt.After(latest.UpdatedAt) || (template.UpdatedAt.Equal(latest.UpdatedAt) && template.EventID > latest.EventID) {
			latest = template
		}
	}
	return latest, nil
}

func (r *Reactor) findExistingProvisioningResult(ctx context.Context, requestEvent *nostr.Event) (*nostr.Event, error) {
	if requestEvent == nil {
		return nil, nil
	}
	factoryPubkey := strings.TrimSpace(r.config.SoulFactoryPubkey)
	requestEventID := requestEvent.ID.Hex()
	if r.findProvisioningResultFn != nil {
		result, err := r.findProvisioningResultFn(ctx, requestEventID)
		if err != nil || !authoritativeProvisioningResult(result, requestEvent, factoryPubkey) {
			return nil, err
		}
		return result, nil
	}
	bus := r.relayBus
	if bus == nil {
		return nil, nil
	}
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindProvisioningResult)},
		Tags:  nostr.TagMap{tagEvent: []string{requestEventID}},
		Limit: 5,
	}
	if factoryPubkey != "" {
		parsed, err := nostr.PubKeyFromHex(factoryPubkey)
		if err != nil {
			return nil, fmt.Errorf("invalid Soul Factory pubkey for provisioning result lookup: %w", err)
		}
		filter.Authors = []nostr.PubKey{parsed}
	}
	results, err := bus.Query(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if authoritativeProvisioningResult(result, requestEvent, factoryPubkey) {
			return result, nil
		}
	}
	return nil, nil
}

func authoritativeProvisioningResult(result, requestEvent *nostr.Event, factoryPubkey string) bool {
	factoryPubkey = strings.TrimSpace(factoryPubkey)
	if factoryPubkey == "" || result == nil || requestEvent == nil || result.Kind != nostr.Kind(domain.KindProvisioningResult) {
		return false
	}
	if result.PubKey.Hex() != factoryPubkey {
		return false
	}
	if tagValue(result.Tags, tagEvent) != requestEvent.ID.Hex() || tagValue(result.Tags, tagPubkey) != requestEvent.PubKey.Hex() {
		return false
	}
	requestKind := tagValue(result.Tags, tagRequestKind)
	if requestKind != "" && requestKind != strconv.Itoa(domain.KindProvisioningRequest) {
		return false
	}
	return isProvisioningTerminalStatus(tagValue(result.Tags, tagStatus))
}

func isProvisioningTerminalStatus(status string) bool {
	switch status {
	case "success", "error", "failed", "completed":
		return true
	default:
		return false
	}
}

func draftLookupFilters(draftRef, draftEventID string) []nostr.Filter {
	if draftEventID != "" {
		parsed, err := nostr.IDFromHex(draftEventID)
		if err == nil {
			return []nostr.Filter{{IDs: []nostr.ID{parsed}, Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulDraft)}, Limit: 1}}
		}
		return []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulDraft)}, Tags: nostr.TagMap{tagEvent: []string{draftEventID}}, Limit: 1}}
	}
	author, identifier, ok := parseParameterizedRef(domain.KindSoulDraft, draftRef)
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulDraft)}, Limit: 1}
	if ok && author != "" {
		if parsed, err := nostr.PubKeyFromHex(author); err == nil {
			filter.Authors = []nostr.PubKey{parsed}
		}
	}
	if identifier != "" {
		filter.Tags = nostr.TagMap{tagParameterizedD: []string{identifier}}
	}
	if identifier == "" && draftRef != "" {
		filter.Tags = nostr.TagMap{tagParameterizedD: []string{draftRef}}
	}
	return []nostr.Filter{filter}
}

func templateLookupFilters(templateRef string) []nostr.Filter {
	if templateRef == "" {
		return nil
	}
	author, identifier, ok := parseParameterizedRef(domain.KindSoulTemplate, templateRef)
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulTemplate)}, Limit: 1}
	if ok && author != "" {
		if parsed, err := nostr.PubKeyFromHex(author); err == nil {
			filter.Authors = []nostr.PubKey{parsed}
		}
	}
	if identifier != "" {
		filter.Tags = nostr.TagMap{tagParameterizedD: []string{identifier}}
	} else {
		filter.Tags = nostr.TagMap{tagParameterizedD: []string{templateRef}}
	}
	return []nostr.Filter{filter}
}

func parseParameterizedRef(kind int, ref string) (author, identifier string, ok bool) {
	ref = strings.TrimSpace(ref)
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) == 3 && parts[0] == strconv.Itoa(kind) {
		return strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), true
	}
	if len(parts) == 2 && len(parts[0]) == 64 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
	}
	return "", ref, false
}

func draftMatchesRef(draft *domain.SoulDraft, ref string) bool {
	if ref == "" || draft == nil {
		return true
	}
	author, identifier, ok := parseParameterizedRef(domain.KindSoulDraft, ref)
	if ok && author != "" && draft.CreatedBy != author {
		return false
	}
	return identifier == "" || draft.AgentID == identifier || draft.EventID == ref
}

func templateMatchesRef(template *domain.SoulTemplate, ref string) bool {
	if ref == "" || template == nil {
		return true
	}
	author, identifier, ok := parseParameterizedRef(domain.KindSoulTemplate, ref)
	if ok && author != "" && template.Author != author {
		return false
	}
	return identifier == "" || template.Identifier == identifier || template.EventID == ref
}

func draftTemplateRef(draft *domain.SoulDraft) string {
	if draft == nil {
		return ""
	}
	return draft.TemplateRef
}

func parameterizedCoordinate(kind int, author, identifier string) string {
	if strings.TrimSpace(author) == "" || strings.TrimSpace(identifier) == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s:%s", kind, strings.TrimSpace(author), strings.TrimSpace(identifier))
}

func hashDraftContent(content domain.SoulDraftContent) (string, error) {
	body, err := content.ToJSON()
	if err != nil {
		return "", fmt.Errorf("hash draft content: %w", err)
	}
	digest := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
