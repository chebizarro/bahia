package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// DomainHandler processes intents for a single domain family (e.g. "service",
// "environment"). F2/F3 register concrete handlers; F1 provides the registry.
//
// # How to add a domain slice
//
// 1. Implement DomainHandler for your domain (e.g. ServiceIntentHandler).
// 2. Call IntentProcessor.RegisterHandler("service", handler) at startup.
// 3. Add your domain string to RegisteredIntentDomains below.
// 4. The processor routes matching intents to your handler automatically.
//
// The handler receives a validated, authorized, deduplicated intent with the
// winning content for its coordinate. It must reconcile the entity toward the
// desired state (level-triggered: the full desired state, not a diff).
type DomainHandler interface {
	// HandleIntent processes one intent. The processor has already:
	//   - verified the intent_id is not a duplicate
	//   - validated the event structure
	//   - authorized the pubkey via TrustSet
	//   - selected the winning intent across authors
	//
	// Returns nil on success. A non-nil error causes a rejection status.
	HandleIntent(ctx context.Context, intent *Intent) error

	// PermissionFor returns the domain.Permission required for the given op.
	PermissionFor(op string) domain.Permission
}

// FleetScopedHandler is an optional interface that DomainHandler
// implementations may satisfy to signal that authorization for their domain
// uses fleet-operator identity (config authorized_pubkeys) rather than the
// default per-org RBAC check.
//
// When the intent processor detects that a handler implements this interface
// and IsFleetScoped() returns true, it authorizes the actor by checking
// whether the pubkey appears in TrustSet.FleetOps() instead of calling
// TrustSet.HasPermission. This is required for domains like deployment
// policies where the authorized principals are fleet operators who are
// explicitly NOT org members (design §2.2/§2.4).
type FleetScopedHandler interface {
	IsFleetScoped() bool
}

// SelfAuthorizingHandler is an optional interface that DomainHandler
// implementations may satisfy when the default per-org RBAC or fleet-scoped
// authorization is insufficient. The org domain needs this because org-create
// uses fleet-ops/bootstrap-owner auth while member/invite operations use
// per-org RBAC (design §2.2, §7 Wave 5 O1).
//
// When the intent processor detects that a handler implements this interface,
// it delegates authorization to AuthorizeIntent instead of the default
// TrustSet.HasPermission or FleetOps check.
type SelfAuthorizingHandler interface {
	AuthorizeIntent(ctx context.Context, trustSet *TrustSet, intent *Intent) error
}

// Intent is the parsed, validated representation of a kind-30900 intent event.
type Intent struct {
	// Event is the original Nostr event.
	Event *nostr.Event
	// Domain is the domain family tag value (e.g. "service", "environment").
	Domain string
	// Op is the advisory operation tag (create, update, delete).
	Op string
	// Schema is the intent schema tag.
	Schema string
	// OrgID is the org UUID from the org tag.
	OrgID uuid.UUID
	// IntentID is the idempotency key from the intent_id tag.
	IntentID string
	// Coordinate is the d-tag value (entity coordinate).
	Coordinate string
	// Content is the parsed JSON content.
	Content map[string]interface{}
	// Evaluation is the daemon-computed policy decision for an evaluate intent.
	Evaluation *domain.PolicyEvaluation
	// ExpectedUpdatedAt is the canonical record's updated_at revision.
	ExpectedUpdatedAt *time.Time
	// Actor is the pubkey that originated the intent. For relay-path intents
	// this is Event.PubKey; for in-process dispatch it is the ContextVM/REST
	// caller's pubkey.
	Actor string
}

// IntentProcessorConfig configures the processor.
type IntentProcessorConfig struct {
	// EnabledDomains is the set of domain families for which intents are
	// processed. An intent for a domain not in this set is silently ignored.
	EnabledDomains map[string]bool
}

// IntentProcessor is the shared pipeline for relay and in-process intents.
// See design §3.2 for the seven processing steps.
type IntentProcessor struct {
	mu       sync.RWMutex
	handlers map[string]DomainHandler
	config   IntentProcessorConfig

	trustSet        *TrustSet
	store           *localstore.Store
	status          *IntentStatusPublisher
	giftWrapIngress *IntentGiftWrapIngress
	logger          *zap.Logger
}

// NewIntentProcessor creates a processor with the given trust set and local
// store for idempotency. The status publisher may be nil (status events are
// then not emitted).
func NewIntentProcessor(
	trustSet *TrustSet,
	store *localstore.Store,
	status *IntentStatusPublisher,
	config IntentProcessorConfig,
	logger *zap.Logger,
) *IntentProcessor {
	return &IntentProcessor{
		handlers: make(map[string]DomainHandler),
		config:   config,
		trustSet: trustSet,
		store:    store,
		status:   status,
		logger:   logger.Named("intent-processor"),
	}
}

// RegisterHandler registers a domain handler. F2/F3/F4 call this at startup.
func (p *IntentProcessor) RegisterHandler(domain string, handler DomainHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[domain] = handler
	p.logger.Info("registered intent domain handler", zap.String("domain", domain))
}

// SetGiftWrapIngress sets the gift-wrap ingress for sensitive-domain plaintext
// rejection on the relay path. Call after constructing the ingress in app.go.
func (p *IntentProcessor) SetGiftWrapIngress(ig *IntentGiftWrapIngress) {
	p.giftWrapIngress = ig
}

// Handler returns the registered handler for a domain, or nil.
func (p *IntentProcessor) Handler(domain string) DomainHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handlers[domain]
}

// ProcessRelayIntent handles an intent arriving from the relay subscription.
// Untrusted authors are dropped silently (§2.3). Known principals lacking
// permission get a bounded rejection status.
func (p *IntentProcessor) ProcessRelayIntent(ctx context.Context, ev *nostr.Event) error {
	intent, err := ParseIntent(ev)
	if err != nil {
		if intent != nil && p.trustSet.IsKnownPrincipal(ev.PubKey.Hex()) && p.status != nil {
			intent.Actor = ev.PubKey.Hex()
			p.status.PublishRejection(ctx, intent, err.Error())
			return err
		}
		p.logger.Debug("dropping malformed intent event",
			zap.String("event_id", ev.ID.Hex()),
			zap.Error(err),
		)
		return nil // silent drop for malformed events
	}
	intent.Actor = ev.PubKey.Hex()

	// Reject plaintext intents for sensitive domains (§1.7).
	if p.giftWrapIngress != nil && p.giftWrapIngress.RejectPlaintextSensitiveIntent(ctx, intent) {
		return fmt.Errorf("plaintext intent rejected for sensitive domain %q", intent.Domain)
	}

	return p.process(ctx, intent, false)
}

// ProcessInProcess handles an intent from the dual-dispatch path (ContextVM/
// REST/MCP). The caller has already authorized the request through the
// existing encryptedTenantAuthorizer or REST auth middleware. The actor is the
// original requester's pubkey, not the daemon's.
//
// This path shares the same pipeline and idempotency store as the relay path,
// preventing double application (§4.1).
func (p *IntentProcessor) ProcessInProcess(ctx context.Context, intent *Intent) error {
	if raw, ok := intent.Content["expected_updated_at"]; ok {
		revision, err := parseIntentRevision(raw)
		if err != nil {
			if p.status != nil {
				p.status.PublishRejection(ctx, intent, err.Error())
			}
			return err
		}
		intent.ExpectedUpdatedAt = revision
	}
	return p.process(ctx, intent, true)
}

func (p *IntentProcessor) process(ctx context.Context, intent *Intent, inProcess bool) error {
	// Step 0: Check domain is enabled.
	if !p.config.EnabledDomains[intent.Domain] {
		if inProcess {
			return fmt.Errorf("intent domain %q is disabled", intent.Domain)
		}
		p.logger.Debug("intent for disabled domain, ignoring",
			zap.String("domain", intent.Domain),
			zap.String("intent_id", intent.IntentID),
		)
		return nil
	}

	// Step 1: Deduplicate by intent_id.
	if p.isProcessed(intent.IntentID) {
		p.logger.Debug("skipping already-processed intent",
			zap.String("intent_id", intent.IntentID),
		)
		return nil
	}

	// Step 2: Validate (ParseIntent already validated structure).
	// Additional validation: check that we have a handler.
	p.mu.RLock()
	handler := p.handlers[intent.Domain]
	p.mu.RUnlock()
	if handler == nil {
		if inProcess {
			return fmt.Errorf("intent domain %q has no handler", intent.Domain)
		}
		p.logger.Debug("no handler registered for domain, ignoring",
			zap.String("domain", intent.Domain),
			zap.String("intent_id", intent.IntentID),
		)
		return nil
	}

	// Step 3: Authorize.
	// SelfAuthorizingHandler: the handler does its own authorization (e.g. the
	// org domain has per-operation auth: fleet-ops for org create, per-org RBAC
	// for member/invite ops). See design §2.2, §7 Wave 5 O1.
	if sa, ok := handler.(SelfAuthorizingHandler); ok {
		if err := sa.AuthorizeIntent(ctx, p.trustSet, intent); err != nil {
			p.logger.Info("self-authorizing handler rejected intent",
				zap.String("actor", intent.Actor),
				zap.String("domain", intent.Domain),
				zap.String("intent_id", intent.IntentID),
				zap.Error(err),
			)
			if p.status != nil {
				p.status.PublishRejection(ctx, intent, err.Error())
			}
			return err
		}
	} else {
		perm := handler.PermissionFor(intent.Op)
		authorized := false
		if fs, ok := handler.(FleetScopedHandler); ok && fs.IsFleetScoped() {
			// Fleet-scoped domain: check fleet operator identity instead of
			// per-org RBAC. Fleet operators are NOT org members (§2.2).
			authorized = p.isFleetOperator(intent.Actor)
		} else {
			authorized = p.trustSet.HasPermission(ctx, intent.OrgID, intent.Actor, perm)
		}
		if !authorized {
			if p.trustSet.IsKnownPrincipal(intent.Actor) {
				// Known principal, insufficient permission → publish rejection.
				p.logger.Info("rejecting intent from known principal lacking permission",
					zap.String("actor", intent.Actor),
					zap.String("permission", string(perm)),
					zap.String("intent_id", intent.IntentID),
				)
				if p.status != nil {
					p.status.PublishRejection(ctx, intent, fmt.Sprintf("insufficient permission: %s", perm))
				}
				return fmt.Errorf("insufficient permission: %s", perm)
			}
			// Unknown relay author → silent drop (§2.3). An in-process
			// caller already has a transport identity and must get a refusal.
			if inProcess {
				return fmt.Errorf("untrusted intent actor %q", intent.Actor)
			}
			p.logger.Debug("dropping intent from untrusted author",
				zap.String("actor", intent.Actor),
				zap.String("intent_id", intent.IntentID),
			)
			return nil
		}
	}

	// Steps 4-5: Reconcile toward desired state (domain handler).
	// The handler implements level-triggered reconciliation: it receives the
	// full desired state and reconciles the entity, regardless of whether it
	// has seen prior events for this coordinate.
	if err := handler.HandleIntent(ctx, intent); err != nil {
		p.logger.Warn("intent handler failed",
			zap.String("domain", intent.Domain),
			zap.String("intent_id", intent.IntentID),
			zap.Error(err),
		)
		if p.status != nil {
			if IsRevisionConflict(err) {
				p.status.PublishConflict(ctx, intent)
			} else {
				p.status.PublishRejection(ctx, intent, err.Error())
			}
		}
		return err
	}

	// A compute intent has no registry record: its result must be published
	// before deduplication marks it complete, so a failed publication can retry.
	if intent.Evaluation != nil {
		if p.status == nil {
			return fmt.Errorf("intent outcome status publisher is not configured")
		}
		if err := p.status.PublishAcceptedEvaluation(ctx, intent); err != nil {
			return err
		}
	}

	// Step 6: Mark processed (idempotency).
	p.markProcessed(intent)

	// Step 7: Publish canonical state is done by the domain handler.
	if p.status != nil && intent.Evaluation == nil {
		p.status.PublishAccepted(ctx, intent)
	}

	p.logger.Info("intent processed",
		zap.String("domain", intent.Domain),
		zap.String("op", intent.Op),
		zap.String("intent_id", intent.IntentID),
		zap.String("coordinate", intent.Coordinate),
	)
	return nil
}

// IsProcessed reports whether an intent_id has completed the shared pipeline.
// MCP uses this only to distinguish an idempotent replay from a fresh write
// whose previously visible canonical record has not yet advanced.
func (p *IntentProcessor) IsProcessed(intentID string) bool {
	return p.isProcessed(intentID)
}

// ProcessedIntentRecord is the local correlation retained for a completed
// intent. It lets in-process retry transports return the original event ID
// even after a canonical entity has been tombstoned.
type ProcessedIntentRecord struct {
	Actor      string `json:"actor"`
	OrgID      string `json:"org_id"`
	Domain     string `json:"domain"`
	Op         string `json:"op"`
	Coordinate string `json:"coordinate"`
	EventID    string `json:"event_id"`
}

// ProcessedIntent returns the durable idempotency marker for an intent ID.
func (p *IntentProcessor) ProcessedIntent(intentID string) *ProcessedIntentRecord {
	if p.store == nil || intentID == "" {
		return nil
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{30078}, Tags: nostr.TagMap{"d": {"intent-processed:" + intentID}}, Limit: 1}
	for marker := range p.store.QueryEvents(filter) {
		var record ProcessedIntentRecord
		if err := json.Unmarshal([]byte(marker.Content), &record); err == nil {
			return &record
		}
	}
	return nil
}

// isFleetOperator reports whether pubkey is a fleet operator.
func (p *IntentProcessor) isFleetOperator(pubkey string) bool {
	for _, pk := range p.trustSet.FleetOps() {
		if pk == pubkey {
			return true
		}
	}
	return false
}

// isProcessed checks whether an intent_id has already been processed.
// Uses the local bbolt store for persistence across restarts.
func (p *IntentProcessor) isProcessed(intentID string) bool {
	if p.store == nil || intentID == "" {
		return false
	}
	filter := nostr.Filter{
		Tags: nostr.TagMap{"intent_id": {intentID}},
	}
	// Look for a stored intent-processed marker in the local store.
	// We use a convention: kind 30078 (NIP-78 app-specific) with
	// d = "intent-processed:<intent_id>".
	filter.Kinds = []nostr.Kind{30078}
	filter.Tags = nostr.TagMap{"d": {"intent-processed:" + intentID}}
	filter.Limit = 1
	for range p.store.QueryEvents(filter) {
		return true
	}
	return false
}

// markProcessed durably records that an intent_id has been processed.
func (p *IntentProcessor) markProcessed(intent *Intent) {
	if p.store == nil || intent == nil || intent.IntentID == "" {
		return
	}
	intentID := intent.IntentID
	record := ProcessedIntentRecord{Actor: intent.Actor, OrgID: intent.OrgID.String(), Domain: intent.Domain, Op: intent.Op, Coordinate: intent.Coordinate}
	if intent.Event != nil {
		record.EventID = intent.Event.ID.Hex()
	}
	content, _ := json.Marshal(record)
	// Store a kind 30078 marker event in the local store.
	marker := nostr.Event{
		Kind:      30078,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "intent-processed:" + intentID},
			{"intent_id", intentID},
		},
		Content: string(content),
	}
	// The marker doesn't need a real signature; it's local-only.
	// Set a deterministic ID to make it addressable.
	marker.ID = marker.GetID()
	if _, err := p.store.SaveEvent(marker); err != nil {
		p.logger.Warn("failed to persist intent-processed marker",
			zap.String("intent_id", intentID),
			zap.Error(err),
		)
	}
}

// ParseIntent extracts a validated Intent from a kind 30900 event.
func ParseIntent(ev *nostr.Event) (*Intent, error) {
	if ev == nil {
		return nil, fmt.Errorf("nil event")
	}
	if int(ev.Kind) != 30900 {
		return nil, fmt.Errorf("expected kind 30900, got %d", ev.Kind)
	}

	// Check for bahia-intent tag.
	hasBahiaIntent := false
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == "bahia-intent" {
			hasBahiaIntent = true
			break
		}
	}
	if !hasBahiaIntent {
		return nil, fmt.Errorf("missing t=bahia-intent tag")
	}

	intent := &Intent{Event: ev}

	// Extract tags.
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "d":
			intent.Coordinate = tag[1]
		case "domain":
			intent.Domain = tag[1]
		case "op":
			intent.Op = tag[1]
		case "schema":
			intent.Schema = tag[1]
		case "org":
			parsed, err := uuid.Parse(tag[1])
			if err != nil {
				return nil, fmt.Errorf("invalid org tag: %w", err)
			}
			intent.OrgID = parsed
		case "intent_id":
			intent.IntentID = tag[1]
		}
	}

	// Validate required tags.
	if intent.Coordinate == "" {
		return nil, fmt.Errorf("missing d tag")
	}
	if intent.Domain == "" {
		return nil, fmt.Errorf("missing domain tag")
	}
	if intent.IntentID == "" {
		return nil, fmt.Errorf("missing intent_id tag")
	}
	if intent.OrgID == uuid.Nil && intent.Domain != "dns" && intent.Domain != "ml" && intent.Domain != "worker" {
		return nil, fmt.Errorf("missing or invalid org tag")
	}

	// Parse content.
	if ev.Content != "" {
		var content map[string]interface{}
		decoder := json.NewDecoder(strings.NewReader(ev.Content))
		decoder.UseNumber()
		if err := decoder.Decode(&content); err != nil {
			return nil, fmt.Errorf("invalid content JSON: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("invalid content JSON: trailing data")
		}
		intent.Content = content

		// Check for expected_updated_at in content.
		if raw, ok := content["expected_updated_at"]; ok {
			revision, err := parseIntentRevision(raw)
			if err != nil {
				return intent, err
			}
			intent.ExpectedUpdatedAt = revision
		}
	}

	// Default op.
	if intent.Op == "" {
		intent.Op = "update"
	}

	return intent, nil
}

// parseIntentRevision accepts the wire representation of a canonical updated_at.
// Numeric epochs have no defined unit in the intent protocol and are rejected.
func parseIntentRevision(raw any) (*time.Time, error) {
	value, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("invalid expected_updated_at: must be an RFC3339 timestamp string, got %T", raw)
	}
	revision, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("invalid expected_updated_at: %w", err)
	}
	return &revision, nil
}

// RevisionMatches compares an intent token with the precision published in
// canonical records (Postgres timestamptz microseconds).
func (intent *Intent) RevisionMatches(recordUpdatedAt time.Time) bool {
	return intent.ExpectedUpdatedAt != nil && domain.SameRevision(recordUpdatedAt, *intent.ExpectedUpdatedAt)
}

// IntentDomainEnabled reports whether a domain is in the enabled set.
func IntentDomainEnabled(enabledDomains []string, domain string) bool {
	for _, d := range enabledDomains {
		if strings.EqualFold(d, domain) {
			return true
		}
	}
	return false
}

// RegisteredIntentDomains is the set of intent handlers wired in app.New.
// Keep this list in sync with every RegisterHandler call in internal/app/app.go.
var RegisteredIntentDomains = []string{
	"service", "environment", "policy", "package", "backup", "llm", "ml",
	"dns", "worker", "deployment", "runtime", "org", "secret", "notification",
}

// BuildEnabledDomains enables every registered domain except explicit opt-outs.
// A non-empty deprecated allowlist retains the Phase 3 selection semantics.
func BuildEnabledDomains(disabled, legacyAllowlist []string) map[string]bool {
	m := make(map[string]bool, len(RegisteredIntentDomains))
	allowed := make(map[string]bool, len(legacyAllowlist))
	for _, domain := range legacyAllowlist {
		allowed[strings.ToLower(strings.TrimSpace(domain))] = true
	}
	for _, domain := range RegisteredIntentDomains {
		if len(legacyAllowlist) == 0 || allowed[domain] {
			m[domain] = true
		}
	}
	for _, domain := range disabled {
		delete(m, strings.ToLower(strings.TrimSpace(domain)))
	}
	return m
}
