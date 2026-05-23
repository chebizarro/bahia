package nostr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

const FIPSOverlayAdvertKind = 37195

// OverlayAdvert is the Kind 37195 content Bahia consumes from FIPS nodes.
type OverlayAdvert struct {
	Identifier   string                  `json:"identifier"`
	Version      int                     `json:"version"`
	Endpoints    []overlayAdvertEndpoint `json:"endpoints"`
	SignalRelays []string                `json:"signalRelays,omitempty"`
	STUNServers  []string                `json:"stunServers,omitempty"`
}

type overlayAdvertEndpoint struct {
	Transport string `json:"transport"`
	Addr      string `json:"addr"`
	Address   string `json:"address"`
}

// FIPSWorkerUpdateHandler is invoked after a FIPS advert updates a worker record.
type FIPSWorkerUpdateHandler func(ctx context.Context, worker *domain.Worker, advert OverlayAdvert)

// FIPSSubscriber subscribes to FIPS overlay adverts and applies them to matching workers.
type FIPSSubscriber struct {
	pool                *RelayPool
	workerRepo          repository.WorkerRepository
	appNamespace        string
	autoRegisterWorkers bool
	allowedPubkeys      map[string]struct{}
	logger              *zap.Logger
	now                 func() time.Time
	handlers            []FIPSWorkerUpdateHandler

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// FIPSSubscriberOption configures a FIPSSubscriber.
type FIPSSubscriberOption func(*FIPSSubscriber)

// WithFIPSAppNamespace sets the expected FIPS advert identifier and d tag.
func WithFIPSAppNamespace(namespace string) FIPSSubscriberOption {
	return func(s *FIPSSubscriber) {
		if strings.TrimSpace(namespace) != "" {
			s.appNamespace = strings.TrimSpace(namespace)
		}
	}
}

// WithFIPSAutoRegisterWorkers allows unknown allowlisted FIPS pubkeys to create worker entries.
func WithFIPSAutoRegisterWorkers(enabled bool) FIPSSubscriberOption {
	return func(s *FIPSSubscriber) { s.autoRegisterWorkers = enabled }
}

// WithFIPSAllowedNpubs limits accepted FIPS advert authors. Entries may be npub or 64-character hex pubkeys.
func WithFIPSAllowedNpubs(values []string) FIPSSubscriberOption {
	return func(s *FIPSSubscriber) { s.allowedPubkeys = normalizeFIPSAllowedPubkeys(values) }
}

// WithFIPSWorkerUpdateHandler adds a post-update callback.
func WithFIPSWorkerUpdateHandler(handler FIPSWorkerUpdateHandler) FIPSSubscriberOption {
	return func(s *FIPSSubscriber) {
		if handler != nil {
			s.handlers = append(s.handlers, handler)
		}
	}
}

func withFIPSClock(now func() time.Time) FIPSSubscriberOption {
	return func(s *FIPSSubscriber) {
		if now != nil {
			s.now = now
		}
	}
}

// NewFIPSSubscriber creates a FIPS overlay advert subscriber around an existing relay pool.
func NewFIPSSubscriber(pool *RelayPool, workerRepo repository.WorkerRepository, logger *zap.Logger, opts ...FIPSSubscriberOption) *FIPSSubscriber {
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &FIPSSubscriber{
		pool:         pool,
		workerRepo:   workerRepo,
		appNamespace: "fips-overlay-v1",
		logger:       logger.Named("fips-subscriber"),
		now:          func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NewFIPSSubscriberFromRelayURLs creates a FIPS subscriber with its own relay pool.
func NewFIPSSubscriberFromRelayURLs(relayURLs []string, workerRepo repository.WorkerRepository, logger *zap.Logger, opts ...FIPSSubscriberOption) *FIPSSubscriber {
	if logger == nil {
		logger = zap.NewNop()
	}
	pool := NewRelayPool(relayURLs, logger.Named("fips-relay-pool"))
	return NewFIPSSubscriber(pool, workerRepo, logger, opts...)
}

// Name identifies the background runner.
func (s *FIPSSubscriber) Name() string { return "fips-subscriber" }

// Start begins the subscription lifecycle in the background.
func (s *FIPSSubscriber) Start(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("fips subscriber is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return fmt.Errorf("fips subscriber already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.Run(runCtx); err != nil && runCtx.Err() == nil {
			s.logger.Warn("fips subscriber stopped", zap.Error(err))
		}
	}()
	return nil
}

// Stop cancels the active subscription and waits for shutdown.
func (s *FIPSSubscriber) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// Run subscribes until ctx is cancelled, reconnecting with exponential backoff after relay closures/errors.
func (s *FIPSSubscriber) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("fips subscriber is nil")
	}
	if s.pool == nil {
		return fmt.Errorf("fips subscriber relay pool is required")
	}
	if s.workerRepo == nil {
		return fmt.Errorf("fips subscriber worker repository is required")
	}
	backoff := DefaultBackoff()
	for {
		err := s.subscribe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := backoff.Next()
		s.logger.Warn("fips subscription ended, reconnecting with backoff", zap.Error(err), zap.Duration("delay", delay), zap.Int("attempt", backoff.Attempt()))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

func (s *FIPSSubscriber) subscribe(ctx context.Context) error {
	merged, err := s.pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{s.filter()})
	if err != nil {
		return err
	}
	defer merged.Close()

	s.logger.Info("subscribed to FIPS overlay adverts", zap.Strings("relays", s.pool.URLs()), zap.String("namespace", s.appNamespace))
	authAttempted := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				s.logger.Debug("relay sent FIPS EOSE", zap.String("relay", eose.RelayURL), zap.String("subscription_id", eose.SubscriptionID))
			} else {
				merged.RelayEOSE = nil
			}
		case closed, ok := <-merged.Closed:
			if ok {
				if s.handleRelayClosed(ctx, closed, authAttempted) {
					return nil
				}
			} else {
				merged.Closed = nil
			}
		case <-merged.EndOfStoredEvents:
			s.logger.Info("FIPS overlay advert EOSE received")
			merged.EndOfStoredEvents = nil
		case ev, ok := <-merged.Events:
			if !ok {
				return nil
			}
			s.handleEvent(ctx, ev)
		}
	}
}

func (s *FIPSSubscriber) filter() gonostr.Filter {
	return gonostr.Filter{
		Kinds: []int{FIPSOverlayAdvertKind},
		Tags:  gonostr.TagMap{"d": []string{s.appNamespace}},
	}
}

func (s *FIPSSubscriber) handleRelayClosed(ctx context.Context, closed RelayClosed, authAttempted map[string]struct{}) bool {
	s.logger.Warn("relay closed FIPS subscription", zap.String("relay", closed.RelayURL), zap.String("subscription_id", closed.SubscriptionID), zap.String("reason", closed.Reason))
	if !IsAuthRequiredReason(closed.Reason) || closed.RelayURL == "" || s.pool == nil {
		return false
	}
	if _, ok := authAttempted[closed.RelayURL]; ok {
		return false
	}
	authAttempted[closed.RelayURL] = struct{}{}
	if err := s.pool.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
		s.logger.Warn("relay FIPS subscription auth failed", zap.String("relay", closed.RelayURL), zap.String("reason", closed.Reason), zap.Error(err))
		return false
	}
	return true
}

func (s *FIPSSubscriber) handleEvent(ctx context.Context, ev *gonostr.Event) {
	if err := ValidateInboundEvent(ev, s.now(), InboundEventMaxFutureSkew); err != nil {
		eventID := ""
		if ev != nil {
			eventID = ev.ID
		}
		s.logger.Warn("dropping invalid FIPS advert", zap.String("event_id", eventID), zap.Error(err))
		return
	}
	worker, advert, err := s.workerFromEvent(ctx, ev)
	if err != nil {
		s.logger.Warn("dropping FIPS advert", zap.String("event_id", ev.ID), zap.String("pubkey", ev.PubKey), zap.Error(err))
		return
	}
	if worker == nil {
		s.logger.Debug("ignoring FIPS advert from unknown worker", zap.String("event_id", ev.ID), zap.String("pubkey", ev.PubKey))
		return
	}
	if err := s.workerRepo.Upsert(ctx, worker); err != nil {
		s.logger.Warn("updating worker from FIPS advert failed", zap.String("event_id", ev.ID), zap.String("pubkey", ev.PubKey), zap.Error(err))
		return
	}
	for _, handler := range s.handlers {
		handler(ctx, worker, advert)
	}
}

func (s *FIPSSubscriber) workerFromEvent(ctx context.Context, ev *gonostr.Event) (*domain.Worker, OverlayAdvert, error) {
	if ev.Kind != FIPSOverlayAdvertKind {
		return nil, OverlayAdvert{}, fmt.Errorf("unexpected event kind %d", ev.Kind)
	}
	if !eventHasTagValue(ev.Tags, "d", s.appNamespace) {
		return nil, OverlayAdvert{}, fmt.Errorf("missing expected d tag %q", s.appNamespace)
	}
	if len(s.allowedPubkeys) > 0 {
		if _, ok := s.allowedPubkeys[strings.ToLower(ev.PubKey)]; !ok {
			return nil, OverlayAdvert{}, fmt.Errorf("pubkey is not allowlisted")
		}
	}
	advert, err := ParseOverlayAdvert(ev.Content, s.appNamespace)
	if err != nil {
		return nil, OverlayAdvert{}, err
	}
	endpoints := advert.FIPSEndpoints()
	overlayIP, err := FIPSOverlayAddress(ev.PubKey)
	if err != nil {
		return nil, OverlayAdvert{}, err
	}
	worker, err := s.workerRepo.GetByPubKey(ctx, ev.PubKey)
	if err != nil {
		return nil, OverlayAdvert{}, fmt.Errorf("lookup worker by pubkey: %w", err)
	}
	if worker == nil {
		if !s.autoRegisterWorkers {
			return nil, advert, nil
		}
		now := s.now()
		worker = &domain.Worker{
			PubKey:              ev.PubKey,
			Name:                ev.PubKey,
			MaxConcurrentJobs:   1,
			LastAdvertisementAt: now,
			Status:              domain.WorkerStatusOnline,
			SchedulingState:     domain.WorkerSchedulingActive,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
	}
	worker.FIPSOverlayAddr = overlayIP.String()
	worker.FIPSEndpoints = endpoints
	worker.UpdatedAt = s.now()
	return worker, advert, nil
}

// ParseOverlayAdvert validates and parses a FIPS OverlayAdvert JSON body.
func ParseOverlayAdvert(content string, expectedIdentifier string) (OverlayAdvert, error) {
	var advert OverlayAdvert
	if err := json.Unmarshal([]byte(content), &advert); err != nil {
		return OverlayAdvert{}, fmt.Errorf("parse FIPS overlay advert JSON: %w", err)
	}
	advert.Identifier = strings.TrimSpace(advert.Identifier)
	if advert.Identifier == "" {
		return OverlayAdvert{}, fmt.Errorf("FIPS overlay advert identifier is required")
	}
	if expected := strings.TrimSpace(expectedIdentifier); expected != "" && advert.Identifier != expected {
		return OverlayAdvert{}, fmt.Errorf("FIPS overlay advert identifier %q does not match %q", advert.Identifier, expected)
	}
	if advert.Version <= 0 {
		return OverlayAdvert{}, fmt.Errorf("FIPS overlay advert version must be positive")
	}
	for i := range advert.Endpoints {
		advert.Endpoints[i].Transport = strings.TrimSpace(advert.Endpoints[i].Transport)
		advert.Endpoints[i].Addr = strings.TrimSpace(advert.Endpoints[i].Addr)
		advert.Endpoints[i].Address = strings.TrimSpace(advert.Endpoints[i].Address)
		if advert.Endpoints[i].Address == "" {
			advert.Endpoints[i].Address = advert.Endpoints[i].Addr
		}
		if advert.Endpoints[i].Transport == "" {
			return OverlayAdvert{}, fmt.Errorf("FIPS overlay advert endpoint %d transport is required", i)
		}
		if advert.Endpoints[i].Address == "" {
			return OverlayAdvert{}, fmt.Errorf("FIPS overlay advert endpoint %d address is required", i)
		}
	}
	return advert, nil
}

// FIPSEndpoints converts advert endpoints into Bahia domain transport endpoints.
func (a OverlayAdvert) FIPSEndpoints() []domain.FIPSTransportEndpoint {
	if len(a.Endpoints) == 0 {
		return nil
	}
	out := make([]domain.FIPSTransportEndpoint, 0, len(a.Endpoints))
	for _, endpoint := range a.Endpoints {
		address := endpoint.Address
		if address == "" {
			address = endpoint.Addr
		}
		out = append(out, domain.FIPSTransportEndpoint{Transport: endpoint.Transport, Address: address})
	}
	return out
}

// FIPSOverlayAddress derives the fd00::/8 IPv6 address from a Nostr hex pubkey.
func FIPSOverlayAddress(hexPubkey string) (net.IP, error) {
	pubkeyBytes, err := hex.DecodeString(strings.TrimSpace(hexPubkey))
	if err != nil {
		return nil, fmt.Errorf("decode pubkey: %w", err)
	}
	if len(pubkeyBytes) != 32 {
		return nil, fmt.Errorf("pubkey must be 32 bytes, got %d", len(pubkeyBytes))
	}
	hash := sha256.Sum256(pubkeyBytes)
	ip := make(net.IP, net.IPv6len)
	ip[0] = 0xfd
	copy(ip[1:], hash[:15])
	return ip, nil
}

func normalizeFIPSAllowedPubkeys(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "npub1") {
			prefix, decoded, err := nip19.Decode(value)
			if err != nil || prefix != "npub" {
				continue
			}
			if pubkey, ok := decoded.(string); ok {
				value = pubkey
			}
		}
		value = strings.ToLower(value)
		if len(value) != 64 {
			continue
		}
		if _, err := hex.DecodeString(value); err != nil {
			continue
		}
		out[value] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func eventHasTagValue(tags gonostr.Tags, key string, expected string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == expected {
			return true
		}
	}
	return false
}
