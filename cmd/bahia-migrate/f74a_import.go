package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	gonostr "fiatjaf.com/nostr"
	"github.com/jackc/pgx/v5/pgxpool"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// runF74aImport is deliberately absent from app.New. It requires an operator
// to stop normal writers and use the daemon's exclusive local outbox files.
func runF74aImport(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, stdout, stderr io.Writer) int {
	if !cfg.Nostr.PublishEnabled {
		return reportError(stderr, "f74a-import requires nostr.publish_enabled")
	}
	key := strings.TrimSpace(cfg.Nostr.PrivateKey)
	secret, err := gonostr.SecretKeyFromHex(key)
	if err != nil {
		return reportError(stderr, "f74a-import requires a valid nostr.private_key: %v", err)
	}
	author := secret.Public()
	relays := f74aControlPlaneRelays(cfg.Nostr)
	if len(relays) == 0 {
		return reportError(stderr, "f74a-import requires control-plane relays")
	}
	logger, err := zap.NewProduction()
	if err != nil {
		return reportError(stderr, "creating logger: %v", err)
	}
	defer func() { _ = logger.Sync() }()
	store, err := localstore.Open(cfg.Nostr.LocalStore.Path)
	if err != nil {
		return reportError(stderr, "opening exclusive local event store (stop daemon first): %v", err)
	}
	defer store.Close()
	outbox, err := localstore.OpenOutbox(cfg.Nostr.LocalStore.ResolvedOutboxPath())
	if err != nil {
		return reportError(stderr, "opening exclusive local outbox (stop daemon first): %v", err)
	}
	defer outbox.Close()
	if moved := outbox.MovedAside(); moved != "" {
		return reportError(stderr, "local outbox corruption moved %s aside; recover it before importing", moved)
	}
	poolRelays := nostradapter.NewRelayPool(relays, logger, nostradapter.WithPrivateKey(key))
	defer poolRelays.Close()
	poolRelays.Connect(ctx)
	pub := nostradapter.NewPublisher(cfg.Nostr, poolRelays, nil, logger,
		nostradapter.WithPublishTarget(repository.NostrPublishTargetControlPlane),
		nostradapter.WithLocalOutbox(outbox, store))
	publishCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pub.Run(publishCtx) }()
	defer func() { cancel(); <-done }()
	registry := service.NewRegistryService(
		repository.NewPgServiceRepository(pool), repository.NewPgEnvironmentRepository(pool),
		repository.NewPgBuildRepository(pool), repository.NewPgArtifactRepository(pool),
		repository.NewPgDeploymentIntentRepository(pool), repository.NewPgDeploymentRunRepository(pool),
		repository.NewPgRuntimeObservationRepository(pool), repository.NewPgEnvironmentServiceStateRepository(pool),
		nil, nil, logger)
	history := nostradapter.NewLocalEventRepository(store, nil).Authored(author.Hex())
	projector := nostradapter.NewProjector(cfg.Nostr, registry, pub, history, logger)
	if !projector.Enabled() {
		return reportError(stderr, "f74a-import could not enable canonical projector")
	}
	signer, err := controlplane.NewPrivateKeySigner(key)
	if err != nil {
		return reportError(stderr, "f74a-import signer: %v", err)
	}
	trust := controlplane.NewTrustSet(cfg.Nostr.AuthorizedPubkeys, logger)
	ock := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
		Signer: signer, ServicePubkey: author.Hex(), Publisher: projector,
		History: nostradapter.NewProjectorOCKEnvelopeHistory(history),
		Members: controlplane.NewTrustSetMemberSource(trust, nil), Logger: logger,
	})
	canonical := nostradapter.NewF74aCanonicalPublisher(projector, controlplane.NewConfidentialEncryptor(ock, logger))
	runner := service.NewF74aBackfillRunner(service.F74aBackfillConfig{
		Marker: outbox, Source: repository.NewPgF74aBackfillSource(pool), Publisher: canonical,
		Pending: func(context.Context) (int64, error) { counts, err := outbox.Counts(); return counts.Pending, err },
		SemanticDelivered: func(ctx context.Context, pkg *domain.SBOMPackage) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			entry, found, err := outbox.LatestByCoordinate(repository.NostrPublishTargetControlPlane,
				gonostr.Kind(nostradapter.KindCASControlState), author, nostradapter.SBOMPackageDTag(pkg))
			if err != nil || !found {
				return false, err
			}
			// A pending or refused delivery is not proof. An old accepted entry only
			// counts if it is the current non-tombstone semantic coordinate.
			deletedFalse := false
			for _, tag := range entry.Event.Tags {
				if len(tag) >= 2 && tag[0] == "deleted" && tag[1] == "false" {
					deletedFalse = true
				}
			}
			if !deletedFalse {
				return false, fmt.Errorf("semantic package coordinate is tombstoned")
			}
			return entry.Delivered, nil
		},
	})
	if err := runner.RunMigration(ctx); err != nil {
		snap := runner.Snapshot()
		return reportError(stderr, "f74a-import incomplete (phase=%s processed=%d): %v", snap.Phase, snap.Processed, err)
	}
	if _, err := fmt.Fprintf(stdout, "f74a-import complete: processed=%d\n", runner.Snapshot().Processed); err != nil {
		return reportError(stderr, "writing result: %v", err)
	}
	return 0
}

func f74aControlPlaneRelays(cfg config.NostrConfig) []string {
	if cfg.Sidecar.Enabled {
		if cfg.Sidecar.BackendURL != "" {
			return []string{cfg.Sidecar.BackendURL}
		}
		if cfg.Sidecar.PublicURL != "" {
			return []string{cfg.Sidecar.PublicURL}
		}
	}
	return cfg.ContextVMRelayPolicyRelays()
}
