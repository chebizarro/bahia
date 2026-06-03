package nostr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

// StandbyNodeDefinition is the Nostr wire shape for kind 31402 continuity standby definitions.
type StandbyNodeDefinition struct {
	WorkerPubKey  string                  `json:"worker_pubkey"`
	Host          string                  `json:"host"`
	Role          string                  `json:"role"`
	ServiceKey    string                  `json:"service_key"`
	Tier          domain.StandbyTier      `json:"tier,omitempty"`
	ArtifactRef   string                  `json:"artifact_ref,omitempty"`
	Supports      []string                `json:"supports,omitempty"`
	Profiles      []domain.ContinuityMode `json:"profiles"`
	UpdatedAt     time.Time               `json:"updated_at"`
	SourceEventID string                  `json:"source_event_id,omitempty"`
}

// ContinuityCommandRequest is the shared Nostr wire shape for failover and recovery requests.
type ContinuityCommandRequest struct {
	ServiceKey         string                `json:"service_key"`
	TargetWorkerPubKey string                `json:"target_worker_pubkey"`
	TargetProfile      domain.ContinuityMode `json:"target_profile,omitempty"`
	RecipeName         string                `json:"recipe_name,omitempty"`
	RequestedBy        string                `json:"requested_by,omitempty"`
	RequestEventID     string                `json:"request_event_id,omitempty"`
	IdempotencyKey     string                `json:"idempotency_key"`
	Reason             string                `json:"reason,omitempty"`
	Metadata           map[string]any        `json:"metadata,omitempty"`
}

// BahiaIdentityPayload is the JSON content for kind 31410 Bahia identity definitions.
type BahiaIdentityPayload struct {
	Version        string `json:"version"`
	CatalogVersion string `json:"catalog_version"`
	Mode           string `json:"mode"`
	StartedAt      int64  `json:"started_at"`
}

// ReplayCheckpointPayload is the JSON content for kind 31411 Bahia replay checkpoints.
type ReplayCheckpointPayload struct {
	CatalogVersion string           `json:"catalog_version"`
	Cursors        map[string]int64 `json:"cursors"`
	Phase          string           `json:"phase"`
}

// ReadinessStatusPayload is the JSON content for kind 30360 Bahia readiness status events.
type ReadinessStatusPayload struct {
	Phase         string            `json:"phase"`
	ActiveTier    int               `json:"active_tier"`
	RequestedTier int               `json:"requested_tier"`
	Ready         bool              `json:"ready"`
	Checks        map[string]string `json:"checks"`
}

// EncodeBahiaIdentity serializes a Bahia identity definition as a kind 31410 replaceable event.
func EncodeBahiaIdentity(payload BahiaIdentityPayload, dTag string) gonostr.Event {
	return encodeBahiaSystemEvent(KindBahiaIdentityDefinition, dTag, "bahia-identity", payload)
}

// DecodeBahiaIdentity deserializes a kind 31410 Bahia identity definition.
func DecodeBahiaIdentity(ev *gonostr.Event) (*BahiaIdentityPayload, error) {
	var payload BahiaIdentityPayload
	if err := decodeBahiaSystemEvent(ev, KindBahiaIdentityDefinition, &payload, "Bahia identity"); err != nil {
		return nil, err
	}
	return &payload, nil
}

// EncodeReplayCheckpoint serializes a Bahia replay checkpoint as a kind 31411 replaceable event.
func EncodeReplayCheckpoint(payload ReplayCheckpointPayload, dTag string) gonostr.Event {
	return encodeBahiaSystemEvent(KindBahiaReplayCheckpoint, dTag, "bahia-replay-checkpoint", payload)
}

// DecodeReplayCheckpoint deserializes a kind 31411 Bahia replay checkpoint.
func DecodeReplayCheckpoint(ev *gonostr.Event) (*ReplayCheckpointPayload, error) {
	var payload ReplayCheckpointPayload
	if err := decodeBahiaSystemEvent(ev, KindBahiaReplayCheckpoint, &payload, "replay checkpoint"); err != nil {
		return nil, err
	}
	return &payload, nil
}

// EncodeReadinessStatus serializes a Bahia readiness status as a kind 30360 replaceable event.
func EncodeReadinessStatus(payload ReadinessStatusPayload, dTag string) gonostr.Event {
	return encodeBahiaSystemEvent(KindBahiaReadinessStatus, dTag, "bahia-readiness", payload)
}

// DecodeReadinessStatus deserializes a kind 30360 Bahia readiness status.
func DecodeReadinessStatus(ev *gonostr.Event) (*ReadinessStatusPayload, error) {
	var payload ReadinessStatusPayload
	if err := decodeBahiaSystemEvent(ev, KindBahiaReadinessStatus, &payload, "readiness status"); err != nil {
		return nil, err
	}
	return &payload, nil
}

// EncodeContinuityProfileEvent serializes a service continuity profile as a kind 31400 tag-block event.
func EncodeContinuityProfileEvent(profile domain.ServiceContinuityProfile) (gonostr.Event, error) {
	if err := profile.Validate(); err != nil {
		return gonostr.Event{}, err
	}
	tags := gonostr.Tags{
		{"d", "continuity-profile:" + profile.ServiceKey},
		{"service", profile.ServiceKey},
	}
	if profile.PrimaryWorkerPubKey != "" {
		tags = append(tags, gonostr.Tag{"primary", profile.PrimaryWorkerPubKey})
		tags = append(tags, gonostr.Tag{"p", profile.PrimaryWorkerPubKey})
	}
	for _, mode := range orderedContinuityModes(profile.Profiles) {
		spec := profile.Profiles[mode]
		tags = append(tags, gonostr.Tag{"profile", string(mode)})
		for _, req := range sortedStrings(spec.Requires) {
			tags = append(tags, gonostr.Tag{"requires", req})
		}
		for _, disabled := range sortedStrings(spec.Disables) {
			tags = append(tags, gonostr.Tag{"disables", disabled})
		}
		for _, key := range sortedMapKeys(spec.Limits) {
			tags = append(tags, gonostr.Tag{"limit", key, spec.Limits[key]})
		}
		for _, key := range sortedMapKeys(spec.Attributes) {
			tags = append(tags, gonostr.Tag{"attr", key, spec.Attributes[key]})
		}
	}
	return continuityEventBase(KindContinuityProfile, tags, ""), nil
}

// DecodeContinuityProfileEvent deserializes a kind 31400 tag-block event.
func DecodeContinuityProfileEvent(event *gonostr.Event) (*domain.ServiceContinuityProfile, error) {
	if event == nil {
		return nil, fmt.Errorf("continuity profile event is nil")
	}
	if event.Kind != KindContinuityProfile {
		return nil, fmt.Errorf("unexpected continuity profile kind %d", event.Kind)
	}
	if continuityTagValue(event.Tags, "d") == "" {
		return nil, fmt.Errorf("continuity profile d tag is required")
	}
	profile := &domain.ServiceContinuityProfile{
		ServiceKey:          strings.TrimSpace(continuityTagValue(event.Tags, "service")),
		PrimaryWorkerPubKey: strings.TrimSpace(continuityTagValue(event.Tags, "primary")),
		Profiles:            map[domain.ContinuityMode]domain.ContinuityProfileSpec{},
		UpdatedAt:           event.CreatedAt.Time().UTC(),
		SourceEventID:       event.ID,
	}
	var current domain.ContinuityMode
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "profile":
			mode := domain.ContinuityMode(strings.TrimSpace(tag[1]))
			if !mode.IsValid() {
				return nil, fmt.Errorf("continuity profile mode %q is invalid", mode)
			}
			current = mode
			if _, ok := profile.Profiles[current]; !ok {
				profile.Profiles[current] = domain.ContinuityProfileSpec{}
			}
		case "requires":
			spec, err := profileSpecForCurrent(profile.Profiles, current, tag[0])
			if err != nil {
				return nil, err
			}
			spec.Requires = append(spec.Requires, strings.TrimSpace(tag[1]))
			profile.Profiles[current] = spec
		case "disables", "disable":
			spec, err := profileSpecForCurrent(profile.Profiles, current, tag[0])
			if err != nil {
				return nil, err
			}
			spec.Disables = append(spec.Disables, strings.TrimSpace(tag[1]))
			profile.Profiles[current] = spec
		case "limit":
			if len(tag) < 3 {
				return nil, fmt.Errorf("continuity profile limit tag requires key and value")
			}
			spec, err := profileSpecForCurrent(profile.Profiles, current, tag[0])
			if err != nil {
				return nil, err
			}
			if spec.Limits == nil {
				spec.Limits = map[string]string{}
			}
			spec.Limits[strings.TrimSpace(tag[1])] = strings.TrimSpace(tag[2])
			profile.Profiles[current] = spec
		case "attr", "attribute":
			if len(tag) < 3 {
				return nil, fmt.Errorf("continuity profile attribute tag requires key and value")
			}
			spec, err := profileSpecForCurrent(profile.Profiles, current, tag[0])
			if err != nil {
				return nil, err
			}
			if spec.Attributes == nil {
				spec.Attributes = map[string]string{}
			}
			spec.Attributes[strings.TrimSpace(tag[1])] = strings.TrimSpace(tag[2])
			profile.Profiles[current] = spec
		}
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return profile, nil
}

// EncodeFailoverPolicyEvent serializes a failover recipe as a kind 31401 indexed-tag event with JSON content.
func EncodeFailoverPolicyEvent(recipe domain.ContinuityRecipe) (gonostr.Event, error) {
	recipe.Kind = domain.ContinuityRecipeKindFailover
	return encodeContinuityRecipeEvent(KindFailoverPolicy, "failover-policy", recipe)
}

// DecodeFailoverPolicyEvent deserializes a kind 31401 failover policy event.
func DecodeFailoverPolicyEvent(event *gonostr.Event) (*domain.ContinuityRecipe, error) {
	return decodeContinuityRecipeEvent(event, KindFailoverPolicy, domain.ContinuityRecipeKindFailover)
}

// EncodeRecoveryWorkflowEvent serializes a recovery recipe as a kind 31404 indexed-tag event with JSON content.
func EncodeRecoveryWorkflowEvent(recipe domain.ContinuityRecipe) (gonostr.Event, error) {
	recipe.Kind = domain.ContinuityRecipeKindRecovery
	return encodeContinuityRecipeEvent(KindRecoveryWorkflow, "recovery-workflow", recipe)
}

// DecodeRecoveryWorkflowEvent deserializes a kind 31404 recovery workflow event.
func DecodeRecoveryWorkflowEvent(event *gonostr.Event) (*domain.ContinuityRecipe, error) {
	return decodeContinuityRecipeEvent(event, KindRecoveryWorkflow, domain.ContinuityRecipeKindRecovery)
}

// EncodeStandbyNodeDefinitionEvent serializes a standby definition as kind 31402 tags.
func EncodeStandbyNodeDefinitionEvent(def StandbyNodeDefinition) (gonostr.Event, error) {
	if err := validateStandbyNodeDefinition(&def); err != nil {
		return gonostr.Event{}, err
	}
	tags := gonostr.Tags{
		{"d", standbyDefinitionDTag(def)},
		{"worker", def.WorkerPubKey},
		{"p", def.WorkerPubKey},
		{"host", def.Host},
		{"role", def.Role},
		{"service", def.ServiceKey},
	}
	if def.Tier != "" {
		tags = append(tags, gonostr.Tag{"tier", string(def.Tier)})
	}
	if def.ArtifactRef != "" {
		tags = append(tags, gonostr.Tag{"artifact_ref", def.ArtifactRef})
	}
	for _, support := range sortedStrings(def.Supports) {
		tags = append(tags, gonostr.Tag{"supports", support})
	}
	profiles := append([]domain.ContinuityMode(nil), def.Profiles...)
	sort.Slice(profiles, func(i, j int) bool { return string(profiles[i]) < string(profiles[j]) })
	for _, profile := range profiles {
		tags = append(tags, gonostr.Tag{"profile", string(profile)})
	}
	return continuityEventBase(KindStandbyNodeDefinition, tags, ""), nil
}

// DecodeStandbyNodeDefinitionEvent deserializes a kind 31402 standby definition event.
func DecodeStandbyNodeDefinitionEvent(event *gonostr.Event) (*StandbyNodeDefinition, error) {
	if event == nil {
		return nil, fmt.Errorf("standby node definition event is nil")
	}
	if event.Kind != KindStandbyNodeDefinition {
		return nil, fmt.Errorf("unexpected standby node definition kind %d", event.Kind)
	}
	if continuityTagValue(event.Tags, "d") == "" {
		return nil, fmt.Errorf("standby node definition d tag is required")
	}
	def := &StandbyNodeDefinition{
		WorkerPubKey:  firstNonEmpty(continuityTagValue(event.Tags, "worker"), continuityTagValue(event.Tags, "p")),
		Host:          continuityTagValue(event.Tags, "host"),
		Role:          continuityTagValue(event.Tags, "role"),
		ServiceKey:    continuityTagValue(event.Tags, "service"),
		Tier:          domain.StandbyTier(continuityTagValue(event.Tags, "tier")),
		ArtifactRef:   firstNonEmpty(continuityTagValue(event.Tags, "artifact_ref"), continuityTagValue(event.Tags, "image_ref"), continuityTagValue(event.Tags, "artifact"), continuityTagValue(event.Tags, "artifact_id")),
		Supports:      continuityTagValues(event.Tags, "supports"),
		UpdatedAt:     event.CreatedAt.Time().UTC(),
		SourceEventID: event.ID,
	}
	for _, raw := range continuityTagValues(event.Tags, "profile") {
		mode := domain.ContinuityMode(raw)
		if !mode.IsValid() {
			return nil, fmt.Errorf("standby profile %q is invalid", raw)
		}
		def.Profiles = append(def.Profiles, mode)
	}
	if err := validateStandbyNodeDefinition(def); err != nil {
		return nil, err
	}
	return def, nil
}

// EncodeReplicationPolicyEvent serializes a replication policy as a kind 31403 indexed-tag event with JSON content.
func EncodeReplicationPolicyEvent(policy domain.ReplicationPolicy) (gonostr.Event, error) {
	if err := validateReplicationPolicy(&policy); err != nil {
		return gonostr.Event{}, err
	}
	content, err := json.Marshal(policy)
	if err != nil {
		return gonostr.Event{}, fmt.Errorf("marshal replication policy: %w", err)
	}
	tags := gonostr.Tags{{"d", "replication-policy:" + policy.ServiceKey}, {"service", policy.ServiceKey}}
	return continuityEventBase(KindReplicationPolicy, tags, string(content)), nil
}

// DecodeReplicationPolicyEvent deserializes a kind 31403 replication policy event.
func DecodeReplicationPolicyEvent(event *gonostr.Event) (*domain.ReplicationPolicy, error) {
	if event == nil {
		return nil, fmt.Errorf("replication policy event is nil")
	}
	if event.Kind != KindReplicationPolicy {
		return nil, fmt.Errorf("unexpected replication policy kind %d", event.Kind)
	}
	if continuityTagValue(event.Tags, "d") == "" {
		return nil, fmt.Errorf("replication policy d tag is required")
	}
	if strings.TrimSpace(event.Content) == "" {
		return nil, fmt.Errorf("replication policy content is required")
	}
	var policy domain.ReplicationPolicy
	if err := json.Unmarshal([]byte(event.Content), &policy); err != nil {
		return nil, fmt.Errorf("decode replication policy content: %w", err)
	}
	if serviceTag := strings.TrimSpace(continuityTagValue(event.Tags, "service")); serviceTag != "" {
		if policy.ServiceKey != "" && policy.ServiceKey != serviceTag {
			return nil, fmt.Errorf("replication policy service tag and content differ")
		}
		policy.ServiceKey = serviceTag
	}
	policy.UpdatedAt = event.CreatedAt.Time().UTC()
	policy.SourceEventID = event.ID
	if err := validateReplicationPolicy(&policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

const heartbeatObservationStatusSchema = "bahia.status.continuity-heartbeat.v1"

// EncodeHeartbeatObservationEvent serializes a worker heartbeat as a NIP-38 status event.
func EncodeHeartbeatObservationEvent(obs domain.HeartbeatObservation) (gonostr.Event, error) {
	if err := validateHeartbeatObservation(&obs); err != nil {
		return gonostr.Event{}, err
	}
	tags := gonostr.Tags{
		{"d", "continuity:heartbeat:" + obs.WorkerPubKey},
		{"domain", "continuity"},
		{"schema", heartbeatObservationStatusSchema},
		{"status", "online"},
		{"legacy_kind", strconv.Itoa(KindHeartbeatObservation)},
		{"worker", obs.WorkerPubKey},
		{"p", obs.WorkerPubKey},
		{"sequence", strconv.FormatUint(obs.Sequence, 10)},
		{"interval_ms", strconv.FormatInt(obs.Interval.Milliseconds(), 10)},
	}
	if obs.ExpiresAfter > 0 {
		tags = append(tags, gonostr.Tag{"expires_after_ms", strconv.FormatInt(obs.ExpiresAfter.Milliseconds(), 10)})
	}
	event := continuityEventBase(KindNIP38Status, tags, "")
	if !obs.ObservedAt.IsZero() {
		event.CreatedAt = gonostr.Timestamp(obs.ObservedAt.Unix())
	}
	return event, nil
}

// DecodeHeartbeatObservationEvent deserializes a canonical NIP-38 heartbeat status.
// Legacy kind 30350 is accepted only for local historical decode/migration paths;
// runtime subscriptions and new publishes must use KindNIP38Status.
func DecodeHeartbeatObservationEvent(event *gonostr.Event) (*domain.HeartbeatObservation, error) {
	if event == nil {
		return nil, fmt.Errorf("heartbeat observation event is nil")
	}
	if event.Kind != KindNIP38Status && event.Kind != KindHeartbeatObservation {
		return nil, fmt.Errorf("unexpected heartbeat observation kind %d", event.Kind)
	}
	if event.Kind == KindNIP38Status {
		if schema := continuityTagValue(event.Tags, "schema"); schema != "" && schema != heartbeatObservationStatusSchema {
			return nil, fmt.Errorf("unexpected heartbeat observation schema %q", schema)
		}
	}
	if continuityTagValue(event.Tags, "d") == "" {
		return nil, fmt.Errorf("heartbeat observation d tag is required")
	}
	sequence, err := parseUintTag(event.Tags, "sequence")
	if err != nil {
		return nil, err
	}
	interval, err := parseDurationMillisTag(event.Tags, "interval_ms")
	if err != nil {
		return nil, err
	}
	expiresAfter := time.Duration(0)
	if continuityTagValue(event.Tags, "expires_after_ms") != "" {
		expiresAfter, err = parseDurationMillisTag(event.Tags, "expires_after_ms")
		if err != nil {
			return nil, err
		}
	}
	obs := &domain.HeartbeatObservation{
		WorkerPubKey: firstNonEmpty(continuityTagValue(event.Tags, "worker"), continuityTagValue(event.Tags, "p"), event.PubKey),
		ObservedAt:   event.CreatedAt.Time().UTC(),
		Sequence:     sequence,
		Interval:     interval,
		ExpiresAfter: expiresAfter,
	}
	if err := validateHeartbeatObservation(obs); err != nil {
		return nil, err
	}
	return obs, nil
}

// EncodeFailoverRequestEvent serializes a kind 38430 failover request.
func EncodeFailoverRequestEvent(req ContinuityCommandRequest) (gonostr.Event, error) {
	return encodeContinuityCommandEvent(KindFailoverRequest, "failover", req)
}

// DecodeFailoverRequestEvent deserializes a kind 38430 failover request.
func DecodeFailoverRequestEvent(event *gonostr.Event) (*ContinuityCommandRequest, error) {
	return decodeContinuityCommandEvent(event, KindFailoverRequest)
}

// EncodeRecoveryRequestEvent serializes a kind 38431 recovery request.
func EncodeRecoveryRequestEvent(req ContinuityCommandRequest) (gonostr.Event, error) {
	return encodeContinuityCommandEvent(KindRecoveryRequest, "recovery", req)
}

// DecodeRecoveryRequestEvent deserializes a kind 38431 recovery request.
func DecodeRecoveryRequestEvent(event *gonostr.Event) (*ContinuityCommandRequest, error) {
	return decodeContinuityCommandEvent(event, KindRecoveryRequest)
}

func encodeContinuityRecipeEvent(kind int, prefix string, recipe domain.ContinuityRecipe) (gonostr.Event, error) {
	if err := recipe.Validate(); err != nil {
		return gonostr.Event{}, err
	}
	content, err := json.Marshal(recipe)
	if err != nil {
		return gonostr.Event{}, fmt.Errorf("marshal continuity recipe: %w", err)
	}
	dTag := prefix + ":" + recipe.ServiceKey + ":" + recipe.Name
	tags := gonostr.Tags{
		{"d", dTag},
		{"service", recipe.ServiceKey},
		{"recipe", recipe.Name},
		{"recipe-kind", string(recipe.Kind)},
	}
	return continuityEventBase(kind, tags, string(content)), nil
}

func decodeContinuityRecipeEvent(event *gonostr.Event, expectedKind int, expectedRecipeKind domain.ContinuityRecipeKind) (*domain.ContinuityRecipe, error) {
	if event == nil {
		return nil, fmt.Errorf("continuity recipe event is nil")
	}
	if event.Kind != expectedKind {
		return nil, fmt.Errorf("unexpected continuity recipe kind %d", event.Kind)
	}
	if continuityTagValue(event.Tags, "d") == "" {
		return nil, fmt.Errorf("continuity recipe d tag is required")
	}
	if strings.TrimSpace(event.Content) == "" {
		return nil, fmt.Errorf("continuity recipe content is required")
	}
	var recipe domain.ContinuityRecipe
	if err := json.Unmarshal([]byte(event.Content), &recipe); err != nil {
		return nil, fmt.Errorf("decode continuity recipe content: %w", err)
	}
	if serviceTag := strings.TrimSpace(continuityTagValue(event.Tags, "service")); serviceTag != "" {
		if recipe.ServiceKey != "" && recipe.ServiceKey != serviceTag {
			return nil, fmt.Errorf("continuity recipe service tag and content differ")
		}
		recipe.ServiceKey = serviceTag
	}
	if nameTag := strings.TrimSpace(continuityTagValue(event.Tags, "recipe")); nameTag != "" {
		if recipe.Name != "" && recipe.Name != nameTag {
			return nil, fmt.Errorf("continuity recipe tag and content name differ")
		}
		recipe.Name = nameTag
	}
	if kindTag := strings.TrimSpace(continuityTagValue(event.Tags, "recipe-kind")); kindTag != "" && kindTag != string(expectedRecipeKind) {
		return nil, fmt.Errorf("continuity recipe-kind tag %q does not match expected %q", kindTag, expectedRecipeKind)
	}
	recipe.Kind = expectedRecipeKind
	recipe.UpdatedAt = event.CreatedAt.Time().UTC()
	recipe.SourceEventID = event.ID
	if err := recipe.Validate(); err != nil {
		return nil, err
	}
	return &recipe, nil
}

func encodeContinuityCommandEvent(kind int, command string, req ContinuityCommandRequest) (gonostr.Event, error) {
	if err := validateContinuityCommandRequest(&req); err != nil {
		return gonostr.Event{}, err
	}
	content, err := json.Marshal(req)
	if err != nil {
		return gonostr.Event{}, fmt.Errorf("marshal continuity %s request: %w", command, err)
	}
	tags := gonostr.Tags{
		{"d", req.IdempotencyKey},
		{"service", req.ServiceKey},
		{"target", req.TargetWorkerPubKey},
		{"p", req.TargetWorkerPubKey},
		{"command", command},
	}
	if req.TargetProfile != "" {
		tags = append(tags, gonostr.Tag{"profile", string(req.TargetProfile)})
	}
	if req.RecipeName != "" {
		tags = append(tags, gonostr.Tag{"recipe", req.RecipeName})
	}
	return continuityEventBase(kind, tags, string(content)), nil
}

func decodeContinuityCommandEvent(event *gonostr.Event, expectedKind int) (*ContinuityCommandRequest, error) {
	if event == nil {
		return nil, fmt.Errorf("continuity command event is nil")
	}
	if event.Kind != expectedKind {
		return nil, fmt.Errorf("unexpected continuity command kind %d", event.Kind)
	}
	idempotencyKey := strings.TrimSpace(continuityTagValue(event.Tags, "d"))
	if idempotencyKey == "" {
		return nil, fmt.Errorf("continuity command d tag is required")
	}
	serviceTag := strings.TrimSpace(continuityTagValue(event.Tags, "service"))
	targetTag := strings.TrimSpace(continuityTagValue(event.Tags, "target"))
	var req ContinuityCommandRequest
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &req); err != nil {
			return nil, fmt.Errorf("decode continuity command content: %w", err)
		}
	}
	if serviceTag != "" {
		if req.ServiceKey != "" && req.ServiceKey != serviceTag {
			return nil, fmt.Errorf("continuity command service tag and content differ")
		}
		req.ServiceKey = serviceTag
	}
	if targetTag != "" {
		if req.TargetWorkerPubKey != "" && req.TargetWorkerPubKey != targetTag {
			return nil, fmt.Errorf("continuity command target tag and content differ")
		}
		req.TargetWorkerPubKey = targetTag
	}
	if req.TargetProfile == "" {
		req.TargetProfile = domain.ContinuityMode(continuityTagValue(event.Tags, "profile"))
	}
	if req.RecipeName == "" {
		req.RecipeName = continuityTagValue(event.Tags, "recipe")
	}
	req.IdempotencyKey = idempotencyKey
	req.RequestedBy = event.PubKey
	req.RequestEventID = event.ID
	if err := validateContinuityCommandRequest(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func continuityEventBase(kind int, tags gonostr.Tags, content string) gonostr.Event {
	return gonostr.Event{Kind: kind, CreatedAt: gonostr.Now(), Tags: tags, Content: content}
}

func encodeBahiaSystemEvent(kind int, dTag, topic string, payload any) gonostr.Event {
	content, _ := json.Marshal(payload)
	return continuityEventBase(kind, gonostr.Tags{{"d", strings.TrimSpace(dTag)}, {"t", "bahia"}, {"t", topic}}, string(content))
}

func decodeBahiaSystemEvent(ev *gonostr.Event, expectedKind int, out any, label string) error {
	if ev == nil {
		return fmt.Errorf("%s event is nil", label)
	}
	if ev.Kind != expectedKind {
		return fmt.Errorf("unexpected %s kind %d", label, ev.Kind)
	}
	if continuityTagValue(ev.Tags, "d") == "" {
		return fmt.Errorf("%s d tag is required", label)
	}
	if strings.TrimSpace(ev.Content) == "" {
		return fmt.Errorf("%s content is required", label)
	}
	if err := json.Unmarshal([]byte(ev.Content), out); err != nil {
		return fmt.Errorf("decode %s content: %w", label, err)
	}
	return nil
}

func profileSpecForCurrent(profiles map[domain.ContinuityMode]domain.ContinuityProfileSpec, current domain.ContinuityMode, tag string) (domain.ContinuityProfileSpec, error) {
	if current == "" {
		return domain.ContinuityProfileSpec{}, fmt.Errorf("continuity profile %s tag appeared before any profile tag", tag)
	}
	return profiles[current], nil
}

func validateStandbyNodeDefinition(def *StandbyNodeDefinition) error {
	if def == nil {
		return fmt.Errorf("standby node definition is nil")
	}
	def.WorkerPubKey = strings.TrimSpace(def.WorkerPubKey)
	def.Host = strings.TrimSpace(def.Host)
	def.Role = strings.TrimSpace(def.Role)
	def.ServiceKey = strings.TrimSpace(def.ServiceKey)
	def.ArtifactRef = strings.TrimSpace(def.ArtifactRef)
	if def.WorkerPubKey == "" {
		return fmt.Errorf("standby worker pubkey is required")
	}
	if def.Host == "" {
		return fmt.Errorf("standby host is required")
	}
	if def.Role == "" {
		return fmt.Errorf("standby role is required")
	}
	if def.ServiceKey == "" {
		return fmt.Errorf("standby service key is required")
	}
	if len(def.Profiles) == 0 {
		return fmt.Errorf("standby profiles are required")
	}
	for _, profile := range def.Profiles {
		if !profile.IsValid() {
			return fmt.Errorf("standby profile %q is invalid", profile)
		}
	}
	return nil
}

func standbyDefinitionDTag(def StandbyNodeDefinition) string {
	return "standby-node:" + def.WorkerPubKey + ":" + def.ServiceKey
}

func validateReplicationPolicy(policy *domain.ReplicationPolicy) error {
	if policy == nil {
		return fmt.Errorf("replication policy is nil")
	}
	policy.ServiceKey = strings.TrimSpace(policy.ServiceKey)
	if policy.ServiceKey == "" {
		return fmt.Errorf("replication policy service key is required")
	}
	if len(policy.Targets) == 0 {
		return fmt.Errorf("replication policy targets are required")
	}
	for i := range policy.Targets {
		policy.Targets[i].WorkerPubKey = strings.TrimSpace(policy.Targets[i].WorkerPubKey)
		policy.Targets[i].Strategy = strings.TrimSpace(policy.Targets[i].Strategy)
		if policy.Targets[i].WorkerPubKey == "" {
			return fmt.Errorf("replication target %d worker pubkey is required", i)
		}
		if policy.Targets[i].Strategy == "" {
			return fmt.Errorf("replication target %d strategy is required", i)
		}
		if policy.Targets[i].MaxStaleness <= 0 {
			return fmt.Errorf("replication target %d max staleness must be positive", i)
		}
		for _, mode := range policy.Targets[i].RequiredForModes {
			if !mode.IsValid() {
				return fmt.Errorf("replication target %d mode %q is invalid", i, mode)
			}
		}
	}
	return nil
}

func validateHeartbeatObservation(obs *domain.HeartbeatObservation) error {
	if obs == nil {
		return fmt.Errorf("heartbeat observation is nil")
	}
	obs.WorkerPubKey = strings.TrimSpace(obs.WorkerPubKey)
	if obs.WorkerPubKey == "" {
		return fmt.Errorf("heartbeat worker pubkey is required")
	}
	if obs.Interval <= 0 {
		return fmt.Errorf("heartbeat interval must be positive")
	}
	if obs.ExpiresAfter < 0 {
		return fmt.Errorf("heartbeat expires_after must not be negative")
	}
	return nil
}

func validateContinuityCommandRequest(req *ContinuityCommandRequest) error {
	if req == nil {
		return fmt.Errorf("continuity command request is nil")
	}
	req.ServiceKey = strings.TrimSpace(req.ServiceKey)
	req.TargetWorkerPubKey = strings.TrimSpace(req.TargetWorkerPubKey)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.RecipeName = strings.TrimSpace(req.RecipeName)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.ServiceKey == "" {
		return fmt.Errorf("continuity command service key is required")
	}
	if req.TargetWorkerPubKey == "" {
		return fmt.Errorf("continuity command target worker pubkey is required")
	}
	if req.IdempotencyKey == "" {
		return fmt.Errorf("continuity command idempotency key is required")
	}
	if req.TargetProfile != "" && !req.TargetProfile.IsValid() {
		return fmt.Errorf("continuity command target profile %q is invalid", req.TargetProfile)
	}
	return nil
}

func parseUintTag(tags gonostr.Tags, key string) (uint64, error) {
	raw := strings.TrimSpace(continuityTagValue(tags, key))
	if raw == "" {
		return 0, fmt.Errorf("%s tag is required", key)
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s tag is invalid: %w", key, err)
	}
	return value, nil
}

func parseDurationMillisTag(tags gonostr.Tags, key string) (time.Duration, error) {
	raw := strings.TrimSpace(continuityTagValue(tags, key))
	if raw == "" {
		return 0, fmt.Errorf("%s tag is required", key)
	}
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s tag is invalid: %w", key, err)
	}
	if millis <= 0 {
		return 0, fmt.Errorf("%s tag must be positive", key)
	}
	return time.Duration(millis) * time.Millisecond, nil
}

func continuityTagValue(tags gonostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return strings.TrimSpace(tag[1])
		}
	}
	return ""
}

func continuityTagValues(tags gonostr.Tags, key string) []string {
	values := []string{}
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			if value := strings.TrimSpace(tag[1]); value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sortedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func orderedContinuityModes(profiles map[domain.ContinuityMode]domain.ContinuityProfileSpec) []domain.ContinuityMode {
	preferred := []domain.ContinuityMode{
		domain.ContinuityModeFull,
		domain.ContinuityModeDegraded,
		domain.ContinuityModeEmergency,
		domain.ContinuityModeOffline,
	}
	modes := make([]domain.ContinuityMode, 0, len(profiles))
	seen := map[domain.ContinuityMode]struct{}{}
	for _, mode := range preferred {
		if _, ok := profiles[mode]; ok {
			modes = append(modes, mode)
			seen[mode] = struct{}{}
		}
	}
	extra := make([]domain.ContinuityMode, 0)
	for mode := range profiles {
		if _, ok := seen[mode]; !ok {
			extra = append(extra, mode)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return string(extra[i]) < string(extra[j]) })
	return append(modes, extra...)
}
