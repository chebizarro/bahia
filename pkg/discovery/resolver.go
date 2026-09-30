package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
)

// Bahia publishes DNS endpoint state (live and tombstone) only through the
// projector's canonical control-state envelope: kind 30900 with domain=dns,
// schema=bahia.cp-state.v1, legacy_kind=31976, deleted=true|false and
// t=dns-endpoint. Legacy kind 31976 is no longer published and its tombstones
// land on 30900, so the resolver reads 30900 only.
var endpointLegacyKind = kinds.CPStateFamilyDNSEndpoint.TagValue()

const (
	resolverReconnectInitialBackoff = time.Second
	resolverReconnectMaxBackoff     = 30 * time.Second

	// resolverSinceOverlap is subtracted from the newest created_at seen after
	// EOSE when resubscribing. Relays apply since to live events as well, so
	// the overlap has to cover producer clock skew and delayed (outbox retry)
	// publishes; it matches the inbound future-skew tolerance.
	resolverSinceOverlap = nostradapter.InboundEventMaxFutureSkew
)

// errNotDNSEndpoint marks 30900 state that is not a DNS endpoint record. Relays
// that ignore #t can return it; it is skipped, not treated as invalid.
var errNotDNSEndpoint = errors.New("not a DNS endpoint record")

// Endpoint represents a resolved DNS endpoint from Bahia's canonical kind 30900
// DNS endpoint state. Port is 0 and Protocol is empty when the producer did not
// project them (for example service endpoints resolved from observed hosts).
type Endpoint struct {
	FQDN         string
	Name         string
	Environment  string
	ZoneName     string
	Address      string
	Port         int
	Protocol     string
	Health       string
	Capabilities []string
	Runtime      string
	Hardware     string
	UpdatedAt    time.Time
}

// RelayAdvisoryMetadata records best-effort relay self-reported metadata without
// changing the configured relay set or Bahia service trust boundary.
type RelayAdvisoryMetadata struct {
	RelayURL      string
	Status        string
	Error         string
	Name          string
	SupportedNIPs []int
	Warnings      []string
	Limitations   RelayAdvisoryLimitations
	ObservedAt    time.Time
}

// RelayAdvisoryLimitations captures NIP-11 limitation flags that may affect
// operators but must not remove configured relays by themselves.
type RelayAdvisoryLimitations struct {
	AuthRequired     bool
	PaymentRequired  bool
	RestrictedWrites bool
	MaxLimit         int
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithLogger configures the logger used by the resolver.
func WithLogger(logger *zap.Logger) Option {
	return func(r *Resolver) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// WithPrivateKey configures the resolver to answer NIP-42 AUTH challenges from relays.
func WithPrivateKey(privateKeyHex string) Option {
	return func(r *Resolver) {
		r.privateKey = strings.TrimSpace(privateKeyHex)
	}
}

type relayPool interface {
	Connect(context.Context)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error)
	FetchAllRelayInfo(context.Context) map[string]*nip11.RelayInformationDocument
	AuthenticateRelay(context.Context, string) error
	Close()
}

type relayPoolFactory func([]string, *zap.Logger, string) relayPool

// Resolver maintains a live cache of DNS endpoints from Bahia's canonical kind
// 30900 DNS endpoint state.
type Resolver struct {
	relayURLs    []string
	authorPubkey string

	logger      *zap.Logger
	privateKey  string
	poolFactory relayPoolFactory

	mu            sync.RWMutex
	records       map[string]endpointRecord
	relayMetadata map[string]RelayAdvisoryMetadata
	// syncedThrough is the newest created_at the resolver is known to be
	// caught up to: set at EOSE from the backfill and advanced by live events.
	// Zero means no subscription has reached EOSE yet.
	syncedThrough nostr.Timestamp

	ready     chan struct{}
	readyOnce sync.Once

	lifecycleMu sync.Mutex
	pool        relayPool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	started     bool
}

type endpointRecord struct {
	endpoint  Endpoint
	createdAt nostr.Timestamp
	eventID   string
	deleted   bool
}

// endpointContent is the subset of the projector's domain.DNSEndpoint JSON
// (live records) and tombstone content the resolver reads.
type endpointContent struct {
	Name         string   `json:"name"`
	Environment  string   `json:"environment"`
	Zone         string   `json:"zone"`
	FQDN         string   `json:"fqdn"`
	Address      string   `json:"address"`
	Port         int      `json:"port"`
	Protocol     string   `json:"protocol"`
	Health       string   `json:"health"`
	Runtime      string   `json:"runtime"`
	Hardware     string   `json:"hardware"`
	Capabilities []string `json:"capabilities"`
	Deleted      bool     `json:"deleted"`
}

// New creates a Resolver connected to the given relay URLs.
func New(relayURLs []string, authorPubkey string, opts ...Option) *Resolver {
	r := &Resolver{
		relayURLs:     append([]string(nil), relayURLs...),
		authorPubkey:  authorPubkey,
		logger:        zap.NewNop(),
		poolFactory:   newRelayPool,
		records:       make(map[string]endpointRecord),
		relayMetadata: make(map[string]RelayAdvisoryMetadata),
		ready:         make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start connects to relays and begins subscribing to DNS endpoint state.
func (r *Resolver) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("discovery resolver start: nil context")
	}
	if len(r.relayURLs) == 0 {
		return errors.New("discovery resolver start: no relay URLs configured")
	}
	if strings.TrimSpace(r.authorPubkey) == "" {
		return errors.New("discovery resolver start: author pubkey is required")
	}
	if _, err := nostrutil.PubKeyFromHex(r.authorPubkey); err != nil {
		return fmt.Errorf("discovery resolver start: author pubkey must be valid hex: %w", err)
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	pool := r.poolFactory(r.relayURLs, r.logger, r.privateKey)
	r.pool = pool
	r.cancel = cancel
	r.started = true
	r.wg.Add(1)
	go r.run(runCtx, pool)
	return nil
}

// Stop gracefully disconnects from relays.
func (r *Resolver) Stop() error {
	r.lifecycleMu.Lock()
	if !r.started {
		r.lifecycleMu.Unlock()
		return nil
	}
	cancel := r.cancel
	pool := r.pool
	r.started = false
	r.cancel = nil
	r.pool = nil
	r.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pool != nil {
		pool.Close()
	}
	r.wg.Wait()
	return nil
}

// Ready is closed once the first subscription has received EOSE from every
// relay, i.e. stored endpoint state has been backfilled. Lookups before that
// may miss endpoints that exist on the relays.
func (r *Resolver) Ready() <-chan struct{} {
	return r.ready
}

// Resolve looks up an endpoint by name and environment.
func (r *Resolver) Resolve(name, environment string) (Endpoint, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, record := range r.records {
		if record.deleted {
			continue
		}
		if record.endpoint.Name == name && record.endpoint.Environment == environment {
			return cloneEndpoint(record.endpoint), true
		}
	}
	return Endpoint{}, false
}

// ResolveByFQDN looks up by full FQDN.
func (r *Resolver) ResolveByFQDN(fqdn string) (Endpoint, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, record := range r.records {
		if record.deleted {
			continue
		}
		if record.endpoint.FQDN == fqdn {
			return cloneEndpoint(record.endpoint), true
		}
	}
	return Endpoint{}, false
}

// FindByCapability returns endpoints matching a capability (e.g. "llm", "speech", "gpu").
func (r *Resolver) FindByCapability(capability string) []Endpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var endpoints []Endpoint
	for _, record := range r.records {
		if record.deleted {
			continue
		}
		for _, candidate := range record.endpoint.Capabilities {
			if candidate == capability {
				endpoints = append(endpoints, cloneEndpoint(record.endpoint))
				break
			}
		}
	}
	return endpoints
}

// Endpoints returns all currently cached endpoints.
func (r *Resolver) Endpoints() []Endpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	endpoints := make([]Endpoint, 0, len(r.records))
	for _, record := range r.records {
		if record.deleted {
			continue
		}
		endpoints = append(endpoints, cloneEndpoint(record.endpoint))
	}
	return endpoints
}

// RelayMetadata returns advisory relay metadata collected from best-effort NIP-11
// probes. The configured relay URLs remain authoritative even when metadata is
// missing, malformed, or limiting.
func (r *Resolver) RelayMetadata() map[string]RelayAdvisoryMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]RelayAdvisoryMetadata, len(r.relayMetadata))
	for relayURL, metadata := range r.relayMetadata {
		metadata.SupportedNIPs = append([]int(nil), metadata.SupportedNIPs...)
		metadata.Warnings = append([]string(nil), metadata.Warnings...)
		out[relayURL] = metadata
	}
	return out
}

func newRelayPool(relayURLs []string, logger *zap.Logger, privateKey string) relayPool {
	opts := []nostradapter.RelayPoolOption(nil)
	if privateKey != "" {
		opts = append(opts, nostradapter.WithPrivateKey(privateKey))
	}
	return nostradapter.NewRelayPool(relayURLs, logger, opts...)
}

func (r *Resolver) run(ctx context.Context, pool relayPool) {
	defer r.wg.Done()
	r.prepareRelays(ctx, pool)

	backoff := resolverReconnectInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := r.subscribeUntilClosed(ctx, pool)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.logger.Warn("discovery resolver subscription ended; reconnecting", zap.Error(err), zap.Duration("delay", backoff))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < resolverReconnectMaxBackoff {
			backoff *= 2
			if backoff > resolverReconnectMaxBackoff {
				backoff = resolverReconnectMaxBackoff
			}
		}
	}
}

func (r *Resolver) prepareRelays(ctx context.Context, pool relayPool) {
	infos := pool.FetchAllRelayInfo(ctx)
	now := time.Now().UTC()
	for _, relayURL := range r.relayURLs {
		metadata := advisoryMetadataFromNIP11(relayURL, infos[relayURL], now)
		r.recordRelayMetadata(metadata)
		if metadata.Status == "metadata-unavailable" || metadata.Status == "metadata-malformed" {
			r.logger.Warn("relay NIP-11 metadata unavailable", zap.String("relay", relayURL), zap.String("status", metadata.Status), zap.String("error", metadata.Error))
			continue
		}
		r.logger.Info("relay NIP-11 metadata loaded", zap.String("relay", relayURL), zap.String("name", metadata.Name), zap.Ints("supported_nips", metadata.SupportedNIPs), zap.Strings("warnings", metadata.Warnings))
		if metadata.Limitations.AuthRequired && r.privateKey == "" {
			r.logger.Warn("relay metadata requires NIP-42 AUTH but resolver has no private key", zap.String("relay", relayURL))
		}
	}
	pool.Connect(ctx)
}

func (r *Resolver) recordRelayMetadata(metadata RelayAdvisoryMetadata) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relayMetadata[metadata.RelayURL] = metadata
}

func advisoryMetadataFromNIP11(relayURL string, info *nip11.RelayInformationDocument, observedAt time.Time) RelayAdvisoryMetadata {
	metadata := RelayAdvisoryMetadata{
		RelayURL:   relayURL,
		Status:     "metadata-unavailable",
		Error:      "NIP-11 metadata unavailable",
		ObservedAt: observedAt,
	}
	if info == nil {
		return metadata
	}

	supportedNIPs, warnings := supportedNIPsFromNIP11(info.SupportedNIPs)
	metadata.Status = "metadata-ok"
	metadata.Error = ""
	metadata.Name = info.Name
	metadata.SupportedNIPs = supportedNIPs
	metadata.Warnings = warnings
	if len(warnings) > 0 {
		metadata.Status = "metadata-malformed"
		metadata.Error = strings.Join(warnings, "; ")
	}
	if info.Limitation != nil {
		metadata.Limitations = RelayAdvisoryLimitations{
			AuthRequired:     info.Limitation.AuthRequired,
			PaymentRequired:  info.Limitation.PaymentRequired,
			RestrictedWrites: info.Limitation.RestrictedWrites,
			MaxLimit:         info.Limitation.MaxLimit,
		}
		limitWarnings := limitationWarnings(metadata.Limitations)
		if len(limitWarnings) > 0 {
			metadata.Warnings = append(metadata.Warnings, limitWarnings...)
			if metadata.Status == "metadata-ok" {
				metadata.Status = "metadata-limited"
			}
		}
	}
	return metadata
}

func supportedNIPsFromNIP11(values []any) ([]int, []string) {
	nips := make([]int, 0, len(values))
	warnings := make([]string, 0)
	seen := make(map[int]struct{})
	for _, value := range values {
		var nip int
		switch typed := value.(type) {
		case int:
			nip = typed
		case int64:
			nip = int(typed)
		case float64:
			if typed != float64(int(typed)) {
				warnings = append(warnings, fmt.Sprintf("unsupported non-integer supported_nips value %v", typed))
				continue
			}
			nip = int(typed)
		default:
			warnings = append(warnings, fmt.Sprintf("unsupported supported_nips value %T", value))
			continue
		}
		if nip <= 0 {
			warnings = append(warnings, fmt.Sprintf("invalid supported_nips value %d", nip))
			continue
		}
		if _, ok := seen[nip]; ok {
			continue
		}
		seen[nip] = struct{}{}
		nips = append(nips, nip)
	}
	return nips, warnings
}

func limitationWarnings(limitations RelayAdvisoryLimitations) []string {
	warnings := make([]string, 0)
	if limitations.AuthRequired {
		warnings = append(warnings, "auth-required")
	}
	if limitations.PaymentRequired {
		warnings = append(warnings, "payment-required")
	}
	if limitations.RestrictedWrites {
		warnings = append(warnings, "restricted-writes")
	}
	if limitations.MaxLimit > 0 {
		warnings = append(warnings, fmt.Sprintf("max-limit:%d", limitations.MaxLimit))
	}
	return warnings
}

func (r *Resolver) subscribeUntilClosed(ctx context.Context, pool relayPool) error {
	authAttempted := make(map[string]struct{})
	for {
		merged, err := pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{r.subscriptionFilter()})
		if err != nil {
			return err
		}
		retry, err := r.consume(ctx, pool, merged, authAttempted)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
	}
}

func (r *Resolver) consume(ctx context.Context, pool relayPool, merged *nostradapter.MergedSubscription, authAttempted map[string]struct{}) (bool, error) {
	if merged == nil {
		return false, nil
	}
	defer merged.Close()
	// Backfill-then-live: until EOSE, stored events may arrive in any order,
	// so the resume cursor only moves once the whole backfill has been seen.
	caughtUp := false
	var newest nostr.Timestamp
	for merged.Events != nil || merged.EndOfStoredEvents != nil || merged.RelayEOSE != nil || merged.Closed != nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				r.logger.Info("relay sent EOSE", zap.String("relay", eose.RelayURL), zap.String("subscription_id", eose.SubscriptionID))
			} else {
				merged.RelayEOSE = nil
			}
		case <-merged.EndOfStoredEvents:
			r.logger.Info("all relays sent EOSE; historical endpoint catch-up complete")
			merged.EndOfStoredEvents = nil
			caughtUp = true
			r.markSynced(newest)
		case closed, ok := <-merged.Closed:
			if ok {
				if r.handleClosed(ctx, pool, closed, authAttempted) {
					return true, nil
				}
			} else {
				merged.Closed = nil
			}
		case ev, ok := <-merged.Events:
			if !ok {
				merged.Events = nil
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				continue
			}
			if err := r.applyEvent(ev); err != nil {
				if errors.Is(err, errNotDNSEndpoint) {
					r.logger.Debug("skipped non-endpoint control state", zap.String("event_id", eventID(ev)), zap.Error(err))
				} else {
					r.logger.Warn("ignored invalid discovery endpoint event", zap.String("event_id", eventID(ev)), zap.Error(err))
				}
				continue
			}
			if caughtUp {
				r.markSynced(ev.CreatedAt)
			} else if ev.CreatedAt > newest {
				newest = ev.CreatedAt
			}
		}
	}
	return false, errors.New("subscription event stream closed")
}

func (r *Resolver) handleClosed(ctx context.Context, pool relayPool, closed nostradapter.RelayClosed, authAttempted map[string]struct{}) bool {
	r.logger.Warn("relay closed subscription", zap.String("relay", closed.RelayURL), zap.String("subscription_id", closed.SubscriptionID), zap.String("reason", closed.Reason))
	if !nostradapter.IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		r.logger.Warn("relay authentication failed", zap.String("relay", closed.RelayURL), zap.Error(err))
		return false
	}
	return true
}

// markSynced records that the resolver has seen everything up to createdAt
// and releases Ready waiters.
func (r *Resolver) markSynced(createdAt nostr.Timestamp) {
	r.mu.Lock()
	if createdAt > r.syncedThrough {
		r.syncedThrough = createdAt
	}
	r.mu.Unlock()
	r.readyOnce.Do(func() { close(r.ready) })
}

// subscriptionFilter scopes the REQ to canonical DNS endpoint state from the
// Bahia service key. #t is a single-letter tag, so NIP-01 relays index it; the
// envelope's domain/schema/legacy_kind tags are multi-letter and are checked
// locally. After a completed backfill, resubscribes start from the newest seen
// created_at minus resolverSinceOverlap instead of re-downloading everything.
func (r *Resolver) subscriptionFilter() nostr.Filter {
	pubkey, _ := nostrutil.PubKeyFromHex(r.authorPubkey)
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{pubkey},
		Tags:    nostr.TagMap{"t": []string{kinds.DNSEndpointTopic}},
	}
	r.mu.RLock()
	synced := r.syncedThrough
	r.mu.RUnlock()
	if since := int64(synced) - int64(resolverSinceOverlap/time.Second); synced > 0 && since > 0 {
		filter.Since = nostr.Timestamp(since)
	}
	return filter
}

// applyEvent folds one DNS endpoint record into the cache. Per (pubkey, d) the
// newest created_at wins and equal created_at is broken by the lowest event
// id (NIP-01), so the result does not depend on arrival order. A tombstone is
// kept as a deleted record so older live events cannot resurrect it.
func (r *Resolver) applyEvent(event *nostr.Event) error {
	if err := r.validateEnvelope(event); err != nil {
		return err
	}
	coordinate := nostrutil.EventPubKeyHex(event) + ":" + event.Tags.GetD()
	id := nostrutil.EventIDHex(event)

	r.mu.RLock()
	current, ok := r.records[coordinate]
	r.mu.RUnlock()
	if ok && !supersedes(event.CreatedAt, id, current) {
		return nil
	}

	endpoint, deleted, err := endpointFromEvent(event)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Recheck: another applyEvent may have raced in between the locks.
	if current, ok := r.records[coordinate]; ok && !supersedes(event.CreatedAt, id, current) {
		return nil
	}
	if deleted {
		r.records[coordinate] = endpointRecord{createdAt: event.CreatedAt, eventID: id, deleted: true}
		return nil
	}
	r.records[coordinate] = endpointRecord{endpoint: endpoint, createdAt: event.CreatedAt, eventID: id}
	return nil
}

func supersedes(createdAt nostr.Timestamp, id string, current endpointRecord) bool {
	if createdAt != current.createdAt {
		return createdAt > current.createdAt
	}
	return id < current.eventID
}

// validateEnvelope accepts only signed canonical DNS endpoint records from the
// configured Bahia service key; relays that ignore #t may return other 30900
// state, and relays do not enforce authors on our behalf.
func (r *Resolver) validateEnvelope(event *nostr.Event) error {
	if event == nil {
		return errors.New("nil event")
	}
	if err := nostradapter.ValidateInboundEvent(event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return err
	}
	if int(event.Kind) != kinds.CASControlState {
		return fmt.Errorf("unexpected kind %d", event.Kind)
	}
	if pubkey := nostrutil.EventPubKeyHex(event); pubkey != r.authorPubkey {
		return fmt.Errorf("unexpected author %s", pubkey)
	}
	if domain := firstTagValue(event.Tags, kinds.CASControlStateTagDomain); domain != kinds.DNSDomain {
		return fmt.Errorf("%w: domain %q", errNotDNSEndpoint, domain)
	}
	if schema := firstTagValue(event.Tags, kinds.CASControlStateTagSchema); schema != kinds.CASControlStateSchema {
		return fmt.Errorf("%w: schema %q", errNotDNSEndpoint, schema)
	}
	if legacyKind := firstTagValue(event.Tags, kinds.CASControlStateTagLegacyKind); legacyKind != endpointLegacyKind {
		return fmt.Errorf("%w: legacy_kind %q", errNotDNSEndpoint, legacyKind)
	}
	if event.Tags.GetD() == "" {
		return errors.New("missing d tag coordinate")
	}
	return nil
}

// endpointFromEvent parses the projector's DNS endpoint record: dnsEndpointTags
// plus domain.DNSEndpoint JSON content for live records, or the tombstone
// content. Live records carry deleted=false, so the tag value is compared
// rather than its presence.
func endpointFromEvent(event *nostr.Event) (Endpoint, bool, error) {
	var content endpointContent
	if strings.TrimSpace(event.Content) != "" {
		if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
			return Endpoint{}, false, fmt.Errorf("parse endpoint content JSON: %w", err)
		}
	}
	if content.Deleted || firstTagValue(event.Tags, kinds.CASControlStateTagDeleted) == "true" {
		return Endpoint{}, true, nil
	}

	fqdn := firstString(firstTagValue(event.Tags, "dns"), content.FQDN)
	if fqdn == "" {
		return Endpoint{}, false, errors.New("missing dns tag FQDN")
	}
	address := firstString(firstTagValue(event.Tags, "addr"), content.Address)
	if address == "" {
		return Endpoint{}, false, errors.New("endpoint address is required")
	}
	port := content.Port
	if raw := firstTagValue(event.Tags, "port"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Endpoint{}, false, fmt.Errorf("endpoint port tag %q is invalid: %w", raw, err)
		}
		port = parsed
	}
	if port < 0 || port > 65535 {
		return Endpoint{}, false, fmt.Errorf("endpoint port %d is invalid", port)
	}

	environment := firstString(firstTagValue(event.Tags, "environment"), content.Environment)
	zone := firstString(firstTagValue(event.Tags, "zone"), content.Zone)
	capabilities := allTagValues(event.Tags, "capability")
	if len(capabilities) == 0 {
		capabilities = append(capabilities, content.Capabilities...)
	}
	return Endpoint{
		FQDN:         fqdn,
		Name:         firstString(content.Name, endpointName(fqdn, environment, zone)),
		Environment:  environment,
		ZoneName:     zone,
		Address:      address,
		Port:         port,
		Protocol:     firstString(firstTagValue(event.Tags, "proto"), content.Protocol),
		Health:       firstString(firstTagValue(event.Tags, "health"), content.Health),
		Capabilities: capabilities,
		Runtime:      firstString(firstTagValue(event.Tags, "runtime"), content.Runtime),
		Hardware:     firstString(firstTagValue(event.Tags, "hardware"), content.Hardware),
		UpdatedAt:    time.Unix(int64(event.CreatedAt), 0).UTC(),
	}, false, nil
}

func firstTagValue(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func allTagValues(tags nostr.Tags, key string) []string {
	values := make([]string, 0)
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			values = append(values, tag[1])
		}
	}
	return values
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func endpointName(fqdn, environment, zone string) string {
	name := fqdn
	if zone != "" {
		zoneSuffix := "." + zone
		name = strings.TrimSuffix(name, zoneSuffix)
	}
	if environment != "" {
		envSuffix := "." + environment
		name = strings.TrimSuffix(name, envSuffix)
	}
	if name == "" || name == fqdn {
		parts := strings.Split(fqdn, ".")
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return name
}

func eventID(event *nostr.Event) string {
	if event == nil {
		return ""
	}
	return nostrutil.EventIDHex(event)
}

func cloneEndpoint(endpoint Endpoint) Endpoint {
	endpoint.Capabilities = append([]string(nil), endpoint.Capabilities...)
	return endpoint
}
