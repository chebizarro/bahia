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

	mu      sync.RWMutex
	records map[string]endpointRecord

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
	Port     int             `json:"port"`
	Protocol string          `json:"protocol"`
	Deleted  bool            `json:"deleted"`
	Metadata json.RawMessage `json:"metadata"`
}

// New creates a Resolver connected to the given relay URLs.
func New(relayURLs []string, authorPubkey string, opts ...Option) *Resolver {
	r := &Resolver{
		relayURLs:    append([]string(nil), relayURLs...),
		authorPubkey: authorPubkey,
		logger:       zap.NewNop(),
		poolFactory:  newRelayPool,
		records:      make(map[string]endpointRecord),
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
	record, ok := r.records[fqdn]
	if !ok || record.deleted {
		return Endpoint{}, false
	}
	return cloneEndpoint(record.endpoint), true
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
	for relayURL, info := range infos {
		if info == nil {
			r.logger.Warn("relay NIP-11 metadata unavailable", zap.String("relay", relayURL))
			continue
		}
		r.logger.Info("relay NIP-11 metadata loaded", zap.String("relay", relayURL), zap.String("name", info.Name), zap.Any("supported_nips", info.SupportedNIPs))
		if info.Limitation != nil && info.Limitation.AuthRequired && r.privateKey == "" {
			r.logger.Warn("relay metadata requires NIP-42 AUTH but resolver has no private key", zap.String("relay", relayURL))
		}
	}
	pool.Connect(ctx)
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

	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.records[endpoint.FQDN]
	if ok && current.createdAt >= event.CreatedAt {
		return nil
	}
	if deleted {
		r.records[endpoint.FQDN] = endpointRecord{createdAt: event.CreatedAt, deleted: true}
		return nil
	}
	r.records[endpoint.FQDN] = endpointRecord{endpoint: endpoint, createdAt: event.CreatedAt}
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

	fqdn := event.Tags.GetD()
	if fqdn == "" {
		return Endpoint{}, false, errors.New("missing d tag FQDN")
	}

	var content endpointContent
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return Endpoint{}, false, fmt.Errorf("parse endpoint content JSON: %w", err)
	}
	if !content.Deleted {
		if strings.TrimSpace(content.Address) == "" {
			return Endpoint{}, false, errors.New("endpoint content address is required")
		}
		if content.Port <= 0 || content.Port > 65535 {
			return Endpoint{}, false, fmt.Errorf("endpoint content port %d is invalid", content.Port)
		}
		if strings.TrimSpace(content.Protocol) == "" {
			return Endpoint{}, false, errors.New("endpoint content protocol is required")
		}
	}

	environment := firstTagValue(event.Tags, "env")
	zone := firstTagValue(event.Tags, "zone")
	endpoint := Endpoint{
		FQDN:         fqdn,
		Name:         endpointName(fqdn, environment, zone),
		Environment:  environment,
		ZoneName:     zone,
		Address:      content.Address,
		Port:         content.Port,
		Protocol:     content.Protocol,
		Health:       firstTagValue(event.Tags, "health"),
		Capabilities: allTagValues(event.Tags, "cap"),
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
