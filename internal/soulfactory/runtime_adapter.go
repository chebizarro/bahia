package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	RuntimeMethodProvision       = "soulfactory.provision"
	RuntimeMethodUpdate          = "soulfactory.update"
	RuntimeMethodSuspend         = "soulfactory.suspend"
	RuntimeMethodResume          = "soulfactory.resume"
	RuntimeMethodRedeploy        = "soulfactory.redeploy"
	RuntimeMethodRevoke          = "soulfactory.revoke"
	RuntimeMethodAvatarGenerate  = "soulfactory.avatar.generate"
	RuntimeMethodAvatarSet       = "soulfactory.avatar.set"
	RuntimeMethodAvatarList      = "soulfactory.avatar.list"
	RuntimeMethodAvatarStatus    = "soulfactory.avatar.status"
	RuntimeMethodVoiceConfigure  = "soulfactory.voice.configure"
	RuntimeMethodVoicePreview    = "soulfactory.voice.preview"
	RuntimeMethodVoiceSample     = "soulfactory.voice.sample"
	RuntimeMethodVoiceList       = "soulfactory.voice.list"
	RuntimeMethodMemoryConfigure = "soulfactory.memory.configure"
	RuntimeMethodMemoryStatus    = "soulfactory.memory.status"
	RuntimeMethodMemoryReindex   = "soulfactory.memory.reindex"
	RuntimeMethodConfigReload    = "soulfactory.config.reload"

	kindNIP65RelayListMetadata = 10002
)

// RuntimeAdapter invokes the shared SoulFactory runtime-control contract for a
// concrete runtime implementation such as OpenClaw or Metiq. Implementations are
// transport adapters only; actual runtime execution remains inside the runtime
// bridges.
type RuntimeAdapter interface {
	Runtime() domain.RuntimeTarget
	DiscoverCapabilities(ctx context.Context, policy domain.SoulRelayPolicySpec) ([]RuntimeCapability, error)
	Execute(ctx context.Context, req RuntimeAdapterRequest) (*RuntimeControlResultEnvelope, error)
}

// RuntimeAdapterTransport is intentionally the existing SoulFactory relay-bus
// surface. It publishes with NIP-01 OK enforcement and subscribes until EOSE
// without introducing polling or request/response transport semantics.
type RuntimeAdapterTransport interface {
	Publish(context.Context, nostr.Event) (int, error)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*RelayBusSubscription, error)
	Close()
}

type RuntimeAdapterTransportFactory func([]string) (RuntimeAdapterTransport, error)

// RuntimeAdapterConfig wires one runtime adapter instance.
type RuntimeAdapterConfig struct {
	Target           domain.RuntimeTarget
	ControllerPubkey string
	Signer           soulClientSigner
	Relays           []string
	Transport        RuntimeAdapterTransport
	TransportFactory RuntimeAdapterTransportFactory
	Logger           *slog.Logger
	Now              func() time.Time
	CapabilityLimit  int
}

// RuntimeCapability is the normalized kind:30317 SoulFactory runtime capability
// descriptor accepted from both OpenClaw and Metiq bridge shapes.
type RuntimeCapability struct {
	ID                string
	Pubkey            string
	Identifier        string
	Coordinate        string
	Runtime           domain.RuntimeTarget
	Schema            string
	ControlSchema     string
	Methods           []string
	ControllerPubkeys []string
	RelayHints        domain.SoulRelayPolicySpec
	CreatedAt         time.Time
	Event             *nostr.Event
	Compatible        bool
}

// RuntimeAdapterRequest describes one signed kind:38384 control request. The
// request must already carry the operator/soul/spec context resolved by the
// caller; draft-backed provisioning is intentionally out of this adapter slice.
type RuntimeAdapterRequest struct {
	Method         string
	Operator       RuntimeOperatorRef
	Soul           RuntimeSoulRef
	Target         RuntimeTargetRef
	Params         map[string]interface{}
	DraftPolicy    domain.SoulRelayPolicySpec
	Capability     *RuntimeCapability
	RequestKind    int
	Action         domain.SoulActionType
	IdempotencyKey string
}

// RuntimeControlResultEnvelope is the JSON content of a kind:38386 runtime
// control result.
type RuntimeControlResultEnvelope struct {
	Schema               string                 `json:"schema"`
	Method               string                 `json:"method"`
	IdempotencyKey       string                 `json:"idempotency_key"`
	RequestEvent         string                 `json:"request_event"`
	OperatorRequestEvent string                 `json:"operator_request_event"`
	Status               string                 `json:"status"`
	Result               map[string]interface{} `json:"result,omitempty"`
	Error                *RuntimeControlError   `json:"error"`
	Event                *nostr.Event           `json:"-"`
}

type RuntimeControlError struct {
	Code      string                 `json:"code"`
	Message   string                 `json:"message"`
	Retryable bool                   `json:"retryable"`
	Details   map[string]interface{} `json:"details,omitempty"`
}

type OpenClawRuntimeAdapter struct{ *runtimeControlAdapter }
type MetiqRuntimeAdapter struct{ *runtimeControlAdapter }

func NewOpenClawRuntimeAdapter(config RuntimeAdapterConfig) (*OpenClawRuntimeAdapter, error) {
	config.Target = domain.RuntimeTargetOpenClaw
	adapter, err := newRuntimeControlAdapter(config)
	if err != nil {
		return nil, err
	}
	return &OpenClawRuntimeAdapter{runtimeControlAdapter: adapter}, nil
}

func NewMetiqRuntimeAdapter(config RuntimeAdapterConfig) (*MetiqRuntimeAdapter, error) {
	config.Target = domain.RuntimeTargetMetiq
	adapter, err := newRuntimeControlAdapter(config)
	if err != nil {
		return nil, err
	}
	return &MetiqRuntimeAdapter{runtimeControlAdapter: adapter}, nil
}

type runtimeControlAdapter struct {
	target           domain.RuntimeTarget
	controllerPubkey string
	signer           soulClientSigner
	relays           []string
	transport        RuntimeAdapterTransport
	factory          RuntimeAdapterTransportFactory
	logger           *slog.Logger
	now              func() time.Time
	capabilityLimit  int
}

func newRuntimeControlAdapter(config RuntimeAdapterConfig) (*runtimeControlAdapter, error) {
	if config.Target == "" {
		return nil, fmt.Errorf("runtime target is required")
	}
	if strings.TrimSpace(config.ControllerPubkey) == "" {
		return nil, fmt.Errorf("SoulFactory controller pubkey is required")
	}
	if config.Signer == nil {
		return nil, fmt.Errorf("SoulFactory service signer is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	factory := config.TransportFactory
	if factory == nil {
		factory = func(relays []string) (RuntimeAdapterTransport, error) {
			return NewSoulFactoryRelayBus(relays, WithRelayBusSigner(config.Signer), WithRelayBusLogger(logger))
		}
	}
	limit := config.CapabilityLimit
	if limit <= 0 {
		limit = 50
	}
	return &runtimeControlAdapter{
		target:           config.Target,
		controllerPubkey: strings.TrimSpace(config.ControllerPubkey),
		signer:           config.Signer,
		relays:           normalizeSoulRelays(config.Relays),
		transport:        config.Transport,
		factory:          factory,
		logger:           logger.With("component", "soulfactory-runtime-adapter", "runtime", string(config.Target)),
		now:              firstNowFunc(config.Now),
		capabilityLimit:  limit,
	}, nil
}

func (a *runtimeControlAdapter) Runtime() domain.RuntimeTarget { return a.target }

func (a *runtimeControlAdapter) DiscoverCapabilities(ctx context.Context, policy domain.SoulRelayPolicySpec) ([]RuntimeCapability, error) {
	transport, closeTransport, err := a.transportForRelays(discoveryRelays(policy, a.relays))
	if err != nil {
		return nil, err
	}
	defer closeTransport()

	filters := []nostr.Filter{{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindRuntimeCapability)},
		Tags:  nostr.TagMap{tagRuntime: []string{string(a.target)}},
		Limit: a.capabilityLimit,
	}}
	events, err := collectRuntimeAdapterEvents(ctx, transport, filters)
	if err != nil {
		return nil, err
	}
	capabilities := make([]RuntimeCapability, 0, len(events))
	latestByCoordinate := map[string]RuntimeCapability{}
	for _, event := range events {
		capability, ok := ParseRuntimeCapabilityEvent(event)
		if !ok || !capability.Supports(a.target, "", a.controllerPubkey) {
			continue
		}
		key := firstNonEmpty(capability.Coordinate, capability.ID)
		current, exists := latestByCoordinate[key]
		if !exists || capability.CreatedAt.After(current.CreatedAt) || (capability.CreatedAt.Equal(current.CreatedAt) && capability.ID > current.ID) {
			latestByCoordinate[key] = capability
		}
	}
	for _, capability := range latestByCoordinate {
		capabilities = append(capabilities, capability)
	}
	sort.Slice(capabilities, func(i, j int) bool {
		if !capabilities[i].CreatedAt.Equal(capabilities[j].CreatedAt) {
			return capabilities[i].CreatedAt.After(capabilities[j].CreatedAt)
		}
		if capabilities[i].Pubkey != capabilities[j].Pubkey {
			return capabilities[i].Pubkey < capabilities[j].Pubkey
		}
		return capabilities[i].ID > capabilities[j].ID
	})
	return capabilities, nil
}

func (a *runtimeControlAdapter) Execute(ctx context.Context, req RuntimeAdapterRequest) (*RuntimeControlResultEnvelope, error) {
	if err := a.prepareRequest(ctx, &req); err != nil {
		return nil, err
	}

	selectedRelays, err := a.selectControlRelays(ctx, req)
	if err != nil {
		return nil, err
	}
	transport, closeTransport, err := a.transportForRelays(selectedRelays)
	if err != nil {
		return nil, err
	}
	defer closeTransport()

	envelope := RuntimeControlEnvelope{
		Schema:         domain.SoulFactoryRuntimeControlSchema,
		Method:         req.Method,
		IdempotencyKey: req.IdempotencyKey,
		RequestedAt:    a.now().Unix(),
		Operator:       req.Operator,
		Controller:     RuntimeControllerRef{Pubkey: a.controllerPubkey},
		Target:         req.Target,
		Soul:           req.Soul,
		Params:         req.Params,
	}
	event, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		return nil, err
	}
	appendTag(&event.Tags, tagCapability, req.Capability.ID)
	if req.RequestKind != 0 {
		appendTag(&event.Tags, tagRequestKind, strconv.Itoa(req.RequestKind))
	}
	appendTag(&event.Tags, tagAction, string(req.Action))
	for _, relay := range selectedRelays {
		appendTag(&event.Tags, "relay", relay)
	}
	if err := signGoNostrEvent(ctx, a.signer, event); err != nil {
		return nil, fmt.Errorf("sign runtime control request: %w", err)
	}
	if event.PubKey.Hex() != a.controllerPubkey {
		return nil, fmt.Errorf("runtime control request signed by %s, want controller %s", event.PubKey.Hex(), a.controllerPubkey)
	}
	runtimePubkey, err := nostr.PubKeyFromHex(req.Target.RuntimePubkey)
	if err != nil {
		return nil, fmt.Errorf("parse runtime pubkey for result subscription: %w", err)
	}

	filters := []nostr.Filter{{
		Kinds:   []nostr.Kind{nostr.Kind(domain.KindRuntimeControlResult)},
		Authors: []nostr.PubKey{runtimePubkey},
		Tags: nostr.TagMap{
			tagEvent:          []string{event.ID.Hex()},
			tagPubkey:         []string{a.controllerPubkey},
			"idempotency-key": []string{req.IdempotencyKey},
		},
	}}
	sub, err := transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("subscribe for runtime control result: %w", err)
	}
	defer sub.Close()

	published, err := transport.Publish(ctx, *event)
	if err != nil {
		return nil, fmt.Errorf("publish runtime control request: %w", err)
	}
	if published == 0 {
		return nil, fmt.Errorf("runtime control request was not accepted by any relay")
	}

	result, err := awaitRuntimeControlResult(ctx, sub, event, req, a.controllerPubkey)
	if err != nil {
		return nil, err
	}
	if result.Status != "success" {
		if result.Error != nil {
			return result, fmt.Errorf("runtime %s response: %s: %s", result.Status, result.Error.Code, result.Error.Message)
		}
		return result, fmt.Errorf("runtime %s response", result.Status)
	}
	return result, nil
}

func (a *runtimeControlAdapter) prepareRequest(ctx context.Context, req *RuntimeAdapterRequest) error {
	req.Method = strings.TrimSpace(req.Method)
	if req.Method == "" {
		return fmt.Errorf("runtime control method is required")
	}
	if req.Operator.Pubkey == "" || req.Operator.RequestEvent == "" {
		return fmt.Errorf("runtime control request requires operator pubkey and request event")
	}
	if req.Soul.ID == "" || req.Soul.SpecHash == "" {
		return fmt.Errorf("runtime control request requires soul id and spec hash")
	}
	if req.Target.Runtime == "" {
		req.Target.Runtime = a.target
	}
	if req.Target.Runtime != a.target {
		return fmt.Errorf("adapter runtime %s cannot execute target %s", a.target, req.Target.Runtime)
	}
	if req.Target.AgentID == "" {
		req.Target.AgentID = req.Soul.ID
	}
	if req.Params == nil {
		req.Params = map[string]interface{}{}
	}
	if req.Capability == nil {
		capabilities, err := a.DiscoverCapabilities(ctx, req.DraftPolicy)
		if err != nil {
			return err
		}
		capability := selectRuntimeCapability(capabilities, a.target, req.Method, a.controllerPubkey, req.Target.RuntimePubkey)
		if capability == nil {
			return fmt.Errorf("no compatible %s runtime capability found for method %s", a.target, req.Method)
		}
		req.Capability = capability
	}
	if req.Target.RuntimePubkey == "" {
		req.Target.RuntimePubkey = req.Capability.Pubkey
	}
	if req.Target.RuntimePubkey == "" {
		return fmt.Errorf("runtime control request requires target runtime pubkey")
	}
	if !req.Capability.Supports(a.target, req.Method, a.controllerPubkey) {
		return fmt.Errorf("runtime capability %s does not support %s for controller %s", req.Capability.ID, req.Method, a.controllerPubkey)
	}
	if req.Capability.Pubkey != "" && req.Capability.Pubkey != req.Target.RuntimePubkey {
		return fmt.Errorf("runtime capability pubkey %s does not match target %s", req.Capability.Pubkey, req.Target.RuntimePubkey)
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = runtimeIdempotencyKey(a.controllerPubkey, req.Method, req.Operator.RequestEvent, req.Target.RuntimePubkey, req.Target.AgentID, req.Soul.SpecHash)
	}
	return nil
}

func (a *runtimeControlAdapter) selectControlRelays(ctx context.Context, req RuntimeAdapterRequest) ([]string, error) {
	nip65 := domain.SoulRelayPolicySpec{}
	if req.DraftPolicy.NIP65Discovery && req.Target.RuntimePubkey != "" {
		nip65 = a.fetchRuntimeNIP65Policy(ctx, req.Target.RuntimePubkey, discoveryRelays(req.DraftPolicy, a.relayFallbacks(req.Capability)))
	}
	relays := mergeRelayGroups(
		req.DraftPolicy.Control,
		req.DraftPolicy.Read,
		req.DraftPolicy.Write,
		req.Capability.RelayHints.Control,
		req.Capability.RelayHints.Read,
		req.Capability.RelayHints.Write,
		nip65.Control,
		nip65.Read,
		nip65.Write,
		a.relays,
	)
	if len(relays) == 0 {
		return nil, fmt.Errorf("no control relays available for %s runtime %s", a.target, req.Target.RuntimePubkey)
	}
	return relays, nil
}

func (a *runtimeControlAdapter) relayFallbacks(capability *RuntimeCapability) []string {
	if capability == nil {
		return a.relays
	}
	return mergeRelayGroups(capability.RelayHints.Control, capability.RelayHints.Read, capability.RelayHints.Write, a.relays)
}

func (a *runtimeControlAdapter) fetchRuntimeNIP65Policy(ctx context.Context, runtimePubkey string, queryRelays []string) domain.SoulRelayPolicySpec {
	transport, closeTransport, err := a.transportForRelays(queryRelays)
	if err != nil {
		a.logger.Warn("skip NIP-65 runtime relay discovery", "runtime_pubkey", runtimePubkey, "error", err)
		return domain.SoulRelayPolicySpec{}
	}
	defer closeTransport()
	parsedRuntimePubkey, err := nostr.PubKeyFromHex(runtimePubkey)
	if err != nil {
		a.logger.Warn("skip NIP-65 runtime relay discovery", "runtime_pubkey", runtimePubkey, "error", err)
		return domain.SoulRelayPolicySpec{}
	}
	events, err := collectRuntimeAdapterEvents(ctx, transport, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(kindNIP65RelayListMetadata)}, Authors: []nostr.PubKey{parsedRuntimePubkey}, Limit: 1}})
	if err != nil {
		a.logger.Warn("NIP-65 runtime relay discovery failed", "runtime_pubkey", runtimePubkey, "error", err)
		return domain.SoulRelayPolicySpec{}
	}
	var latest *nostr.Event
	for _, event := range events {
		if event.Kind != nostr.Kind(kindNIP65RelayListMetadata) || event.PubKey.Hex() != runtimePubkey || !validSignedEvent(event) {
			continue
		}
		if latest == nil || event.CreatedAt > latest.CreatedAt || (event.CreatedAt == latest.CreatedAt && event.ID.Hex() > latest.ID.Hex()) {
			latest = event
		}
	}
	return parseNIP65RelayPolicy(latest)
}

func (a *runtimeControlAdapter) transportForRelays(relays []string) (RuntimeAdapterTransport, func(), error) {
	if a.transport != nil {
		return a.transport, func() {}, nil
	}
	relays = normalizeSoulRelays(relays)
	if len(relays) == 0 {
		return nil, func() {}, fmt.Errorf("at least one relay is required")
	}
	transport, err := a.factory(relays)
	if err != nil {
		return nil, func() {}, err
	}
	return transport, transport.Close, nil
}

func ParseRuntimeCapabilityEvent(event *nostr.Event) (RuntimeCapability, bool) {
	if event == nil || event.Kind != nostr.Kind(domain.KindRuntimeCapability) || !validSignedEvent(event) {
		return RuntimeCapability{}, false
	}
	var content struct {
		Schema            string   `json:"schema"`
		Runtime           string   `json:"runtime"`
		Methods           []string `json:"methods"`
		ControlSchema     string   `json:"control_schema"`
		ControlSchemaAlt  string   `json:"controlSchema"`
		ControllerPubkeys []string `json:"controller_pubkeys"`
		ControllerAlt     []string `json:"controllerPubkeys"`
		RelayHints        struct {
			Read    []string `json:"read"`
			Write   []string `json:"write"`
			Control []string `json:"control"`
		} `json:"relay_hints"`
		RelayHintsAlt struct {
			Read    []string `json:"read"`
			Write   []string `json:"write"`
			Control []string `json:"control"`
		} `json:"relayHints"`
	}
	if strings.TrimSpace(event.Content) != "" {
		_ = json.Unmarshal([]byte(event.Content), &content)
	}

	capability := RuntimeCapability{
		ID:                event.ID.Hex(),
		Pubkey:            event.PubKey.Hex(),
		Identifier:        tagValue(event.Tags, tagParameterizedD),
		Runtime:           domain.RuntimeTarget(firstNonEmpty(tagValue(event.Tags, tagRuntime), content.Runtime)),
		Schema:            firstNonEmpty(tagValue(event.Tags, tagSchema), content.Schema),
		ControlSchema:     firstNonEmpty(tagValue(event.Tags, "control-schema"), content.ControlSchema, content.ControlSchemaAlt),
		Methods:           append([]string{}, content.Methods...),
		ControllerPubkeys: append(append([]string{}, content.ControllerPubkeys...), content.ControllerAlt...),
		RelayHints: domain.SoulRelayPolicySpec{
			Read:    append(append([]string{}, content.RelayHints.Read...), content.RelayHintsAlt.Read...),
			Write:   append(append([]string{}, content.RelayHints.Write...), content.RelayHintsAlt.Write...),
			Control: append(append([]string{}, content.RelayHints.Control...), content.RelayHintsAlt.Control...),
		},
		CreatedAt: event.CreatedAt.Time(),
		Event:     event,
	}
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "method":
			capability.Methods = append(capability.Methods, tag[1])
		case "controller":
			capability.ControllerPubkeys = append(capability.ControllerPubkeys, tag[1])
		case "relay":
			scope := "control"
			if len(tag) > 2 {
				scope = strings.TrimSpace(tag[2])
			}
			appendRelayHint(&capability.RelayHints, scope, tag[1])
		case "read-relay", tagRelayRead:
			appendRelayHint(&capability.RelayHints, "read", tag[1])
		case "write-relay", tagRelayWrite:
			appendRelayHint(&capability.RelayHints, "write", tag[1])
		case "control-relay", tagRelayControl:
			appendRelayHint(&capability.RelayHints, "control", tag[1])
		}
	}
	capability.Methods = uniqueStrings(capability.Methods)
	capability.ControllerPubkeys = uniqueStrings(capability.ControllerPubkeys)
	capability.RelayHints.Read = normalizeSoulRelays(capability.RelayHints.Read)
	capability.RelayHints.Write = normalizeSoulRelays(capability.RelayHints.Write)
	capability.RelayHints.Control = normalizeSoulRelays(capability.RelayHints.Control)
	capability.Coordinate = eventCoordinate(event)
	capability.Compatible = capability.Schema == domain.SoulFactoryRuntimeCapabilitySchema && capability.ControlSchema == domain.SoulFactoryRuntimeControlSchema
	return capability, true
}

func (c RuntimeCapability) Supports(runtime domain.RuntimeTarget, method, controllerPubkey string) bool {
	if !c.Compatible {
		return false
	}
	if runtime != "" && c.Runtime != runtime {
		return false
	}
	if method != "" && !stringInSlice(method, c.Methods) {
		return false
	}
	if controllerPubkey != "" && len(c.ControllerPubkeys) > 0 && !stringInSlice(controllerPubkey, c.ControllerPubkeys) {
		return false
	}
	return true
}

func BuildProvisionRuntimeParamsFromDraft(draft domain.SoulDraftContent) map[string]interface{} {
	draft = draft.MigrateToLatest()
	return map[string]interface{}{
		"schema": draft.SchemaVersion(),
		"identity": map[string]interface{}{
			"name":    draft.Identity.Name,
			"purpose": draft.Identity.Purpose,
			"tier":    draft.Identity.Tier,
			"nip05":   draft.Identity.NIP05,
			"theme":   draft.Identity.Theme,
			"emoji":   draft.Identity.Emoji,
		},
		"persona": draft.Persona,
		"avatar":  draft.Avatar,
		"voice":   draft.Voice,
		"memory":  draft.Memory,
		"runtime": map[string]interface{}{
			"target":         draft.Runtime.Target,
			"capability_ref": draft.Runtime.CapabilityRef,
		},
		"permissions": map[string]interface{}{
			"allowed_kinds":   draft.Permissions.AllowedKinds,
			"tool_grants":     draft.Permissions.ToolGrants,
			"approval_policy": draft.Permissions.ApprovalPolicy,
		},
		"relay_policy": draft.RelayPolicy,
		"workspace":    draft.Workspace,
		"assets":       draft.Assets,
	}
}

func collectRuntimeAdapterEvents(ctx context.Context, transport RuntimeAdapterTransport, filters []nostr.Filter) ([]*nostr.Event, error) {
	sub, err := transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	var events []*nostr.Event
	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-eose:
			return events, nil
		case event, ok := <-sub.Events:
			if !ok {
				return events, nil
			}
			if event == nil || !validSignedEvent(event) {
				continue
			}
			eventID := event.ID.Hex()
			if _, exists := seen[eventID]; exists {
				continue
			}
			seen[eventID] = struct{}{}
			events = append(events, event)
		}
	}
}

func awaitRuntimeControlResult(ctx context.Context, sub *RelayBusSubscription, requestEvent *nostr.Event, req RuntimeAdapterRequest, controllerPubkey string) (*RuntimeControlResultEnvelope, error) {
	seen := map[string]struct{}{}
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-eose:
			eose = nil
		case event, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("runtime result subscription closed before correlated result")
			}
			eventID := event.ID.Hex()
			if _, duplicate := seen[eventID]; duplicate {
				continue
			}
			seen[eventID] = struct{}{}
			result, ok := parseRuntimeControlResultEvent(event)
			if !ok || !runtimeResultCorrelates(result, requestEvent, req, controllerPubkey) {
				continue
			}
			return result, nil
		}
	}
}

func parseRuntimeControlResultEvent(event *nostr.Event) (*RuntimeControlResultEnvelope, bool) {
	if event == nil || event.Kind != nostr.Kind(domain.KindRuntimeControlResult) || !validSignedEvent(event) {
		return nil, false
	}
	var result RuntimeControlResultEnvelope
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, false
	}
	if result.Schema == "" {
		result.Schema = tagValue(event.Tags, tagSchema)
	}
	if result.Method == "" {
		result.Method = tagValue(event.Tags, "method")
	}
	if result.IdempotencyKey == "" {
		result.IdempotencyKey = tagValue(event.Tags, "idempotency-key")
	}
	if result.RequestEvent == "" {
		result.RequestEvent = tagValue(event.Tags, tagEvent)
	}
	if result.Status == "" {
		result.Status = tagValue(event.Tags, tagStatus)
	}
	result.Event = event
	return &result, true
}

func runtimeResultCorrelates(result *RuntimeControlResultEnvelope, requestEvent *nostr.Event, req RuntimeAdapterRequest, controllerPubkey string) bool {
	if result == nil || result.Event == nil || requestEvent == nil {
		return false
	}
	if result.Event.PubKey.Hex() != req.Target.RuntimePubkey || result.Schema != domain.SoulFactoryRuntimeControlSchema {
		return false
	}
	if result.RequestEvent != requestEvent.ID.Hex() || result.OperatorRequestEvent != req.Operator.RequestEvent {
		return false
	}
	if result.Method != req.Method || result.IdempotencyKey != req.IdempotencyKey {
		return false
	}
	if tagValue(result.Event.Tags, tagPubkey) != controllerPubkey || tagValue(result.Event.Tags, tagEvent) != requestEvent.ID.Hex() {
		return false
	}
	if value := tagValue(result.Event.Tags, tagSoul); value != "" && value != req.Soul.ID {
		return false
	}
	if value := tagValue(result.Event.Tags, tagAgentID); value != "" && value != req.Target.AgentID {
		return false
	}
	if value := tagValue(result.Event.Tags, tagSpecHash); value != "" && value != req.Soul.SpecHash {
		return false
	}
	return true
}

func selectRuntimeCapability(capabilities []RuntimeCapability, runtime domain.RuntimeTarget, method, controllerPubkey, runtimePubkey string) *RuntimeCapability {
	for i := range capabilities {
		capability := &capabilities[i]
		if runtimePubkey != "" && capability.Pubkey != runtimePubkey {
			continue
		}
		if capability.Supports(runtime, method, controllerPubkey) {
			return capability
		}
	}
	return nil
}

func runtimeIdempotencyKey(controllerPubkey, method, operatorRequest, runtimePubkey, agentID, specHash string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{controllerPubkey, method, operatorRequest, runtimePubkey, agentID, specHash}, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func discoveryRelays(policy domain.SoulRelayPolicySpec, fallback []string) []string {
	return mergeRelayGroups(policy.Control, policy.Read, policy.Write, fallback)
}

func mergeRelayGroups(groups ...[]string) []string {
	var merged []string
	for _, group := range groups {
		merged = append(merged, group...)
	}
	return normalizeSoulRelays(merged)
}

func parseNIP65RelayPolicy(event *nostr.Event) domain.SoulRelayPolicySpec {
	policy := domain.SoulRelayPolicySpec{}
	if event == nil {
		return policy
	}
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "r" {
			continue
		}
		marker := ""
		if len(tag) > 2 {
			marker = strings.ToLower(strings.TrimSpace(tag[2]))
		}
		switch marker {
		case "read":
			policy.Read = append(policy.Read, tag[1])
		case "write":
			policy.Write = append(policy.Write, tag[1])
		default:
			policy.Read = append(policy.Read, tag[1])
			policy.Write = append(policy.Write, tag[1])
		}
	}
	policy.Read = normalizeSoulRelays(policy.Read)
	policy.Write = normalizeSoulRelays(policy.Write)
	policy.Control = mergeRelayGroups(policy.Read, policy.Write)
	return policy
}

func appendRelayHint(policy *domain.SoulRelayPolicySpec, scope, relay string) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "read":
		policy.Read = append(policy.Read, relay)
	case "write":
		policy.Write = append(policy.Write, relay)
	default:
		policy.Control = append(policy.Control, relay)
	}
}

func eventCoordinate(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	identifier := tagValue(event.Tags, tagParameterizedD)
	if identifier == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s:%s", event.Kind, event.PubKey.Hex(), identifier)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func firstNowFunc(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return time.Now
}
