package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"go.uber.org/zap"
)

// preflightTrustEpoch resolves the durable local head before the signer is
// opened. The activated key supersedes the static signer pin, but the signer
// must still prove possession of that exact key during servicesigner.Open.
func preflightTrustEpoch(ctx context.Context, cfg *config.Config, admission *nostrout.Admission, logger *zap.Logger) (*trustepoch.Verifier, trustepoch.Resolution, *trustepoch.BoltPersistence, error) {
	if cfg.Nostr.ServiceID == "" {
		return nil, trustepoch.Resolution{}, nil, nil
	}
	legacy := cfg.Nostr.PublicKey
	state, err := trustepoch.OpenBolt(cfg.Nostr.LocalStore.Path + ".trust-epoch.bolt")
	if err != nil {
		return nil, trustepoch.Resolution{}, nil, err
	}
	events, err := localstore.Open(cfg.Nostr.LocalStore.Path)
	if err != nil {
		_ = state.Close()
		return nil, trustepoch.Resolution{}, nil, err
	}
	v := &trustepoch.Verifier{OperatorRoot: cfg.Nostr.OperatorRootPubkey, LegacyPubkey: legacy,
		Source: trustepoch.LocalSource{Events: events}, Persistence: state}
	if cfg.Nostr.TrustEpochInventoryPath != "" {
		v.Inventory = trustepoch.FileInventory{Path: cfg.Nostr.TrustEpochInventoryPath}
	}
	if relays := ControlPlaneRelayURLs(cfg.Nostr); len(relays) > 0 {
		if err := syncTrustEpochHead(ctx, relays, cfg.Nostr.ServiceID, cfg.Nostr.OperatorRootPubkey,
			admission, logger, v, events); err != nil {
			_ = events.Close()
			_ = state.Close()
			return nil, trustepoch.Resolution{}, nil, err
		}
	}
	resolved, resolveErr := v.ServiceIdentity(ctx, cfg.Nostr.ServiceID)
	closeErr := events.Close()
	if resolveErr != nil || closeErr != nil {
		_ = state.Close()
		if resolveErr != nil {
			return nil, trustepoch.Resolution{}, nil, resolveErr
		}
		return nil, trustepoch.Resolution{}, nil, closeErr
	}
	return v, resolved, state, nil
}

// EOSE completes this bounded bootstrap read but is never treated as proof
// that no epoch exists. A timeout, CLOSED or unavailable relay fails startup;
// a persisted accepted head remains authoritative against omissions.
func syncTrustEpochHead(ctx context.Context, relays []string, serviceID, root string,
	admission *nostrout.Admission, logger *zap.Logger, verifier *trustepoch.Verifier, store *localstore.Store) error {
	operator, err := nostr.PubKeyFromHex(root)
	if err != nil {
		return fmt.Errorf("parse operator root: %w", err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pool := nostrAdapter.NewRelayPool(relays, logger, nostrAdapter.WithOutboundAdmission(admission))
	pool.Connect(queryCtx)
	defer pool.Close()
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{operator}, Tags: nostr.TagMap{"d": []string{"trust-epoch:" + serviceID}}}
	sub, err := pool.SubscribeAllWithEOSE(queryCtx, []nostr.Filter{filter})
	if err != nil {
		return fmt.Errorf("subscribe operator trust epoch: %w", err)
	}
	defer sub.Close()
	var head *nostr.Event
	consider := func(event *nostr.Event) {
		if event != nil && (head == nil || event.CreatedAt > head.CreatedAt ||
			(event.CreatedAt == head.CreatedAt && event.ID.Hex() < head.ID.Hex())) {
			copy := *event
			head = &copy
		}
	}
	for {
		select {
		case event, ok := <-sub.Events:
			if !ok {
				return fmt.Errorf("trust epoch stream closed before EOSE")
			}
			consider(event)
		case <-sub.EndOfStoredEvents:
			draining := true
			for draining {
				select {
				case event, ok := <-sub.Events:
					if ok {
						consider(event)
					} else {
						draining = false
					}
				default:
					draining = false
				}
			}
			if head != nil {
				if _, err := verifier.AcceptHead(queryCtx, serviceID, *head); err != nil {
					return err
				}
				if _, err := store.SaveEvent(*head); err != nil {
					return err
				}
			}
			return nil
		case <-queryCtx.Done():
			return fmt.Errorf("trust epoch head query incomplete: %w", queryCtx.Err())
		}
	}
}

type trustEpochAdmission struct {
	mu                  sync.RWMutex
	verifier            *trustepoch.Verifier
	resolution          trustepoch.Resolution
	serviceSignerPubkey string
	logger              *zap.Logger
	hostile             atomic.Uint64
	poisoned            bool
}

func (a *trustEpochAdmission) admit(ev nostr.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ev.PubKey.Hex() == a.verifier.OperatorRoot && ev.Kind == nostr.Kind(kinds.CASControlState) &&
		ev.Tags.GetD() == "trust-epoch:"+a.resolution.ServiceID {
		resolved, err := a.verifier.AcceptHead(context.Background(), a.resolution.ServiceID, ev)
		if err != nil {
			a.poisoned = true
			a.logger.Error("unverifiable trust epoch head", zap.Error(err))
			return err
		}
		a.resolution = resolved
		if resolved.Pubkey != a.serviceSignerPubkey {
			a.logger.Error("activated trust epoch differs from running signer; service ingestion is fenced",
				zap.Uint64("epoch", resolved.Epoch))
		}
		return nil
	}
	if a.poisoned {
		count := a.hostile.Add(1)
		a.logger.Warn("event rejected after unverifiable trust epoch head",
			zap.String("event_id", ev.ID.Hex()), zap.Uint64("count", count))
		return fmt.Errorf("event rejected after unverifiable trust epoch head")
	}
	old := a.verifier.LegacyPubkey
	if a.resolution.Record != nil {
		old = a.resolution.Record.OldPubkey
	}
	current := a.resolution.Pubkey
	if ev.PubKey.Hex() != old && ev.PubKey.Hex() != current {
		return nil
	}
	if a.resolution.JudgeEvent(ev) == trustepoch.Hostile {
		count := a.hostile.Add(1)
		a.logger.Warn("hostile service event rejected before local persistence",
			zap.String("event_id", ev.ID.Hex()), zap.Uint64("count", count))
		return fmt.Errorf("hostile service event rejected at trust epoch admission")
	}
	return nil
}

func (a *trustEpochAdmission) allowsHistoricalReplay(ev nostr.Event) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return !a.poisoned && a.resolution.Record != nil && a.resolution.Record.Activated &&
		ev.PubKey.Hex() == a.resolution.Record.OldPubkey &&
		a.resolution.JudgeEvent(ev) == trustepoch.PreEpochAccepted
}

func enqueueDaemonEpochAck(ctx context.Context, resolved trustepoch.Resolution, signer nostr.Keyer,
	store *localstore.Store, publisher *nostrAdapter.Publisher) error {
	if resolved.Record == nil || !resolved.Record.Activated {
		return nil
	}
	if signer == nil || publisher == nil {
		return fmt.Errorf("activated trust epoch requires daemon signer and publisher")
	}
	pubkey, err := nostr.PubKeyFromHex(resolved.Pubkey)
	if err != nil {
		return err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{pubkey},
		Tags:    nostr.TagMap{"d": []string{"trust-epoch-ack:" + resolved.ServiceID + ":daemon"}}}
	for ev := range store.QueryEvents(filter) {
		if trustepoch.VerifyAcknowledgment(ev, resolved, "daemon") == nil {
			return nil
		}
	}
	ack, err := trustepoch.NewAcknowledgment(resolved, "daemon", nostr.Now())
	if err != nil {
		return err
	}
	if err := signer.SignEvent(ctx, &ack); err != nil {
		return fmt.Errorf("sign daemon trust epoch ack: %w", err)
	}
	if err := trustepoch.VerifyAcknowledgment(ack, resolved, "daemon"); err != nil {
		return err
	}
	return publisher.Enqueue(ctx, ack, "trust-epoch-ack", nil)
}
