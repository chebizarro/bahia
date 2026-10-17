package main

import (
	"context"
	"fmt"
	"sync"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"go.uber.org/zap"
)

type dnsTrustEpoch struct {
	mu         sync.RWMutex
	verifier   *trustepoch.Verifier
	state      *trustepoch.BoltPersistence
	resolution trustepoch.Resolution
	healthy    bool
	updates    chan struct{}
	sub        *nostradapter.MergedSubscription
	cancel     context.CancelFunc
	done       chan struct{}
	started    bool
	ackSigner  nostr.SecretKey
	store      *localstore.Store
	ackOutput  interface {
		Publish(context.Context, nostr.Event) (int, error)
	}
}

func startDNSTrustEpoch(ctx context.Context, cfg config, pool *nostradapter.RelayPool,
	store *localstore.Store, ackPrivateKey string, logger *zap.Logger) (*dnsTrustEpoch, error) {
	ackSigner, err := nostr.SecretKeyFromHex(ackPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid DNS acknowledgment identity")
	}
	if ackSigner.Public().Hex() == cfg.AuthorizedPubkey || ackSigner.Public().Hex() == cfg.OperatorRootPubkey {
		return nil, fmt.Errorf("DNS acknowledgment identity must be independent")
	}
	state, err := trustepoch.OpenBolt(cfg.StorePath + ".trust-epoch.bolt")
	if err != nil {
		return nil, err
	}
	v := &trustepoch.Verifier{OperatorRoot: cfg.OperatorRootPubkey,
		LegacyPubkey: cfg.AuthorizedPubkey, Source: trustepoch.LocalSource{Events: store}, Persistence: state}
	if cfg.TrustEpochInventoryPath != "" {
		v.Inventory = trustepoch.FileInventory{Path: cfg.TrustEpochInventoryPath}
	}
	root, err := nostr.PubKeyFromHex(cfg.OperatorRootPubkey)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{root}, Tags: nostr.TagMap{"d": {"trust-epoch:" + cfg.ServiceID}}}
	subscriber := &nostradapter.StoreBackedSubscriber{Pool: pool, Store: store, Logger: logger,
		ReconcileKinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}}
	sub, err := subscriber.SubscribeAllWithEOSE(runCtx, []nostr.Filter{filter})
	if err != nil {
		cancel()
		_ = state.Close()
		return nil, err
	}
	guard := &dnsTrustEpoch{verifier: v, state: state, sub: sub, cancel: cancel,
		ackSigner: ackSigner, ackOutput: pool, store: store,
		updates: make(chan struct{}, 1), done: make(chan struct{})}
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				guard.Close()
				return nil, fmt.Errorf("DNS trust epoch stream closed before EOSE")
			}
			if ev == nil {
				continue
			}
			if _, err := v.AcceptHead(runCtx, cfg.ServiceID, *ev); err != nil {
				guard.Close()
				return nil, err
			}
		case <-sub.EndOfStoredEvents:
			if err := sub.StoredEventsIncomplete(nil); err != nil {
				guard.Close()
				return nil, fmt.Errorf("DNS trust epoch history incomplete: %w", err)
			}
			resolved, err := v.ServiceIdentity(runCtx, cfg.ServiceID)
			if err != nil {
				guard.Close()
				return nil, err
			}
			guard.resolution, guard.healthy = resolved, true
			if err := guard.publishAck(runCtx, resolved); err != nil {
				guard.Close()
				return nil, err
			}
			guard.started = true
			go guard.watch(runCtx, cfg.ServiceID)
			return guard, nil
		case <-runCtx.Done():
			guard.Close()
			return nil, runCtx.Err()
		}
	}
}

func (g *dnsTrustEpoch) watch(ctx context.Context, serviceID string) {
	defer close(g.done)
	for {
		select {
		case ev, ok := <-g.sub.Events:
			if !ok {
				g.poison()
				return
			}
			if ev == nil {
				continue
			}
			resolved, err := g.verifier.AcceptHead(ctx, serviceID, *ev)
			if err != nil {
				g.poison()
				return
			}
			if err := g.publishAck(ctx, resolved); err != nil {
				g.poison()
				return
			}
			g.mu.Lock()
			g.resolution = resolved
			g.mu.Unlock()
			select {
			case g.updates <- struct{}{}:
			default:
			}
		case <-ctx.Done():
			return
		}
	}
}

func (g *dnsTrustEpoch) publishAck(ctx context.Context, resolved trustepoch.Resolution) error {
	if resolved.Record == nil || !resolved.Record.Activated {
		return nil
	}
	if g.ackOutput == nil || g.store == nil {
		return fmt.Errorf("DNS acknowledgment publisher unavailable")
	}
	return (trustepoch.AckPublisher{ConsumerID: "dns", Authority: g.ackSigner.Public().Hex(),
		OperatorRoot: g.verifier.OperatorRoot,
		Existing: func(_ context.Context, filter nostr.Filter) ([]nostr.Event, error) {
			var events []nostr.Event
			for ev := range g.store.QueryEvents(filter) {
				events = append(events, ev)
			}
			return events, nil
		}, Sign: func(_ context.Context, ev *nostr.Event) error { return ev.Sign(g.ackSigner) },
		Publish: func(ctx context.Context, ev nostr.Event) error {
			accepted, err := g.ackOutput.Publish(ctx, ev)
			if err != nil {
				return fmt.Errorf("publish DNS trust acknowledgment: %w", err)
			}
			if accepted == 0 {
				return fmt.Errorf("publish DNS trust acknowledgment: no relay accepted")
			}
			if _, err := g.store.SaveEvent(ev); err != nil {
				return fmt.Errorf("persist DNS trust acknowledgment: %w", err)
			}
			return nil
		}}).Acknowledge(ctx, resolved)
}

func (g *dnsTrustEpoch) poison() {
	g.mu.Lock()
	g.healthy = false
	g.mu.Unlock()
	select {
	case g.updates <- struct{}{}:
	default:
	}
}

func (g *dnsTrustEpoch) Judge(ctx context.Context, ev nostr.Event) error {
	g.mu.RLock()
	healthy := g.healthy
	serviceID := g.resolution.ServiceID
	g.mu.RUnlock()
	if !healthy {
		return fmt.Errorf("DNS trust epoch unavailable")
	}
	resolved, err := g.verifier.ServiceIdentity(ctx, serviceID)
	if err != nil {
		g.poison()
		return err
	}
	if resolved.JudgeEvent(ev) == trustepoch.Hostile {
		return fmt.Errorf("DNS service event rejected by trust epoch")
	}
	return nil
}

func (g *dnsTrustEpoch) CurrentAuthors() ([]nostr.PubKey, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.healthy {
		return nil, fmt.Errorf("DNS trust epoch unavailable")
	}
	seen := map[string]bool{}
	var authors []nostr.PubKey
	keys := []string{g.verifier.LegacyPubkey, g.resolution.Pubkey}
	if g.resolution.Record != nil {
		keys = append(keys, g.resolution.Record.OldPubkey)
	}
	for _, raw := range keys {
		if seen[raw] {
			continue
		}
		key, err := nostr.PubKeyFromHex(raw)
		if err != nil {
			return nil, err
		}
		authors = append(authors, key)
		seen[raw] = true
	}
	return authors, nil
}

func (g *dnsTrustEpoch) Updates() <-chan struct{} { return g.updates }

func (g *dnsTrustEpoch) Close() {
	g.cancel()
	g.sub.Close()
	if g.started {
		<-g.done
	}
	_ = g.state.Close()
}
