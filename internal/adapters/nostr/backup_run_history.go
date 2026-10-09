package nostr

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const backupRunHistoryLimit = 1024

// BackupRunHistoryStore is the disposable local relay cache. Any matching
// pre-ledger coordinate vetoes a new admission; absence here is never proof.
type BackupRunHistoryStore interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

// BackupRunHistoryProof is an ephemeral, still-open relay observation. It is
// not a writer fence and cannot authorize a signed run by itself.
type BackupRunHistoryProof struct {
	ServicePubkey   nostr.PubKey
	Coordinate      string
	RelayURLs       []string
	TopologyEpoch   map[string]uint64
	ConnectionEpoch map[string]uint64
	ObservedAt      time.Time

	pool   *RelayPool
	store  BackupRunHistoryStore
	sub    *MergedSubscription
	cancel context.CancelFunc
	once   sync.Once
	closed atomic.Bool
}

func (proof *BackupRunHistoryProof) Close() {
	if proof == nil {
		return
	}
	proof.once.Do(func() {
		proof.closed.Store(true)
		if proof.sub != nil {
			proof.sub.Close()
		}
		if proof.cancel != nil {
			proof.cancel()
		}
	})
}

// StillCurrent rejects a proof if the relay set, any topology incarnation,
// any websocket connection, or the local cache changed before fenced stage.
func (proof *BackupRunHistoryProof) StillCurrent() error {
	if proof == nil || proof.pool == nil || proof.sub == nil || proof.closed.Load() {
		return fmt.Errorf("backup run history proof is unavailable or closed")
	}
	if !slices.Equal(proof.RelayURLs, proof.pool.URLs()) {
		return fmt.Errorf("backup run history relay topology changed")
	}
	for _, url := range proof.RelayURLs {
		if proof.pool.RelayEpoch(url) != proof.TopologyEpoch[url] || proof.pool.backupHistoryConnectionEpoch(url) != proof.ConnectionEpoch[url] {
			return fmt.Errorf("backup run history relay epoch changed: %s", url)
		}
	}
	for {
		select {
		case event, open := <-proof.sub.Events:
			if !open {
				return fmt.Errorf("backup run history subscription ended")
			}
			if event != nil {
				return fmt.Errorf("backup run coordinate appeared on canonical relay")
			}
		default:
			goto drained
		}
	}
drained:
	if backupRunCoordinateInStore(proof.store, proof.ServicePubkey, proof.Coordinate) {
		return fmt.Errorf("backup run coordinate already exists in local relay cache")
	}
	if !proof.sub.AllRelaysReachedEOSE() || proof.sub.StoredEventsIncomplete(nil) != nil {
		return fmt.Errorf("backup run history proof lost complete relay EOSE")
	}
	return nil
}

func backupRunCoordinateFilter(service nostr.PubKey, coordinate string) nostr.Filter {
	return nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{service},
		Tags: nostr.TagMap{"d": {coordinate}}, Limit: backupRunHistoryLimit}
}

func backupRunCoordinateInStore(store BackupRunHistoryStore, service nostr.PubKey, coordinate string) bool {
	if store == nil {
		return false
	}
	for range store.QueryEvents(backupRunCoordinateFilter(service, coordinate)) {
		return true
	}
	return false
}

func (p *RelayPool) backupHistoryConnectionEpoch(url string) uint64 {
	p.mu.RLock()
	mr := p.relays[url]
	p.mu.RUnlock()
	if mr == nil {
		return 0
	}
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if !mr.connected || mr.relay == nil || !mr.relay.IsConnected() {
		return 0
	}
	return mr.connectionEpoch
}

func (p *RelayPool) recycleBackupHistoryRelay(url string, epoch uint64) {
	p.mu.RLock()
	mr := p.relays[url]
	p.mu.RUnlock()
	if mr == nil {
		return
	}
	mr.mu.Lock()
	if mr.connectionEpoch != epoch || mr.relay == nil {
		mr.mu.Unlock()
		return
	}
	relay := mr.relay
	mr.connected = false
	mr.mu.Unlock()
	_ = relay.Close()
	p.recordRelayConnectionState(url, false)
}

// InspectBackupRunHistory obtains an exact EOSE-bounded absence observation
// from every configured canonical relay. It must run off ProcessSync.Apply:
// NIP-11, websocket setup and a stalled EOSE all involve network waits.
func (p *RelayPool) InspectBackupRunHistory(ctx context.Context, store BackupRunHistoryStore, service nostr.PubKey, coordinate string) (_ *BackupRunHistoryProof, err error) {
	if p == nil || service == (nostr.PubKey{}) || len(coordinate) <= len("backup-run:") || coordinate[:len("backup-run:")] != "backup-run:" {
		return nil, fmt.Errorf("backup run history requires a pool, service key and run coordinate")
	}
	ctx, cancel := BoundStoredEventsWait(ctx, DefaultStoredEventsTimeout)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	urls := p.URLs()
	if len(urls) == 0 {
		return nil, fmt.Errorf("backup run history has no configured canonical relays")
	}
	topology := make(map[string]uint64, len(urls))
	for _, url := range urls {
		if topology[url] = p.RelayEpoch(url); topology[url] == 0 {
			return nil, fmt.Errorf("backup run history relay is not configured: %s", url)
		}
		if info, infoErr := p.FetchRelayInfo(ctx, url, true); infoErr != nil || info == nil {
			if infoErr != nil {
				return nil, fmt.Errorf("backup run history needs NIP-11 for %s: %w", url, infoErr)
			}
			return nil, fmt.Errorf("backup run history needs NIP-11 for %s", url)
		}
	}
	if backupRunCoordinateInStore(store, service, coordinate) {
		return nil, fmt.Errorf("backup run coordinate already exists in local relay cache")
	}
	sub, err := p.SubscribeWithOptions(ctx, []nostr.Filter{backupRunCoordinateFilter(service, coordinate)}, SubscribeOptions{Relays: urls, AwaitUnavailableRelays: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			sub.Close()
		}
	}()
	for {
		select {
		case event, open := <-sub.Events:
			if open && event != nil {
				return nil, fmt.Errorf("backup run coordinate already exists on canonical relay %s", sub.EventSource(event.ID.Hex()))
			}
			if !open {
				return nil, fmt.Errorf("backup run history subscription ended before a fenced stage")
			}
		case <-sub.EndOfStoredEvents:
			// The EOSE signal and buffered event channel are selected independently.
			// Drain every event sent before EOSE before treating an empty answer as
			// absence, rather than winning a random select over a queued conflict.
			for draining := true; draining; {
				select {
				case event, open := <-sub.Events:
					if !open {
						return nil, fmt.Errorf("backup run history subscription ended before a fenced stage")
					}
					if event != nil {
						return nil, fmt.Errorf("backup run coordinate already exists on canonical relay %s", sub.EventSource(event.ID.Hex()))
					}
				default:
					draining = false
				}
			}
			if !sub.AllRelaysReachedEOSE() || len(sub.StoredOutcomes()) != len(urls) || !slices.Equal(sub.RelayURLs(), urls) {
				return nil, fmt.Errorf("backup run history lacks all configured relay EOSE: %w", sub.StoredEventsIncomplete(nil))
			}
			if incomplete := sub.StoredEventsIncomplete(nil); incomplete != nil {
				return nil, incomplete
			}
			connections := make(map[string]uint64, len(urls))
			for _, url := range urls {
				connections[url] = p.backupHistoryConnectionEpoch(url)
			}
			proof := &BackupRunHistoryProof{ServicePubkey: service, Coordinate: coordinate, RelayURLs: urls,
				TopologyEpoch: topology, ConnectionEpoch: connections, ObservedAt: time.Now().UTC(),
				pool: p, store: store, sub: sub, cancel: cancel}
			if err := proof.StillCurrent(); err != nil {
				return nil, err
			}
			return proof, nil
		case <-ctx.Done():
			for _, outcome := range sub.StoredOutcomes() {
				if outcome.Status == RelayStoredPending {
					p.recycleBackupHistoryRelay(outcome.RelayURL, p.backupHistoryConnectionEpoch(outcome.RelayURL))
				}
			}
			return nil, sub.StoredEventsIncomplete(ctx.Err())
		}
	}
}
