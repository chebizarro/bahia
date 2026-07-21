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
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Config holds reactor configuration.
type Config struct {
	Relays                        []string
	AdditionalRelays              []string // Supplemental relays for drafts and provisioning events
	SoulFactoryPubkey             string
	AuthorizedPubkeys             []string
	SignetBunkerURI               string
	BlossomURL                    string
	QdrantURL                     string
	AgentMemoryURL                string
	LemmyURL                      string // FLUX avatar generation
	PublishLegacyLifecycleResults bool   // migration-only 1951 alias for lifecycle action results
}

// Reactor subscribes to Nostr events and dispatches handlers.
type Reactor struct {
	config                   Config
	generator                SoulGenerator
	signer                   Signer
	provisioner              ProvisioningEngine
	lifecycleHandler         *LifecycleHandler
	logger                   *slog.Logger
	relayBus                 *SoulFactoryRelayBus
	publishFn                func(context.Context, *nostr.Event, []string) error
	getSoulFn                func(context.Context, string) (*domain.AgentSoul, error)
	getDraftFn               func(context.Context, string, string) (*domain.SoulDraft, error)
	getTemplateFn            func(context.Context, string) (*domain.SoulTemplate, error)
	findLifecycleResultFn    func(context.Context, string) (*nostr.Event, error)
	findProvisioningResultFn func(context.Context, string) (*nostr.Event, error)

	mu   sync.Mutex
	runs map[string]*domain.ProvisioningRun // requestID -> run
}

// SoulGenerator generates soul content from briefs.
type SoulGenerator interface {
	Generate(ctx context.Context, input domain.SoulGeneratorInput) (*domain.SoulGeneratorOutput, error)
}

// ProvisioningEngine executes provisioning without allowing placeholder success paths.
// Lifecycle actions are orchestrated exclusively by LifecycleHandler.
type ProvisioningEngine interface {
	Provision(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*domain.AgentSoul, error)
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

// WithLifecycleHandler installs the single lifecycle action orchestrator used
// for kind:1950 events. This is primarily for wiring integrations in tests and
// full provisioner construction; nil leaves the default local handler in place.
func WithLifecycleHandler(handler *LifecycleHandler) ReactorOption {
	return func(r *Reactor) {
		if handler != nil {
			r.lifecycleHandler = handler
		}
	}
}

// InstallProvisioningEngine installs the production provisioning engine after
// dependent adapters have been constructed around this reactor.
func (r *Reactor) InstallProvisioningEngine(engine ProvisioningEngine) error {
	if r == nil {
		return fmt.Errorf("soul factory reactor is not configured")
	}
	if engine == nil {
		return fmt.Errorf("soul factory provisioning engine is not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.provisioner = engine
	return nil
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
		generator:   generator,
		signer:      signer,
		provisioner: unavailableProvisioningEngine{},
		logger:      logger.With("component", "soulfactory"),
		runs:        make(map[string]*domain.ProvisioningRun),
	}
	if allRelays := normalizeSoulRelays(append(append([]string{}, config.Relays...), config.AdditionalRelays...)); len(allRelays) > 0 && signer != nil {
		if bus, err := NewSoulFactoryRelayBus(allRelays, WithRelayBusSigner(signer), WithRelayBusLogger(r.logger)); err == nil {
			r.relayBus = bus
		}
	}

	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	return r
}

func (r *Reactor) lifecycle() *LifecycleHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lifecycleHandler == nil {
		r.lifecycleHandler = NewLifecycleHandler(r, nil, nil, r.logger)
	}
	return r.lifecycleHandler
}

// Run starts the reactor and blocks until context is cancelled.
func (r *Reactor) Run(ctx context.Context) error {
	r.logger.Info("starting soul factory canonical ContextVM worker")
	<-ctx.Done()
	r.logger.Info("reactor shutting down")
	return ctx.Err()
}

// handleEvent dispatches events to the appropriate handler.
func (r *Reactor) handleEvent(ctx context.Context, event *nostr.Event) {
	if event.Kind != nostr.Kind(domain.KindProvisioningRequest) {
		r.logger.Warn("unexpected event kind", "kind", event.Kind)
		return
	}
	switch tagValue(event.Tags, "method") {
	case ContextVMMethodProvision:
		go r.handleProvisioningRequest(ctx, event)
	case ContextVMMethodAction:
		go r.handleSoulAction(ctx, event)
	default:
		r.logger.Warn("unexpected Soul Factory ContextVM method", "method", tagValue(event.Tags, "method"))
	}
}

// handleProvisioningRequest processes a kind:5950 provisioning request.
func (r *Reactor) handleProvisioningRequest(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "requester", event.PubKey)
	logger.Info("received provisioning request")

	// Validate authorization
	if !r.isAuthorizedProvisioner(event.PubKey.Hex()) {
		logger.Warn("unauthorized provisioning request")
		if err := r.publishError(ctx, event, "unauthorized", "requester not in authorized provisioners list"); err != nil {
			logger.Error("failed to publish provisioning error", "error", err)
		}
		return
	}

	factoryPubkey := strings.TrimSpace(r.config.SoulFactoryPubkey)
	if factoryPubkey == "" {
		logger.Error("SoulFactory pubkey is not configured")
		if err := r.publishError(ctx, event, "config_error", "SoulFactory pubkey is required"); err != nil {
			logger.Error("failed to publish provisioning error", "error", err)
		}
		return
	}

	// Parse request
	req, err := r.parseProvisioningRequest(event)
	if err != nil {
		logger.Error("failed to parse request", "error", err)
		if publishErr := r.publishError(ctx, event, "parse_error", err.Error()); publishErr != nil {
			logger.Error("failed to publish provisioning error", "error", publishErr)
		}
		return
	}

	logger = logger.With("agent_id", req.AgentID)

	if existing, err := r.findExistingProvisioningResult(ctx, event); err != nil {
		logger.Warn("failed to check existing provisioning terminal result", "error", err)
	} else if existing != nil {
		logger.Info("ignoring provisioning request with existing terminal result", "result_event", existing.ID)
		return
	}

	logger.Info("starting provisioning workflow")

	// Create and reserve run tracker before external side effects so duplicate
	// live delivery of the same request cannot race into provisioning.
	run := &domain.ProvisioningRun{
		ID:              domain.NewUUID(),
		RequestID:       event.ID.Hex(),
		AgentID:         req.AgentID,
		Status:          domain.ProvisioningStatusRunning,
		CurrentStep:     domain.StepGenerate,
		Steps:           make([]domain.ProvisioningStepResult, 0, len(domain.ProvisioningSteps)),
		RequesterPubkey: event.PubKey.Hex(),
		DraftRef:        req.DraftRef,
		DraftEventID:    req.DraftEventID,
		SpecHash:        req.SpecHash,
		StartedAt:       time.Now(),
	}

	r.mu.Lock()
	if existingRun := r.runs[event.ID.Hex()]; existingRun != nil && existingRun.Status == domain.ProvisioningStatusRunning {
		r.mu.Unlock()
		logger.Info("ignoring duplicate in-flight provisioning request")
		return
	}
	r.runs[event.ID.Hex()] = run
	r.mu.Unlock()

	// Run provisioning workflow
	result, err := r.provisioner.Provision(ctx, req, run)
	if err != nil {
		logger.Error("provisioning failed", "error", err, "step", run.CurrentStep)
		run.Status = domain.ProvisioningStatusFailed
		run.Error = err.Error()
		now := time.Now()
		run.CompletedAt = &now
		if publishErr := r.publishError(ctx, event, string(run.CurrentStep), err.Error()); publishErr != nil {
			logger.Error("failed to publish provisioning error", "error", publishErr)
		}
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

	if err := r.publishResult(ctx, event, result); err != nil {
		logger.Error("failed to publish provisioning result", "error", err)
		run.Status = domain.ProvisioningStatusFailed
		run.Error = fmt.Sprintf("publish provisioning result: %v", err)
	}
}

// handleSoulAction processes a kind:1950 lifecycle action through the single
// lifecycle_handler.go orchestration path.
func (r *Reactor) handleSoulAction(ctx context.Context, event *nostr.Event) {
	logger := r.logger.With("event_id", event.ID, "initiator", event.PubKey)
	logger.Info("received soul action")
	if err := r.lifecycle().HandleAction(ctx, event); err != nil {
		logger.Error("soul action failed", "error", err)
	}
}

// isAuthorizedProvisioner checks if a pubkey is authorized to provision.
func (r *Reactor) isAuthorizedProvisioner(pubkey string) bool {
	return slices.Contains(r.config.AuthorizedPubkeys, pubkey)
}

// parseProvisioningRequest extracts request data from a kind:5950 event.
func (r *Reactor) parseProvisioningRequest(event *nostr.Event) (*domain.ProvisioningRequest, error) {
	return ParseProvisioningRequestEvent(event)
}

// parseSoulAction extracts action data from a kind:1950 event.
func (r *Reactor) parseSoulAction(event *nostr.Event) (*domain.SoulAction, error) {
	return ParseSoulActionEvent(event)
}

// publishStatus publishes a kind:6950 progress event.
func (r *Reactor) PublishStatus(ctx context.Context, requestEvent *nostr.Event, step domain.ProvisioningStep, current, total int, message string) error {
	event := BuildProvisioningStatusEvent(requestEvent, step, current, total, message)

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign status event: %w", err)
	}

	if err := r.publish(ctx, event, r.provisioningPublicationRelays()); err != nil {
		return err
	}
	return r.publishCanonicalProvisioningObservable(ctx, requestEvent, event)
}

// publishResult publishes a kind:7950 success result event.
func (r *Reactor) publishResult(ctx context.Context, requestEvent *nostr.Event, soul *domain.AgentSoul) error {
	event, err := BuildProvisioningSuccessResultEvent(requestEvent, soul, r.config.SoulFactoryPubkey)
	if err != nil {
		return err
	}

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign result event: %w", err)
	}

	if err := r.publish(ctx, event, r.provisioningPublicationRelays()); err != nil {
		return err
	}
	return r.publishCanonicalProvisioningObservable(ctx, requestEvent, event)
}

func (r *Reactor) publishActionError(ctx context.Context, sourceEvent *nostr.Event, action *domain.SoulAction, message string) error {
	if action.Initiator == "" {
		action.Initiator = sourceEvent.PubKey.Hex()
	}
	event, err := BuildActionResultEvent(action, "error", map[string]interface{}{"error": message}, ActionResultLegacy)
	if err != nil {
		return err
	}
	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign action error event: %w", err)
	}
	return r.publish(ctx, event, r.provisioningPublicationRelays())
}

// publishError publishes a kind:7950 error result event.
func (r *Reactor) publishError(ctx context.Context, requestEvent *nostr.Event, step, message string) error {
	event := BuildProvisioningErrorResultEvent(requestEvent, step, message)

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign error event: %w", err)
	}

	if err := r.publish(ctx, event, r.provisioningPublicationRelays()); err != nil {
		return err
	}
	return r.publishCanonicalProvisioningObservable(ctx, requestEvent, event)
}

// publishSoul publishes a kind:31951 agent soul event.
func (r *Reactor) PublishSoul(ctx context.Context, soul *domain.AgentSoul) error {
	event := BuildAgentSoulEvent(soul)

	if err := r.signer.Sign(ctx, event); err != nil {
		return fmt.Errorf("sign soul event: %w", err)
	}

	soul.EventID = event.ID.Hex()

	return r.publish(ctx, event, r.provisioningPublicationRelays())
}

func (r *Reactor) provisioningPublicationRelays() []string {
	return normalizeUsablePublishRelays(append(append([]string{}, r.config.Relays...), r.config.AdditionalRelays...))
}

func normalizeUsablePublishRelays(relays []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(relays))
	for _, relay := range relays {
		relay = strings.TrimRight(strings.TrimSpace(relay), "/")
		if relay == "" {
			continue
		}
		if _, ok := seen[relay]; ok {
			continue
		}
		seen[relay] = struct{}{}
		out = append(out, relay)
	}
	return out
}

// publish sends an event to the specified relays.
func (r *Reactor) publish(ctx context.Context, event *nostr.Event, relays []string) error {
	relays = normalizeUsablePublishRelays(relays)
	if r.publishFn != nil {
		return r.publishFn(ctx, event, relays)
	}
	if len(relays) == 0 {
		return fmt.Errorf("no Soul Factory relays configured for publishing kind %d", event.Kind)
	}
	bus, err := NewSoulFactoryRelayBus(relays, WithRelayBusSigner(r.signer), WithRelayBusLogger(r.logger))
	if err != nil {
		return err
	}
	defer bus.Close()
	published, err := bus.Publish(ctx, *event)
	if err != nil {
		return err
	}
	if published == 0 {
		return fmt.Errorf("event was not accepted by any relay")
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
	if r.getSoulFn != nil {
		return r.getSoulFn(ctx, agentID)
	}
	agentID = normalizeSoulLookupRef(agentID)
	if agentID == "" {
		return nil, nil
	}

	relays := normalizeUsablePublishRelays(r.config.Relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("no Soul Factory relays configured for soul lookup")
	}
	bus, err := NewSoulFactoryRelayBus(relays, WithRelayBusSigner(r.signer), WithRelayBusLogger(r.logger))
	if err != nil {
		return nil, err
	}
	defer bus.Close()
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindAgentSoul)},
		Tags:  nostr.TagMap{tagParameterizedD: {agentID}},
		Limit: 1,
	}
	if factoryPubkey := strings.TrimSpace(r.config.SoulFactoryPubkey); factoryPubkey != "" {
		parsed, err := nostr.PubKeyFromHex(factoryPubkey)
		if err != nil {
			return nil, fmt.Errorf("invalid Soul Factory pubkey for soul lookup: %w", err)
		}
		filter.Authors = []nostr.PubKey{parsed}
	}
	events, err := bus.Query(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 || events[0] == nil {
		return nil, nil
	}
	return r.parseSoulEvent(events[0]), nil
}

func normalizeSoulLookupRef(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Split(value, ":")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return value
}

// parseSoulEvent converts a Nostr event to an AgentSoul.
func (r *Reactor) parseSoulEvent(event *nostr.Event) *domain.AgentSoul {
	return ParseAgentSoulEvent(event)
}
