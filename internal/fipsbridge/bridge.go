package fipsbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	DefaultHostsPath            = "/etc/fips/hosts"
	DefaultManagedSectionMarker = "# bahia-managed"
)

// Bahia publishes DNS endpoint state (live and tombstone) only through the
// projector's canonical control-state envelope: kind 30900 with domain=dns,
// schema=bahia.cp-state.v1, legacy_kind=31976, deleted=true|false and
// t=dns-endpoint. Legacy kind 31976 is no longer published, and its
// tombstones now land on 30900, so a 31976 subscription would only replay
// stale endpoints that can never be removed.
var endpointLegacyKind = kinds.CPStateFamilyDNSEndpoint.TagValue()

// Config controls the standalone Bahia endpoint to FIPS hosts bridge.
type Config struct {
	BahiaPubkey          string   `yaml:"bahia_pubkey"`
	RelayURLs            []string `yaml:"relay_urls"`
	HostsPath            string   `yaml:"hosts_path"`
	ManagedSectionMarker string   `yaml:"managed_section_marker"`
	HealthFilter         bool     `yaml:"health_filter"`
	CapabilityFilter     []string `yaml:"capability_filter"`
	EnvironmentFilter    []string `yaml:"environment_filter"`
}

type configFile struct {
	Bridge rawConfig `yaml:"bridge"`
}

type rawConfig struct {
	BahiaPubkey          string   `yaml:"bahia_pubkey"`
	RelayURLs            []string `yaml:"relay_urls"`
	HostsPath            string   `yaml:"hosts_path"`
	ManagedSectionMarker string   `yaml:"managed_section_marker"`
	HealthFilter         *bool    `yaml:"health_filter"`
	CapabilityFilter     []string `yaml:"capability_filter"`
	EnvironmentFilter    []string `yaml:"environment_filter"`
}

// DefaultConfig returns the Phase A.2 defaults from the integration design.
func DefaultConfig() Config {
	return Config{
		HostsPath:            DefaultHostsPath,
		ManagedSectionMarker: DefaultManagedSectionMarker,
		HealthFilter:         true,
	}
}

// LoadConfig parses a bridge YAML file and applies defaults.
func LoadConfig(data []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(data))) == 0 {
		return cfg, nil
	}
	var wrapped configFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wrapped); err != nil {
		return Config{}, fmt.Errorf("parse bridge config: %w", err)
	}
	loaded := wrapped.Bridge
	if loaded.BahiaPubkey != "" {
		cfg.BahiaPubkey = loaded.BahiaPubkey
	}
	if loaded.RelayURLs != nil {
		cfg.RelayURLs = loaded.RelayURLs
	}
	if loaded.HostsPath != "" {
		cfg.HostsPath = loaded.HostsPath
	}
	if loaded.ManagedSectionMarker != "" {
		cfg.ManagedSectionMarker = loaded.ManagedSectionMarker
	}
	if loaded.HealthFilter != nil {
		cfg.HealthFilter = *loaded.HealthFilter
	}
	if loaded.CapabilityFilter != nil {
		cfg.CapabilityFilter = loaded.CapabilityFilter
	}
	if loaded.EnvironmentFilter != nil {
		cfg.EnvironmentFilter = loaded.EnvironmentFilter
	}
	cfg.normalize()
	return cfg, nil
}

func (c *Config) normalize() {
	c.BahiaPubkey = normalizePubkeyString(c.BahiaPubkey)
	c.RelayURLs = compactStrings(c.RelayURLs)
	if strings.TrimSpace(c.HostsPath) == "" {
		c.HostsPath = DefaultHostsPath
	}
	if strings.TrimSpace(c.ManagedSectionMarker) == "" {
		c.ManagedSectionMarker = DefaultManagedSectionMarker
	}
	c.CapabilityFilter = compactStrings(c.CapabilityFilter)
	c.EnvironmentFilter = compactStrings(c.EnvironmentFilter)
}

func (c Config) validate() error {
	if strings.TrimSpace(c.BahiaPubkey) == "" {
		return fmt.Errorf("bahia_pubkey is required")
	}
	if _, err := nostrutil.PubKeyFromHex(c.BahiaPubkey); err != nil {
		return fmt.Errorf("bahia_pubkey must be a valid 32-byte hex pubkey: %w", err)
	}
	if len(c.RelayURLs) == 0 {
		return fmt.Errorf("at least one relay URL is required")
	}
	return nil
}

// Bridge subscribes to Bahia endpoint events and rewrites the managed FIPS hosts section.
type Bridge struct {
	cfg    Config
	pool   relayPool
	writer hostsWriter
	logger *slog.Logger
	now    func() time.Time
	// latest is the newest accepted event per (kind, pubkey, d) coordinate.
	latest map[string]replaceableCursor
	// routes holds the hosts entry each coordinate currently contributes.
	// Keying by coordinate lets a tombstone (which carries no service tag)
	// remove exactly what its live record added.
	routes map[string]hostRoute
	// entries is the managed hosts section derived from routes.
	entries map[string]string
	// caughtUp is set once the relays report EOSE; until then backfill only
	// updates state and pendingFlush records that a write is owed.
	caughtUp     bool
	pendingFlush bool
}

type hostsWriter interface {
	Write(ctx context.Context, entries map[string]string) error
}

type replaceableCursor struct {
	CreatedAt nostr.Timestamp
	EventID   string
}

type hostRoute struct {
	Label  string
	Npub   string
	Cursor replaceableCursor
}

type relayPool interface {
	Connect(context.Context)
	Close()
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error)
	AuthenticateRelay(context.Context, string) error
}

// NewBridge constructs a bridge using Bahia's Nostr relay pool implementation.
func NewBridge(cfg Config, logger *slog.Logger) (*Bridge, error) {
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	pool := nostradapter.NewRelayPool(cfg.RelayURLs, zap.NewNop())
	return newBridgeWithPool(cfg, pool, logger), nil
}

func newBridgeWithPool(cfg Config, pool relayPool, logger *slog.Logger) *Bridge {
	cfg.normalize()
	if logger == nil {
		logger = slog.Default()
	}
	return &Bridge{
		cfg:     cfg,
		pool:    pool,
		writer:  NewHostsWriter(cfg.HostsPath, cfg.ManagedSectionMarker),
		logger:  logger.With("component", "fips-bahia-bridge"),
		now:     func() time.Time { return time.Now().UTC() },
		latest:  make(map[string]replaceableCursor),
		routes:  make(map[string]hostRoute),
		entries: make(map[string]string),
	}
}

// Run keeps the Nostr subscription open until the context is canceled.
func (b *Bridge) Run(ctx context.Context) error {
	if b.pool == nil {
		return fmt.Errorf("relay pool is not configured")
	}
	b.pool.Connect(ctx)
	b.fetchRelayMetadata(ctx)
	defer b.pool.Close()

	backoff := time.Second
	for {
		err := b.subscribeOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		b.logger.Warn("subscription ended; reconnecting", "error", err, "delay", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (b *Bridge) fetchRelayMetadata(ctx context.Context) {
	fetcher, ok := b.pool.(interface {
		FetchAllRelayInfo(context.Context) map[string]*nip11.RelayInformationDocument
	})
	if !ok {
		return
	}
	infos := fetcher.FetchAllRelayInfo(ctx)
	for relayURL, info := range infos {
		if info == nil {
			b.logger.Warn("relay NIP-11 metadata unavailable", "relay", relayURL)
			continue
		}
		b.logger.Info("relay NIP-11 metadata loaded", "relay", relayURL, "name", info.Name, "supported_nips", info.SupportedNIPs)
	}
}

func (b *Bridge) subscribeOnce(ctx context.Context) error {
	filters := []nostr.Filter{b.subscriptionFilter()}
	authAttempted := make(map[string]struct{})
	for {
		merged, err := b.pool.SubscribeAllWithEOSE(ctx, filters)
		if err != nil {
			return err
		}
		retry, err := b.consume(ctx, merged, authAttempted)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
	}
}

func (b *Bridge) subscriptionFilter() nostr.Filter {
	authors := []nostr.PubKey(nil)
	if pubkey, err := nostrutil.PubKeyFromHex(b.cfg.BahiaPubkey); err == nil {
		authors = []nostr.PubKey{pubkey}
	}
	// #t is a single-letter tag, so NIP-01 relays index it; the envelope's
	// domain/schema/legacy_kind tags are multi-letter and are checked locally.
	return nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: authors,
		Tags:    nostr.TagMap{"t": []string{kinds.DNSEndpointTopic}},
	}
}

func (b *Bridge) consume(ctx context.Context, merged *nostradapter.MergedSubscription, authAttempted map[string]struct{}) (bool, error) {
	if merged == nil {
		return false, nil
	}
	defer merged.Close()
	for merged.Events != nil || merged.EndOfStoredEvents != nil || merged.RelayEOSE != nil || merged.Closed != nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				b.logger.Info("relay sent EOSE", "relay", eose.RelayURL, "subscription_id", eose.SubscriptionID)
			} else {
				merged.RelayEOSE = nil
			}
		case <-merged.EndOfStoredEvents:
			b.logger.Info("all relays sent EOSE; historical endpoint catch-up complete")
			merged.EndOfStoredEvents = nil
			if err := b.markCaughtUp(ctx); err != nil {
				b.logger.Warn("hosts write after catch-up failed", "error", err)
			}
		case closed, ok := <-merged.Closed:
			if ok {
				if b.handleClosed(ctx, closed, authAttempted) {
					return true, nil
				}
			} else {
				merged.Closed = nil
			}
		case ev, ok := <-merged.Events:
			if !ok {
				return false, nil
			}
			if err := b.HandleEvent(ctx, ev); err != nil {
				b.logger.Warn("endpoint event ignored", "event_id", eventID(ev), "error", err)
			}
		}
	}
	return false, nil
}

func (b *Bridge) handleClosed(ctx context.Context, closed nostradapter.RelayClosed, authAttempted map[string]struct{}) bool {
	b.logger.Warn("relay closed subscription", "relay", closed.RelayURL, "subscription_id", closed.SubscriptionID, "reason", closed.Reason)
	if !nostradapter.IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || b.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := b.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		if recorder, ok := b.pool.(interface{ RecordRelayError(string, string) }); ok {
			recorder.RecordRelayError(closed.RelayURL, "auth-unavailable: "+closed.Reason+": "+err.Error())
		}
		b.logger.Warn("relay authentication failed", "relay", closed.RelayURL, "error", err)
		return false
	}
	return true
}

// markCaughtUp ends the backfill phase and writes the hosts section once if
// backfill changed anything, instead of rewriting it per stored event.
func (b *Bridge) markCaughtUp(ctx context.Context) error {
	b.caughtUp = true
	if !b.pendingFlush {
		return nil
	}
	b.pendingFlush = false
	return b.writer.Write(ctx, b.entries)
}

// HandleEvent validates and applies a single Bahia endpoint event.
func (b *Bridge) HandleEvent(ctx context.Context, ev *nostr.Event) error {
	if err := nostradapter.ValidateInboundEvent(ev, b.now(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return err
	}
	if err := validateEndpointEnvelope(ev); err != nil {
		return err
	}
	pubkey := nostrutil.EventPubKeyHex(ev)
	if pubkey != b.cfg.BahiaPubkey {
		return fmt.Errorf("unexpected author %s", pubkey)
	}
	eventID := nostrutil.EventIDHex(ev)
	coordinate := replaceableCoordinate(ev)
	if cursor, ok := b.latest[coordinate]; ok {
		if ev.CreatedAt < cursor.CreatedAt {
			return nil
		}
		if ev.CreatedAt == cursor.CreatedAt && eventID >= cursor.EventID {
			return nil
		}
	}

	endpoint, err := ParseEndpointEvent(ev)
	if err != nil {
		return err
	}

	cursor := replaceableCursor{CreatedAt: ev.CreatedAt, EventID: eventID}
	b.latest[coordinate] = cursor
	if b.routable(endpoint) {
		b.routes[coordinate] = hostRoute{Label: endpoint.ServiceLabel, Npub: endpoint.Npub, Cursor: cursor}
	} else {
		delete(b.routes, coordinate)
	}
	changed := b.rebuildEntries()

	if !b.caughtUp {
		b.pendingFlush = true
		return nil
	}
	if changed {
		return b.writer.Write(ctx, b.entries)
	}
	return nil
}

// validateEndpointEnvelope accepts only the projector's canonical DNS
// endpoint envelope; relays that ignore #t may hand back other 30900 state.
func validateEndpointEnvelope(ev *nostr.Event) error {
	if int(ev.Kind) != kinds.CASControlState {
		return fmt.Errorf("unexpected kind %d", ev.Kind)
	}
	if domain := tagValue(ev, kinds.CASControlStateTagDomain); domain != kinds.DNSDomain {
		return fmt.Errorf("unexpected domain %q", domain)
	}
	if schema := tagValue(ev, kinds.CASControlStateTagSchema); schema != kinds.CASControlStateSchema {
		return fmt.Errorf("unexpected schema %q", schema)
	}
	if legacyKind := tagValue(ev, kinds.CASControlStateTagLegacyKind); legacyKind != endpointLegacyKind {
		return fmt.Errorf("not a DNS endpoint record (legacy_kind %q)", legacyKind)
	}
	if tagValue(ev, kinds.CASControlStateTagD) == "" {
		return fmt.Errorf("missing d tag")
	}
	return nil
}

func (b *Bridge) routable(endpoint Endpoint) bool {
	return !endpoint.ShouldRemove(b.cfg.HealthFilter) && endpoint.Npub != "" && b.endpointAllowed(endpoint)
}

// rebuildEntries derives the hosts section from the per-coordinate routes.
// When two coordinates map to the same label, the newest event wins (lowest
// event id on a created_at tie) so the result is independent of arrival order.
func (b *Bridge) rebuildEntries() bool {
	winners := make(map[string]hostRoute, len(b.routes))
	for _, route := range b.routes {
		current, ok := winners[route.Label]
		if !ok || route.Cursor.CreatedAt > current.Cursor.CreatedAt ||
			(route.Cursor.CreatedAt == current.Cursor.CreatedAt && route.Cursor.EventID < current.Cursor.EventID) {
			winners[route.Label] = route
		}
	}
	entries := make(map[string]string, len(winners))
	for label, route := range winners {
		entries[label] = route.Npub
	}
	if maps.Equal(entries, b.entries) {
		return false
	}
	b.entries = entries
	return true
}

func (b *Bridge) endpointAllowed(endpoint Endpoint) bool {
	if endpoint.ServiceLabel == "" {
		return false
	}
	if len(b.cfg.EnvironmentFilter) > 0 && !slices.Contains(b.cfg.EnvironmentFilter, endpoint.Environment) {
		return false
	}
	if len(b.cfg.CapabilityFilter) > 0 {
		for _, capability := range endpoint.Capabilities {
			if slices.Contains(b.cfg.CapabilityFilter, capability) {
				return true
			}
		}
		return false
	}
	return true
}

// Endpoint is the subset of a Bahia DNSEndpointState needed by FIPS hosts.
type Endpoint struct {
	FQDN         string
	Service      string
	Route        string
	Environment  string
	Health       string
	Npub         string
	Capabilities []string
	ServiceLabel string
	Tombstone    bool
}

func (e Endpoint) ShouldRemove(healthFilter bool) bool {
	if e.Tombstone {
		return true
	}
	if !healthFilter {
		return false
	}
	return e.Health != "healthy"
}

// ParseEndpointEvent extracts FQDN, health, npub, filters, and service label
// from a DNS endpoint record (the projector's domain.DNSEndpoint content and
// dnsEndpointTags, or its tombstone).
func ParseEndpointEvent(ev *nostr.Event) (Endpoint, error) {
	if ev == nil {
		return Endpoint{}, fmt.Errorf("nil event")
	}
	var content struct {
		FQDN         string   `json:"fqdn"`
		DNS          string   `json:"dns"`
		Service      string   `json:"service"`
		Route        string   `json:"route"`
		Environment  string   `json:"environment"`
		Health       string   `json:"health"`
		Npub         string   `json:"npub"`
		WorkerPubkey string   `json:"worker_pubkey"`
		Capabilities []string `json:"capabilities"`
		Deleted      bool     `json:"deleted"`
	}
	if strings.TrimSpace(ev.Content) != "" {
		if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
			return Endpoint{}, fmt.Errorf("parse endpoint content: %w", err)
		}
	}

	endpoint := Endpoint{
		FQDN:         firstNonEmpty(tagValue(ev, "dns"), content.FQDN, content.DNS),
		Service:      firstNonEmpty(tagValue(ev, "service"), content.Service),
		Route:        firstNonEmpty(tagValue(ev, "route"), content.Route),
		Environment:  firstNonEmpty(tagValue(ev, "environment"), content.Environment),
		Health:       strings.ToLower(firstNonEmpty(tagValue(ev, "health"), content.Health)),
		Npub:         firstNonEmpty(tagValue(ev, "npub"), content.Npub, content.WorkerPubkey),
		Capabilities: append([]string{}, content.Capabilities...),
		// Live records carry deleted=false, so compare the value.
		Tombstone: content.Deleted || tagValue(ev, kinds.CASControlStateTagDeleted) == "true",
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "capability" {
			endpoint.Capabilities = append(endpoint.Capabilities, strings.TrimSpace(tag[1]))
		}
	}
	endpoint.Capabilities = compactStrings(endpoint.Capabilities)
	endpoint.ServiceLabel = serviceLabel(endpoint)
	if endpoint.Npub != "" {
		npub, err := normalizeNpub(endpoint.Npub)
		if err != nil {
			return Endpoint{}, err
		}
		endpoint.Npub = npub
	}
	if endpoint.Health == "" {
		endpoint.Health = "unknown"
	}
	return endpoint, nil
}

func normalizePubkeyString(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "npub1") {
		pubkey, err := nostrutil.DecodeNpubToHex(value)
		if err == nil {
			return pubkey
		}
	}
	return value
}

func normalizeNpub(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "npub1") {
		if _, err := nostrutil.DecodeNpubToHex(value); err != nil {
			return "", fmt.Errorf("invalid npub: %w", err)
		}
		return value, nil
	}
	if len(value) == 64 {
		npub, err := nostrutil.EncodeNpubFromHex(value)
		if err != nil {
			return "", fmt.Errorf("encode worker pubkey as npub: %w", err)
		}
		return npub, nil
	}
	return "", fmt.Errorf("worker identity must be npub or 32-byte hex pubkey")
}

func serviceLabel(endpoint Endpoint) string {
	service := sanitizeLabel(endpoint.Service)
	route := sanitizeLabel(endpoint.Route)
	if service != "" && route != "" {
		return service + "-" + route
	}
	if service != "" {
		return service
	}
	return ServiceLabelFromFQDN(endpoint.FQDN, endpoint.Environment)
}

// ServiceLabelFromFQDN removes the zone suffix from a Bahia endpoint FQDN.
func ServiceLabelFromFQDN(fqdn, environment string) string {
	fqdn = strings.Trim(strings.TrimSpace(fqdn), ".")
	if fqdn == "" {
		return ""
	}
	parts := strings.Split(fqdn, ".")
	environment = strings.TrimSpace(environment)
	if environment != "" {
		for i, part := range parts {
			if part == environment && i > 0 {
				return sanitizeLabel(strings.Join(parts[:i], "-"))
			}
		}
	}
	return sanitizeLabel(parts[0])
}

func sanitizeLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(value, ".fips")
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func replaceableCoordinate(ev *nostr.Event) string {
	return fmt.Sprintf("%d:%s:%s", ev.Kind, nostrutil.EventPubKeyHex(ev), tagValue(ev, "d"))
}

func tagValue(ev *nostr.Event, key string) string {
	if ev == nil {
		return ""
	}
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return strings.TrimSpace(tag[1])
		}
	}
	return ""
}

func eventID(ev *nostr.Event) string {
	if ev == nil {
		return ""
	}
	return nostrutil.EventIDHex(ev)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}
