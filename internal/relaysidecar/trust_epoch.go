package relaysidecar

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/servicesigner"
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
	if !t.resolution.IsServiceAuthor(pubkey) && pubkey != t.verifier.LegacyPubkey {
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
	if !t.resolution.IsServiceAuthor(ev.PubKey.Hex()) && ev.PubKey.Hex() != t.verifier.LegacyPubkey {
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

func (t *sidecarTrustEpoch) snapshot() trustepoch.Resolution {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.resolution
}

type ackSignerOpener func(context.Context, config.NostrConfig) (nostr.Keyer, error)

type sidecarAcknowledgment struct {
	mu     sync.Mutex
	cfg    config.SidecarAckSignerConfig
	file   string
	open   ackSignerOpener
	keyer  nostr.Keyer
	cancel context.CancelFunc
	status string
	wake   chan struct{}
	trust  *sidecarTrustEpoch
	store  *eventStore
	relay  *khatru.Relay
	logger *zap.Logger
}

func newSidecarAcknowledgment(cfg config.RelaySidecarConfig, trust *sidecarTrustEpoch, store *eventStore,
	relay *khatru.Relay, logger *zap.Logger, opener ackSignerOpener) *sidecarAcknowledgment {
	if opener == nil {
		opener = func(ctx context.Context, signerCfg config.NostrConfig) (nostr.Keyer, error) {
			return servicesigner.Open(ctx, signerCfg, servicesigner.Options{
				OnAuthURL: func(string) { logger.Warn(servicesigner.AuthURLMessage) },
			})
		}
	}
	status := "configured"
	if cfg.AckSigner.Method == "" && cfg.AckPrivateKeyFile == "" {
		status = "unavailable"
	}
	return &sidecarAcknowledgment{cfg: cfg.AckSigner, file: cfg.AckPrivateKeyFile, open: opener,
		status: status, wake: make(chan struct{}, 1), trust: trust, store: store, relay: relay, logger: logger}
}

func (a *sidecarAcknowledgment) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *sidecarAcknowledgment) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
			_ = a.acknowledge(ctx)
		}
	}
}

func (a *sidecarAcknowledgment) closeSession() {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	if closer, ok := a.keyer.(io.Closer); ok {
		_ = closer.Close()
	}
	a.keyer = nil
}

func (a *sidecarAcknowledgment) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closeSession()
}

func (a *sidecarAcknowledgment) health() string {
	if !a.mu.TryLock() {
		return "configured"
	}
	status, signer := a.status, a.keyer
	a.mu.Unlock()
	if signer == nil && status == "ok" {
		return "unavailable"
	}
	if status != "ok" {
		return status
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := servicesigner.CheckHealth(ctx, signer); err != nil {
		return "unavailable"
	}
	return "ok"
}

func (a *sidecarAcknowledgment) fail() error {
	a.status = "unavailable"
	a.closeSession()
	a.logger.Error("sidecar trust epoch acknowledgment signer unavailable or publication failed")
	return fmt.Errorf("sidecar trust epoch acknowledgment unavailable")
}

func (a *sidecarAcknowledgment) acknowledge(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.trust == nil {
		return nil
	}
	resolved := a.trust.snapshot()
	if resolved.Record == nil || !resolved.Record.Activated {
		return nil
	}
	if a.keyer == nil {
		if a.cfg.Method == "" && a.file == "" {
			return a.fail()
		}
		if a.file != "" {
			raw, err := os.ReadFile(a.file)
			if err != nil {
				return a.fail()
			}
			a.keyer, err = nostrutil.NewLocalKeyer(strings.TrimSpace(string(raw)))
			if err != nil {
				return a.fail()
			}
		} else {
			openCtx, cancel := context.WithCancel(context.Background())
			stop := context.AfterFunc(ctx, cancel)
			keyer, err := a.open(openCtx, config.NostrConfig{PublicKey: a.cfg.PublicKey, Signer: a.cfg.NostrSignerConfig})
			stop()
			if err != nil || ctx.Err() != nil || keyer == nil {
				cancel()
				if closer, ok := keyer.(io.Closer); ok {
					_ = closer.Close()
				}
				return a.fail()
			}
			a.keyer, a.cancel = keyer, cancel
		}
	}
	pubkey, err := a.keyer.GetPublicKey(ctx)
	if err != nil || pubkey == nostr.ZeroPK || (a.cfg.Method != "" && pubkey.Hex() != a.cfg.PublicKey) {
		return a.fail()
	}
	err = (trustepoch.AckPublisher{ConsumerID: "sidecar", Authority: pubkey.Hex(),
		OperatorRoot: a.trust.verifier.OperatorRoot,
		Existing: func(ctx context.Context, filter nostr.Filter) ([]nostr.Event, error) {
			var events []nostr.Event
			for ev := range a.store.Query(ctx, filter, 0) {
				events = append(events, ev)
			}
			return events, ctx.Err()
		}, Sign: func(ctx context.Context, ev *nostr.Event) error {
			if err := a.keyer.SignEvent(ctx, ev); err != nil {
				return fmt.Errorf("ack signer refused event")
			}
			if ev.PubKey != pubkey {
				return fmt.Errorf("ack signer returned a different public key")
			}
			return nil
		},
		Publish: func(ctx context.Context, ev nostr.Event) error {
			_, err := a.relay.AddEvent(ctx, ev)
			return err
		}}).Acknowledge(ctx, resolved)
	if err != nil {
		return a.fail()
	}
	a.status = "ok"
	return nil
}
