package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
)

// assistantTranscriptKeyProviderForStartup selects the exact configured
// historical key source. Wrapped mode is read-only: it cannot append a new
// transcript or checkpoint even if other assistant runtime components run.
func assistantTranscriptKeyProviderForStartup(ctx context.Context, cfg *config.Config, servicePubkey string, relays []string) (service.AssistantTranscriptKeyProvider, error) {
	return assistantTranscriptKeyProviderForStartupWithClient(ctx, cfg, servicePubkey, relays, func(options signet.Config) (*signet.Client, error) {
		return signet.NewClient(options, slog.Default())
	})
}

func assistantTranscriptKeyProviderForStartupWithClient(ctx context.Context, cfg *config.Config, servicePubkey string, relays []string, newClient func(signet.Config) (*signet.Client, error)) (service.AssistantTranscriptKeyProvider, error) {
	if cfg == nil || cfg.Assistant.WrappedKeys.Mode == "" || cfg.Assistant.WrappedKeys.Mode == "legacy_v1" {
		return assistantTranscriptKeyProvider(cfg)
	}
	if cfg.Assistant.WrappedKeys.Mode != "wrapped_read_only" {
		return nil, errors.New("unsupported assistant wrapped-key mode")
	}
	pubkey, err := nostr.PubKeyFromHex(servicePubkey)
	if err != nil || pubkey == nostr.ZeroPK {
		return nil, errors.New("assistant wrapped mode requires existing service pubkey")
	}
	selected := cfg.Assistant.WrappedKeys
	manifest, err := loadAssistantWrappedKeyManifestPinned(selected.ManifestPath, selected.ExpectedGeneration)
	if err != nil {
		return nil, fmt.Errorf("load assistant wrapped key manifest: %w", err)
	}
	owner, err := nostr.SecretKeyFromHex(strings.TrimSpace(selected.OwnerClientSecretKey))
	if err != nil || owner.Public() == pubkey {
		return nil, errors.New("assistant wrapped mode requires distinct dedicated Signet owner key")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, selected.LeaseExpiresAt)
	if err != nil || !time.Now().Before(expiresAt) {
		return nil, errors.New("assistant wrapped mode requires unexpired Signet writer lease")
	}
	lease := signet.WriterLease{Epoch: selected.LeaseEpoch, OwnerPubkey: owner.Public(), ExpiresAt: expiresAt}
	leaseSource := func(context.Context) (signet.WriterLease, error) { return lease, nil }
	timeout := selected.ConnectTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := newClient(signet.Config{BunkerURI: selected.SignetBunkerURI, Relays: relays, ClientSecretKey: strings.TrimSpace(selected.OwnerClientSecretKey), RequireReal: true, AllowMock: false, ConnectTimeout: timeout, ClosedRetryBudget: cfg.Nostr.ClosedRetryBudget, EpochLease: leaseSource, ExpectedServicePubkey: pubkey.Hex()})
	if err != nil {
		return nil, fmt.Errorf("initialize fenced assistant Signet reader: %w", err)
	}
	defer client.Close()
	signer, err := signet.NewEpochSigner(client, pubkey.Hex(), leaseSource)
	if err != nil {
		return nil, fmt.Errorf("initialize fenced assistant epoch signer: %w", err)
	}
	if err := client.Connect(connectCtx); err != nil {
		return nil, fmt.Errorf("connect fenced assistant Signet reader: %w", err)
	}
	provider, err := openAssistantWrappedKeyManifest(connectCtx, signer, pubkey, manifest)
	if err != nil {
		return nil, fmt.Errorf("open assistant wrapped key manifest: %w", err)
	}
	return provider, nil
}

func assistantWrappedProviderFromStoredManifest(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey, path, generation string) (service.AssistantTranscriptKeyProvider, error) {
	manifest, err := loadAssistantWrappedKeyManifestPinned(path, generation)
	if err != nil {
		return nil, fmt.Errorf("load assistant wrapped key manifest: %w", err)
	}
	provider, err := openAssistantWrappedKeyManifest(ctx, wrapper, servicePubkey, manifest)
	if err != nil {
		return nil, fmt.Errorf("open assistant wrapped key manifest: %w", err)
	}
	return provider, nil
}
