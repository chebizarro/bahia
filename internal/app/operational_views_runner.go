package app

import (
	"context"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"go.uber.org/zap"
)

type operationalViewPublisher interface {
	PublishSoulRuntimePolicy(context.Context, nostrAdapter.SoulRuntimePolicy) error
	PublishBlossomAdmin(context.Context, []string, map[string]string) error
	PublishBlossomBlob(context.Context, string, blossom.BlobDescriptor) error
}

// operationalViewsRunner performs one startup observation. Future daemon-owned
// uploads publish at their mutation site; no relay or Blossom polling is used.
type operationalViewsRunner struct {
	publisher operationalViewPublisher
	blossom   *blossom.Client
	policy    nostrAdapter.SoulRuntimePolicy
	owners    []string
	logger    *zap.Logger
}

func (r *operationalViewsRunner) Name() string { return "operational-views" }

func (r *operationalViewsRunner) Run(ctx context.Context) error {
	if err := r.publisher.PublishSoulRuntimePolicy(ctx, r.policy); err != nil {
		return err
	}
	if r.blossom != nil {
		health := map[string]string{}
		for server, err := range r.blossom.HealthCheck(ctx) {
			if err != nil {
				health[server] = err.Error()
			} else {
				health[server] = "ok"
			}
		}
		if err := r.publisher.PublishBlossomAdmin(ctx, r.blossom.Servers(), health); err != nil {
			return err
		}
		seen := map[string]struct{}{}
		for _, owner := range r.owners {
			owner = strings.ToLower(strings.TrimSpace(owner))
			if len(owner) != 64 {
				continue
			}
			if _, exists := seen[owner]; exists {
				continue
			}
			seen[owner] = struct{}{}
			blobs, err := r.blossom.ListByPubkey(ctx, owner)
			if err != nil {
				r.logger.Warn("Blossom owner listing unavailable", zap.String("owner", owner), zap.Error(err))
				continue
			}
			for _, blob := range blobs {
				if err := r.publisher.PublishBlossomBlob(ctx, owner, blob); err != nil {
					return err
				}
			}
		}
	}
	<-ctx.Done()
	return nil
}

func blossomOwnerKey(privateKey string) string {
	if privateKey == "" {
		return ""
	}
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(privateKey)
	if err != nil {
		return ""
	}
	return pubkey
}
