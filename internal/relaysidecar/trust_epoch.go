package relaysidecar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"go.uber.org/zap"
)

type sidecarEpochSource struct{ store *eventStore }

func (s sidecarEpochSource) Head(ctx context.Context, serviceID, root string) (*nostr.Event, bool, error) {
	pubkey, err := nostr.PubKeyFromHex(root)
	if err != nil {
		return nil, false, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{pubkey}, Tags: nostr.TagMap{"d": []string{"trust-epoch:" + serviceID}}}
	var head *nostr.Event
	for event := range s.store.Query(ctx, filter, 0) {
		if head == nil || event.CreatedAt > head.CreatedAt ||
			(event.CreatedAt == head.CreatedAt && event.ID.Hex() < head.ID.Hex()) {
			copy := event
			head = &copy
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return head, head != nil, nil
}

type sidecarTrustEpoch struct {
	mu         sync.RWMutex
	verifier   *trustepoch.Verifier
	resolution trustepoch.Resolution
	state      *trustepoch.BoltPersistence
	poisoned   bool
	hostile    atomic.Uint64
	logger     *zap.Logger
}

func openSidecarTrustEpoch(ctx context.Context, cfg config.NostrConfig, store *eventStore, logger *zap.Logger) (*sidecarTrustEpoch, error) {
	if cfg.ServiceID == "" {
		return nil, nil
	}
	legacy := cfg.PublicKey
	state, err := trustepoch.OpenBolt(filepath.Join(cfg.Sidecar.DataDir, "trust-epoch.bolt"))
	if err != nil {
		return nil, err
	}
	v := &trustepoch.Verifier{OperatorRoot: cfg.OperatorRootPubkey, LegacyPubkey: legacy,
		Source: sidecarEpochSource{store: store}, Persistence: state}
	if cfg.TrustEpochInventoryPath != "" {
		v.Inventory = trustepoch.FileInventory{Path: cfg.TrustEpochInventoryPath}
	}
	resolved, err := v.ServiceIdentity(ctx, cfg.ServiceID)
	if err != nil {
		_ = state.Close()
		return nil, err
	}
	return &sidecarTrustEpoch{verifier: v, resolution: resolved, state: state, logger: logger}, nil
}

// PreflightTrustEpoch lets cmd/relay replace the static signer pin before
// opening the signer. New() independently repeats the verification.
func PreflightTrustEpoch(ctx context.Context, cfg config.NostrConfig, logger *zap.Logger) (string, error) {
	if cfg.ServiceID == "" {
		return cfg.PublicKey, nil
	}
	store, err := openEventStore(ctx, cfg.Sidecar.DataDir, logger)
	if err != nil {
		return "", err
	}
	trust, trustErr := openSidecarTrustEpoch(ctx, cfg, store, logger)
	closeErr := store.Close()
	if trustErr != nil {
		return "", trustErr
	}
	if trust == nil {
		return "", fmt.Errorf("trust epoch preflight is not configured")
	}
	defer func() { _ = trust.state.Close() }()
	if closeErr != nil {
		return "", closeErr
	}
	return trust.resolution.Pubkey, nil
}

func (t *sidecarTrustEpoch) servicePubkey() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.resolution.Pubkey
}

func (t *sidecarTrustEpoch) serviceID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.resolution.ServiceID
}

func (t *sidecarTrustEpoch) isPoisoned() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.poisoned
}

func (t *sidecarTrustEpoch) readDecision(pubkey string) (known, allowed bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	old := t.verifier.LegacyPubkey
	if t.resolution.Record != nil {
		old = t.resolution.Record.OldPubkey
	}
	if pubkey != old && pubkey != t.resolution.Pubkey {
		return false, false
	}
	return true, !t.poisoned && pubkey == t.resolution.Pubkey
}

func (t *sidecarTrustEpoch) judgeWrite(ev nostr.Event) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ev.PubKey.Hex() == t.verifier.OperatorRoot && ev.Kind == nostr.Kind(kinds.CASControlState) &&
		ev.Tags.GetD() == "trust-epoch:"+t.resolution.ServiceID {
		resolved, err := t.verifier.AcceptHead(context.Background(), t.resolution.ServiceID, ev)
		if err != nil {
			t.poisoned = true
			t.logger.Error("sidecar trust epoch head is unverifiable", zap.Error(err))
			return true, err
		}
		t.resolution = resolved
		return false, nil
	}
	if t.poisoned {
		count := t.hostile.Add(1)
		t.logger.Warn("sidecar event refused after unverifiable trust epoch head",
			zap.String("event_id", ev.ID.Hex()), zap.Uint64("count", count))
		return true, fmt.Errorf("unverifiable trust epoch head")
	}
	old := t.verifier.LegacyPubkey
	if t.resolution.Record != nil {
		old = t.resolution.Record.OldPubkey
	}
	if ev.PubKey.Hex() != old && ev.PubKey.Hex() != t.resolution.Pubkey {
		return false, nil
	}
	if t.resolution.JudgeEvent(ev) == trustepoch.Hostile {
		count := t.hostile.Add(1)
		t.logger.Warn("hostile service event refused by sidecar", zap.String("event_id", ev.ID.Hex()), zap.Uint64("count", count))
		return true, fmt.Errorf("hostile service event at trust epoch")
	}
	return false, nil
}

func (t *sidecarTrustEpoch) isVerifiedReplay(ev nostr.Event) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return !t.poisoned && t.resolution.Record != nil && t.resolution.Record.Activated &&
		ev.PubKey.Hex() == t.resolution.Record.OldPubkey &&
		t.resolution.JudgeEvent(ev) == trustepoch.PreEpochAccepted
}

func acknowledgeSidecarEpoch(ctx context.Context, trust *sidecarTrustEpoch, ackKeyFile string,
	store *eventStore, relay *khatru.Relay) error {
	if trust == nil || trust.resolution.Record == nil || !trust.resolution.Record.Activated {
		return nil
	}
	if ackKeyFile == "" {
		return fmt.Errorf("activated trust epoch requires a dedicated relay acknowledgment identity")
	}
	raw, err := os.ReadFile(ackKeyFile)
	if err != nil {
		return fmt.Errorf("read relay acknowledgment identity: %w", err)
	}
	key, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("invalid relay acknowledgment identity")
	}
	pubkey := key.Public()
	if pubkey.Hex() == trust.resolution.Pubkey || pubkey.Hex() == trust.resolution.Record.OldPubkey ||
		pubkey.Hex() == trust.verifier.OperatorRoot {
		return fmt.Errorf("relay acknowledgment identity must be independent")
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{pubkey},
		Tags:    nostr.TagMap{"d": []string{"trust-epoch-ack:" + trust.resolution.ServiceID + ":sidecar"}}}
	for ev := range store.Query(ctx, filter, 0) {
		if trustepoch.VerifyAcknowledgmentAuthority(ev, trust.resolution, "sidecar", pubkey.Hex(), false) == nil {
			return nil
		}
	}
	ack, err := trustepoch.NewAcknowledgment(trust.resolution, "sidecar", nostr.Now())
	if err != nil {
		return err
	}
	if err := ack.Sign(key); err != nil {
		return fmt.Errorf("sign sidecar trust epoch ack: %w", err)
	}
	if err := trustepoch.VerifyAcknowledgmentAuthority(ack, trust.resolution, "sidecar", pubkey.Hex(), false); err != nil {
		return err
	}
	if _, err := relay.AddEvent(ctx, ack); err != nil {
		return fmt.Errorf("store sidecar trust epoch ack: %w", err)
	}
	return nil
}
