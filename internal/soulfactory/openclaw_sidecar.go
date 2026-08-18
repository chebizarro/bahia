package soulfactory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

var openClawSoulFactoryMethods = []string{
	RuntimeMethodProvision,
	RuntimeMethodUpdate,
	RuntimeMethodSuspend,
	RuntimeMethodResume,
	RuntimeMethodRedeploy,
	RuntimeMethodRevoke,
	RuntimeMethodAvatarGenerate,
	RuntimeMethodAvatarSet,
	RuntimeMethodAvatarList,
	RuntimeMethodAvatarStatus,
	RuntimeMethodVoiceConfigure,
	RuntimeMethodVoicePreview,
	RuntimeMethodVoiceSample,
	RuntimeMethodVoiceList,
	RuntimeMethodMemoryConfigure,
	RuntimeMethodMemoryStatus,
	RuntimeMethodMemoryReindex,
	RuntimeMethodPersonaConfigure,
	RuntimeMethodPersonaPreview,
	RuntimeMethodPersonaUpdate,
	RuntimeMethodConfigReload,
}

var openClawCommandDriverDefaultMethods = []string{
	RuntimeMethodProvision,
	RuntimeMethodUpdate,
	RuntimeMethodPersonaUpdate,
	RuntimeMethodRevoke,
}

// OpenClawControlDriver is the owned sidecar seam into local OpenClaw control
// surfaces. Implementations may wrap OpenClaw CLI/gateway-local commands,
// plugin SDK entrypoints, or test fakes, but must not expose REST lifecycle
// control for SoulFactory.
type OpenClawControlDriver interface {
	Methods() []string
	Execute(context.Context, OpenClawControlInvocation) (*OpenClawControlOutcome, error)
}

type OpenClawControlInvocation struct {
	Event    *nostr.Event           `json:"-"`
	Envelope RuntimeControlEnvelope `json:"envelope"`
	Method   string                 `json:"method"`
	AgentID  string                 `json:"agent_id"`
	SoulID   string                 `json:"soul_id"`
	SpecHash string                 `json:"spec_hash"`
	Params   map[string]interface{} `json:"params"`
}

type OpenClawControlOutcome struct {
	Status string                 `json:"status"`
	Result map[string]interface{} `json:"result,omitempty"`
	Error  *RuntimeControlError   `json:"error"`
}

type OpenClawCommandDriver struct {
	Command     string
	Args        []string
	Dir         string
	Env         []string
	MethodsList []string
}

func (d OpenClawCommandDriver) Methods() []string {
	if len(d.MethodsList) > 0 {
		return uniqueStrings(d.MethodsList)
	}
	return append([]string{}, openClawCommandDriverDefaultMethods...)
}

func (d OpenClawCommandDriver) Execute(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	if strings.TrimSpace(d.Command) == "" {
		return nil, fmt.Errorf("openclaw command driver is not configured")
	}
	payload, err := json.Marshal(invocation)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenClaw invocation: %w", err)
	}
	cmd := exec.CommandContext(ctx, d.Command, d.Args...)
	cmd.Dir = d.Dir
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(), d.Env...)
	cmd.Env = append(cmd.Env,
		"SOULFACTORY_METHOD="+invocation.Method,
		"SOULFACTORY_AGENT_ID="+invocation.AgentID,
		"SOULFACTORY_SPEC_HASH="+invocation.SpecHash,
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("openclaw command failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	var outcome OpenClawControlOutcome
	if err := json.Unmarshal(bytes.TrimSpace(out), &outcome); err != nil {
		return nil, fmt.Errorf("parse openclaw command result: %w", err)
	}
	return normalizeOpenClawOutcome(invocation, &outcome), nil
}

type OpenClawSidecarConfig struct {
	RuntimePubkey            string
	Signer                   soulClientSigner
	TrustedControllerPubkeys []string
	Identifier               string
	Relays                   []string
	RelayHints               domain.SoulRelayPolicySpec
	Transport                RuntimeAdapterTransport
	Driver                   OpenClawControlDriver
	IdempotencyStore         OpenClawIdempotencyStore
	Logger                   *slog.Logger
	Now                      func() time.Time
	CapabilityAdditionalTags nostr.Tags
}

type OpenClawSidecar struct {
	runtimePubkey       string
	signer              soulClientSigner
	trusted             map[string]struct{}
	trustedList         []string
	identifier          string
	relays              []string
	relayHints          domain.SoulRelayPolicySpec
	transport           RuntimeAdapterTransport
	driver              OpenClawControlDriver
	store               OpenClawIdempotencyStore
	logger              *slog.Logger
	now                 func() time.Time
	methods             []string
	capabilityTags      nostr.Tags
	readinessMu         sync.RWMutex
	capabilityPublished bool
	subscriptionEOSE    bool
	lastReadinessError  string
}

type OpenClawSidecarReadiness struct {
	Ready               bool   `json:"ready"`
	CapabilityPublished bool   `json:"capability_published"`
	SubscriptionEOSE    bool   `json:"subscription_eose"`
	LastError           string `json:"last_error,omitempty"`
}

func NewOpenClawSidecar(config OpenClawSidecarConfig) (*OpenClawSidecar, error) {
	if strings.TrimSpace(config.RuntimePubkey) == "" {
		return nil, fmt.Errorf("OpenClaw runtime pubkey is required")
	}
	if config.Signer == nil {
		return nil, fmt.Errorf("OpenClaw runtime signer is required")
	}
	if config.Driver == nil {
		return nil, fmt.Errorf("OpenClaw sidecar control driver is required")
	}
	trusted := map[string]struct{}{}
	for _, pubkey := range uniqueStrings(config.TrustedControllerPubkeys) {
		trusted[pubkey] = struct{}{}
	}
	if len(trusted) == 0 {
		return nil, fmt.Errorf("at least one trusted SoulFactory controller pubkey is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	methods := uniqueStrings(config.Driver.Methods())
	if len(methods) == 0 {
		return nil, fmt.Errorf("OpenClaw sidecar driver must support at least one SoulFactory method")
	}
	sort.Strings(methods)
	identifier := strings.TrimSpace(config.Identifier)
	if identifier == "" {
		identifier = "openclaw-soulfactory-sidecar"
	}
	store := config.IdempotencyStore
	if store == nil {
		store = NewMemoryOpenClawIdempotencyStore()
	}
	transport := config.Transport
	if transport == nil {
		relays := normalizeSoulRelays(config.Relays)
		if len(relays) == 0 {
			return nil, fmt.Errorf("at least one relay is required when no sidecar transport is supplied")
		}
		bus, err := NewSoulFactoryRelayBus(relays, WithRelayBusSigner(config.Signer), WithRelayBusLogger(logger))
		if err != nil {
			return nil, err
		}
		transport = bus
	}
	return &OpenClawSidecar{
		runtimePubkey:  strings.TrimSpace(config.RuntimePubkey),
		signer:         config.Signer,
		trusted:        trusted,
		trustedList:    sortedMapKeys(trusted),
		identifier:     identifier,
		relays:         normalizeSoulRelays(config.Relays),
		relayHints:     config.RelayHints,
		transport:      transport,
		driver:         config.Driver,
		store:          store,
		logger:         logger.With("component", "openclaw-soulfactory-sidecar"),
		now:            firstNowFunc(config.Now),
		methods:        methods,
		capabilityTags: append(nostr.Tags{}, config.CapabilityAdditionalTags...),
	}, nil
}

func (s *OpenClawSidecar) Close() {
	if s != nil && s.transport != nil {
		s.transport.Close()
	}
}

func (s *OpenClawSidecar) Run(ctx context.Context) error {
	s.resetReadiness()
	defer s.clearSubscriptionReadiness()
	if err := s.PublishCapability(ctx); err != nil {
		s.setReadinessError(err)
		return err
	}
	authors := make([]nostr.PubKey, 0, len(s.trustedList))
	for _, trustedPubkey := range s.trustedList {
		parsed, err := nostr.PubKeyFromHex(trustedPubkey)
		if err != nil {
			return fmt.Errorf("invalid trusted OpenClaw controller pubkey: %w", err)
		}
		authors = append(authors, parsed)
	}
	filters := []nostr.Filter{{
		Kinds:   []nostr.Kind{nostr.Kind(domain.KindRuntimeControlRequest)},
		Authors: authors,
		Tags: nostr.TagMap{
			tagPubkey: []string{s.runtimePubkey},
			tagSchema: []string{domain.SoulFactoryRuntimeControlSchema},
			tagMethod: s.methods,
		},
	}}
	sub, err := s.transport.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		s.setReadinessError(err)
		return fmt.Errorf("subscribe OpenClaw SoulFactory sidecar: %w", err)
	}
	defer sub.Close()
	eose := sub.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-eose:
			s.logger.Info("OpenClaw SoulFactory sidecar backfill complete")
			s.markSubscriptionEOSE()
			eose = nil
		case event, ok := <-sub.Events:
			if !ok {
				return fmt.Errorf("OpenClaw SoulFactory sidecar subscription closed")
			}
			if _, err := s.HandleControlEvent(ctx, event); err != nil {
				s.logger.Warn("OpenClaw SoulFactory request rejected or failed", "event", event.ID, "error", err)
			}
		}
	}
}

func (s *OpenClawSidecar) PublishCapability(ctx context.Context) error {
	event, err := s.BuildCapabilityEvent()
	if err != nil {
		return err
	}
	accepted, err := s.transport.Publish(ctx, *event)
	if err != nil {
		return fmt.Errorf("publish OpenClaw runtime capability: %w", err)
	}
	if accepted == 0 {
		return fmt.Errorf("OpenClaw runtime capability was not accepted by any relay")
	}
	s.markCapabilityPublished()
	return nil
}

func (s *OpenClawSidecar) Readiness() OpenClawSidecarReadiness {
	if s == nil {
		return OpenClawSidecarReadiness{LastError: "sidecar is not configured"}
	}
	s.readinessMu.RLock()
	defer s.readinessMu.RUnlock()
	return OpenClawSidecarReadiness{
		Ready:               s.capabilityPublished && s.subscriptionEOSE,
		CapabilityPublished: s.capabilityPublished,
		SubscriptionEOSE:    s.subscriptionEOSE,
		LastError:           s.lastReadinessError,
	}
}

func (s *OpenClawSidecar) markCapabilityPublished() {
	s.readinessMu.Lock()
	s.capabilityPublished = true
	s.lastReadinessError = ""
	s.readinessMu.Unlock()
}

func (s *OpenClawSidecar) markSubscriptionEOSE() {
	s.readinessMu.Lock()
	s.subscriptionEOSE = true
	s.lastReadinessError = ""
	s.readinessMu.Unlock()
}

func (s *OpenClawSidecar) setReadinessError(err error) {
	s.readinessMu.Lock()
	if err != nil {
		s.lastReadinessError = err.Error()
	}
	s.readinessMu.Unlock()
}

func (s *OpenClawSidecar) resetReadiness() {
	s.readinessMu.Lock()
	s.capabilityPublished = false
	s.subscriptionEOSE = false
	s.lastReadinessError = ""
	s.readinessMu.Unlock()
}

func (s *OpenClawSidecar) clearSubscriptionReadiness() {
	s.readinessMu.Lock()
	s.subscriptionEOSE = false
	s.readinessMu.Unlock()
}

func (s *OpenClawSidecar) BuildCapabilityEvent() (*nostr.Event, error) {
	content, err := json.Marshal(map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            string(domain.RuntimeTargetOpenClaw),
		"methods":            s.methods,
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": s.trustedList,
		"relay_hints": map[string]interface{}{
			"read":    s.relayHints.Read,
			"write":   s.relayHints.Write,
			"control": s.relayHints.Control,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal OpenClaw capability: %w", err)
	}
	tags := nostr.Tags{
		{tagParameterizedD, s.identifier},
		{tagRuntime, string(domain.RuntimeTargetOpenClaw)},
		{tagSchema, domain.SoulFactoryRuntimeCapabilitySchema},
		{"control-schema", domain.SoulFactoryRuntimeControlSchema},
	}
	for _, method := range s.methods {
		tags = append(tags, nostr.Tag{tagMethod, method})
	}
	for _, controller := range s.trustedList {
		tags = append(tags, nostr.Tag{"controller", controller})
	}
	appendRelayTags := func(scope string, relays []string) {
		for _, relay := range normalizeSoulRelays(relays) {
			tags = append(tags, nostr.Tag{"relay", relay, scope})
		}
	}
	appendRelayTags("read", s.relayHints.Read)
	appendRelayTags("write", s.relayHints.Write)
	appendRelayTags("control", s.relayHints.Control)
	tags = append(tags, s.capabilityTags...)
	event := &nostr.Event{Kind: nostr.Kind(domain.KindRuntimeCapability), CreatedAt: nostr.Timestamp(s.now().Unix()), Tags: tags, Content: string(content)}
	if err := signGoNostrEvent(context.Background(), s.signer, event); err != nil {
		return nil, fmt.Errorf("sign OpenClaw capability: %w", err)
	}
	if event.PubKey.Hex() != s.runtimePubkey {
		return nil, fmt.Errorf("OpenClaw capability signed by %s, want runtime pubkey %s", event.PubKey.Hex(), s.runtimePubkey)
	}
	return event, nil
}

func (s *OpenClawSidecar) HandleControlEvent(ctx context.Context, event *nostr.Event) (*RuntimeControlResultEnvelope, error) {
	request, validationErr := s.ValidateControlEvent(event)
	if validationErr != nil {
		if request != nil && request.Envelope != nil {
			result, publishErr := s.publishRejected(ctx, event, *request.Envelope, validationErr)
			if publishErr != nil {
				return result, publishErr
			}
			return result, fmt.Errorf("%s: %s", validationErr.Code, validationErr.Message)
		}
		return nil, fmt.Errorf("%s: %s", validationErr.Code, validationErr.Message)
	}
	fingerprint := request.Fingerprint()
	if cached, ok := s.store.Get(request.Envelope.IdempotencyKey); ok {
		if cached.Fingerprint != fingerprint {
			conflict := &RuntimeControlError{Code: "duplicate_conflict", Message: "idempotency key was reused for a conflicting OpenClaw request", Retryable: false}
			return s.publishRejected(ctx, event, *request.Envelope, conflict)
		}
		return s.publishOutcome(ctx, event, *request.Envelope, cached.Outcome)
	}
	invocation := OpenClawControlInvocation{
		Event:    event,
		Envelope: *request.Envelope,
		Method:   request.Envelope.Method,
		AgentID:  request.Envelope.Target.AgentID,
		SoulID:   request.Envelope.Soul.ID,
		SpecHash: request.Envelope.Soul.SpecHash,
		Params:   request.Envelope.Params,
	}
	outcome, err := s.driver.Execute(ctx, invocation)
	if err != nil {
		outcome = &OpenClawControlOutcome{Status: "failed", Error: &RuntimeControlError{Code: "execution_failed", Message: err.Error(), Retryable: true}}
	}
	outcome = normalizeOpenClawOutcome(invocation, outcome)
	if err := s.store.Put(request.Envelope.IdempotencyKey, OpenClawStoredResult{Fingerprint: fingerprint, Outcome: outcome}); err != nil {
		persisted := &OpenClawControlOutcome{Status: "failed", Error: &RuntimeControlError{Code: "execution_failed", Message: "persist OpenClaw idempotency result: " + err.Error(), Retryable: true}}
		return s.publishOutcome(ctx, event, *request.Envelope, persisted)
	}
	return s.publishOutcome(ctx, event, *request.Envelope, outcome)
}

type OpenClawValidatedRequest struct {
	Event    *nostr.Event
	Envelope *RuntimeControlEnvelope
}

func (r OpenClawValidatedRequest) Fingerprint() string {
	if r.Envelope == nil {
		return ""
	}
	data, err := json.Marshal(r.Envelope)
	if err != nil {
		parts := []string{
			r.Envelope.Controller.Pubkey,
			r.Envelope.Method,
			r.Envelope.Operator.RequestEvent,
			r.Envelope.Target.RuntimePubkey,
			r.Envelope.Target.AgentID,
			r.Envelope.Soul.ID,
			r.Envelope.Soul.SpecHash,
			fmt.Sprint(r.Envelope.Params),
		}
		return strings.Join(parts, "\x00")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *OpenClawSidecar) ValidateControlEvent(event *nostr.Event) (*OpenClawValidatedRequest, *RuntimeControlError) {
	if event == nil {
		return nil, controlError("invalid_schema", "nil runtime control request", false)
	}
	if event.Kind != nostr.Kind(domain.KindRuntimeControlRequest) {
		return nil, controlError("invalid_schema", fmt.Sprintf("unexpected runtime control kind %d", event.Kind), false)
	}
	if !validSignedEvent(event) {
		return nil, controlError("invalid_signature", "runtime control request has invalid id, signature, or timestamp", false)
	}
	envelope, err := ParseRuntimeControlRequestEvent(event)
	if err != nil {
		return nil, controlError("invalid_schema", err.Error(), false)
	}
	request := &OpenClawValidatedRequest{Event: event, Envelope: envelope}
	if event.PubKey.Hex() == s.runtimePubkey {
		return request, controlError("unauthorized_controller", "runtime must not accept self-authored control requests", false)
	}
	for _, tag := range []string{tagPubkey, tagMethod, tagEvent, tagSoul, tagAgentID, "controller", tagIdempotencyKey, tagSpecHash, tagSchema} {
		if tagValue(event.Tags, tag) == "" {
			return request, controlError("missing_required_tag", "missing required tag: "+tag, false)
		}
	}
	if envelope.Schema != domain.SoulFactoryRuntimeControlSchema || tagValue(event.Tags, tagSchema) != domain.SoulFactoryRuntimeControlSchema {
		return request, controlError("unsupported_schema_version", "unsupported SoulFactory runtime control schema", false)
	}
	if envelope.Target.Runtime != domain.RuntimeTargetOpenClaw {
		return request, controlError("misaddressed_request", "request target runtime is not openclaw", false)
	}
	if tagValue(event.Tags, tagPubkey) != s.runtimePubkey || envelope.Target.RuntimePubkey != s.runtimePubkey {
		return request, controlError("misaddressed_request", "request is not addressed to this OpenClaw runtime pubkey", false)
	}
	if tagValue(event.Tags, "controller") != envelope.Controller.Pubkey || envelope.Controller.Pubkey != event.PubKey.Hex() {
		return request, controlError("unauthorized_controller", "controller tag/content must match the signing pubkey", false)
	}
	if _, ok := s.trusted[event.PubKey.Hex()]; !ok {
		return request, controlError("unauthorized_controller", "controller pubkey is not trusted by this OpenClaw sidecar", false)
	}
	if !stringInSlice(envelope.Method, s.methods) || tagValue(event.Tags, tagMethod) != envelope.Method {
		return request, controlError("unsupported_method", "SoulFactory method is not supported by this OpenClaw sidecar", false)
	}
	if tagValue(event.Tags, tagEvent) != envelope.Operator.RequestEvent || envelope.Operator.RequestEvent == "" || envelope.Operator.Pubkey == "" {
		return request, controlError("invalid_schema", "operator pubkey and request event correlation are required", false)
	}
	if tagValue(event.Tags, tagSoul) != envelope.Soul.ID || envelope.Soul.ID == "" {
		return request, controlError("invalid_schema", "soul tag/content mismatch", false)
	}
	if tagValue(event.Tags, tagAgentID) != envelope.Target.AgentID || envelope.Target.AgentID == "" {
		return request, controlError("invalid_schema", "agent-id tag/content mismatch", false)
	}
	if tagValue(event.Tags, tagIdempotencyKey) != envelope.IdempotencyKey || envelope.IdempotencyKey == "" {
		return request, controlError("invalid_schema", "idempotency-key tag/content mismatch", false)
	}
	if tagValue(event.Tags, tagSpecHash) != envelope.Soul.SpecHash || envelope.Soul.SpecHash == "" {
		return request, controlError("spec_hash_mismatch", "spec-hash tag/content mismatch", false)
	}
	if envelope.RequestedAt != 0 {
		now := s.now().Unix()
		if envelope.RequestedAt > now+600 || envelope.RequestedAt < now-365*24*60*60 {
			return request, controlError("stale_request", "runtime control requested_at is outside policy window", false)
		}
	}
	if err := validateOpenClawMethodParams(envelope.Method, envelope.Params); err != nil {
		return request, err
	}
	return request, nil
}

func (s *OpenClawSidecar) publishRejected(ctx context.Context, requestEvent *nostr.Event, envelope RuntimeControlEnvelope, ctrlErr *RuntimeControlError) (*RuntimeControlResultEnvelope, error) {
	outcome := &OpenClawControlOutcome{Status: "rejected", Error: ctrlErr}
	return s.publishOutcome(ctx, requestEvent, envelope, outcome)
}

func (s *OpenClawSidecar) publishOutcome(ctx context.Context, requestEvent *nostr.Event, envelope RuntimeControlEnvelope, outcome *OpenClawControlOutcome) (*RuntimeControlResultEnvelope, error) {
	outcome = normalizeOpenClawOutcome(OpenClawControlInvocation{Envelope: envelope, Method: envelope.Method, AgentID: envelope.Target.AgentID, SoulID: envelope.Soul.ID, SpecHash: envelope.Soul.SpecHash, Params: envelope.Params}, outcome)
	result := RuntimeControlResultEnvelope{
		Schema:               domain.SoulFactoryRuntimeControlSchema,
		Method:               envelope.Method,
		IdempotencyKey:       envelope.IdempotencyKey,
		RequestEvent:         requestEvent.ID.Hex(),
		OperatorRequestEvent: envelope.Operator.RequestEvent,
		Status:               outcome.Status,
		Result:               outcome.Result,
		Error:                outcome.Error,
	}
	content, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenClaw runtime result: %w", err)
	}
	tags := nostr.Tags{
		{tagPubkey, envelope.Controller.Pubkey},
		{tagEvent, requestEvent.ID.Hex()},
		{tagMethod, envelope.Method},
		{tagIdempotencyKey, envelope.IdempotencyKey},
		{tagAgentID, envelope.Target.AgentID},
		{tagSoul, envelope.Soul.ID},
		{tagSpecHash, envelope.Soul.SpecHash},
		{tagSchema, domain.SoulFactoryRuntimeControlSchema},
		{tagStatus, outcome.Status},
	}
	event := &nostr.Event{Kind: nostr.Kind(domain.KindRuntimeControlResult), CreatedAt: nostr.Timestamp(s.now().Unix()), Tags: tags, Content: string(content)}
	if err := signGoNostrEvent(ctx, s.signer, event); err != nil {
		return nil, fmt.Errorf("sign OpenClaw runtime result: %w", err)
	}
	if event.PubKey.Hex() != s.runtimePubkey {
		return nil, fmt.Errorf("OpenClaw result signed by %s, want runtime pubkey %s", event.PubKey.Hex(), s.runtimePubkey)
	}
	accepted, err := s.transport.Publish(ctx, *event)
	if err != nil {
		return nil, fmt.Errorf("publish OpenClaw runtime result: %w", err)
	}
	if accepted == 0 {
		return nil, fmt.Errorf("OpenClaw runtime result was not accepted by any relay")
	}
	result.Event = event
	return &result, nil
}

type OpenClawStoredResult struct {
	Fingerprint string
	Outcome     *OpenClawControlOutcome
}

type OpenClawIdempotencyStore interface {
	Get(string) (OpenClawStoredResult, bool)
	Put(string, OpenClawStoredResult) error
}

type memoryOpenClawIdempotencyStore struct {
	mu      sync.Mutex
	results map[string]OpenClawStoredResult
}

func NewMemoryOpenClawIdempotencyStore() OpenClawIdempotencyStore {
	return &memoryOpenClawIdempotencyStore{results: map[string]OpenClawStoredResult{}}
}

type fileOpenClawIdempotencyStore struct {
	mu      sync.Mutex
	path    string
	results map[string]OpenClawStoredResult
}

func NewFileOpenClawIdempotencyStore(path string) (OpenClawIdempotencyStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("idempotency store path is required")
	}
	store := &fileOpenClawIdempotencyStore{path: path, results: map[string]OpenClawStoredResult{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(data, &store.results); err != nil {
		return nil, fmt.Errorf("parse OpenClaw idempotency store %s: %w", path, err)
	}
	return store, nil
}

func (s *memoryOpenClawIdempotencyStore) Get(key string) (OpenClawStoredResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.results[key]
	return result, ok
}

func (s *memoryOpenClawIdempotencyStore) Put(key string, result OpenClawStoredResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[key] = result
	return nil
}

func (s *fileOpenClawIdempotencyStore) Get(key string) (OpenClawStoredResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.results[key]
	return result, ok
}

func (s *fileOpenClawIdempotencyStore) Put(key string, result OpenClawStoredResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[string]OpenClawStoredResult, len(s.results)+1)
	for existingKey, existingResult := range s.results {
		next[existingKey] = existingResult
	}
	next[key] = result
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWriteFileMode(s.path, data, 0o600); err != nil {
		return err
	}
	s.results = next
	return nil
}

func atomicWriteFileMode(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func validateOpenClawMethodParams(method string, params map[string]interface{}) *RuntimeControlError {
	if params == nil {
		params = map[string]interface{}{}
	}
	requireObject := func(name string) *RuntimeControlError {
		if _, ok := params[name].(map[string]interface{}); ok {
			return nil
		}
		return controlError("missing_required_param", "missing required object param: "+name, false)
	}
	requireString := func(name string) *RuntimeControlError {
		if value, ok := params[name].(string); ok && strings.TrimSpace(value) != "" {
			return nil
		}
		return controlError("missing_required_param", "missing required string param: "+name, false)
	}
	switch method {
	case RuntimeMethodProvision:
		for _, name := range []string{"identity", "runtime", "permissions", "relay_policy", "workspace", "assets"} {
			if err := requireObject(name); err != nil {
				return err
			}
		}
	case RuntimeMethodUpdate:
		if _, hasPatch := params["patch"]; !hasPatch {
			if _, hasResolved := params["resolved_spec"]; !hasResolved {
				return controlError("missing_required_param", "update requires patch or resolved_spec", false)
			}
		}
		for _, name := range []string{"previous_spec_hash", "new_spec_hash", "update_mode"} {
			if err := requireString(name); err != nil {
				return err
			}
		}
	case RuntimeMethodSuspend, RuntimeMethodResume:
		return requireString("reason")
	case RuntimeMethodRedeploy:
		if err := requireString("reason"); err != nil {
			return err
		}
		if strategy, ok := params["strategy"].(string); !ok || !stringInSlice(strategy, []string{"restart", "rebuild", "migrate"}) {
			return controlError("missing_required_param", "redeploy requires strategy restart, rebuild, or migrate", false)
		}
	case RuntimeMethodRevoke:
		if err := requireString("reason"); err != nil {
			return err
		}
		if _, ok := params["revoke_runtime_credentials"].(bool); !ok {
			return controlError("missing_required_param", "revoke requires revoke_runtime_credentials boolean", false)
		}
	case RuntimeMethodAvatarGenerate:
		if _, ok := params["generation"].(map[string]interface{}); !ok {
			if err := requireString("prompt"); err != nil {
				return controlError("missing_required_param", "avatar.generate requires generation object or prompt string", false)
			}
		}
	case RuntimeMethodAvatarSet:
		return requireString("avatar_ref")
	case RuntimeMethodAvatarList, RuntimeMethodAvatarStatus:
		// Optional params only.
	case RuntimeMethodVoiceConfigure:
		if hasVoiceRuntimeConfig(params) {
			return nil
		}
		return controlError("missing_required_param", "voice.configure requires voice config, proposed voice, or openclaw config", false)
	case RuntimeMethodVoicePreview, RuntimeMethodVoiceSample:
		if hasVoiceRuntimeConfig(params) {
			return nil
		}
		return controlError("missing_required_param", "voice preview requires voice config, proposed voice, or openclaw config", false)
	case RuntimeMethodVoiceList:
		// Optional params only.
	case RuntimeMethodMemoryConfigure:
		if _, ok := params["memory_config"].(map[string]interface{}); !ok {
			return controlError("missing_required_param", "memory.configure requires memory_config object", false)
		}
	case RuntimeMethodMemoryStatus:
		// Optional params only.
	case RuntimeMethodMemoryReindex:
		if schema, ok := params["schema"].(string); !ok || schema != SoulFactoryMemoryReindexSchema {
			return controlError("invalid_reindex_request", "memory.reindex requires soulfactory-memory-reindex/v1 schema", false)
		}
		if mode, ok := params["mode"].(string); !ok || normalizeMemoryReindexMode(mode) == "" {
			return controlError("invalid_reindex_request", "memory.reindex mode must be incremental or full", false)
		}
		if _, ok := params["memory_config"].(map[string]interface{}); !ok {
			return controlError("missing_required_param", "memory.reindex requires memory_config object", false)
		}
	case RuntimeMethodPersonaConfigure, RuntimeMethodPersonaPreview, RuntimeMethodPersonaUpdate:
		if _, err := ParsePersonaRuntimeParams(params); err != nil {
			return controlError("invalid_persona_request", err.Error(), false)
		}
	case RuntimeMethodConfigReload:
		if _, err := ParseConfigReloadRequest(params); err != nil {
			return controlError("invalid_reload_request", err.Error(), false)
		}
	default:
		return controlError("unsupported_method", "unsupported SoulFactory method", false)
	}
	return nil
}

func hasVoiceRuntimeConfig(params map[string]interface{}) bool {
	if params == nil {
		return false
	}
	if _, ok := params["openclaw"].(map[string]interface{}); ok {
		return true
	}
	if _, ok := params["voice_config"].(map[string]interface{}); ok {
		return true
	}
	if _, ok := params["voice"].(map[string]interface{}); ok {
		return true
	}
	if proposed, ok := params["proposed"].(map[string]interface{}); ok {
		if _, ok := proposed["voice"]; ok {
			return true
		}
	}
	return false
}

func normalizeOpenClawOutcome(invocation OpenClawControlInvocation, outcome *OpenClawControlOutcome) *OpenClawControlOutcome {
	if outcome == nil {
		outcome = &OpenClawControlOutcome{Status: "success"}
	}
	if outcome.Status == "" {
		outcome.Status = "success"
	}
	if outcome.Status == "success" {
		outcome.Error = nil
		if outcome.Result == nil {
			outcome.Result = map[string]interface{}{}
		}
		if _, ok := outcome.Result["agent_id"]; !ok {
			outcome.Result["agent_id"] = invocation.AgentID
		}
		if _, ok := outcome.Result["runtime"]; !ok {
			outcome.Result["runtime"] = string(domain.RuntimeTargetOpenClaw)
		}
		if _, ok := outcome.Result["runtime_binding"]; !ok {
			outcome.Result["runtime_binding"] = "openclaw://agents/" + invocation.AgentID
		}
		if _, ok := outcome.Result["state"]; !ok {
			outcome.Result["state"] = "running"
		}
		if _, ok := outcome.Result["spec_hash"]; !ok {
			outcome.Result["spec_hash"] = invocation.SpecHash
		}
		if _, ok := outcome.Result["observed_at"]; !ok {
			outcome.Result["observed_at"] = time.Now().Unix()
		}
		if _, ok := outcome.Result["warnings"]; !ok {
			outcome.Result["warnings"] = []string{}
		}
	}
	if outcome.Status != "success" && outcome.Error == nil {
		outcome.Error = &RuntimeControlError{Code: "execution_failed", Message: "OpenClaw sidecar execution did not succeed", Retryable: true}
	}
	return outcome
}

func controlError(code, message string, retryable bool) *RuntimeControlError {
	return &RuntimeControlError{Code: code, Message: message, Retryable: retryable}
}

func sortedMapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
