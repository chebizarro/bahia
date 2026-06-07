package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

const (
	// KindDNSEndpointState is the Nostr kind for DNS endpoint state events.
	KindDNSEndpointState = 31976

	resolverReconnectInitialBackoff = time.Second
	resolverReconnectMaxBackoff     = 30 * time.Second
)

// Endpoint represents a resolved DNS endpoint from a kind 31976 event.
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

// Resolver maintains a live cache of DNS endpoints from Nostr kind 31976 events.
type Resolver struct {
	relayURLs    []string
	authorPubkey string

	logger      *zap.Logger
	privateKey  string
	poolFactory relayPoolFactory

	mu            sync.RWMutex
	records       map[string]endpointRecord
	relayMetadata map[string]RelayAdvisoryMetadata

	lifecycleMu sync.Mutex
	pool        relayPool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	started     bool
}

type endpointRecord struct {
	endpoint  Endpoint
	createdAt nostr.Timestamp
	deleted   bool
}

type endpointContent struct {
	Address  string          `json:"address"`
	Addr     string          `json:"addr"`
	Port     int             `json:"port"`
	Protocol string          `json:"protocol"`
	Proto    string          `json:"proto"`
	FQDN     string          `json:"fqdn"`
	DNS      string          `json:"dns"`
	Deleted  bool            `json:"deleted"`
	Metadata json.RawMessage `json:"metadata"`
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
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Start connects to relays and begins subscribing to kind 31976 events.
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
	filters := []nostr.Filter{r.subscriptionFilter()}
	authAttempted := make(map[string]struct{})
	for {
		merged, err := pool.SubscribeAllWithEOSE(ctx, filters)
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
				r.logger.Warn("ignored invalid discovery endpoint event", zap.String("event_id", eventID(ev)), zap.Error(err))
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

func (r *Resolver) subscriptionFilter() nostr.Filter {
	return nostr.Filter{
		Kinds:   []int{KindDNSEndpointState},
		Authors: []string{r.authorPubkey},
	}
}

func (r *Resolver) applyEvent(event *nostr.Event) error {
	endpoint, deleted, err := r.endpointFromEvent(event)
	if err != nil {
		return err
	}

	coordinate := event.Tags.GetD()
	if coordinate == "" {
		return errors.New("missing d tag coordinate")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.records[coordinate]
	if ok && current.createdAt >= event.CreatedAt {
		return nil
	}
	if deleted {
		r.records[coordinate] = endpointRecord{createdAt: event.CreatedAt, deleted: true}
		return nil
	}
	r.records[coordinate] = endpointRecord{endpoint: endpoint, createdAt: event.CreatedAt}
	return nil
}

func (r *Resolver) endpointFromEvent(event *nostr.Event) (Endpoint, bool, error) {
	if event == nil {
		return Endpoint{}, false, errors.New("nil event")
	}
	if err := nostradapter.ValidateInboundEvent(event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return Endpoint{}, false, err
	}
	if event.Kind != KindDNSEndpointState {
		return Endpoint{}, false, fmt.Errorf("unexpected kind %d", event.Kind)
	}
	if r.authorPubkey != "" && event.PubKey != r.authorPubkey {
		return Endpoint{}, false, fmt.Errorf("unexpected author %s", event.PubKey)
	}

	coordinate := event.Tags.GetD()
	if coordinate == "" {
		return Endpoint{}, false, errors.New("missing d tag coordinate")
	}

	var content endpointContent
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return Endpoint{}, false, fmt.Errorf("parse endpoint content JSON: %w", err)
	}

	fqdn := firstString(firstTagValue(event.Tags, "dns"), content.FQDN, content.DNS)
	if fqdn == "" {
		return Endpoint{}, false, errors.New("missing dns tag FQDN")
	}
	address := firstString(content.Addr, content.Address)
	protocol := firstString(content.Proto, content.Protocol)
	if !content.Deleted {
		if strings.TrimSpace(address) == "" {
			return Endpoint{}, false, errors.New("endpoint content address is required")
		}
		if content.Port <= 0 || content.Port > 65535 {
			return Endpoint{}, false, fmt.Errorf("endpoint content port %d is invalid", content.Port)
		}
		if strings.TrimSpace(protocol) == "" {
			return Endpoint{}, false, errors.New("endpoint content protocol is required")
		}
	}

	environment := firstString(firstTagValue(event.Tags, "env"), firstTagValue(event.Tags, "environment"))
	zone := firstTagValue(event.Tags, "zone")
	endpoint := Endpoint{
		FQDN:         fqdn,
		Name:         endpointName(fqdn, environment, zone),
		Environment:  environment,
		ZoneName:     zone,
		Address:      address,
		Port:         content.Port,
		Protocol:     protocol,
		Health:       firstTagValue(event.Tags, "health"),
		Capabilities: allTagValues(event.Tags, "capability"),
		Runtime:      firstTagValue(event.Tags, "runtime"),
		Hardware:     firstTagValue(event.Tags, "hardware"),
		UpdatedAt:    time.Unix(int64(event.CreatedAt), 0).UTC(),
	}
	return endpoint, content.Deleted, nil
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
	return event.ID
}

func cloneEndpoint(endpoint Endpoint) Endpoint {
	endpoint.Capabilities = append([]string(nil), endpoint.Capabilities...)
	return endpoint
}
