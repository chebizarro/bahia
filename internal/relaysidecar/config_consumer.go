package relaysidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/atomicfile"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	configListKind         = nostr.Kind(kinds.ConfigACLList)
	configPolicyKind       = nostr.Kind(kinds.ConfigPolicy)
	configStatusKind       = nostr.Kind(kinds.CASControlState)
	configStatusSchema     = "cascadia.config.status.v2"
	configMembershipSchema = "cascadia.config.membership.v1"
	configRelaySchema      = "cascadia.config.relay-sidecar.v1"
)

// ConfigEventSigner signs independently owned events. Implementations must be
// safe for concurrent calls from Handle and the background activation loop.
type ConfigEventSigner interface {
	Sign(context.Context, *nostr.Event) error
}

// ConfigStatusPublisher publishes config status events. Handle and the background
// activation loop may call Publish concurrently; implementations must support
// concurrent use. Accepted and applied publications are not ordered by the caller.
type ConfigStatusPublisher interface {
	Publish(context.Context, nostr.Event) (int, error)
}

type ConfigProjection struct {
	ServiceID        string
	PolicyName       string
	Scope            string
	Version          int
	Schema           string
	EventID          string
	Author           string
	AllowedPubkeys   []string
	BannedPubkeys    []string
	RelayName        *string
	RelayDescription *string
	RelayIcon        *string
}

type configProjectionState struct {
	Version int                          `json:"version"`
	Desired map[string]desiredCoordinate `json:"desired"`
	Applied map[string]appliedCoordinate `json:"applied"`
	Pending []pendingActivation          `json:"pending"`
	Last    *persistedConfigProjection   `json:"last,omitempty"`
}

type desiredCoordinate struct {
	appliedCoordinate
}

type pendingActivation struct {
	Coordinate string                     `json:"coordinate"`
	Projection *persistedConfigProjection `json:"projection"`
}

type persistedConfigProjection struct {
	ServiceID        string   `json:"service_id"`
	PolicyName       string   `json:"policy_name"`
	Scope            string   `json:"scope"`
	Version          int      `json:"version"`
	Schema           string   `json:"schema"`
	EventID          string   `json:"event_id"`
	Author           string   `json:"author"`
	AllowedPubkeys   []string `json:"allowed_pubkeys,omitempty"`
	BannedPubkeys    []string `json:"banned_pubkeys,omitempty"`
	RelayName        *string  `json:"relay_name,omitempty"`
	RelayDescription *string  `json:"relay_description,omitempty"`
	RelayIcon        *string  `json:"relay_icon,omitempty"`
}

type ConfigConsumerConfig struct {
	ServiceID      string
	Scope          string
	ProjectionPath string
	TrustedAuthors []string
	Signer         ConfigEventSigner
	Publisher      ConfigStatusPublisher
	Now            func() time.Time
	Apply          func(ConfigProjection) error
}

type ConfigConsumer struct {
	mu         sync.Mutex
	serviceID  string
	scope      string
	path       string
	trusted    map[string]struct{}
	signer     ConfigEventSigner
	publisher  ConfigStatusPublisher
	now        func() time.Time
	apply      func(ConfigProjection) error
	state      configProjectionState
	activateCh chan struct{}
}

func NewConfigConsumer(cfg ConfigConsumerConfig) (*ConfigConsumer, error) {
	if strings.TrimSpace(cfg.ServiceID) == "" || strings.TrimSpace(cfg.Scope) == "" {
		return nil, fmt.Errorf("config consumer service_id and scope are required")
	}
	if strings.TrimSpace(cfg.ProjectionPath) == "" {
		return nil, fmt.Errorf("config consumer projection path is required")
	}
	if cfg.Signer == nil || cfg.Publisher == nil || cfg.Apply == nil {
		return nil, fmt.Errorf("config consumer signer, publisher, and apply function are required")
	}
	trusted, err := normalizePubkeys(cfg.TrustedAuthors)
	if err != nil || len(trusted) == 0 {
		return nil, fmt.Errorf("config consumer requires at least one valid trusted author")
	}
	consumer := &ConfigConsumer{
		serviceID:  cfg.ServiceID,
		scope:      cfg.Scope,
		path:       cfg.ProjectionPath,
		trusted:    make(map[string]struct{}, len(trusted)),
		signer:     cfg.Signer,
		publisher:  cfg.Publisher,
		now:        cfg.Now,
		apply:      cfg.Apply,
		state:      configProjectionState{Version: 2, Desired: map[string]desiredCoordinate{}, Applied: map[string]appliedCoordinate{}, Pending: nil},
		activateCh: make(chan struct{}, 1),
	}
	if consumer.now == nil {
		consumer.now = time.Now
	}
	for _, author := range trusted {
		consumer.trusted[author] = struct{}{}
	}
	data, err := os.ReadFile(consumer.path)
	if err == nil {
		if err := json.Unmarshal(data, &consumer.state); err != nil {
			return nil, fmt.Errorf("parse config-fabric projection %s: %w", consumer.path, err)
		}
		if consumer.state.Version == 1 {
			var legacy struct {
				Version  int                          `json:"version"`
				Accepted map[string]appliedCoordinate `json:"accepted"`
				Last     *persistedConfigProjection   `json:"last,omitempty"`
			}
			if err := json.Unmarshal(data, &legacy); err != nil {
				return nil, fmt.Errorf("migrate config-fabric projection %s: %w", consumer.path, err)
			}
			// v1 wrote "accepted" BEFORE activation ran, so an accepted version
			// is not evidence that it was ever applied - that is the defect this
			// schema change exists to fix. Migrate it to Desired only and leave
			// Applied empty so startup replays it. Apply is idempotent (it
			// assigns relay allowlists and metadata from the projection), so the
			// cost of replaying an already-live config is one redundant apply;
			// the cost of assuming it applied is silently running stale config.
			consumer.state.Version = 2
			consumer.state.Desired = make(map[string]desiredCoordinate, len(legacy.Accepted))
			consumer.state.Applied = map[string]appliedCoordinate{}
			consumer.state.Pending = nil
			for coord, entry := range legacy.Accepted {
				consumer.state.Desired[coord] = desiredCoordinate{appliedCoordinate: entry}
			}
			if legacy.Last != nil {
				consumer.state.Pending = []pendingActivation{{
					Coordinate: legacy.Last.Author + "\x00" + legacy.Last.ServiceID + "\x00" + legacy.Last.Scope + "\x00" + legacy.Last.PolicyName,
					Projection: legacy.Last,
				}}
			}
			consumer.state.Last = nil
		}
		if consumer.state.Version != 2 {
			return nil, fmt.Errorf("unsupported config-fabric projection version %d", consumer.state.Version)
		}
		if consumer.state.Desired == nil {
			consumer.state.Desired = map[string]desiredCoordinate{}
		}
		if consumer.state.Pending == nil {
			consumer.state.Pending = nil
		}
		consumer.state.Last = nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config-fabric projection %s: %w", consumer.path, err)
	}
	return consumer, nil
}

func (c *ConfigConsumer) Handle(ctx context.Context, event nostr.Event) error {
	if event.Kind != configListKind && event.Kind != configPolicyKind {
		return nil
	}
	projection, err := c.validate(event)
	if err != nil {
		return c.publishStatus(ctx, projection, event.ID.Hex(), "rejected", err.Error())
	}
	c.mu.Lock()
	coordinate := projection.Author + "\x00" + projection.ServiceID + "\x00" + projection.Scope + "\x00" + projection.PolicyName
	desired := c.state.Desired[coordinate]
	if projection.Version <= desired.Version {
		c.mu.Unlock()
		return c.publishStatus(ctx, projection, event.ID.Hex(), "rejected", fmt.Sprintf("version %d does not advance desired version %d", projection.Version, desired.Version))
	}
	next := c.state
	next.Desired = cloneDesired(c.state.Desired)
	next.Desired[coordinate] = desiredCoordinate{
		appliedCoordinate: appliedCoordinate{Author: projection.Author, EventID: projection.EventID, Version: projection.Version},
	}
	next.Pending = append(append([]pendingActivation(nil), c.state.Pending...), pendingActivation{
		Coordinate: coordinate,
		Projection: persistedProjection(projection),
	})
	if err := c.persist(ctx, next); err != nil {
		c.mu.Unlock()
		return c.publishStatus(ctx, projection, event.ID.Hex(), "rejected", "persist desired projection: "+err.Error())
	}
	c.state = next
	c.mu.Unlock()
	select {
	case c.activateCh <- struct{}{}:
	default:
	}
	return c.publishStatus(ctx, projection, event.ID.Hex(), "accepted", "")
}

func cloneDesired(in map[string]desiredCoordinate) map[string]desiredCoordinate {
	out := make(map[string]desiredCoordinate, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (c *ConfigConsumer) Start(ctx context.Context) {
	go c.activateLoop(ctx)
}

func (c *ConfigConsumer) activateLoop(ctx context.Context) {
	select {
	case <-c.activateCh:
	case <-ctx.Done():
		return
	}
	c.processPending(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.activateCh:
			c.processPending(ctx)
		}
	}
}

func (c *ConfigConsumer) processPending(ctx context.Context) {
	for {
		c.mu.Lock()
		if len(c.state.Pending) == 0 {
			c.mu.Unlock()
			return
		}
		next := c.state.Pending[0]
		c.mu.Unlock()

		projection := ConfigProjection{
			ServiceID:        next.Projection.ServiceID,
			PolicyName:       next.Projection.PolicyName,
			Scope:            next.Projection.Scope,
			Version:          next.Projection.Version,
			Schema:           next.Projection.Schema,
			EventID:          next.Projection.EventID,
			Author:           next.Projection.Author,
			AllowedPubkeys:   append([]string(nil), next.Projection.AllowedPubkeys...),
			BannedPubkeys:    append([]string(nil), next.Projection.BannedPubkeys...),
			RelayName:        next.Projection.RelayName,
			RelayDescription: next.Projection.RelayDescription,
			RelayIcon:        next.Projection.RelayIcon,
		}
		if err := c.apply(projection); err != nil {
			return
		}
		c.mu.Lock()
		c.state.Pending = c.state.Pending[1:]
		if nd := c.state.Desired[next.Coordinate]; nd.Version < next.Projection.Version {
			nd = desiredCoordinate{appliedCoordinate: appliedCoordinate{
				Author: next.Projection.Author, EventID: next.Projection.EventID, Version: next.Projection.Version,
			}}
			c.state.Desired = cloneDesired(c.state.Desired)
			c.state.Desired[next.Coordinate] = nd
		}
		c.state.Applied = cloneCoordinates(c.state.Applied)
		applied := c.state.Applied[next.Coordinate]
		if next.Projection.Version > applied.Version {
			c.state.Applied[next.Coordinate] = appliedCoordinate{
				Author: next.Projection.Author, EventID: next.Projection.EventID, Version: next.Projection.Version,
			}
		}
		persistErr := c.persist(ctx, c.state)
		c.mu.Unlock()
		if persistErr != nil {
			// Activation succeeded; the applied coordinate did not reach disk.
			// Surface it - a silent failure here is what lets a restart replay
			// a version that is already live.
			c.logActivation("persist applied coordinate", projection, persistErr)
		}
		// The config-fabric console keys drift on an "applied" status event
		// (internal/service/config_fabric.go treats status == "applied" as the
		// effective version). Without this every activated config reads as
		// permanently drifted.
		if err := c.publishStatus(ctx, projection, next.Projection.EventID, "applied", ""); err != nil {
			c.logActivation("publish applied status", projection, err)
		}
	}
}

// logActivation reports an activation-path failure. The consumer has no logger
// dependency, so this stays a single choke point to change if one is added.
func (c *ConfigConsumer) logActivation(stage string, projection ConfigProjection, err error) {
	zap.L().Warn("relay sidecar config activation",
		zap.String("stage", stage),
		zap.String("service_id", projection.ServiceID),
		zap.String("policy", projection.PolicyName),
		zap.Int("version", projection.Version),
		zap.Error(err))
}

func persistedProjection(projection ConfigProjection) *persistedConfigProjection {
	return &persistedConfigProjection{
		ServiceID: projection.ServiceID, PolicyName: projection.PolicyName, Scope: projection.Scope,
		Version: projection.Version, Schema: projection.Schema, EventID: projection.EventID, Author: projection.Author,
		AllowedPubkeys: append([]string(nil), projection.AllowedPubkeys...),
		BannedPubkeys:  append([]string(nil), projection.BannedPubkeys...),
		RelayName:      projection.RelayName, RelayDescription: projection.RelayDescription, RelayIcon: projection.RelayIcon,
	}
}

func (c *ConfigConsumer) validate(event nostr.Event) (ConfigProjection, error) {
	projection := ConfigProjection{EventID: event.ID.Hex(), Author: event.PubKey.Hex()}
	if !event.CheckID() || !event.VerifySignature() {
		return projection, fmt.Errorf("desired event id or signature is invalid")
	}
	if _, ok := c.trusted[event.PubKey.Hex()]; !ok {
		return projection, fmt.Errorf("desired event author is not trusted")
	}
	serviceID, err := exactlyOneEventTag(event.Tags, "service")
	if err != nil {
		return projection, err
	}
	scope, err := exactlyOneEventTag(event.Tags, "scope")
	if err != nil {
		return projection, err
	}
	versionTag, err := exactlyOneEventTag(event.Tags, "version")
	if err != nil {
		return projection, err
	}
	version, err := strconv.Atoi(versionTag)
	if err != nil || version < 1 {
		return projection, fmt.Errorf("desired event version must be a positive integer")
	}
	schema, err := exactlyOneEventTag(event.Tags, "schema")
	if err != nil {
		return projection, err
	}
	dTag, err := exactlyOneEventTag(event.Tags, "d")
	if err != nil {
		return projection, err
	}
	prefix := "service:" + serviceID + ":"
	if !strings.HasPrefix(dTag, prefix) {
		return projection, fmt.Errorf("desired event d tag is invalid")
	}
	projection.ServiceID = serviceID
	projection.Scope = scope
	projection.Version = version
	projection.Schema = schema
	projection.PolicyName = strings.TrimPrefix(dTag, prefix)
	if serviceID != c.serviceID || scope != c.scope {
		return projection, fmt.Errorf("desired event target does not match this sidecar")
	}
	switch event.Kind {
	case configListKind:
		if projection.PolicyName != "membership" || schema != configMembershipSchema {
			return projection, fmt.Errorf("unsupported NIP-51 config coordinate")
		}
	case configPolicyKind:
		if projection.PolicyName != "relay-sidecar" || schema != configRelaySchema {
			return projection, fmt.Errorf("unsupported NIP-78 config coordinate")
		}
	}
	var envelope struct {
		ServiceID  string         `json:"service_id"`
		Scope      string         `json:"scope"`
		Version    int            `json:"version"`
		Schema     string         `json:"schema"`
		Policy     map[string]any `json:"policy"`
		SecretRefs map[string]any `json:"secret_refs"`
	}
	if err := json.Unmarshal([]byte(event.Content), &envelope); err != nil {
		return projection, fmt.Errorf("parse desired event content: %w", err)
	}
	if envelope.ServiceID != serviceID || envelope.Scope != scope || envelope.Version != version || envelope.Schema != schema {
		return projection, fmt.Errorf("desired event tag/content mismatch")
	}
	if len(envelope.SecretRefs) > 0 {
		return projection, fmt.Errorf("relay-sidecar policy does not accept secret references")
	}
	if event.Kind == configListKind {
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "p" {
				projection.AllowedPubkeys = append(projection.AllowedPubkeys, tag[1])
			}
		}
		normalized, err := normalizePubkeys(projection.AllowedPubkeys)
		if err != nil {
			return projection, err
		}
		projection.AllowedPubkeys = normalized
		return projection, nil
	}
	if envelope.Policy == nil {
		return projection, fmt.Errorf("NIP-78 relay-sidecar policy object is required")
	}
	allowed, err := stringSlicePolicy(envelope.Policy, "allowed_pubkeys")
	if err != nil {
		return projection, err
	}
	banned, err := stringSlicePolicy(envelope.Policy, "banned_pubkeys")
	if err != nil {
		return projection, err
	}
	if projection.AllowedPubkeys, err = normalizePubkeys(allowed); err != nil {
		return projection, err
	}
	if projection.BannedPubkeys, err = normalizePubkeys(banned); err != nil {
		return projection, err
	}
	for _, pubkey := range projection.AllowedPubkeys {
		for _, bannedPubkey := range projection.BannedPubkeys {
			if pubkey == bannedPubkey {
				return projection, fmt.Errorf("pubkey cannot be both allowed and banned")
			}
		}
	}
	if value, ok := envelope.Policy["name"]; ok {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return projection, fmt.Errorf("relay metadata name must be a non-empty string")
		}
		name = strings.TrimSpace(name)
		projection.RelayName = &name
	}
	if value, ok := envelope.Policy["description"]; ok {
		description, ok := value.(string)
		if !ok {
			return projection, fmt.Errorf("relay metadata description must be a string")
		}
		projection.RelayDescription = &description
	}
	if value, ok := envelope.Policy["icon"]; ok {
		icon, ok := value.(string)
		if !ok {
			return projection, fmt.Errorf("relay metadata icon must be a string")
		}
		projection.RelayIcon = &icon
	}
	return projection, nil
}

func stringSlicePolicy(policy map[string]any, key string) ([]string, error) {
	value, exists := policy[key]
	if !exists {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of pubkeys", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must contain only pubkey strings", key)
		}
		out = append(out, value)
	}
	return out, nil
}

func (c *ConfigConsumer) persist(ctx context.Context, state configProjectionState) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicfile.WriteFile(ctx, c.path, ".config-fabric-*.tmp", data, 0o600); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(c.path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (c *ConfigConsumer) publishStatus(ctx context.Context, projection ConfigProjection, desiredEventID string, status, reason string) error {
	if projection.ServiceID == "" || projection.Scope == "" || projection.PolicyName == "" || projection.Version < 1 {
		return fmt.Errorf("reject desired config: %s", reason)
	}
	content := map[string]any{
		"service_id":      projection.ServiceID,
		"scope":           projection.Scope,
		"version":         projection.Version,
		"policy_schema":   projection.Schema,
		"config_event_id": desiredEventID,
		"status":          status,
	}
	if status == "applied" {
		content["effective_version"] = projection.Version
		content["last_applied_event_id"] = desiredEventID
	} else {
		content["reason"] = strings.TrimSpace(reason)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	// Each phase is a durable fact about one signed desired event. Sharing an
	// address across phases or target versions lets NIP-01 replacement erase
	// applied truth, regardless of publication order or timestamp precision.
	event := nostr.Event{
		Kind:      configStatusKind,
		CreatedAt: nostr.Timestamp(c.now().Unix()),
		Tags: nostr.Tags{
			{"d", "config-status:" + projection.ServiceID + ":" + projection.PolicyName + ":" + projection.Scope + ":" + desiredEventID + ":" + status},
			{"domain", "config-status"},
			{"schema", configStatusSchema},
			{"status", status},
			{"service", projection.ServiceID},
			{"scope", projection.Scope},
			{"version", strconv.Itoa(projection.Version)},
			{"e", desiredEventID},
		},
		Content: string(raw),
	}
	if err := c.signer.Sign(ctx, &event); err != nil {
		return fmt.Errorf("sign config status: %w", err)
	}
	accepted, err := c.publisher.Publish(ctx, event)
	if err != nil {
		return fmt.Errorf("publish config status: %w", err)
	}
	if accepted == 0 {
		return fmt.Errorf("publish config status: no relay accepted event")
	}
	if status == "rejected" {
		return fmt.Errorf("reject desired config: %s", reason)
	}
	return nil
}
