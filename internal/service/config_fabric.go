package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
)

// Config-fabric kinds come from the cascadia-nips generated registry through internal/kinds.
const (
	ConfigFabricListKind   = kinds.ConfigACLList
	ConfigFabricPolicyKind = kinds.ConfigPolicy
	ConfigFabricStatusKind = kinds.CASControlState
)

const (
	configStatusSchemaV3     = "cascadia.config.status.v3"
	configStatusSchema       = "cascadia.config.status.v2"
	legacyConfigStatusSchema = "cascadia.config.status.v1"
	configEntityType         = "config-fabric.desired"
)

var (
	configServiceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	configPolicyPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	configSchemaPattern    = regexp.MustCompile(`^cascadia\.config\.([a-z0-9][a-z0-9._-]*)\.v1$`)
	suspiciousKeyPattern   = regexp.MustCompile(`(?i)(^|_)(password|passwd|private_?key|secret|token|api_?key|credential)($|_)`)
)

type ConfigFabricSigner interface {
	GetPublicKey(ctx context.Context) (string, error)
	Sign(ctx context.Context, event *nostr.Event) error
}

// ConfigFabricPublisher delivers an operator-signed config-fabric event through
// the control-plane publish outbox without re-signing it. A nil error means the
// publish quorum accepted it; an error matching nostrutil.ErrPublishIncomplete
// means the event is durably queued and its runner keeps retrying the relays
// that have not accepted. Relays that have not accepted are retried either way.
// An error matching nostrutil.ErrPublishAbandoned means the outbox gave up on
// the event: its row is failed and it is not desired state.
type ConfigFabricPublisher interface {
	PublishPresignedEvent(ctx context.Context, event nostr.Event, entityType string) error
}

// Config-fabric delivery states reported on a publish receipt.
const (
	// ConfigDeliveryAccepted: the publish quorum of control-plane relays
	// accepted the event (remaining relays are still retried).
	ConfigDeliveryAccepted = "accepted"
	// ConfigDeliveryQueued: fewer relays than the publish quorum have accepted
	// yet; the signed event is durable and still being retried. Do not
	// republish it.
	ConfigDeliveryQueued = "queued"
)

type ConfigListItem struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
}

type ConfigPublishRequest struct {
	Kind       int              `json:"kind"`
	ServiceID  string           `json:"service_id"`
	PolicyName string           `json:"policy_name"`
	Scope      string           `json:"scope"`
	Version    int              `json:"version"`
	Schema     string           `json:"schema"`
	Policy     map[string]any   `json:"policy,omitempty"`
	SecretRefs map[string]any   `json:"secret_refs,omitempty"`
	Items      []ConfigListItem `json:"items,omitempty"`
}

type ConfigPublishReceipt struct {
	EventID string `json:"event_id"`
	PubKey  string `json:"pubkey"`
	Kind    int    `json:"kind"`
	Version int    `json:"version"`
	DTag    string `json:"d_tag"`
	// Delivery is ConfigDeliveryAccepted or ConfigDeliveryQueued.
	Delivery string `json:"delivery"`
}

type ConfigRollbackRequest struct {
	EventID string `json:"event_id"`
}

type ConfigFabricVersion struct {
	EventID    string           `json:"event_id"`
	PubKey     string           `json:"pubkey"`
	Kind       int              `json:"kind"`
	Version    int              `json:"version"`
	Schema     string           `json:"schema"`
	CreatedAt  time.Time        `json:"created_at"`
	Policy     map[string]any   `json:"policy,omitempty"`
	SecretRefs map[string]any   `json:"secret_refs,omitempty"`
	Items      []ConfigListItem `json:"items,omitempty"`
}

type ConfigFabricStatus struct {
	EventID            string    `json:"event_id"`
	PubKey             string    `json:"pubkey"`
	ConfigEventID      string    `json:"config_event_id"`
	Version            int       `json:"version"`
	Status             string    `json:"status"`
	EffectiveVersion   int       `json:"effective_version,omitempty"`
	LastAppliedEventID string    `json:"last_applied_event_id,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type ConfigDrift struct {
	ServiceID           string `json:"service_id"`
	PolicyName          string `json:"policy_name"`
	Scope               string `json:"scope"`
	DesiredEventID      string `json:"desired_event_id"`
	DesiredVersion      int    `json:"desired_version"`
	AppliedEventID      string `json:"applied_event_id,omitempty"`
	AppliedVersion      int    `json:"applied_version,omitempty"`
	Drift               bool   `json:"drift"`
	LastRejectionReason string `json:"last_rejection_reason,omitempty"`
	// Withdrawn reports that a consumer withdrew the current desired event
	// because its author deleted it (NIP-09) or it expired (NIP-40). The
	// consumer keeps enforcing the last applied config: an absent membership
	// or policy document would read as an empty allowlist, which admits every
	// pubkey. Publishing a newer version replaces it.
	Withdrawn       bool                  `json:"withdrawn,omitempty"`
	WithdrawnReason string                `json:"withdrawn_reason,omitempty"`
	Desired         *ConfigFabricVersion  `json:"desired,omitempty"`
	Effective       *ConfigFabricVersion  `json:"effective,omitempty"`
	Versions        []ConfigFabricVersion `json:"versions"`
	StatusHistory   []ConfigFabricStatus  `json:"status_history"`
}

// ConfigFabricDeliveryQuery reports the publish-outbox state of a desired
// config-fabric event. Without Postgres the NostrEventRecord carries no
// PublishState; this query lets ListDrift determine whether a version was
// abandoned by asking the local outbox directly (bahia-irsry.61).
type ConfigFabricDeliveryQuery interface {
	DeliveryOutcome(ctx context.Context, id string) (nostrutil.DeliveryOutcome, error)
}

type ConfigFabricService struct {
	repo      repository.NostrEventRepository
	publisher ConfigFabricPublisher
	signer    ConfigFabricSigner
	delivery  ConfigFabricDeliveryQuery
	now       func() time.Time
	mu        sync.Mutex
}

// ConfigFabricServiceOption configures a ConfigFabricService.
type ConfigFabricServiceOption func(*ConfigFabricService)

// WithDeliveryQuery sets the delivery-state query used by ListDrift to
// determine whether a desired version was abandoned when the event record
// carries no PublishState (non-Postgres mode, bahia-irsry.61).
func WithDeliveryQuery(q ConfigFabricDeliveryQuery) ConfigFabricServiceOption {
	return func(s *ConfigFabricService) { s.delivery = q }
}

func NewConfigFabricService(repo repository.NostrEventRepository, publisher ConfigFabricPublisher, signer ConfigFabricSigner, opts ...ConfigFabricServiceOption) *ConfigFabricService {
	s := &ConfigFabricService{repo: repo, publisher: publisher, signer: signer, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *ConfigFabricService) Publish(ctx context.Context, request ConfigPublishRequest) (*ConfigPublishReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishLocked(ctx, request)
}

func (s *ConfigFabricService) publishLocked(ctx context.Context, request ConfigPublishRequest) (*ConfigPublishReceipt, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("config-fabric storage is not configured")
	}
	if s.publisher == nil {
		return nil, fmt.Errorf("config-fabric relay publisher is not configured")
	}
	if s.signer == nil {
		return nil, fmt.Errorf("config-fabric operator Signet signer is not configured")
	}
	event, err := composeConfigEvent(request, s.now())
	if err != nil {
		return nil, err
	}
	pubkey, err := s.signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve config-fabric operator pubkey: %w", err)
	}
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if !isHex(pubkey, 32) {
		return nil, fmt.Errorf("config-fabric operator signer returned an invalid pubkey")
	}
	if err := s.requireMonotonicVersion(ctx, pubkey, request); err != nil {
		return nil, err
	}
	if err := s.signer.Sign(ctx, event); err != nil {
		return nil, fmt.Errorf("sign config-fabric event with operator Signet identity: %w", err)
	}
	if event.PubKey.Hex() != pubkey || !event.VerifySignature() {
		return nil, fmt.Errorf("config-fabric operator signer returned an invalid event signature")
	}
	if err := s.persistDesired(ctx, *event); err != nil {
		return nil, err
	}
	// The publisher owns delivery state from here: the row stays pending until
	// every control-plane relay has accepted or reached a terminal state.
	delivery := ConfigDeliveryAccepted
	if err := s.publisher.PublishPresignedEvent(ctx, *event, configEntityType); err != nil {
		if !nostrutil.IsPublishQueued(err) {
			// Includes nostrutil.ErrPublishAbandoned: every control-plane
			// relay rejected the event permanently, its outbox row is failed
			// and ListDrift does not treat it as desired state.
			return nil, fmt.Errorf("publish config-fabric event: %w", err)
		}
		// Durable and still being retried: report it as queued rather than
		// failing, so callers do not re-sign a second desired version.
		delivery = ConfigDeliveryQueued
	}
	return &ConfigPublishReceipt{EventID: event.ID.Hex(), PubKey: pubkey, Kind: request.Kind, Version: request.Version, DTag: configDTag(request.ServiceID, request.PolicyName), Delivery: delivery}, nil
}

func (s *ConfigFabricService) Rollback(ctx context.Context, eventID string) (*ConfigPublishReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repo == nil {
		return nil, fmt.Errorf("config-fabric storage is not configured")
	}
	if s.signer == nil {
		return nil, fmt.Errorf("config-fabric operator Signet signer is not configured")
	}
	eventID = strings.ToLower(strings.TrimSpace(eventID))
	if !isHex(eventID, 32) {
		return nil, fmt.Errorf("rollback event_id must be a 64-character hex event id")
	}
	prior, err := s.repo.GetByID(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("load rollback config event: %w", err)
	}
	if prior == nil {
		return nil, fmt.Errorf("rollback config event not found")
	}
	request, err := publishRequestFromRecord(*prior)
	if err != nil {
		return nil, fmt.Errorf("rollback source is not a valid desired-state event: %w", err)
	}
	pubkey, err := s.signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve config-fabric operator pubkey: %w", err)
	}
	maxVersion, err := s.maxVersion(ctx, strings.ToLower(strings.TrimSpace(pubkey)), request)
	if err != nil {
		return nil, err
	}
	request.Version = maxVersion + 1
	return s.publishLocked(ctx, request)
}

func composeConfigEvent(request ConfigPublishRequest, createdAt time.Time) (*nostr.Event, error) {
	if err := validateConfigRequest(request); err != nil {
		return nil, err
	}
	content := map[string]any{
		"service_id": request.ServiceID,
		"scope":      request.Scope,
		"version":    request.Version,
		"schema":     request.Schema,
	}
	if request.Kind == ConfigFabricPolicyKind {
		content["policy"] = request.Policy
		if len(request.SecretRefs) > 0 {
			content["secret_refs"] = request.SecretRefs
		}
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("encode config-fabric content: %w", err)
	}
	tags := nostr.Tags{
		{"d", configDTag(request.ServiceID, request.PolicyName)},
		{"service", request.ServiceID},
		{"scope", request.Scope},
		{"version", strconv.Itoa(request.Version)},
		{"schema", request.Schema},
	}
	for _, item := range request.Items {
		tags = append(tags, nostr.Tag{item.Tag, item.Value})
	}
	return &nostr.Event{Kind: nostr.Kind(request.Kind), CreatedAt: nostr.Timestamp(createdAt.Unix()), Tags: tags, Content: string(encoded)}, nil
}

// ComposeConfigEvent builds the canonical NIP-51/NIP-78 desired event consumed
// by the relay sidecar. The caller signs and publishes the returned event.
func ComposeConfigEvent(request ConfigPublishRequest, createdAt time.Time) (*nostr.Event, error) {
	return composeConfigEvent(request, createdAt)
}

func validateConfigRequest(request ConfigPublishRequest) error {
	if request.Kind != ConfigFabricListKind && request.Kind != ConfigFabricPolicyKind {
		return fmt.Errorf("kind must be %d (NIP-51 list) or %d (NIP-78 policy)", ConfigFabricListKind, ConfigFabricPolicyKind)
	}
	if !configServiceIDPattern.MatchString(request.ServiceID) {
		return fmt.Errorf("service_id has invalid shape")
	}
	if !configPolicyPattern.MatchString(request.PolicyName) {
		return fmt.Errorf("policy_name has invalid shape")
	}
	if !validConfigScope(request.Scope) {
		return fmt.Errorf("scope must be prod, staging, fleet, or host:<host>")
	}
	if request.Version < 1 {
		return fmt.Errorf("version must be a positive integer")
	}
	match := configSchemaPattern.FindStringSubmatch(request.Schema)
	if match == nil || match[1] != request.PolicyName {
		return fmt.Errorf("schema must be cascadia.config.<policy_name>.v1")
	}
	if request.Kind == ConfigFabricListKind {
		if request.Schema != "cascadia.config.membership.v1" {
			return fmt.Errorf("NIP-51 config lists require cascadia.config.membership.v1")
		}
		if request.Policy != nil || request.SecretRefs != nil {
			return fmt.Errorf("NIP-51 config lists use item tags, not policy content")
		}
		if len(request.Items) == 0 {
			return fmt.Errorf("NIP-51 config list requires at least one item")
		}
		for _, item := range request.Items {
			if err := validateListItem(item); err != nil {
				return err
			}
		}
		return nil
	}
	if request.Policy == nil {
		return fmt.Errorf("NIP-78 config policy requires a policy object")
	}
	if len(request.Items) != 0 {
		return fmt.Errorf("NIP-78 config policy does not accept list items")
	}
	if err := rejectSecrets(request.Policy, "policy"); err != nil {
		return err
	}
	if err := validateSecretRefs(request.SecretRefs); err != nil {
		return err
	}
	return nil
}

func validateListItem(item ConfigListItem) error {
	switch item.Tag {
	case "p":
		if !isHex(strings.ToLower(item.Value), 32) {
			return fmt.Errorf("p list item must be a 64-character hex pubkey")
		}
	case "r":
		parsed, err := url.ParseRequestURI(item.Value)
		if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss" && parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("r list item must be an http(s) or ws(s) URL")
		}
	case "a":
		parts := strings.Split(item.Value, ":")
		if len(parts) < 3 {
			return fmt.Errorf("a list item must be a Nostr address coordinate")
		}
	default:
		return fmt.Errorf("list item tag must be one of p, a, or r")
	}
	return nil
}

func validateSecretRefs(refs map[string]any) error {
	for name, raw := range refs {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("secret_refs.%s must be an object", name)
		}
		provider, _ := entry["provider"].(string)
		ref, _ := entry["ref"].(string)
		if provider != "signet" && provider != "file" && provider != "service" {
			return fmt.Errorf("secret_refs.%s.provider must be signet, file, or service", name)
		}
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("secret_refs.%s.ref is required", name)
		}
		if len(entry) != 2 {
			return fmt.Errorf("secret_refs.%s accepts only provider and ref", name)
		}
		if looksLikeSecretValue(ref) {
			return fmt.Errorf("secret_refs.%s.ref looks like a secret value", name)
		}
	}
	return nil
}

func rejectSecrets(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if suspiciousKeyPattern.MatchString(normalized) && !strings.HasSuffix(normalized, "_ref") && !strings.HasSuffix(normalized, "_file") && !strings.HasSuffix(normalized, "_path") {
				return fmt.Errorf("%s.%s looks like a secret-bearing field; publish a secret reference instead", path, key)
			}
			if err := rejectSecrets(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := rejectSecrets(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case string:
		if looksLikeSecretValue(typed) {
			return fmt.Errorf("%s looks like a secret value; publish a secret reference instead", path)
		}
	}
	return nil
}

func looksLikeSecretValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "nsec1") ||
		strings.HasPrefix(lower, "bearer ") ||
		strings.HasPrefix(lower, "sk-") ||
		strings.Contains(trimmed, "-----BEGIN PRIVATE KEY-----") ||
		strings.Contains(trimmed, "-----BEGIN OPENSSH PRIVATE KEY-----")
}

func validConfigScope(scope string) bool {
	if scope == "prod" || scope == "staging" || scope == "fleet" {
		return true
	}
	host := strings.TrimPrefix(scope, "host:")
	return host != scope && configServiceIDPattern.MatchString(strings.ToLower(host))
}

func configDTag(serviceID, policyName string) string {
	return "service:" + serviceID + ":" + policyName
}

func (s *ConfigFabricService) requireMonotonicVersion(ctx context.Context, pubkey string, request ConfigPublishRequest) error {
	maxVersion, err := s.maxVersion(ctx, pubkey, request)
	if err != nil {
		return err
	}
	if request.Version <= maxVersion {
		return fmt.Errorf("version must advance monotonically: got %d, latest is %d", request.Version, maxVersion)
	}
	return nil
}

func (s *ConfigFabricService) maxVersion(ctx context.Context, pubkey string, request ConfigPublishRequest) (int, error) {
	records, err := s.repo.FindByTag(ctx, "d", configDTag(request.ServiceID, request.PolicyName), []int{ConfigFabricListKind, ConfigFabricPolicyKind}, 1000)
	if err != nil {
		return 0, fmt.Errorf("load config-fabric version history: %w", err)
	}
	maxVersion := 0
	for _, record := range records {
		if record.PubKey != pubkey {
			continue
		}
		parsed, err := desiredFromRecord(record)
		if err != nil || parsed.Scope != request.Scope {
			continue
		}
		if parsed.Version > maxVersion {
			maxVersion = parsed.Version
		}
	}
	return maxVersion, nil
}

// isDesiredAbandoned reports whether a desired-state event's publish was
// abandoned. With PostgreSQL the record carries the publish state directly;
// without it, the delivery query (local outbox) is asked (bahia-irsry.61).
func (s *ConfigFabricService) isDesiredAbandoned(ctx context.Context, record repository.NostrEventRecord) bool {
	if record.PublishState == repository.NostrPublishStateFailed {
		return true
	}
	// The local event store does not track publish state, so records read
	// back from it have an empty PublishState. Fall through to the delivery
	// query when one is configured.
	if record.PublishState != "" || s.delivery == nil {
		return false
	}
	outcome, err := s.delivery.DeliveryOutcome(ctx, record.ID)
	if err != nil {
		return false
	}
	return outcome == nostrutil.DeliveryAbandoned
}

func (s *ConfigFabricService) persistDesired(ctx context.Context, event nostr.Event) error {
	tags, err := json.Marshal(event.Tags)
	if err != nil {
		return fmt.Errorf("encode config-fabric tags for storage: %w", err)
	}
	record := &repository.NostrEventRecord{
		ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(), Content: event.Content,
		Tags: tags, Sig: hex.EncodeToString(event.Sig[:]), CreatedAt: event.CreatedAt.Time(), ReceivedAt: s.now(),
		EntityType: configEntityType, PublishState: repository.NostrPublishStatePending,
		// The control-plane publisher's local outbox delivers the event; this
		// row is its archive copy, whose publish state the publisher keeps in
		// step (failed versions are not desired state, see ListDrift). No
		// PostgreSQL runner drains it.
		PublishTarget: repository.LocalOutboxArchiveTarget(repository.NostrPublishTargetControlPlane),
	}
	if _, err := s.repo.Record(ctx, record); err != nil {
		return fmt.Errorf("persist config-fabric desired event before publish: %w", err)
	}
	return nil
}

type desiredConfig struct {
	ServiceID  string
	PolicyName string
	Scope      string
	Version    int
	Schema     string
	EventID    string
	CreatedAt  time.Time
	Record     repository.NostrEventRecord
}

type statusConfig struct {
	ServiceID          string `json:"service_id"`
	Scope              string `json:"scope"`
	Version            int    `json:"version"`
	PolicySchema       string `json:"policy_schema"`
	ConfigEventID      string `json:"config_event_id"`
	Status             string `json:"status"`
	EffectiveVersion   int    `json:"effective_version,omitempty"`
	LastAppliedEventID string `json:"last_applied_event_id,omitempty"`
	Reason             string `json:"reason,omitempty"`
	PolicyName         string
	EventID            string
	PubKey             string
	CreatedAt          time.Time
}

func (s *ConfigFabricService) ListDrift(ctx context.Context) ([]ConfigDrift, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("config-fabric storage is not configured")
	}
	records := make([]repository.NostrEventRecord, 0)
	for _, kind := range []int{ConfigFabricListKind, ConfigFabricPolicyKind, ConfigFabricStatusKind} {
		kindRecords, err := s.repo.ListByKind(ctx, kind, 10000)
		if err != nil {
			return nil, fmt.Errorf("load persisted config-fabric kind %d: %w", kind, err)
		}
		records = append(records, kindRecords...)
	}
	return s.projectDrift(ctx, records), nil
}

// ConfigDriftFromEvents computes the same projection as the REST read path
// from verified events in the operator's local Nostr store.
func ConfigDriftFromEvents(events []nostr.Event) ([]ConfigDrift, error) {
	records := make([]repository.NostrEventRecord, 0, len(events))
	for _, event := range events {
		if !event.CheckID() || !event.VerifySignature() {
			continue
		}
		tags, err := json.Marshal(event.Tags)
		if err != nil {
			return nil, fmt.Errorf("encode config event tags: %w", err)
		}
		records = append(records, repository.NostrEventRecord{
			ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(),
			Content: event.Content, Tags: tags, CreatedAt: time.Unix(int64(event.CreatedAt), 0),
		})
	}
	return (&ConfigFabricService{}).projectDrift(context.Background(), records), nil
}

func (s *ConfigFabricService) projectDrift(ctx context.Context, records []repository.NostrEventRecord) []ConfigDrift {
	desired := map[string]desiredConfig{}
	desiredHistory := map[string][]desiredConfig{}
	statuses := map[string][]statusConfig{}
	for _, record := range records {
		switch record.Kind {
		case ConfigFabricListKind, ConfigFabricPolicyKind:
			if s.isDesiredAbandoned(ctx, record) {
				// The outbox abandoned this version (at publish time or
				// later in its runner): no relay holds it, so it is not
				// desired state. Versions stay monotonic past it.
				continue
			}
			item, err := desiredFromRecord(record)
			if err != nil {
				continue
			}
			key := configCoordinate(item.ServiceID, item.PolicyName, item.Scope)
			desiredHistory[key] = append(desiredHistory[key], item)
			current, ok := desired[key]
			if !ok || item.Version > current.Version || (item.Version == current.Version && item.CreatedAt.After(current.CreatedAt)) {
				desired[key] = item
			}
		case ConfigFabricStatusKind:
			item, err := statusFromRecord(record)
			if err != nil {
				continue
			}
			key := configCoordinate(item.ServiceID, item.PolicyName, item.Scope)
			statuses[key] = append(statuses[key], item)
		}
	}
	result := make([]ConfigDrift, 0, len(desired))
	for key, wanted := range desired {
		view := ConfigDrift{
			ServiceID: wanted.ServiceID, PolicyName: wanted.PolicyName, Scope: wanted.Scope,
			DesiredEventID: wanted.EventID, DesiredVersion: wanted.Version, Drift: true,
			Versions:      make([]ConfigFabricVersion, 0, len(desiredHistory[key])),
			StatusHistory: make([]ConfigFabricStatus, 0, len(statuses[key])),
		}
		sort.Slice(desiredHistory[key], func(i, j int) bool {
			if desiredHistory[key][i].Version != desiredHistory[key][j].Version {
				return desiredHistory[key][i].Version > desiredHistory[key][j].Version
			}
			return desiredHistory[key][i].CreatedAt.After(desiredHistory[key][j].CreatedAt)
		})
		for _, item := range desiredHistory[key] {
			version, err := configVersionFromDesired(item)
			if err != nil {
				continue
			}
			view.Versions = append(view.Versions, version)
			if item.EventID == wanted.EventID {
				copy := version
				view.Desired = &copy
			}
		}
		sort.Slice(statuses[key], func(i, j int) bool { return statuses[key][i].CreatedAt.After(statuses[key][j].CreatedAt) })
		for _, status := range statuses[key] {
			view.StatusHistory = append(view.StatusHistory, ConfigFabricStatus{
				EventID: status.EventID, PubKey: status.PubKey, ConfigEventID: status.ConfigEventID,
				Version: status.Version, Status: status.Status, EffectiveVersion: status.EffectiveVersion,
				LastAppliedEventID: status.LastAppliedEventID, Reason: status.Reason, CreatedAt: status.CreatedAt,
			})
			// Status coordinates preserve per-target facts, not one wall-clock
			// snapshot. Versions are monotonic; a delayed older receipt must
			// not roll the effective configuration backwards.
			if status.EffectiveVersion > view.AppliedVersion && isHex(status.LastAppliedEventID, 32) {
				view.AppliedEventID = status.LastAppliedEventID
				view.AppliedVersion = status.EffectiveVersion
			}
			if view.LastRejectionReason == "" && status.Status == "rejected" {
				view.LastRejectionReason = status.Reason
			}
			// Only a withdrawal of the current desired event matters: a newer
			// published version supersedes an older withdrawn one.
			if !view.Withdrawn && status.Status == "withdrawn" && status.ConfigEventID == wanted.EventID {
				view.Withdrawn = true
				view.WithdrawnReason = status.Reason
			}
		}
		for i := range view.Versions {
			if view.Versions[i].EventID == view.AppliedEventID {
				copy := view.Versions[i]
				view.Effective = &copy
				break
			}
		}
		view.Drift = view.AppliedEventID != view.DesiredEventID || view.AppliedVersion != view.DesiredVersion
		result = append(result, view)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ServiceID != result[j].ServiceID {
			return result[i].ServiceID < result[j].ServiceID
		}
		if result[i].PolicyName != result[j].PolicyName {
			return result[i].PolicyName < result[j].PolicyName
		}
		return result[i].Scope < result[j].Scope
	})
	return result
}

func desiredFromRecord(record repository.NostrEventRecord) (desiredConfig, error) {
	if record.Kind != ConfigFabricListKind && record.Kind != ConfigFabricPolicyKind {
		return desiredConfig{}, fmt.Errorf("unsupported desired event kind")
	}
	var content struct {
		ServiceID  string         `json:"service_id"`
		Scope      string         `json:"scope"`
		Version    int            `json:"version"`
		Schema     string         `json:"schema"`
		Policy     map[string]any `json:"policy"`
		SecretRefs map[string]any `json:"secret_refs"`
	}
	if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
		return desiredConfig{}, err
	}
	tags, err := decodeTags(record.Tags)
	if err != nil {
		return desiredConfig{}, err
	}
	service, err := exactlyOneTag(tags, "service")
	if err != nil || service != content.ServiceID {
		return desiredConfig{}, fmt.Errorf("service tag/content mismatch")
	}
	scope, err := exactlyOneTag(tags, "scope")
	if err != nil || scope != content.Scope {
		return desiredConfig{}, fmt.Errorf("scope tag/content mismatch")
	}
	versionTag, err := exactlyOneTag(tags, "version")
	if err != nil || versionTag != strconv.Itoa(content.Version) {
		return desiredConfig{}, fmt.Errorf("version tag/content mismatch")
	}
	schema, err := exactlyOneTag(tags, "schema")
	if err != nil || schema != content.Schema {
		return desiredConfig{}, fmt.Errorf("schema tag/content mismatch")
	}
	dTag, err := exactlyOneTag(tags, "d")
	if err != nil {
		return desiredConfig{}, err
	}
	prefix := "service:" + service + ":"
	if !strings.HasPrefix(dTag, prefix) {
		return desiredConfig{}, fmt.Errorf("invalid desired d tag")
	}
	policyName := strings.TrimPrefix(dTag, prefix)
	request := ConfigPublishRequest{Kind: record.Kind, ServiceID: service, PolicyName: policyName, Scope: scope, Version: content.Version, Schema: schema, Policy: content.Policy, SecretRefs: content.SecretRefs}
	if record.Kind == ConfigFabricListKind {
		for _, tag := range tags {
			if len(tag) >= 2 && (tag[0] == "p" || tag[0] == "a" || tag[0] == "r") {
				request.Items = append(request.Items, ConfigListItem{Tag: tag[0], Value: tag[1]})
			}
		}
	}
	if err := validateConfigRequest(request); err != nil {
		return desiredConfig{}, err
	}
	return desiredConfig{ServiceID: service, PolicyName: policyName, Scope: scope, Version: content.Version, Schema: schema, EventID: record.ID, CreatedAt: record.CreatedAt, Record: record}, nil
}

func statusFromRecord(record repository.NostrEventRecord) (statusConfig, error) {
	var status statusConfig
	if err := json.Unmarshal([]byte(record.Content), &status); err != nil {
		return status, err
	}
	tags, err := decodeTags(record.Tags)
	if err != nil {
		return status, err
	}
	schema, err := exactlyOneTag(tags, "schema")
	if err != nil || (schema != configStatusSchemaV3 && schema != configStatusSchema && schema != legacyConfigStatusSchema) {
		return status, fmt.Errorf("invalid status schema tag")
	}
	if domain, err := exactlyOneTag(tags, "domain"); err != nil || domain != "config-status" {
		return status, fmt.Errorf("invalid status domain tag")
	}
	if tagged, err := exactlyOneTag(tags, "service"); err != nil || tagged != status.ServiceID {
		return status, fmt.Errorf("status service mismatch")
	}
	if tagged, err := exactlyOneTag(tags, "scope"); err != nil || tagged != status.Scope {
		return status, fmt.Errorf("status scope mismatch")
	}
	if tagged, err := exactlyOneTag(tags, "version"); err != nil || tagged != strconv.Itoa(status.Version) {
		return status, fmt.Errorf("status version mismatch")
	}
	if tagged, err := exactlyOneTag(tags, "status"); err != nil || tagged != status.Status {
		return status, fmt.Errorf("status value mismatch")
	}
	if tagged, err := exactlyOneTag(tags, "e"); err != nil || tagged != status.ConfigEventID {
		return status, fmt.Errorf("status desired event mismatch")
	}
	if !configServiceIDPattern.MatchString(status.ServiceID) || !validConfigScope(status.Scope) || status.Version < 1 || !isHex(status.ConfigEventID, 32) {
		return status, fmt.Errorf("invalid status content")
	}
	schemaMatch := configSchemaPattern.FindStringSubmatch(status.PolicySchema)
	if schemaMatch == nil {
		return status, fmt.Errorf("invalid status policy schema")
	}
	status.PolicyName = schemaMatch[1]
	dTag, err := exactlyOneTag(tags, "d")
	stableDTag := "config-status:" + status.ServiceID + ":" + status.PolicyName + ":" + status.Scope
	// v3 uses a stable d per (service, policy, scope); v2 embeds eventID+status.
	var expectedDTag string
	switch schema {
	case configStatusSchemaV3:
		expectedDTag = stableDTag
	case configStatusSchema:
		expectedDTag = stableDTag + ":" + status.ConfigEventID + ":" + status.Status
	default:
		expectedDTag = stableDTag
	}
	if err != nil || dTag != expectedDTag {
		return status, fmt.Errorf("invalid config status d tag")
	}
	if status.EffectiveVersion < 0 || (status.EffectiveVersion == 0 && status.LastAppliedEventID != "") ||
		(status.EffectiveVersion > 0 && !isHex(status.LastAppliedEventID, 32)) {
		return status, fmt.Errorf("invalid effective config reference")
	}
	if status.Status == "applied" {
		if status.EffectiveVersion < 1 || !isHex(status.LastAppliedEventID, 32) {
			return status, fmt.Errorf("invalid applied status content")
		}
		if (schema == configStatusSchema || schema == configStatusSchemaV3) && (status.EffectiveVersion != status.Version || status.LastAppliedEventID != status.ConfigEventID) {
			return status, fmt.Errorf("applied status target mismatch")
		}
	} else if status.Status == "rejected" || status.Status == "withdrawn" {
		if status.Status == "withdrawn" && schema == legacyConfigStatusSchema {
			return status, fmt.Errorf("withdrawn status requires %s or later", configStatusSchema)
		}
		if strings.TrimSpace(status.Reason) == "" || looksLikeSecretValue(status.Reason) {
			return status, fmt.Errorf("invalid %s status reason", status.Status)
		}
	} else if status.Status != "accepted" {
		return status, fmt.Errorf("invalid status")
	}
	status.EventID = record.ID
	status.PubKey = record.PubKey
	status.CreatedAt = record.CreatedAt
	return status, nil
}

func configVersionFromDesired(desired desiredConfig) (ConfigFabricVersion, error) {
	request, err := publishRequestFromRecord(desired.Record)
	if err != nil {
		return ConfigFabricVersion{}, err
	}
	return ConfigFabricVersion{
		EventID: desired.EventID, PubKey: desired.Record.PubKey, Kind: desired.Record.Kind,
		Version: desired.Version, Schema: desired.Schema, CreatedAt: desired.CreatedAt,
		Policy: request.Policy, SecretRefs: request.SecretRefs, Items: request.Items,
	}, nil
}

func publishRequestFromRecord(record repository.NostrEventRecord) (ConfigPublishRequest, error) {
	desired, err := desiredFromRecord(record)
	if err != nil {
		return ConfigPublishRequest{}, err
	}
	var content struct {
		Policy     map[string]any `json:"policy"`
		SecretRefs map[string]any `json:"secret_refs"`
	}
	if err := json.Unmarshal([]byte(record.Content), &content); err != nil {
		return ConfigPublishRequest{}, err
	}
	request := ConfigPublishRequest{Kind: record.Kind, ServiceID: desired.ServiceID, PolicyName: desired.PolicyName, Scope: desired.Scope, Version: desired.Version, Schema: desired.Schema, Policy: content.Policy, SecretRefs: content.SecretRefs}
	if record.Kind == ConfigFabricListKind {
		tags, _ := decodeTags(record.Tags)
		for _, tag := range tags {
			if len(tag) >= 2 && (tag[0] == "p" || tag[0] == "a" || tag[0] == "r") {
				request.Items = append(request.Items, ConfigListItem{Tag: tag[0], Value: tag[1]})
			}
		}
	}
	return request, nil
}

// ConfigRequestFromEvent validates a locally stored desired event and returns
// its publish payload for rollback at a new version.
func ConfigRequestFromEvent(event nostr.Event) (ConfigPublishRequest, error) {
	if !event.CheckID() || !event.VerifySignature() {
		return ConfigPublishRequest{}, fmt.Errorf("desired event id or signature is invalid")
	}
	tags, err := json.Marshal(event.Tags)
	if err != nil {
		return ConfigPublishRequest{}, err
	}
	return publishRequestFromRecord(repository.NostrEventRecord{
		ID: event.ID.Hex(), Kind: int(event.Kind), PubKey: event.PubKey.Hex(),
		Content: event.Content, Tags: tags, CreatedAt: time.Unix(int64(event.CreatedAt), 0),
	})
}

func decodeTags(raw json.RawMessage) (nostr.Tags, error) {
	var tags nostr.Tags
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, fmt.Errorf("decode event tags: %w", err)
	}
	return tags, nil
}

func exactlyOneTag(tags nostr.Tags, name string) (string, error) {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			count++
			value = tag[1]
		}
	}
	if count != 1 || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("event requires exactly one %s tag", name)
	}
	return value, nil
}

func configCoordinate(serviceID, policyName, scope string) string {
	return serviceID + "\x00" + policyName + "\x00" + scope
}

func isHex(value string, bytes int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == bytes
}
