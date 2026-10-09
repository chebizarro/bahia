package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	gonostr "fiatjaf.com/nostr"
	"fmt"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
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
	ledger := f74aDeliveryLedger{store: outbox, author: author}
	pub.OnDelivered(func(ev gonostr.Event) {
		if err := ledger.accepted(ev); err != nil {
			logger.Error("persist F74a relay acceptance", zap.Error(err))
		}
	})
	pub.OnDeliveryAbandoned(func(ev gonostr.Event) {
		if err := ledger.abandoned(ev); err != nil {
			logger.Error("persist F74a relay refusal", zap.Error(err))
		}
	})
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
	projector := nostradapter.NewProjector(cfg.Nostr, registry, f74aTrackedPublisher{Publisher: pub, ledger: ledger}, history, logger)
	pub.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	if !projector.Enabled() {
		return reportError(stderr, "f74a-import could not enable canonical projector")
	}
	signer, err := controlplane.NewPrivateKeySigner(key)
	if err != nil {
		return reportError(stderr, "f74a-import signer: %v", err)
	}
	trust := f74aMigrationTrustSet(cfg.Nostr, logger)
	members := controlplane.NewTrustSetMemberSource(trust, nil)
	wraps := &f74aEnvelopePublisher{inner: projector}
	ock := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
		Signer: signer, ServicePubkey: author.Hex(), Publisher: wraps,
		History: nostradapter.NewProjectorOCKEnvelopeHistory(history),
		Members: members, Logger: logger,
	})
	source := repository.NewPgF74aBackfillSource(pool)
	releases, err := source.ListReleasesAfter(ctx, uuid.Nil, 1)
	if err != nil {
		return reportError(stderr, "read F74a releases: %v", err)
	}
	if len(releases) > 0 {
		recipients, err := f74aRecipients(ctx, author.Hex(), members)
		if err != nil {
			return reportError(stderr, "read fleet OCK recipients: %v", err)
		}
		manifest, err := f74aPrepareOCK(ctx, outbox, ock, wraps, recipients, author)
		if err != nil {
			return reportError(stderr, "prepare F74a fleet OCK: %v", err)
		}
		ready, err := ledger.proveOCK(ctx, outbox, manifest.Version)
		if err != nil {
			return reportError(stderr, "verify F74a fleet OCK: %v", err)
		}
		if !ready {
			return reportError(stderr, "F74a fleet OCK envelopes lack durable relay quorum proof; retry after delivery")
		}
		recovered, err := ock.GetKeyByVersion(ctx, kinds.FleetOCKScope, manifest.Version)
		if err != nil {
			return reportError(stderr, "recover proved F74a fleet OCK: %v", err)
		}
		digest := sha256.Sum256(recovered.Key[:])
		if hex.EncodeToString(digest[:]) != manifest.KeyHash {
			return reportError(stderr, "proved F74a OCK envelopes do not match recovered content key")
		}
	}
	canonical := nostradapter.NewF74aCanonicalPublisher(projector, controlplane.NewConfidentialEncryptor(ock, logger))
	runner := service.NewF74aBackfillRunner(service.F74aBackfillConfig{
		Marker: outbox, Source: source, Publisher: f74aRecordPublisher{inner: canonical}, Author: author.Hex(),
		Pending: func(context.Context) (int64, error) { counts, err := outbox.Counts(); return counts.Pending, err },
		SemanticDelivered: func(ctx context.Context, pkg *domain.SBOMPackage) (bool, error) {
			return ledger.prove(ctx, "semantic_packages", pkg)
		},
		Delivered: func(ctx context.Context, phase string, item any) (bool, error) {
			accepted, err := ledger.prove(ctx, phase, item)
			if err != nil || !accepted || phase != "releases" {
				return accepted, err
			}
			release := item.(*domain.LLMRelease)
			rec, found, err := ledger.receipt(nostradapter.KindLLMReleaseRegistry, "llm:release:"+release.ID.String())
			if err != nil || !found {
				return false, err
			}
			return ledger.proveOCK(ctx, outbox, rec.OCKVersion)
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
