package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
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
	ExpectedAuthor   string `json:"expected_author,omitempty"` // when set, reject results not signed by this pubkey
}

type SoulFactoryTransport interface {
	Publish(context.Context, nostr.Event) (int, error)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*RelaySubscription, error)
	Close()
}

type staticSoulSigner struct {
	privateKey string
}

func (s staticSoulSigner) Sign(_ context.Context, event *nostr.Event) error {
	secret, err := nostr.SecretKeyFromHex(s.privateKey)
	if err != nil {
		return fmt.Errorf("parse soul factory private key: %w", err)
	}
	return event.Sign(secret)
}

type soulClientSigner interface {
	Sign(context.Context, *nostr.Event) error
}

type NostrClient struct {
	relays                []string
	signer                soulClientSigner
	transport             SoulFactoryTransport
	expectedFactoryPubkey string // when set, reject results not signed by this pubkey
	// replyTimeout bounds each wait for a terminal reply; zero means
	// DefaultSoulFactoryReplyTimeout.
	replyTimeout time.Duration
}

// WithReplyTimeout bounds how long AwaitProvisioningResult, ExecuteSoulAction
// and AwaitSoulActionResult wait for a terminal result. A wait that ends first
// returns *NoTerminalResultError. A non-positive timeout restores
// DefaultSoulFactoryReplyTimeout; an earlier context deadline always wins.
func (c *NostrClient) WithReplyTimeout(timeout time.Duration) *NostrClient {
	c.replyTimeout = timeout
	return c
}

// WithExpectedFactoryPubkey sets the expected SoulFactory service pubkey.
// When set, terminal result events not signed by this pubkey are silently
// dropped, hardening against spoofed results from untrusted authors.
func (c *NostrClient) WithExpectedFactoryPubkey(pubkey string) *NostrClient {
	c.expectedFactoryPubkey = strings.TrimSpace(pubkey)
	return c
}

func NewNostrClient(relays []string, signer soulClientSigner) (*NostrClient, error) {
	relays = normalizeSoulRelays(relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one Soul Factory relay is required")
	}
	if signer == nil {
		return nil, fmt.Errorf("soul factory signer is required")
	}
	relayClient, err := NewRelayClient(relays, WithRelaySigner(signer))
	if err != nil {
		return nil, err
	}
	return &NostrClient{
		relays:    relays,
		signer:    signer,
		transport: relayClient,
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
	relayClient, err := NewRelayClient(relays, WithRelaySigner(signer))
	if err != nil {
		return nil, err
	}
	return &NostrClient{
		relays:    relays,
		signer:    signer,
		transport: relayClient,
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
	filters := []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(domain.KindAgentSoul)}, Limit: limit}}
	events, err := c.collectEvents(ctx, "client.list_souls", filters)
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
		key := soulKey(soul.AgentID, ev.PubKey.Hex())
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
	events, err := c.collectEvents(ctx, "client.get_soul", []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(domain.KindAgentSoul)}, Tags: nostr.TagMap{tagParameterizedD: []string{agentID}}, Limit: 10}})
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
	events, err := c.collectEvents(ctx, "client.list_templates", []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(domain.KindSoulTemplate)}, Limit: limit}})
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
		key := soulKey(template.Identifier, ev.PubKey.Hex())
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
		{tagMethod, RuntimeMethodProvision},
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
	event := &nostr.Event{Kind: nostr.Kind(domain.KindProvisioningRequest), CreatedAt: now, Tags: tags, Content: string(content)}
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
	return &SoulFactoryRequestReceipt{RequestID: event.ID.Hex(), RequesterPubkey: event.PubKey.Hex(), AcceptedRelays: published, StatusKind: domain.KindProvisioningStatus, ResultKind: domain.KindProvisioningResult}, nil
}

func (c *NostrClient) AwaitProvisioningResult(ctx context.Context, receipt *SoulFactoryRequestReceipt, onStatus func(SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
	if receipt == nil || receipt.RequestID == "" || receipt.RequesterPubkey == "" {
		return nil, fmt.Errorf("valid provisioning receipt is required")
	}
	filters := []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(domain.KindProvisioningStatus), nostr.Kind(domain.KindProvisioningResult)}, Tags: nostr.TagMap{tagEvent: []string{receipt.RequestID}, tagPubkey: []string{receipt.RequesterPubkey}}}}
	reply, err := c.awaitTerminal(ctx, receipt.RequestID, filters, map[int]bool{domain.KindProvisioningStatus: true}, map[int]bool{domain.KindProvisioningResult: true}, func(ev *nostr.Event) {
		if onStatus != nil {
			onStatus(statusEventFromNostr(ev))
		}
	}, receipt.ExpectedAuthor)
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
	event := &nostr.Event{Kind: nostr.Kind(domain.KindSoulAction), CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := signGoNostrEvent(ctx, c.signer, event); err != nil {
		return nil, fmt.Errorf("sign soul action: %w", err)
	}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, soulActionReplyFilters(event.ID.Hex()))
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
	return c.awaitSoulActionReply(ctx, sub, event.ID.Hex())
}

// AwaitSoulActionResult waits for the terminal result of an already published
// soul action, for example after ExecuteSoulAction returned
// *NoTerminalResultError. The result is a stored event, so the subscription's
// backfill returns it if the factory has published it since.
func (c *NostrClient) AwaitSoulActionResult(ctx context.Context, requestID string) (*nostr.Event, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("soul action request id is required")
	}
	if c == nil || c.transport == nil {
		return nil, fmt.Errorf("soul factory client is not configured")
	}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, soulActionReplyFilters(requestID))
	if err != nil {
		return nil, fmt.Errorf("subscribe for soul action result: %w", err)
	}
	defer sub.Close()
	return c.awaitSoulActionReply(ctx, sub, requestID)
}

func soulActionReplyFilters(requestID string) []nostr.Filter {
	return []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(domain.KindProvisioningStatus), nostr.Kind(domain.KindProvisioningResult), nostr.Kind(domain.KindSoulActionLegacyResult)}, Tags: nostr.TagMap{tagEvent: []string{requestID}}}}
}

// awaitSoulActionReply waits on sub for the soul action's terminal result,
// bounded by the client's reply timeout.
func (c *NostrClient) awaitSoulActionReply(ctx context.Context, sub *RelaySubscription, requestID string) (*nostr.Event, error) {
	return awaitTerminalReply(ctx, sub, requestID, c.replyTimeout, DefaultSoulFactoryReplyTimeout, func(reply *nostr.Event) replyClass {
		if !validSignedEvent(reply) || !tagHasValue(reply.Tags, "e", requestID) {
			return replyIgnore
		}
		if c.expectedFactoryPubkey != "" && reply.PubKey.Hex() != c.expectedFactoryPubkey {
			return replyIgnore
		}
		if domain.IsLifecycleResultKind(int(reply.Kind)) {
			return replyTerminal
		}
		return replyIgnore
	}, nil)
}

// collectEvents returns the valid stored events matching filters for the
// client's display reads (CLI and MCP listings and lookups). They are
// latest-wins reads, so a partial read is accepted under RelayReadLatestQuorum
// and logged as degraded; see RelayReadPolicy. Actions are not decided on these
// reads: the factory re-reads authoritative state itself.
func (c *NostrClient) collectEvents(ctx context.Context, caller string, filters []nostr.Filter) ([]*nostr.Event, error) {
	if c == nil || c.transport == nil {
		return nil, fmt.Errorf("soul factory client is not configured")
	}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	stored, err := sub.CollectStoredEvents(ctx)
	read, err := resolveRelayRead(ctx, nil, caller, RelayReadLatestQuorum(), stored, err)
	if err != nil {
		return nil, err
	}
	return uniqueValidRelayEvents(read.Events), nil
}

// awaitTerminal subscribes with filters and waits for a terminal reply to
// requestID, bounded by the client's reply timeout (see WithReplyTimeout). A
// wait that ends first returns *NoTerminalResultError.
func (c *NostrClient) awaitTerminal(ctx context.Context, requestID string, filters []nostr.Filter, statusKinds, terminalKinds map[int]bool, onStatus func(*nostr.Event), expectedAuthor ...string) (*nostr.Event, error) {
	var authorCheck string
	if len(expectedAuthor) > 0 {
		authorCheck = strings.TrimSpace(expectedAuthor[0])
	}
	sub, err := c.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	return awaitTerminalReply(ctx, sub, requestID, c.replyTimeout, DefaultSoulFactoryReplyTimeout, func(ev *nostr.Event) replyClass {
		if !validSignedEvent(ev) {
			return replyIgnore
		}
		if authorCheck != "" && ev.PubKey.Hex() != authorCheck {
			return replyIgnore
		}
		switch {
		case statusKinds[int(ev.Kind)]:
			return replyStatus
		case terminalKinds[int(ev.Kind)]:
			return replyTerminal
		}
		return replyIgnore
	}, onStatus)
}

func ParseSoulEvent(event *nostr.Event) *domain.AgentSoul {
	if event == nil {
		return nil
	}
	soul := &domain.AgentSoul{EventID: event.ID.Hex(), SoulMD: event.Content, CreatedAt: event.CreatedAt.Time()}
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
			if kind, err := strconv.Atoi(tag[1]); err == nil {
				soul.AllowedKinds = append(soul.AllowedKinds, kind)
			}
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
	template := &domain.SoulTemplate{EventID: event.ID.Hex(), Author: event.PubKey.Hex(), BasePrompt: event.Content, CreatedAt: event.CreatedAt.Time(), UpdatedAt: event.CreatedAt.Time()}
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
			if kind, err := strconv.Atoi(tag[1]); err == nil {
				template.DefaultKinds = append(template.DefaultKinds, kind)
			}
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
	return SoulFactoryStatusEvent{Kind: int(event.Kind), EventID: event.ID.Hex(), Status: firstValue(tags, "status"), Step: firstValue(tags, "step"), Action: firstValue(tags, "action"), Message: strings.TrimSpace(event.Content), Tags: tags}
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
	return event.VerifySignature()
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
	if event.PubKey == (nostr.PubKey{}) {
		return fmt.Errorf("signed event is missing pubkey")
	}
	return nil
}
