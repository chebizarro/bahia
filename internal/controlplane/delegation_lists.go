package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const delegationCursorOverlap = 10 * time.Minute

// delegationListPool is the relay-pool subscription seam. The pool handles
// reconnects and NIP-42 AUTH; this consumer owns its long-lived REQ.
type delegationListPool interface {
	URLs() []string
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error)
}

type delegationHead struct {
	created nostr.Timestamp
	id      nostr.ID
	grants  map[string]struct{}
}

// DelegationLists projects operator-signed NIP-51 lists into delegation
// authority. Static operator configuration is only the list-author trust root;
// it grants no right to claim a human on its own.
type DelegationLists struct {
	pool    delegationListPool
	store   *localstore.Store
	logger  *zap.Logger
	authors []nostr.PubKey
	agents  []nostr.PubKey
	cursor  string

	mu         sync.RWMutex
	heads      map[string]delegationHead
	caughtUp   bool
	caughtUpCh chan struct{}
}

func NewDelegationLists(pool delegationListPool, store *localstore.Store, trustedAuthors, agentPubkeys []string, logger *zap.Logger) *DelegationLists {
	if logger == nil {
		logger = zap.NewNop()
	}
	l := &DelegationLists{pool: pool, store: store, logger: logger.Named("delegation-lists"), heads: make(map[string]delegationHead), caughtUpCh: make(chan struct{})}
	for _, value := range trustedAuthors {
		if key, err := nostr.PubKeyFromHex(strings.TrimSpace(value)); err == nil && key != nostr.ZeroPK && !slices.Contains(l.authors, key) {
			l.authors = append(l.authors, key)
		}
	}
	for _, value := range agentPubkeys {
		if key, err := nostr.PubKeyFromHex(strings.TrimSpace(value)); err == nil && key != nostr.ZeroPK && !slices.Contains(l.agents, key) {
			l.agents = append(l.agents, key)
		}
	}
	slices.SortFunc(l.authors, func(a, b nostr.PubKey) int { return strings.Compare(a.Hex(), b.Hex()) })
	slices.SortFunc(l.agents, func(a, b nostr.PubKey) int { return strings.Compare(a.Hex(), b.Hex()) })
	var scope strings.Builder
	for _, key := range l.authors {
		scope.WriteString("author:")
		scope.WriteString(key.Hex())
		scope.WriteByte('\n')
	}
	for _, key := range l.agents {
		scope.WriteString("agent:")
		scope.WriteString(key.Hex())
		scope.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(scope.String()))
	l.cursor = "delegation-list:v1:" + hex.EncodeToString(sum[:])
	return l
}

func (l *DelegationLists) Name() string { return "delegation-lists" }

func (l *DelegationLists) filter() nostr.Filter {
	coordinates := make([]string, 0, len(l.agents))
	for _, agent := range l.agents {
		coordinates = append(coordinates, delegationDTag(agent.Hex()))
	}
	return nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.ConfigACLList)}, Authors: slices.Clone(l.authors), Tags: nostr.TagMap{"d": coordinates}}
}

func delegationDTag(agent string) string { return "agent:" + agent + ":delegation" }

func (l *DelegationLists) Load() error {
	if l == nil || l.store == nil {
		return errors.New("delegation lists require a local event store")
	}
	if len(l.authors) == 0 || len(l.agents) == 0 {
		return nil
	}
	for ev := range l.store.QueryEvents(l.filter()) {
		if err := l.accept(&ev, false); err != nil {
			l.logger.Warn("dropping invalid cached delegation list", zap.String("event_id", ev.ID.Hex()), zap.Error(err))
		}
	}
	return nil
}

// Apply validates a relay event before it can change a grant. The local store
// resolves NIP-01 heads per (kind, author, d), including lower-ID tie breaks.
func (l *DelegationLists) Apply(ev *nostr.Event) error { return l.accept(ev, true) }

func (l *DelegationLists) accept(ev *nostr.Event, persist bool) error {
	if l == nil || ev == nil || l.store == nil || ev.Kind != nostr.Kind(kinds.ConfigACLList) || !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("delegation list event has invalid kind, id, or signature")
	}
	if err := nostradapter.ValidateInboundEvent(ev, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return fmt.Errorf("invalid delegation list event: %w", err)
	}
	if !slices.Contains(l.authors, ev.PubKey) {
		return errors.New("delegation list author is not trusted")
	}
	var coordinate string
	grants := make(map[string]struct{})
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "d":
			if coordinate != "" || !slices.ContainsFunc(l.agents, func(agent nostr.PubKey) bool { return tag[1] == delegationDTag(agent.Hex()) }) {
				return errors.New("delegation list coordinate is invalid or repeated")
			}
			coordinate = tag[1]
		case "p":
			key, err := nostr.PubKeyFromHex(tag[1])
			if err != nil || key == nostr.ZeroPK || tag[1] != key.Hex() {
				return errors.New("delegation list p tag must be a lowercase 64-character pubkey")
			}
			grants[key.Hex()] = struct{}{}
		}
	}
	if coordinate == "" {
		return errors.New("delegation list d tag is required")
	}
	if persist {
		stored, err := l.store.SaveEvent(*ev)
		if err != nil {
			return fmt.Errorf("persist delegation list: %w", err)
		}
		if !stored {
			return nil
		}
	}
	key := ev.PubKey.Hex() + ":" + coordinate
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.heads[key]; ok && (current.created > ev.CreatedAt || current.created == ev.CreatedAt && strings.Compare(current.id.Hex(), ev.ID.Hex()) <= 0) {
		return nil
	}
	l.heads[key] = delegationHead{created: ev.CreatedAt, id: ev.ID, grants: grants}
	return nil
}

// Allows is fail-closed until a complete subscription catch-up. A grant by
// any trusted list author is sufficient; each author's coordinate is its own
// NIP-01 latest-wins list, and an empty p-tag set revokes that author's grants.
func (l *DelegationLists) Allows(agent, claimedHuman string) bool {
	if l == nil {
		return false
	}
	agentKey, err := nostr.PubKeyFromHex(agent)
	if err != nil || agentKey == nostr.ZeroPK || agent != agentKey.Hex() {
		return false
	}
	humanKey, err := nostr.PubKeyFromHex(claimedHuman)
	if err != nil || humanKey == nostr.ZeroPK || claimedHuman != humanKey.Hex() {
		return false
	}
	coordinate := delegationDTag(agent)
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.caughtUp {
		return false
	}
	for key, head := range l.heads {
		if strings.HasSuffix(key, ":"+coordinate) {
			if _, ok := head.grants[claimedHuman]; ok {
				return true
			}
		}
	}
	return false
}

func (l *DelegationLists) CaughtUp() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.caughtUp
}

func (l *DelegationLists) resumeFilter() nostr.Filter {
	filter := l.filter()
	if l.pool == nil || l.store == nil {
		return filter
	}
	var since nostr.Timestamp
	for _, relay := range l.pool.URLs() {
		cursor, err := l.store.Cursor(relay, l.cursor)
		if err != nil {
			l.logger.Warn("read delegation cursor failed", zap.String("relay", relay), zap.Error(err))
			return filter
		}
		if cursor == 0 {
			return filter
		}
		if since == 0 || cursor < since {
			since = cursor
		}
	}
	if since > 0 {
		overlap := nostr.Timestamp(delegationCursorOverlap / time.Second)
		if since > overlap {
			filter.Since = since - overlap
		}
	}
	return filter
}

func (l *DelegationLists) Run(ctx context.Context) error {
	if err := l.Load(); err != nil {
		return err
	}
	if l.pool == nil || len(l.authors) == 0 || len(l.agents) == 0 {
		return nil
	}
	backoff := nostradapter.DefaultBackoff()
	for {
		err := l.subscribe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		l.logger.Warn("delegation subscription ended; claims unavailable until catch-up", zap.Error(err))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff.Next()):
		}
	}
}

func (l *DelegationLists) subscribe(ctx context.Context) error {
	l.mu.Lock()
	l.caughtUp = false
	l.caughtUpCh = make(chan struct{})
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.caughtUp = false
		l.mu.Unlock()
	}()
	merged, err := l.pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{l.resumeFilter()})
	if err != nil {
		return err
	}
	defer merged.Close()
	newest := make(map[string]nostr.Timestamp)
	eose := make(map[string]bool)
	allEOSESeen := false
	expectedRelays := l.pool.URLs()
	markCaughtUp := func() {
		if !allEOSESeen || !merged.AllRelaysReachedEOSE() || len(expectedRelays) == 0 {
			return
		}
		for _, relay := range expectedRelays {
			if !eose[relay] {
				return
			}
		}
		l.mu.Lock()
		if !l.caughtUp {
			l.caughtUp = true
			close(l.caughtUpCh)
		}
		l.mu.Unlock()
	}
	eventsCh, eoseCh, closedCh, doneCh := merged.Events, merged.RelayEOSE, merged.Closed, merged.EndOfStoredEvents
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-eventsCh:
			if !ok {
				return errors.New("delegation event subscription closed")
			}
			if err := l.Apply(ev); err != nil {
				l.logger.Warn("dropping delegation list", zap.Error(err))
				continue
			}
			relay := merged.EventSource(ev.ID.Hex())
			if relay != "" && ev.CreatedAt > newest[relay] {
				newest[relay] = ev.CreatedAt
				if eose[relay] {
					if err := l.store.AdvanceCursor(relay, l.cursor, ev.CreatedAt); err != nil {
						return err
					}
				}
			}
		case info, ok := <-eoseCh:
			if !ok {
				eoseCh = nil
				if !l.CaughtUp() {
					return errors.New("delegation list EOSE channel closed before catch-up")
				}
				continue
			}
			eose[info.RelayURL] = true
			if err := l.store.AdvanceCursor(info.RelayURL, l.cursor, newest[info.RelayURL]); err != nil {
				return err
			}
			markCaughtUp()
		case info, ok := <-closedCh:
			if !ok {
				closedCh = nil
				continue
			}
			l.mu.Lock()
			if l.caughtUp {
				l.caughtUp = false
				l.caughtUpCh = make(chan struct{})
			}
			l.mu.Unlock()
			eose[info.RelayURL] = false
			l.logger.Warn("relay closed delegation list REQ", zap.String("relay", info.RelayURL), zap.String("reason", info.Reason))
			if info.Terminal {
				return fmt.Errorf("relay %s refused delegation list REQ: %s", info.RelayURL, info.Reason)
			}
		case <-doneCh:
			doneCh = nil
			allEOSESeen = true
			markCaughtUp()
		}
	}
}
