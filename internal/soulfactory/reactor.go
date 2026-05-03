// Package soulfactory implements the Soul Factory agent provisioning system.
//
// Soul Factory is a Nostr-native agent provisioning service that:
// - Listens for provisioning requests (kind:5950)
// - Listens for lifecycle actions (kind:1950)
// - Orchestrates the provisioning workflow
// - Publishes progress (kind:6950) and results (kind:7950)
package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AuthorizedProvisioners is the list of pubkeys allowed to request provisioning.
var AuthorizedProvisioners = []string{
	"cdee943cbb19c51ab847a66d5d774373aa9f63d287246bb59b0827fa5e637400", // Biz
	"14907326f89ebdfc9cfdabe17bd492aa48abbd59ad5d8cc25295760bdf0e5015", // Stew
}

// SoulFactoryPubkey is the pubkey used by Soul Factory to sign events.
// This should be loaded from config in production.
var SoulFactoryPubkey = "14907326f89ebdfc9cfdabe17bd492aa48abbd59ad5d8cc25295760bdf0e5015"

// Config holds reactor configuration.
type Config struct {
	Relays            []string
	AdditionalRelays  []string // Supplemental relays for drafts and provisioning events
	SoulFactoryPubkey string
	AuthorizedPubkeys []string
	SignetBunkerURI   string
	BlossomURL        string
	QdrantURL         string
	AgentMemoryURL    string
	LemmyURL          string // FLUX avatar generation
}

// Reactor subscribes to Nostr events and dispatches handlers.
type Reactor struct {
	config      Config
	pool        *nostr.SimplePool
	generator   SoulGenerator
	signer      Signer
	provisioner ProvisioningEngine
	logger      *slog.Logger

	mu   sync.Mutex
	runs map[string]*domain.ProvisioningRun // requestID -> run
}

// SoulGenerator generates soul content from briefs.
type SoulGenerator interface {
	Generate(ctx context.Context, input domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error)
}

// ProvisioningEngine executes provisioning and lifecycle operations without
// allowing placeholder success paths.
type ProvisioningEngine interface {
	Provision(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error)
	SuspendSoul(ctx context.Context, soulRef, reason string) error
	ResumeSoul(ctx context.Context, soulRef string) error
	RevokeSoul(ctx context.Context, soulRef, reason string) error
	RegenerateSoul(ctx context.Context, soulRef, newBrief string) error
	RedeploySoul(ctx context.Context, soulRef string) error
}

// ReactorOption customizes a Reactor.
type ReactorOption func(*Reactor)

// WithProvisioningEngine installs an explicit provisioning engine. Without this
// option, provisioning and lifecycle requests fail closed instead of using the
// legacy partial provisioner.
func WithProvisioningEngine(engine ProvisioningEngine) ReactorOption {
	return func(r *Reactor) {
		if engine != nil {
			r.provisioner = engine
		}
	}
}

// Signer handles Nostr event signing via NIP-46.
type Signer interface {
	Sign(ctx context.Context, event *nostr.Event) error
	ProvisionAgent(ctx context.Context, agentID string, allowedKinds []int) (pubkey, npub, bunkerURI string, err error)
	RevokeAgent(ctx context.Context, pubkey string) error
	SuspendAgent(ctx context.Context, pubkey string) error
	ResumeAgent(ctx context.Context, pubkey string) error
}

// NewReactor creates a new Soul Factory reactor.
func NewReactor(config Config, generator SoulGenerator, signer Signer, logger *slog.Logger, opts ...ReactorOption) *Reactor {
	if logger == nil {
		logger = slog.Default()
	}

	r := &Reactor{
		config:      config,
		pool:        nostr.NewSimplePool(context.Background()),
		generator:   generator,
		signer:      signer,
		provisioner: unavailableProvisioningEngine{},
		logger:      logger.With("component", "soulfactory"),
		runs:        make(map[string]*domain.ProvisioningRun),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	return r
}

// Run starts the reactor and blocks until context is cancelled.
func (r *Reactor) Run(ctx context.Context) error {
	r.logger.Info("starting soul factory reactor",
		"relays", r.config.Relays,
		"additional_relays", r.config.AdditionalRelays,
	)

	// Subscribe to provisioning requests and actions
	now := nostr.Now()
	filters := []nostr.Filter{
		{
			Kinds: []int{domain.KindProvisioningRequest},
			Since: &now,
		},
		{
			Kinds: []int{domain.KindSoulAction},
			Since: &now,
		},
	}

	// Combine all relays for subscription
	allRelays := append(r.config.Relays, r.config.AdditionalRelays...)

	sub := r.pool.SubMany(ctx, allRelays, filters)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reactor shutting down")
			return ctx.Err()

		case ev, ok := <-sub:
			if !ok {
				r.logger.Warn("subscription closed; stopping soul factory reactor because reconnect semantics are not implemented here")
				return ErrSoulFactoryUnavailable
			}

			r.handleEvent(ctx, ev.Event)
		}
	}
}

// handleEvent dispatches events to the appropriate handler.
func (r *Reactor) handleEvent(ctx context.Context, event *nostr.Event) {
	switch event.Kind {
	case domain.KindProvisioningRequest:
		go r.handleProvisioningRequest(ctx, event)
	case domain.KindSoulAction:
		go r.handleSoulAction(ctx, event)
	default:
		r.logger.Warn("unexpected event kind", "kind", event.Kind)
	}
}

// handleProvisioningRequest processes a kind:5950 provisioning request.
func (r *Reactor) handleProvisioningRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received provisioning request")

	// Validate authorization
	if !r.isAuthorizedProvisioner(event.PubKey) {
		logger.Warn("unauthorized provisioning request")
		r.publishError(ctx, event, "unauthorized", "requester not in authorized provisioners list")
		return
	}

	// Parse request
	req, err := r.parseProvisioningRequest(event)
	if err != nil {
		logger.Error("failed to parse request", "error", err)
		r.publishError(ctx, event, "parse_error", err.Error())
		return
	}

	logger = logger.With("agent_id", req.AgentID)
	logger.Info("starting provisioning workflow")

	// Create run tracker
	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       event.ID,
		AgentID:         req.AgentID,
		Status:          domain.ProvisioningStatusRunning,
		CurrentStep:     domain.StepGenerate,
		Steps:           make([]domain.ProvisioningStepResult, 0, len(domain.ProvisioningSteps)),
		RequesterPubkey: event.PubKey,
		DraftRef:        req.DraftRef,
		StartedAt:       time.Now(),
	}

	r.mu.Lock()
	r.runs[event.ID] = run
	r.mu.Unlock()

	// Run provisioning workflow
	result, err := r.provisioner.Provision(ctx, req, run)
	if err != nil {
		logger.Error("provisioning failed", "error", err, "step", run.CurrentStep)
		run.Status = domain.ProvisioningStatusFailed
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		r.publishError(ctx, event, string(run.CurrentStep), err.Error())
		return
	}

	// Success
	run.Status = domain.ProvisioningStatusCompleted
	now := time.Now()
	run.CompletedAt = &now

	logger.Info("provisioning completed",
		"npub", result.NostrNpub,
		"service_id", result.BahiaServiceID,
	)

	r.publishResult(ctx, event, result)
}

// handleSoulAction processes a kind:1950 lifecycle action.
func (r *Reactor) handleSoulAction(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "initiator", event.PubKey)
	logger.Info("received soul action")

	// Validate authorization (only fleet operators)
	if !r.isAuthorizedProvisioner(event.PubKey) {
		logger.Warn("unauthorized soul action")
		return
	}

	// Parse action
	action, err := r.parseSoulAction(event)
	if err != nil {
		logger.Error("failed to parse action", "error", err)
		return
	}

	logger = logger.With("soul", action.SoulRef, "action", action.Action)
	logger.Info("executing soul action")

	// Execute action
	switch action.Action {
	case domain.SoulActionSuspend:
		if err := r.provisioner.SuspendSoul(ctx, action.SoulRef, action.Reason); err != nil {
			logger.Error("suspend failed", "error", err)
			r.publishActionError(ctx, event, action, err.Error())
		}
	case domain.SoulActionResume:
		if err := r.provisioner.ResumeSoul(ctx, action.SoulRef); err != nil {
			logger.Error("resume failed", "error", err)
			r.publishActionError(ctx, event, action, err.Error())
		}
	case domain.SoulActionRevoke:
		if err := r.provisioner.RevokeSoul(ctx, action.SoulRef, action.Reason); err != nil {
			logger.Error("revoke failed", "error", err)
			r.publishActionError(ctx, event, action, err.Error())
		}
	case domain.SoulActionRegenerate:
		if err := r.provisioner.RegenerateSoul(ctx, action.SoulRef, action.NewBrief); err != nil {
			logger.Error("regenerate failed", "error", err)
			r.publishActionError(ctx, event, action, err.Error())
		}
	case domain.SoulActionRedeploy:
		if err := r.provisioner.RedeploySoul(ctx, action.SoulRef); err != nil {
			logger.Error("redeploy failed", "error", err)
			r.publishActionError(ctx, event, action, err.Error())
		}
	default:
		logger.Warn("unknown action type", "action", action.Action)
		r.publishActionError(ctx, event, action, fmt.Sprintf("unknown action type: %s", action.Action))
	}
}

// isAuthorizedProvisioner checks if a pubkey is authorized to provision.
func (r *Reactor) isAuthorizedProvisioner(pubkey string) bool {
	provisioners := r.config.AuthorizedPubkeys
	if len(provisioners) == 0 {
		provisioners = AuthorizedProvisioners
	}
	return slices.Contains(provisioners, pubkey)
}

// parseProvisioningRequest extracts request data from a kind:5950 event.
func (r *Reactor) parseProvisioningRequest(event *nostr.Event) (*domain.ProvisioningRequest, error) {
	req := &domain.ProvisioningRequest{
		EventID:   event.ID,
		Requester: event.PubKey,
	}

	// Parse tags
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "agent-id":
			req.AgentID = tag[1]
		case "name":
			req.Name = tag[1]
		case "tier":
			req.Tier = domain.SoulTier(tag[1])
		case "template":
			req.TemplateRef = tag[1]
		case "draft":
			req.DraftRef = tag[1]
		}
	}

	// Parse content (for inline spec)
	if event.Content != "" {
		var content struct {
			Brief string `json:"brief"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err == nil {
			req.Brief = content.Brief
		}
	}

	// Validate required fields
	if req.AgentID == "" {
		return nil, fmt.Errorf("missing agent-id tag")
	}

	// If no inline brief, we need either a draft or a template
	if req.Brief == "" && req.DraftRef == "" && req.TemplateRef == "" {
		return nil, fmt.Errorf("must provide brief, draft, or template")
	}

	// Default tier
	if req.Tier == "" {
		req.Tier = domain.SoulTierStandard
	}

	return req, nil
}

// parseSoulAction extracts action data from a kind:1950 event.
func (r *Reactor) parseSoulAction(event *nostr.Event) (*domain.SoulAction, error) {
	action := &domain.SoulAction{
		ID:        domain.NewUUID(),
		EventID:   event.ID,
		Initiator: event.PubKey,
		CreatedAt: time.Now(),
	}

	// Parse tags
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "soul":
			action.SoulRef = tag[1]
		case "action":
			action.Action = domain.SoulActionType(tag[1])
		case "reason":
			action.Reason = tag[1]
		}
	}

	// Parse content (for regenerate with new brief)
	if event.Content != "" {
		var content struct {
			Brief string `json:"brief"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err == nil {
			action.NewBrief = content.Brief
		}
	}

	// Validate
	if action.SoulRef == "" {
		return nil, fmt.Errorf("missing soul tag")
	}
	if action.Action == "" {
		return nil, fmt.Errorf("missing action tag")
	}

	return action, nil
}

// publishStatus publishes a kind:6950 progress event.
func (r *Reactor) PublishStatus(ctx context.Context, requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string) error {
	event := &nostr.Event{
		Kind:      domain.KindProvisioningStatus,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "processing"},
			{"step", string(step)},
			{"progress", fmt.Sprintf("%d", current), fmt.Sprintf("%d", total)},
		},
		Content: message,
	}

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign status event: %w", err)
	}

	return r.publish(ctx, event, r.config.AdditionalRelays)
}

// publishResult publishes a kind:7950 success result event.
func (r *Reactor) publishResult(ctx context.Context, requestEvent *nostr.Event, soul *domain.AgentSoul) error {
	content, _ := json.Marshal(map[string]interface{}{
		"soul_id":           soul.AgentID,
		"npub":              soul.NostrNpub,
		"pubkey":            soul.NostrPubkey,
		"avatar_url":        soul.AvatarURL,
		"workspace_url":     soul.WorkspaceRepoURL,
		"qdrant_collection": soul.QdrantCollection,
		"bahia_service_id":  soul.BahiaServiceID,
	})

	tags := nostr.Tags{
		{"e", requestEvent.ID, "", "reply"},
		{"p", requestEvent.PubKey},
		{"status", "success"},
		{"soul", fmt.Sprintf("31951:%s:%s", SoulFactoryPubkey, soul.AgentID)},
		{"agent-pubkey", soul.NostrPubkey},
		{"npub", soul.NostrNpub},
	}
	if soul.BahiaServiceID != nil {
		tags = append(tags, nostr.Tag{"service", soul.BahiaServiceID.String()})
	}

	event := &nostr.Event{
		Kind:      domain.KindProvisioningResult,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   string(content),
	}

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign result event: %w", err)
	}

	// Publish to both supplemental and public relays
	allRelays := append(r.config.AdditionalRelays, r.config.Relays...)
	return r.publish(ctx, event, allRelays)
}

func (r *Reactor) publishActionError(ctx context.Context, sourceEvent *nostr.Event, action *domain.SoulAction, message string) error {
	content, _ := json.Marshal(map[string]interface{}{
		"error": message,
	})
	event := &nostr.Event{
		Kind:      domain.KindSoulAction + 1,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", sourceEvent.ID, "", "reply"},
			{"p", sourceEvent.PubKey},
			{"soul", action.SoulRef},
			{"action", string(action.Action)},
			{"status", "error"},
		},
		Content: string(content),
	}
	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign action error event: %w", err)
	}
	return r.publish(ctx, event, r.config.AdditionalRelays)
}

// publishError publishes a kind:7950 error result event.
func (r *Reactor) publishError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	event := &nostr.Event{
		Kind:      domain.KindProvisioningResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
			{"status", "error"},
			{"step", step},
			{"error", step},
		},
		Content: message,
	}

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign error event: %w", err)
	}

	return r.publish(ctx, event, r.config.AdditionalRelays)
}

// publishSoul publishes a kind:31951 agent soul event.
func (r *Reactor) PublishSoul(ctx context.Context, soul *domain.AgentSoul) error {
	tags := nostr.Tags{
		{"d", soul.AgentID},
		{"name", soul.Name},
		{"purpose", soul.Purpose},
		{"tier", string(soul.Tier)},
		{"status", string(soul.Status)},
		{"p", soul.NostrPubkey, "agent"},
		{"npub", soul.NostrNpub},
	}

	if soul.NIP05 != "" {
		tags = append(tags, nostr.Tag{"nip05", soul.NIP05})
	}
	if soul.BunkerURI != "" {
		tags = append(tags, nostr.Tag{"bunker", soul.BunkerURI})
	}
	if soul.AvatarURL != "" {
		tags = append(tags, nostr.Tag{"avatar", soul.AvatarURL})
	}
	if soul.SoulBlobHash != "" {
		tags = append(tags, nostr.Tag{"soul-blob", soul.SoulBlobHash})
	}
	if soul.QdrantCollection != "" {
		tags = append(tags, nostr.Tag{"qdrant", soul.QdrantCollection})
	}
	if soul.WorkspaceRepoURL != "" {
		tags = append(tags, nostr.Tag{"workspace", soul.WorkspaceRepoURL})
	}
	for _, kind := range soul.AllowedKinds {
		tags = append(tags, nostr.Tag{"allowed-kind", fmt.Sprintf("%d", kind)})
	}
	for _, grant := range soul.ToolGrants {
		tag := nostr.Tag{"tool", grant.MCPServer}
		tag = append(tag, grant.Scopes...)
		tags = append(tags, tag)
	}
	if soul.BahiaServiceID != nil {
		tags = append(tags, nostr.Tag{"service", soul.BahiaServiceID.String()})
	}
	if soul.TemplateRef != "" {
		tags = append(tags, nostr.Tag{"template", soul.TemplateRef})
	}
	if soul.DeployStatus != "" {
		tags = append(tags, nostr.Tag{"deploy-status", soul.DeployStatus})
	}

	event := &nostr.Event{
		Kind:      domain.KindAgentSoul,
		CreatedAt: nostr.Now(),
		Tags:      tags,
		Content:   soul.SoulMD,
	}

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign soul event: %w", err)
	}

	soul.EventID = event.ID

	// Publish to public relay
	return r.publish(ctx, event, r.config.Relays)
}

// publish sends an event to the specified relays.
func (r *Reactor) publish(ctx context.Context, event *nostr.Event, relays []string) error {
	for _, relay := range relays {
		if err := r.pool.PublishMany(ctx, []string{relay}, *event); err != nil {
			r.logger.Warn("failed to publish to relay", "relay", relay, "error", err)
			// Continue trying other relays
		}
	}
	return nil
}

// GetRun returns the current provisioning run for a request.
func (r *Reactor) GetRun(requestID string) *domain.ProvisioningRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[requestID]
}

// GetSoul fetches a soul by its d-tag (agent ID) from the relay network.
func (r *Reactor) GetSoul(ctx context.Context, agentID string) (*domain.AgentSoul, error) {
	// Query for the soul event using QuerySingle
	result := r.pool.QuerySingle(ctx, r.config.Relays, nostr.Filter{
		Kinds:   []int{domain.KindAgentSoul},
		Tags:    map[string][]string{"d": {agentID}},
		Authors: []string{r.config.SoulFactoryPubkey},
		Limit:   1,
	})

	if result == nil || result.Event == nil {
		return nil, nil
	}

	return r.parseSoulEvent(result.Event), nil
}

// parseSoulEvent converts a Nostr event to an AgentSoul.
func (r *Reactor) parseSoulEvent(event *nostr.Event) *domain.AgentSoul {
	soul := &domain.AgentSoul{
		EventID:   event.ID,
		SoulMD:    event.Content,
		CreatedAt: event.CreatedAt.Time(),
	}

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
		case "deploy-status":
			soul.DeployStatus = tag[1]
		case "service":
			if id, err := uuid.Parse(tag[1]); err == nil {
				soul.BahiaServiceID = &id
			}
		case "allowed-kind":
			var kind int
			fmt.Sscanf(tag[1], "%d", &kind)
			soul.AllowedKinds = append(soul.AllowedKinds, kind)
		case "tool":
			if len(tag) >= 2 {
				grant := domain.ToolGrant{MCPServer: tag[1]}
				if len(tag) > 2 {
					grant.Scopes = tag[2:]
				}
				soul.ToolGrants = append(soul.ToolGrants, grant)
			}
		}
	}

	return soul
}
