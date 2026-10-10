package app

import (
	"context"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

type operationalViewPublisher interface {
	PublishSoulRuntimePolicy(context.Context, nostrAdapter.SoulRuntimePolicy) error
	PublishBlossomAdmin(context.Context, []string, map[string]string) error
	PublishBlossomBlob(context.Context, string, blossom.BlobDescriptor) error
}

// operatorAllowlistPublisher publishes one scope's operator allowlist as a
// fleet-OCK encrypted record.
type operatorAllowlistPublisher interface {
	PublishOperatorAllowlist(ctx context.Context, scope string, pubkeys []string) error
}

// operatorAllowlistSet is one configured operator allowlist: the scope it is
// published under and the pubkeys the daemon accepts for that scope. An empty
// list (a disabled or emptied scope) tombstones the scope's record.
type operatorAllowlistSet struct {
	scope   string
	pubkeys []string
}

// operatorAllowlistSets derives the published allowlists from config:
// `operators:continuity` mirrors nostr.authorized_pubkeys and
// `operators:soul-factory` mirrors soul_factory.authorized_pubkeys, which only
// authorizes anyone while the Soul Factory is enabled.
func operatorAllowlistSets(cfg *config.Config) []operatorAllowlistSet {
	sets := []operatorAllowlistSet{{scope: kinds.OperatorAllowlistScopeContinuity, pubkeys: append([]string{}, cfg.Nostr.AuthorizedPubkeys...)}}
	soulFactory := operatorAllowlistSet{scope: kinds.OperatorAllowlistScopeSoulFactory}
	if cfg.SoulFactory.Enabled {
		soulFactory.pubkeys = append([]string{}, cfg.SoulFactory.AuthorizedPubkeys...)
	}
	return append(sets, soulFactory)
}

// operationalViewsRunner performs one startup observation. Future daemon-owned
// uploads publish at their mutation site; no relay or Blossom polling is used.
// It also publishes the operator allowlists: config is read at startup, and a
// config change that is not hot-reloadable rebuilds the application, so every
// configuration the daemon enforces is the one it publishes.
type operationalViewsRunner struct {
	publisher     operationalViewPublisher
	allowlists    operatorAllowlistPublisher
	allowlistSets []operatorAllowlistSet
	blossom       *blossom.Client
	policy        nostrAdapter.SoulRuntimePolicy
	owners        []string
	logger        *zap.Logger
}

func (r *operationalViewsRunner) Name() string { return "operational-views" }

func (r *operationalViewsRunner) Run(ctx context.Context) error {
	if err := r.publisher.PublishSoulRuntimePolicy(ctx, r.policy); err != nil {
		return err
	}
	if r.allowlists != nil {
		for _, set := range r.allowlistSets {
			if err := r.allowlists.PublishOperatorAllowlist(ctx, set.scope, set.pubkeys); err != nil {
				return err
			}
		}
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

func blossomOwnerKey(signer nostr.Signer) string {
	if signer == nil {
		return ""
	}
	pubkey, err := signer.GetPublicKey(context.Background())
	if err != nil {
		return ""
	}
	return pubkey.Hex()
}
