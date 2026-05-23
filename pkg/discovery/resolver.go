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
	"go.uber.org/zap"
)

const (
	// KindDNSEndpointState is the Nostr kind for DNS endpoint state events.
	KindDNSEndpointState = 31976

	maxFutureSkew = 15 * time.Minute
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

// WithRefreshInterval configures how often the resolver reissues its live subscription.
func WithRefreshInterval(d time.Duration) Option {
	return func(r *Resolver) {
		r.refreshInterval = d
	}
}

// Resolver maintains a live cache of DNS endpoints from Nostr kind 31976 events.
type Resolver struct {
	relayURLs    []string
	authorPubkey string

	logger          *zap.Logger
	refreshInterval time.Duration

	mu      sync.RWMutex
	records map[string]endpointRecord

	lifecycleMu sync.Mutex
	pool        *nostr.SimplePool
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
		relayURLs:       append([]string(nil), relayURLs...),
		authorPubkey:    authorPubkey,
		logger:          zap.NewNop(),
		refreshInterval: 0,
		records:         make(map[string]endpointRecord),
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
	pool := nostr.NewSimplePool(runCtx)
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
		pool.Close("discovery resolver stopped")
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

func (r *Resolver) run(ctx context.Context, pool *nostr.SimplePool) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		subCtx, cancel := context.WithCancel(ctx)
		events := pool.SubMany(subCtx, r.relayURLs, nostr.Filters{r.subscriptionFilter()})
		var refresh <-chan time.Time
		var ticker *time.Ticker
		if r.refreshInterval > 0 {
			ticker = time.NewTicker(r.refreshInterval)
			refresh = ticker.C
		}

		resubscribe := false
		for !resubscribe {
			select {
			case relayEvent, ok := <-events:
				if !ok {
					resubscribe = true
					continue
				}
				if err := r.applyEvent(relayEvent.Event); err != nil {
					r.logger.Warn("ignored invalid discovery endpoint event", zap.String("relay", relayEvent.Relay.URL), zap.Error(err))
				}
			case <-refresh:
				resubscribe = true
			case <-ctx.Done():
				cancel()
				if ticker != nil {
					ticker.Stop()
				}
				return
			}
		}

		cancel()
		if ticker != nil {
			ticker.Stop()
		}
	}
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
	if event.Kind != KindDNSEndpointState {
		return Endpoint{}, false, fmt.Errorf("unexpected kind %d", event.Kind)
	}
	if r.authorPubkey != "" && event.PubKey != r.authorPubkey {
		return Endpoint{}, false, fmt.Errorf("unexpected author %s", event.PubKey)
	}
	if len(event.ID) != 64 || !event.CheckID() {
		return Endpoint{}, false, errors.New("event id does not match NIP-01 hash")
	}
	validSignature, err := event.CheckSignature()
	if err != nil {
		return Endpoint{}, false, fmt.Errorf("check event signature: %w", err)
	}
	if !validSignature {
		return Endpoint{}, false, errors.New("invalid event signature")
	}
	if event.CreatedAt > nostr.Timestamp(time.Now().Add(maxFutureSkew).Unix()) {
		return Endpoint{}, false, errors.New("event timestamp is too far in the future")
	}

	fqdn := event.Tags.GetD()
	if fqdn == "" {
		return Endpoint{}, false, errors.New("missing d tag FQDN")
	}

	var content endpointContent
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		return Endpoint{}, false, fmt.Errorf("parse endpoint content JSON: %w", err)
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

func cloneEndpoint(endpoint Endpoint) Endpoint {
	endpoint.Capabilities = append([]string(nil), endpoint.Capabilities...)
	return endpoint
}
