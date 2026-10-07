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
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
)

// Bahia publishes DNS endpoint state (live and tombstone) only through the
// projector's canonical control-state envelope: kind 30900 with domain=dns,
// schema=bahia.cp-state.v1, legacy_kind=31976, deleted=true|false and
// t=dns-endpoint. Legacy kind 31976 itself is never published; live records
// and tombstones both land on 30900, so the resolver reads 30900 only.
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

// WithServiceKeys trusts additional Bahia service keys besides the author key
// passed to New, so endpoint state keeps resolving across a key rotation: the
// REQ asks for every trusted author and events from any of them are accepted.
// The keys are one logical publisher: a d-tag coordinate is shared across
// them, so the newest record wins whichever key signed it (a record from the
// new key supersedes the old key's, and a tombstone from either removes it).
// Keys must be 64-character hex; Start rejects invalid ones. Duplicates are
// ignored.
func WithServiceKeys(pubkeys ...string) Option {
	return func(r *Resolver) {
		r.extraAuthors = append(r.extraAuthors, pubkeys...)
	}
}

// WithPrivateKey configures the resolver to answer NIP-42 AUTH challenges from relays.
func WithPrivateKey(privateKeyHex string) Option {
	return func(r *Resolver) {
		r.privateKey = strings.TrimSpace(privateKeyHex)
	}
}

// WithStorePath keeps endpoint events and per-relay sync cursors in a local
// bbolt event store at path. Start then restores endpoints
// from the store before any relay answers, and syncs each relay on its own:
// replaceable endpoint state is reconciled with NIP-77 where the relay
// supports it, so a restart downloads only the events the store lacks, and a
// relay that was down catches up independently when it returns.
//
// The store is a cache: deleting it is safe, and the resolver rebuilds it from
// its relays. One process may open a path once at a time (other processes are
// locked out). Without this option the resolver keeps everything in memory and
// resyncs from its relays on every start.
func WithStorePath(path string) Option {
	return func(r *Resolver) { r.storePath = strings.TrimSpace(path) }
}

type relayPool interface {
	Connect(context.Context)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error)
	FetchAllRelayInfo(context.Context) map[string]*nip11.RelayInformationDocument
	Close()
}

type relayPoolFactory func([]string, *zap.Logger, string) relayPool

// Resolver maintains a live cache of DNS endpoints from Bahia's canonical kind
// 30900 DNS endpoint state, in memory or (WithStorePath) backed by a local
// event store.
type Resolver struct {
	relayURLs    []string
	authorPubkey string
	// extraAuthors are the WithServiceKeys keys as given; authors is the
	// normalized, de-duplicated trusted set (authorPubkey first).
	extraAuthors []string
	authors      []string
	authorSet    map[string]struct{}

	logger     *zap.Logger
	privateKey string
	// storePath enables the local event store (WithStorePath); store is the
	// open handle while started.
	storePath   string
	store       *localstore.Store
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
	r.authorSet = make(map[string]struct{})
	for _, key := range append([]string{authorPubkey}, r.extraAuthors...) {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, ok := r.authorSet[key]; ok {
			continue
		}
		r.authorSet[key] = struct{}{}
		r.authors = append(r.authors, key)
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
	for _, author := range r.authors {
		if _, err := nostrutil.PubKeyFromHex(author); err != nil {
			return fmt.Errorf("discovery resolver start: service pubkey %q must be valid hex: %w", author, err)
		}
	}

	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return nil
	}

	pool := r.poolFactory(r.relayURLs, r.logger, r.privateKey)
	if r.storePath != "" {
		if _, ok := pool.(*nostradapter.RelayPool); !ok {
			pool.Close()
			return errors.New("discovery resolver start: a local event store needs the default relay pool")
		}
		store, err := localstore.Open(r.storePath)
		if err != nil {
			pool.Close()
			return fmt.Errorf("discovery resolver start: open local event store: %w", err)
		}
		r.store = store
		r.restoreStored(store)
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.pool = pool
	r.cancel = cancel
	r.started = true
	r.wg.Add(1)
	go r.run(runCtx, pool, r.store)
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
	store := r.store
	r.started = false
	r.cancel = nil
	r.pool = nil
	r.store = nil
	r.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pool != nil {
		pool.Close()
	}
	r.wg.Wait()
	if store != nil {
		return store.Close()
	}
	return nil
}

// Ready is closed once stored endpoint state has been backfilled from the
// relays. In memory, that is when the first subscription has received EOSE
// from every relay. With a local event store, it is when every relay has
// caught up or failed its first attempt and at least one has caught up, so a
// relay that is down does not hold Ready back; lookups before Ready already
// see the endpoints restored from the store. Lookups before Ready may miss
// endpoints that exist on the relays.
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

func (r *Resolver) run(ctx context.Context, pool relayPool, store *localstore.Store) {
	defer r.wg.Done()
	r.prepareRelays(ctx, pool)
	if store != nil {
		r.syncStore(ctx, pool.(*nostradapter.RelayPool), store)
		return
	}

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

// subscribeUntilClosed runs one subscription until its event stream ends. The
// pool answers NIP-42 challenges and reissues a relay's REQ after an
// "auth-required:" or transient CLOSED or a dropped connection, so a CLOSED
// here needs no handling beyond the log.
func (r *Resolver) subscribeUntilClosed(ctx context.Context, pool relayPool) error {
	merged, err := pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{r.subscriptionFilter(), r.deletionFilter()})
	if err != nil {
		return err
	}
	return r.consume(ctx, merged)
}

func (r *Resolver) consume(ctx context.Context, merged *nostradapter.MergedSubscription) error {
	if merged == nil {
		return nil
	}
	defer merged.Close()
	// Backfill-then-live: until EOSE, stored events may arrive in any order,
	// so the resume cursor only moves once the whole backfill has been seen.
	caughtUp := false
	var newest nostr.Timestamp
	for merged.Events != nil || merged.EndOfStoredEvents != nil || merged.RelayEOSE != nil || merged.Closed != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
				r.logger.Warn("relay closed subscription", zap.String("relay", closed.RelayURL), zap.String("subscription_id", closed.SubscriptionID),
					zap.String("reason", closed.Reason), zap.Bool("terminal", closed.Terminal))
			} else {
				merged.Closed = nil
			}
		case ev, ok := <-merged.Events:
			if !ok {
				// The event stream is the subscription: once it closes
				// nothing more can arrive, whether or not EOSE did. Waiting
				// on the other channels here could block forever when EOSE
				// is still pending, so end the subscription and let run
				// reconnect (a backfill cut short is redone in full, since
				// the cursor only moves after EOSE).
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("subscription event stream closed")
			}
			if !r.applyLogged(ev) {
				continue
			}
			if caughtUp {
				r.markSynced(ev.CreatedAt)
			} else if ev.CreatedAt > newest {
				newest = ev.CreatedAt
			}
		}
	}
	return errors.New("subscription event stream closed")
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

// restoreStored applies the endpoint events already in the local store: the
// latest version per coordinate and key, tombstones included.
func (r *Resolver) restoreStored(store *localstore.Store) {
	restored := 0
	for ev := range store.QueryEvents(r.endpointFilter()) {
		if r.applyLogged(&ev) {
			restored++
		}
	}
	r.logger.Info("restored discovery endpoint events from the local event store", zap.Int("events", restored))
}

// syncStore keeps the local store and the cache in sync with every relay until
// ctx ends (see WithStorePath).
func (r *Resolver) syncStore(ctx context.Context, pool *nostradapter.RelayPool, store *localstore.Store) {
	syncer := &nostradapter.ProcessSync{
		Pool:   pool,
		Store:  store,
		Logger: r.logger,
		Apply:  func(_ context.Context, ev *nostr.Event) { r.applyLogged(ev) },
		RelayCaughtUp: func(relayURL string) {
			r.logger.Info("relay caught up with stored endpoint state", zap.String("relay", relayURL))
		},
		CaughtUp: func() {
			r.logger.Info("historical endpoint catch-up complete")
			r.markSynced(0)
		},
	}
	if err := syncer.Run(ctx, []nostr.Filter{r.endpointFilter(), r.deletionFilter()}); err != nil && ctx.Err() == nil {
		r.logger.Error("discovery resolver sync stopped", zap.Error(err))
	}
}

// applyLogged applies one event and logs why it was ignored; it reports
// whether the event was applied.
func (r *Resolver) applyLogged(ev *nostr.Event) bool {
	err := r.applyEvent(ev)
	switch {
	case err == nil:
		return true
	case errors.Is(err, errNotDNSEndpoint):
		r.logger.Debug("skipped non-endpoint control state", zap.String("event_id", eventID(ev)), zap.Error(err))
	default:
		r.logger.Warn("ignored invalid discovery endpoint event", zap.String("event_id", eventID(ev)), zap.Error(err))
	}
	return false
}

// endpointFilter scopes REQs to canonical DNS endpoint state from the trusted
// Bahia service keys. #t is a single-letter tag, so NIP-01 relays index it;
// the envelope's domain/schema/legacy_kind tags are multi-letter and are
// checked locally.
func (r *Resolver) endpointFilter() nostr.Filter {
	return nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: r.authorPubKeys(),
		Tags:    nostr.TagMap{"t": []string{kinds.DNSEndpointTopic}},
	}
}

// deletionFilter subscribes to NIP-09 kind-5 deletion events that target
// DNS endpoint events (kind 30900). The #k tag scopes the deletion to
// endpoint state only; #t does not apply because kind-5 events carry e/a
// tags, not the target's topic tags. The local store's SaveEvent already
// handles the mechanics (indexing the deletion, removing targeted events),
// so receiving the event is sufficient.
func (r *Resolver) deletionFilter() nostr.Filter {
	return nostr.Filter{
		Kinds:   []nostr.Kind{nostr.KindDeletion},
		Authors: r.authorPubKeys(),
		Tags:    nostr.TagMap{"k": []string{strconv.Itoa(kinds.CASControlState)}},
	}
}

func (r *Resolver) authorPubKeys() []nostr.PubKey {
	authors := make([]nostr.PubKey, 0, len(r.authors))
	for _, author := range r.authors {
		if pubkey, err := nostrutil.PubKeyFromHex(author); err == nil {
			authors = append(authors, pubkey)
		}
	}
	return authors
}

// subscriptionFilter is the in-memory resolver's REQ: endpointFilter and,
// after a completed backfill, a since of the newest seen created_at minus
// resolverSinceOverlap instead of re-downloading everything.
func (r *Resolver) subscriptionFilter() nostr.Filter {
	filter := r.endpointFilter()
	r.mu.RLock()
	synced := r.syncedThrough
	r.mu.RUnlock()
	if since := int64(synced) - int64(resolverSinceOverlap/time.Second); synced > 0 && since > 0 {
		filter.Since = nostr.Timestamp(since)
	}
	return filter
}

// applyEvent folds one DNS endpoint record or NIP-09 kind-5 deletion into
// the cache. Per d coordinate (shared by every trusted service key, see
// WithServiceKeys) the newest created_at wins and equal created_at is broken
// by the lowest event id (NIP-01), so the result does not depend on arrival
// order. A tombstone or deletion is kept as a deleted record so older live
// events cannot resurrect it.
func (r *Resolver) applyEvent(event *nostr.Event) error {
	if err := r.validateEnvelope(event); err != nil {
		return err
	}
	// NIP-09 kind-5: remove every coordinate targeted by "a" tags.
	if event.Kind == nostr.KindDeletion {
		return r.applyDeletion(event)
	}
	coordinate := event.Tags.GetD()
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

// applyDeletion processes a NIP-09 kind-5 event: every "a" tag that names a
// kind-30900 coordinate whose current record was created before the deletion
// is marked deleted.
//
// Addressable re-sync note: since=cursor is NOT correct for addressable
// events (kind 30900), because the latest version's created_at is
// independent of when it was published to the relay. A replaceable event
// created at T1 may be published at T2 >> T1, and a subscription with
// since=T2 would never see it. NIP-77 negentropy reconciliation is the
// correct tool; full re-paging is the fallback. The store-backed path
// already uses ProcessSync with NIP-77; the in-memory path resubscribes
// with since=synced-overlap, which is safe because overlap is short and
// synced is based on created_at, not publication time.
func (r *Resolver) applyDeletion(event *nostr.Event) error {
	id := nostrutil.EventIDHex(event)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "a" {
			continue
		}
		coordinate := coordinateD(tag[1])
		if coordinate == "" {
			continue
		}
		current, ok := r.records[coordinate]
		if !ok {
			// Record we haven't seen — mark it deleted so a late arrival
			// from another relay doesn't resurrect it.
			r.records[coordinate] = endpointRecord{createdAt: event.CreatedAt, eventID: id, deleted: true}
			continue
		}
		if current.createdAt <= event.CreatedAt {
			r.records[coordinate] = endpointRecord{createdAt: event.CreatedAt, eventID: id, deleted: true}
		}
	}
	return nil
}

// coordinateD extracts the d-tag value from an "a" tag coordinate
// (kind:pubkey:d). Returns "" for coordinates that don't have one.
func coordinateD(coordinate string) string {
	parts := strings.SplitN(coordinate, ":", 3)
	if len(parts) < 3 || parts[2] == "" {
		return ""
	}
	return parts[2]
}

func (r *Resolver) trustsAuthor(pubkey string) bool {
	_, ok := r.authorSet[strings.ToLower(pubkey)]
	return ok
}

func supersedes(createdAt nostr.Timestamp, id string, current endpointRecord) bool {
	if createdAt != current.createdAt {
		return createdAt > current.createdAt
	}
	return id < current.eventID
}

// validateEnvelope accepts signed canonical DNS endpoint records and NIP-09
// kind-5 deletion events (targeting kind 30900) from the trusted Bahia service
// keys. Relays that ignore #t may return other 30900 state, and relays do not
// enforce authors on our behalf.
func (r *Resolver) validateEnvelope(event *nostr.Event) error {
	if event == nil {
		return errors.New("nil event")
	}
	if err := nostradapter.ValidateInboundEvent(event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return err
	}
	if pubkey := nostrutil.EventPubKeyHex(event); !r.trustsAuthor(pubkey) {
		return fmt.Errorf("unexpected author %s", pubkey)
	}
	if event.Kind == nostr.KindDeletion {
		return r.validateDeletion(event)
	}
	if int(event.Kind) != kinds.CASControlState {
		return fmt.Errorf("unexpected kind %d", event.Kind)
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

// validateDeletion checks that a kind-5 event targets endpoint state.
func (r *Resolver) validateDeletion(event *nostr.Event) error {
	// Accept only kind-5 events that declare they target kind 30900.
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "k" && tag[1] == strconv.Itoa(kinds.CASControlState) {
			return nil
		}
	}
	return fmt.Errorf("%w: kind-5 does not target kind %d", errNotDNSEndpoint, kinds.CASControlState)
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
