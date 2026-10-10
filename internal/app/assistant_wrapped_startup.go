package app

import (
	"context"
	"errors"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
)

func assistantWrappedKeysSelected(cfg *config.Config) bool {
	return cfg != nil && cfg.Assistant.WrappedKeys.Mode != "" && cfg.Assistant.WrappedKeys.Mode != "legacy_v1"
}

// assistantTranscriptKeyProviderWithSigner selects the exact configured
// historical key source. Wrapped mode is read-only: it cannot append a new
// transcript or checkpoint even if other assistant runtime components run.
// It unwraps the pinned manifest through signer, the service identity's
// keyer whatever holds the key, and never derives a key from the service nsec.
func assistantTranscriptKeyProviderWithSigner(ctx context.Context, cfg *config.Config, signer assistantKeyWrapper) (service.AssistantTranscriptKeyProvider, error) {
	if !assistantWrappedKeysSelected(cfg) {
		return assistantTranscriptKeyProvider(cfg)
	}
	selected := cfg.Assistant.WrappedKeys
	if selected.Mode != "wrapped_read_only" {
		return nil, errors.New("unsupported assistant wrapped-key mode")
	}
	if signer == nil {
		return nil, errors.New("assistant wrapped mode requires the service signer")
	}
	pubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("read service signer pubkey: %w", err)
	}
	if pubkey == nostr.ZeroPK {
		return nil, errors.New("assistant wrapped mode requires the service pubkey")
	}
	return assistantWrappedProviderFromStoredManifest(ctx, signer, pubkey, selected.ManifestPath, selected.ExpectedGeneration)
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
