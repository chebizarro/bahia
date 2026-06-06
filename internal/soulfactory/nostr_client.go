package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	pkgclient "github.com/openagentsinc/bahia/pkg/client"
)

type SoulFactoryStatusEvent struct {
	Kind    int
	EventID string
	Status  string
	Step    string
	Action  string
	Message string
	Tags    map[string][]string
}

type SoulFactoryRequestReceipt struct {
	RequestID        string `json:"request_id"`
	RequesterPubkey  string `json:"requester_pubkey"`
	AcceptedRelays   int    `json:"accepted_relays"`
	StatusKind       int    `json:"status_kind,omitempty"`
	ResultKind       int    `json:"result_kind,omitempty"`
	ActionResultKind int    `json:"action_result_kind,omitempty"`
}

type SoulFactoryTransport interface {
	Publish(context.Context, nostr.Event) (int, error)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*RelayBusSubscription, error)
	Close()
}

type staticSoulSigner struct {
	privateKey string
}

func (s staticSoulSigner) Sign(_ context.Context, event *nostr.Event) error {
	return event.Sign(s.privateKey)
}

type soulClientSigner interface {
	Sign(context.Context, *nostr.Event) error
}

type NostrClient struct {
	relays    []string
	signer    soulClientSigner
	transport SoulFactoryTransport
}

func NewNostrClient(relays []string, signer soulClientSigner) (*NostrClient, error) {
	relays = normalizeSoulRelays(relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one Soul Factory relay is required")
	}
	if signer == nil {
		return nil, fmt.Errorf("soul factory signer is required")
	}
	bus, err := NewSoulFactoryRelayBus(relays, WithRelayBusSigner(signer))
	if err != nil {
		return nil, err
	}
	return &NostrClient{
		relays:    relays,
		signer:    signer,
		transport: bus,
	}, nil
}

func NewNostrClientFromPrivateKey(relays []string, privateKey string) (*NostrClient, error) {
	normalized, err := pkgclient.NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	relays = normalizeSoulRelays(relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one Soul Factory relay is required")
	}
	signer := staticSoulSigner{privateKey: normalized}
	bus, err := NewSoulFactoryRelayBus(relays, WithRelayBusSigner(signer))
	if err != nil {
		return nil, err
	}
	return &NostrClient{
		relays:    relays,
		signer:    signer,
		transport: bus,
	}, nil
}

func (c *NostrClient) Close() {
	if c != nil && c.transport != nil {
		c.transport.Close()
	}
}

func (c *NostrClient) ListSouls(ctx context.Context, limit int, status string) ([]domain.AgentSoul, error) {
	if limit <= 0 {
		limit = 50
	}
	filters := []nostr.Filter{{Kinds: []int{domain.KindAgentSoul}, Limit: limit}}
	events, err := c.collectEvents(ctx, filters)
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.AgentSoul{}
	for _, ev := range events {
		soul := ParseSoulEvent(ev)
		if soul == nil || soul.AgentID == "" {
			continue
		}
		if status != "" && string(soul.Status) != status {
			continue
		}
		key := soulKey(soul.AgentID, ev.PubKey)
		current, ok := latest[key]
		if !ok || soul.CreatedAt.After(current.CreatedAt) {
			latest[key] = *soul
		}
	}
	result := make([]domain.AgentSoul, 0, len(latest))
	for _, soul := range latest {
		result = append(result, soul)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (c *NostrClient) GetSoul(ctx context.Context, agentID string) (*domain.AgentSoul, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	events, err := c.collectEvents(ctx, []nostr.Filter{{Kinds: []int{domain.KindAgentSoul}, Tags: nostr.TagMap{"d": []string{agentID}}, Limit: 10}})
	if err != nil {
		return nil, err
	}
	var latest *domain.AgentSoul
	for _, ev := range events {
		soul := ParseSoulEvent(ev)
		if soul == nil || soul.AgentID != agentID {
			continue
		}
		if latest == nil || soul.CreatedAt.After(latest.CreatedAt) {
			latest = soul
		}
	}
	return latest, nil
}

func (c *NostrClient) ListTemplates(ctx context.Context, limit int, tier string) ([]domain.SoulTemplate, error) {
	if limit <= 0 {
		limit = 50
	}
	events, err := c.collectEvents(ctx, []nostr.Filter{{Kinds: []int{domain.KindSoulTemplate}, Limit: limit}})
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.SoulTemplate{}
	for _, ev := range events {
		template := ParseTemplateEvent(ev)
		if template == nil || template.Identifier == "" {
			continue
		}
		if tier != "" && string(template.Tier) != tier {
			continue
		}
		key := soulKey(template.Identifier, ev.PubKey)
		current, ok := latest[key]
		if !ok || template.UpdatedAt.After(current.UpdatedAt) {
			latest[key] = *template
		}
	}
	result := make([]domain.SoulTemplate, 0, len(latest))
	for _, template := range latest {
		result = append(result, template)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (c *NostrClient) PublishProvisionRequest(ctx context.Context, req domain.ProvisioningRequest) (*SoulFactoryRequestReceipt, error) {
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	name := firstNonEmpty(strings.TrimSpace(req.Name), agentID)
	tier := req.Tier
	if tier == "" {
		tier = domain.SoulTierStandard
	}
	now := nostr.Now()
	templateRef := strings.TrimSpace(req.TemplateRef)
	draftRef := strings.TrimSpace(req.DraftRef)
	draftEventID := strings.TrimSpace(req.DraftEventID)
	specHash := strings.TrimSpace(req.SpecHash)
	runtimePubkey := strings.TrimSpace(req.Runtime.RuntimePubkey)
	capabilityRef := strings.TrimSpace(req.Runtime.CapabilityRef)
	tags := nostr.Tags{
		{tagAgentID, agentID},
		{tagName, name},
		{tagTier, string(tier)},
		{"output", "application/json"},
		{"method", RuntimeMethodProvision},
		{tagRequestKind, fmt.Sprint(domain.KindProvisioningRequest)},
	}
	if templateRef != "" {
		tags = append(tags, nostr.Tag{tagTemplate, templateRef})
	}
	if draftRef != "" {
		tags = append(tags, nostr.Tag{tagDraft, draftRef})
	}
	if draftEventID != "" {
		tags = append(tags, nostr.Tag{tagDraftEvent, draftEventID}, nostr.Tag{tagEvent, draftEventID, "", "draft"})
	}
	if specHash != "" {
		tags = append(tags, nostr.Tag{tagSpecHash, specHash})
	}
	if req.Runtime.Target != "" {
		tags = append(tags, nostr.Tag{tagRuntime, string(req.Runtime.Target)})
	}
	if runtimePubkey != "" {
		tags = append(tags, nostr.Tag{tagRuntimePubkey, runtimePubkey})
	}
	if capabilityRef != "" {
		tags = append(tags, nostr.Tag{tagCapability, capabilityRef})
	}
	content, err := json.Marshal(map[string]interface{}{
		"schema":         "soulfactory-provisioning/v1",
		"method":         RuntimeMethodProvision,
		"agent_id":       agentID,
		"name":           name,
		"tier":           string(tier),
		"template_ref":   templateRef,
		"draft_ref":      draftRef,
		"draft_event_id": draftEventID,
		"spec_hash":      specHash,
		"brief":          strings.TrimSpace(req.Brief),
		"requested_at":   int64(now),
	})
	if err != nil {
		return nil, err
	}
	event := &nostr.Event{Kind: domain.KindProvisioningRequest, CreatedAt: now, Tags: tags, Content: string(content)}
	if err := signGoNostrEvent(ctx, c.signer, event); err != nil {
		return nil, fmt.Errorf("sign provisioning request: %w", err)
	}
	published, err := c.transport.Publish(ctx, *event)
	if published == 0 {
		if err == nil {
			err = fmt.Errorf("request was not accepted by any relay")
		}
		return nil, err
	}
	return &SoulFactoryRequestReceipt{RequestID: event.ID, RequesterPubkey: event.PubKey, AcceptedRelays: published, StatusKind: domain.KindProvisioningStatus, ResultKind: domain.KindProvisioningResult}, nil
}

func (c *NostrClient) AwaitProvisioningResult(ctx context.Context, receipt *SoulFactoryRequestReceipt, onStatus func(SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
	if receipt == nil || receipt.RequestID == "" || receipt.RequesterPubkey == "" {
		return nil, fmt.Errorf("valid provisioning receipt is required")
	}
	filters := []nostr.Filter{{Kinds: []int{domain.KindProvisioningStatus, domain.KindProvisioningResult}, Tags: nostr.TagMap{"e": []string{receipt.RequestID}, "p": []string{receipt.RequesterPubkey}}}}
	reply, err := c.awaitTerminal(ctx, filters, map[int]bool{domain.KindProvisioningStatus: true}, map[int]bool{domain.KindProvisioningResult: true}, func(ev *nostr.Event) {
		if onStatus != nil {
			onStatus(statusEventFromNostr(ev))
		}
	})
	if err != nil {
		return nil, err
	}
	return provisioningRunFromTerminalEvent(receipt.RequestID, reply), nil
}

func (c *NostrClient) ExecuteSoulAction(ctx context.Context, soulRef string, action domain.SoulActionType, reason string, newBrief string) (*nostr.Event, error) {
	soulRef = strings.TrimSpace(soulRef)
	if soulRef == "" {
		return nil, fmt.Errorf("soul_ref is required")
	}
	if strings.TrimSpace(string(action)) == "" {
		return nil, fmt.Errorf("action is required")
	}
	tags := nostr.Tags{{"soul", soulRef}, {"action", string(action)}}
	if strings.TrimSpace(reason) != "" {
		tags = append(tags, nostr.Tag{"reason", strings.TrimSpace(reason)})
	}
	content := ""
	if strings.TrimSpace(newBrief) != "" {
		body, err := json.Marshal(map[string]string{"brief": strings.TrimSpace(newBrief)})
		if err != nil {
			return nil, err
		}
		content = string(body)
	}
	event := &nostr.Event{Kind: domain.KindSoulAction, CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := signGoNostrEvent(ctx, c.signer, event); err != nil {
		return nil, fmt.Errorf("sign soul action: %w", err)
	}
	filters := []nostr.Filter{{Kinds: []int{domain.KindProvisioningStatus, domain.KindProvisioningResult, domain.KindSoulActionLegacyResult}, Tags: nostr.TagMap{"e": []string{event.ID}}}}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("subscribe for soul action result: %w", err)
	}
	defer sub.Close()
	published, err := c.transport.Publish(ctx, *event)
	if published == 0 {
		if err == nil {
			err = fmt.Errorf("request was not accepted by any relay")
		}
		return nil, err
	}
	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-eose:
			eose = nil
		case reply, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("soul action subscription closed before terminal result")
			}
			if !validSignedEvent(reply) || !tagHasValue(reply.Tags, "e", event.ID) {
				continue
			}
			if _, duplicate := seen[reply.ID]; duplicate {
				continue
			}
			seen[reply.ID] = struct{}{}
			if reply.Kind == domain.KindProvisioningStatus {
				continue
			}
			if domain.IsLifecycleResultKind(reply.Kind) {
				return reply, nil
			}
		}
	}
}

func (c *NostrClient) collectEvents(ctx context.Context, filters []nostr.Filter) ([]*nostr.Event, error) {
	if c == nil || c.transport == nil {
		return nil, fmt.Errorf("soul factory client is not configured")
	}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	var result []*nostr.Event
	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-eose:
			return result, nil
		case ev, ok := <-sub.Events:
			if !ok {
				return result, nil
			}
			if ev == nil || !validSignedEvent(ev) {
				continue
			}
			if _, duplicate := seen[ev.ID]; duplicate {
				continue
			}
			seen[ev.ID] = struct{}{}
			result = append(result, ev)
		}
	}
}

func (c *NostrClient) awaitTerminal(ctx context.Context, filters []nostr.Filter, statusKinds, terminalKinds map[int]bool, onStatus func(*nostr.Event)) (*nostr.Event, error) {
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-eose:
			eose = nil
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("reply subscription closed before terminal result")
			}
			if ev == nil || !validSignedEvent(ev) {
				continue
			}
			if _, duplicate := seen[ev.ID]; duplicate {
				continue
			}
			seen[ev.ID] = struct{}{}
			if statusKinds[ev.Kind] {
				if onStatus != nil {
					onStatus(ev)
				}
				continue
			}
			if terminalKinds[ev.Kind] {
				return ev, nil
			}
		}
	}
}

func ParseSoulEvent(event *nostr.Event) *domain.AgentSoul {
	if event == nil {
		return nil
	}
	soul := &domain.AgentSoul{EventID: event.ID, SoulMD: event.Content, CreatedAt: event.CreatedAt.Time()}
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "d":
			soul.AgentID = tag[1]
		case "name":
			soul.Name = tag[1]
		case "purpose":
			soul.Purpose = tag[1]
		case "tier":
			soul.Tier = domain.SoulTier(tag[1])
		case "status":
			soul.Status = domain.SoulStatus(tag[1])
		case "p":
			if len(tag) > 2 && tag[2] == "agent" {
				soul.NostrPubkey = tag[1]
			}
		case "npub":
			soul.NostrNpub = tag[1]
		case "nip05":
			soul.NIP05 = tag[1]
		case "bunker":
			soul.BunkerURI = tag[1]
		case "avatar":
			soul.AvatarURL = tag[1]
		case "soul-blob":
			soul.SoulBlobHash = tag[1]
		case "qdrant":
			soul.QdrantCollection = tag[1]
		case "workspace":
			soul.WorkspaceRepoURL = tag[1]
		case "template":
			soul.TemplateRef = tag[1]
		case "draft-event":
			soul.DraftEventID = tag[1]
		case "deploy-status":
			soul.DeployStatus = tag[1]
		case "service", "bahia-service":
			if id, err := uuid.Parse(tag[1]); err == nil {
				soul.BahiaServiceID = &id
			}
		case "allowed-kind":
			var kind int
			fmt.Sscanf(tag[1], "%d", &kind)
			soul.AllowedKinds = append(soul.AllowedKinds, kind)
		case "tool":
			grant := domain.ToolGrant{MCPServer: tag[1]}
			if len(tag) > 2 {
				grant.Scopes = tag[2:]
			}
			soul.ToolGrants = append(soul.ToolGrants, grant)
		}
	}
	if soul.Status == "" {
		soul.Status = domain.SoulStatusActive
	}
	if soul.Tier == "" {
		soul.Tier = domain.SoulTierStandard
	}
	return soul
}

func ParseTemplateEvent(event *nostr.Event) *domain.SoulTemplate {
	if event == nil {
		return nil
	}
	template := &domain.SoulTemplate{EventID: event.ID, Author: event.PubKey, BasePrompt: event.Content, CreatedAt: event.CreatedAt.Time(), UpdatedAt: event.CreatedAt.Time()}
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "d":
			template.Identifier = tag[1]
		case "name":
			template.Name = tag[1]
		case "description":
			template.Description = tag[1]
		case "tier":
			template.Tier = domain.SoulTier(tag[1])
		case "t":
			template.Tags = append(template.Tags, tag[1])
		case "default-kind":
			var kind int
			fmt.Sscanf(tag[1], "%d", &kind)
			template.DefaultKinds = append(template.DefaultKinds, kind)
		case "tool":
			grant := domain.ToolGrant{MCPServer: tag[1]}
			if len(tag) > 2 {
				grant.Scopes = tag[2:]
			}
			template.DefaultTools = append(template.DefaultTools, grant)
		}
	}
	if template.Tier == "" {
		template.Tier = domain.SoulTierStandard
	}
	return template
}

func provisioningRunFromTerminalEvent(requestID string, event *nostr.Event) *domain.ProvisioningRun {
	status := domain.ProvisioningStatusFailed
	message := strings.TrimSpace(event.Content)
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "status" && tag[1] == "success" {
			status = domain.ProvisioningStatusCompleted
		}
	}
	run := &domain.ProvisioningRun{RequestID: requestID, Status: status}
	if status == domain.ProvisioningStatusCompleted {
		run.Error = ""
		if strings.TrimSpace(message) != "" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(message), &payload); err == nil {
				if agentID, ok := payload["agentId"].(string); ok {
					run.AgentID = agentID
				}
			}
		}
	} else {
		run.Error = firstNonEmpty(message, firstTagValue(event.Tags, "error"), "provisioning failed")
	}
	return run
}

func statusEventFromNostr(event *nostr.Event) SoulFactoryStatusEvent {
	tags := tagMap(event.Tags)
	return SoulFactoryStatusEvent{Kind: event.Kind, EventID: event.ID, Status: firstValue(tags, "status"), Step: firstValue(tags, "step"), Action: firstValue(tags, "action"), Message: strings.TrimSpace(event.Content), Tags: tags}
}

func validSignedEvent(event *nostr.Event) bool {
	if event == nil || !event.CheckID() {
		return false
	}
	now := int64(nostr.Now())
	createdAt := int64(event.CreatedAt)
	if createdAt > now+600 || createdAt < now-365*24*60*60 {
		return false
	}
	ok, err := event.CheckSignature()
	return err == nil && ok
}

func tagHasValue(tags nostr.Tags, name, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}

func tagMap(tags nostr.Tags) map[string][]string {
	mapped := make(map[string][]string)
	for _, tag := range tags {
		if len(tag) < 2 {
			continue
		}
		mapped[tag[0]] = append(mapped[tag[0]], tag[1:]...)
	}
	return mapped
}

func firstValue(tags map[string][]string, key string) string {
	values := tags[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func firstTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func normalizeSoulRelays(relays []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(relays))
	for _, relay := range relays {
		relay = strings.TrimSpace(relay)
		if relay == "" {
			continue
		}
		key := strings.TrimRight(relay, "/")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func soulKey(identifier, author string) string {
	return strings.TrimSpace(author) + ":" + strings.TrimSpace(identifier)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func signGoNostrEvent(ctx context.Context, signer soulClientSigner, event *nostr.Event) error {
	if err := signer.Sign(ctx, event); err != nil {
		return err
	}
	if event.PubKey == "" {
		return fmt.Errorf("signed event is missing pubkey")
	}
	return nil
}
