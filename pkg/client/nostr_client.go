// Package client provides Bahia API clients. NostrClient reads fleet state
// from relays via a per-process local event store (Phase 5 N1).
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"

	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// DefaultEOSETimeout is the default time to wait for EOSE from at least one
// relay before falling back to the stale local store.
const DefaultEOSETimeout = 5 * time.Second

// NostrClientConfig holds the configuration for a NostrClient.
type NostrClientConfig struct {
	// StorePath is the path to the bbolt local event store.
	// Default: $XDG_DATA_HOME/bahia/store/<service-pubkey-prefix>/events.db
	StorePath string

	// ServicePubkey is the hex-encoded pubkey of the Bahia service whose
	// events the client subscribes to.
	ServicePubkey string

	// Pool is the relay pool to subscribe through. The caller owns its
	// lifecycle; NostrClient does not close it.
	Pool SubscriptionPool

	// EOSETimeout bounds the wait for EOSE from at least one relay.
	// Zero means DefaultEOSETimeout.
	EOSETimeout time.Duration
}

// Subscription is a relay subscription that delivers events and signals EOSE.
type Subscription interface {
	// Events returns the channel that delivers relay events.
	Events() <-chan *nostr.Event
	// EndOfStoredEvents returns a channel closed when all relays have sent EOSE.
	EndOfStoredEvents() <-chan struct{}
	// RelayEOSE returns a channel that emits once for each relay that sends EOSE.
	RelayEOSE() <-chan RelayEOSEInfo
	// Close cancels the subscription.
	Close()
}

// RelayEOSEInfo identifies a relay that reached end-of-stored-events.
type RelayEOSEInfo struct {
	RelayURL string
}

// SubscriptionPool is the relay subscription surface the NostrClient needs.
// Wrap internal/adapters/nostr.RelayPool with WrapRelayPool to satisfy it.
type SubscriptionPool interface {
	SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (Subscription, error)
}

// relayPoolAdapter wraps an internal/adapters/nostr.RelayPool to satisfy
// SubscriptionPool.
type relayPoolAdapter struct {
	pool *nostrpool.RelayPool
}

// WrapRelayPool wraps a RelayPool to satisfy the SubscriptionPool interface.
func WrapRelayPool(pool *nostrpool.RelayPool) SubscriptionPool {
	return &relayPoolAdapter{pool: pool}
}

func (a *relayPoolAdapter) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (Subscription, error) {
	sub, err := a.pool.SubscribeAllWithEOSE(ctx, filters)
	if err != nil {
		return nil, err
	}
	return &mergedSubscriptionAdapter{sub: sub}, nil
}

type mergedSubscriptionAdapter struct {
	sub *nostrpool.MergedSubscription
}

func (a *mergedSubscriptionAdapter) Events() <-chan *nostr.Event { return a.sub.Events }
func (a *mergedSubscriptionAdapter) EndOfStoredEvents() <-chan struct{} {
	return a.sub.EndOfStoredEvents
}
func (a *mergedSubscriptionAdapter) RelayEOSE() <-chan RelayEOSEInfo {
	// Bridge the internal type to the public one.
	ch := make(chan RelayEOSEInfo, 1)
	go func() {
		for eose := range a.sub.RelayEOSE {
			ch <- RelayEOSEInfo{RelayURL: eose.RelayURL}
		}
		close(ch)
	}()
	return ch
}
func (a *mergedSubscriptionAdapter) Close() { a.sub.Close() }

// SyncResult describes the freshness of a synced query.
type SyncResult struct {
	// Fresh is true when at least one relay reached EOSE before the timeout.
	Fresh bool
	// StaleSince is non-zero when the query was served from a stale local
	// store because no relay reached EOSE within the timeout.
	StaleSince time.Time
}

// NostrClient reads Bahia state from relays via a local eventstore.
type NostrClient struct {
	store       *localstore.Store
	pool        SubscriptionPool
	servicePub  nostr.PubKey
	eoseTimeout time.Duration

	// domainTopics maps design-level domain names (e.g. "service") to the
	// list of single-letter "t" topic values the projector stamps on those
	// records. Built once from CPStateFamilyTopics.
	domainTopics map[string][]string
}

// NewNostrClient creates a NostrClient backed by the given store and pool.
func NewNostrClient(cfg NostrClientConfig) (*NostrClient, error) {
	if cfg.StorePath == "" {
		return nil, fmt.Errorf("NostrClientConfig.StorePath is required")
	}
	if cfg.ServicePubkey == "" {
		return nil, fmt.Errorf("NostrClientConfig.ServicePubkey is required")
	}
	if cfg.Pool == nil {
		return nil, fmt.Errorf("NostrClientConfig.Pool is required")
	}
	pubkey, err := nostr.PubKeyFromHex(cfg.ServicePubkey)
	if err != nil {
		return nil, fmt.Errorf("invalid service pubkey: %w", err)
	}
	store, err := localstore.Open(cfg.StorePath)
	if err != nil {
		return nil, fmt.Errorf("open local event store: %w", err)
	}
	eoseTimeout := cfg.EOSETimeout
	if eoseTimeout == 0 {
		eoseTimeout = DefaultEOSETimeout
	}
	return &NostrClient{
		store:        store,
		pool:         cfg.Pool,
		servicePub:   pubkey,
		eoseTimeout:  eoseTimeout,
		domainTopics: buildDomainTopics(),
	}, nil
}

// Close releases the local store. The relay pool is not closed (caller-owned).
func (c *NostrClient) Close() error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.Close()
}

// Sync subscribes to the given domain's events from relays, stores them
// locally with cursor advancement, and waits for EOSE or the configured
// timeout. It returns a SyncResult indicating freshness.
//
// Freshness semantics: EOSE from ≥1 relay means the local store is caught up
// for this domain. On timeout, the caller should query the stale store and
// surface the staleness warning — this is a freshness policy, not a failure.
func (c *NostrClient) Sync(ctx context.Context, domain string) (*SyncResult, error) {
	topics, ok := c.domainTopics[domain]
	if !ok {
		return nil, fmt.Errorf("unknown cp-state domain %q", domain)
	}
	return c.syncTopics(ctx, domain, topics)
}

func (c *NostrClient) syncTopics(ctx context.Context, domain string, topics []string) (*SyncResult, error) {
	filter := c.buildFilter(topics)

	// Load per-(relay, filter) cursor.
	filterHash := hashFilter(filter)
	cursor, err := c.store.Cursor("_pool", filterHash)
	if err != nil {
		return nil, fmt.Errorf("read cursor for domain %q: %w", domain, err)
	}
	if cursor > 0 {
		filter.Since = cursor
	}

	sub, err := c.pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// No relays available — serve stale.
		return &SyncResult{Fresh: false, StaleSince: time.Now()}, nil
	}
	defer sub.Close()
	relayEOSE := sub.RelayEOSE()

	timeoutCtx, cancel := context.WithTimeout(ctx, c.eoseTimeout)
	defer cancel()

	var maxCreatedAt nostr.Timestamp
	gotEOSE := false

	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				goto done
			}
			if ev == nil {
				continue
			}
			if !c.validStateEvent(*ev, topics) {
				continue
			}
			if _, saveErr := c.store.SaveEvent(*ev); saveErr != nil {
				return nil, fmt.Errorf("store %s event %s: %w", domain, ev.GetID(), saveErr)
			}
			if ev.CreatedAt > maxCreatedAt {
				maxCreatedAt = ev.CreatedAt
			}
		case <-sub.EndOfStoredEvents():
			gotEOSE = true
			goto done
		case info, ok := <-relayEOSE:
			if !ok {
				relayEOSE = nil
				continue
			}
			if info.RelayURL != "" {
				// At least one relay sent EOSE — we are fresh.
				gotEOSE = true
				goto done
			}
		case <-timeoutCtx.Done():
			goto done
		}
	}

done:
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Drain remaining events after EOSE/timeout (non-blocking).
	drainedMax, err := c.drainEventsSub(sub, topics)
	if err != nil {
		return nil, fmt.Errorf("drain %s events: %w", domain, err)
	}
	if drainedMax > maxCreatedAt {
		maxCreatedAt = drainedMax
	}

	// Advance cursor.
	if maxCreatedAt > 0 {
		if err := c.store.AdvanceCursor("_pool", filterHash, maxCreatedAt); err != nil {
			return nil, fmt.Errorf("advance cursor for domain %q: %w", domain, err)
		}
	}

	if gotEOSE {
		return &SyncResult{Fresh: true}, nil
	}
	return &SyncResult{Fresh: false, StaleSince: time.Now()}, nil
}

// QueryDomain queries the local store for all events in the given domain.
func (c *NostrClient) QueryDomain(domain string) ([]nostr.Event, error) {
	topics, ok := c.domainTopics[domain]
	if !ok {
		return nil, fmt.Errorf("unknown cp-state domain %q", domain)
	}
	return c.queryTopics(topics)
}

func (c *NostrClient) queryTopics(topics []string) ([]nostr.Event, error) {
	filter := c.buildFilter(topics)
	var events []nostr.Event
	for ev := range c.store.QueryEvents(filter) {
		if c.validStateEvent(ev, topics) {
			events = append(events, ev)
		}
	}
	return events, nil
}

// SyncAndQuery is the convenience one-shot for CLI reads: sync, then query,
// returning both the decoded events and a freshness indicator.
func (c *NostrClient) SyncAndQuery(ctx context.Context, domain string) ([]nostr.Event, *SyncResult, error) {
	result, err := c.Sync(ctx, domain)
	if err != nil {
		return nil, nil, err
	}
	events, err := c.QueryDomain(domain)
	if err != nil {
		return nil, nil, err
	}
	return events, result, nil
}

// SyncAndQueryFamily reads only one canonical state family. A service or
// environment read must not subscribe to unrelated families in its domain.
func (c *NostrClient) SyncAndQueryFamily(ctx context.Context, legacyKind int) ([]nostr.Event, *SyncResult, error) {
	family := lookupFamily(legacyKind)
	if family.Topic == "" {
		return nil, nil, fmt.Errorf("unknown cp-state family %d", legacyKind)
	}
	topics := []string{family.Topic}
	result, err := c.syncTopics(ctx, family.Domain, topics)
	if err != nil {
		return nil, nil, err
	}
	events, err := c.queryTopics(topics)
	if err != nil {
		return nil, nil, err
	}
	return events, result, nil
}

// buildFilter constructs a nostr.Filter for the given topics scoped to the
// service pubkey. Uses #t (single-letter) instead of #domain/#schema per the
// multi-letter-filter archtest ban.
func (c *NostrClient) buildFilter(topics []string) nostr.Filter {
	return nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{c.servicePub},
		Tags:    nostr.TagMap{"t": topics},
	}
}

// drainEventsSub reads and stores remaining events from a subscription
// non-blockingly until the event channel closes or drains.
func (c *NostrClient) drainEventsSub(sub Subscription, topics []string) (nostr.Timestamp, error) {
	var maxCreatedAt nostr.Timestamp
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				return maxCreatedAt, nil
			}
			if ev != nil && c.validStateEvent(*ev, topics) {
				if _, err := c.store.SaveEvent(*ev); err != nil {
					return 0, fmt.Errorf("store event %s: %w", ev.GetID(), err)
				}
				if ev.CreatedAt > maxCreatedAt {
					maxCreatedAt = ev.CreatedAt
				}
			}
		default:
			return maxCreatedAt, nil
		}
	}
}

func (c *NostrClient) validStateEvent(ev nostr.Event, topics []string) bool {
	if ev.Kind != nostr.Kind(kinds.CASControlState) || ev.PubKey != c.servicePub || !ev.CheckID() || !ev.VerifySignature() {
		return false
	}
	for _, topic := range topics {
		if tagValue(ev.Tags, "t") == topic {
			return true
		}
	}
	return false
}

// hashFilter produces a deterministic hash of a filter for cursor keying.
func hashFilter(f nostr.Filter) string {
	h := sha256.New()
	// Deterministic serialisation: kinds, authors, tags (sorted).
	for _, k := range f.Kinds {
		fmt.Fprintf(h, "k:%d;", k)
	}
	for _, a := range f.Authors {
		fmt.Fprintf(h, "a:%s;", a.Hex())
	}
	if f.Tags != nil {
		var keys []string
		for k := range f.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vals := make([]string, len(f.Tags[k]))
			copy(vals, f.Tags[k])
			sort.Strings(vals)
			fmt.Fprintf(h, "t:%s=%s;", k, strings.Join(vals, ","))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildDomainTopics builds the domain → topics map from the projector's
// exported cp-state family table. This is the single source of truth —
// no hand-rolled topic lists.
func buildDomainTopics() map[string][]string {
	families := nostrpool.CPStateFamilyTopics()
	m := map[string][]string{}
	for _, f := range families {
		if f.Domain == "service" && f.Entity == "state" {
			m["state"] = appendUnique(m["state"], f.Topic)
			continue
		}
		m[f.Domain] = appendUnique(m[f.Domain], f.Topic)
	}
	// Sort topics within each domain for deterministic filter hashing.
	for k := range m {
		sort.Strings(m[k])
	}
	return m
}

func appendUnique(s []string, v string) []string {
	for _, existing := range s {
		if existing == v {
			return s
		}
	}
	return append(s, v)
}

// Domains returns all known cp-state domain names.
func (c *NostrClient) Domains() []string {
	out := make([]string, 0, len(c.domainTopics))
	for k := range c.domainTopics {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
