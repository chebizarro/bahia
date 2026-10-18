package identityrecovery

import (
	"context"
	"errors"
	"slices"
	"time"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/deletionguard"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
)

// RuntimeOptions changes custody and operator inputs, not production wiring.
// The rehearsal supplies local test keys through OpenSigner; the CLI leaves it
// nil and opens the configured signer transports.
type RuntimeOptions struct {
	OutboundAdmission        *nostrout.Admission
	OpenSigner               func(context.Context, config.ConfigFabricConfig) (nostr.Keyer, error)
	ConfirmDaemonUnreachable bool
	RotationUpdatesPath      string
	DraftPath                string
	Now                      func() time.Time
}

type ProductionRuntime struct {
	Engine         *Engine
	SQLDrafts      *SQLReviewDraftAdapter
	controlledPool *adapter.RelayPool
	cleanupPool    *adapter.RelayPool
}

func (r *ProductionRuntime) Close() error {
	if r == nil {
		return nil
	}
	if r.controlledPool != nil {
		r.controlledPool.Close()
	}
	if r.cleanupPool != nil {
		r.cleanupPool.Close()
	}
	if r.Engine != nil && r.Engine.Ledger != nil {
		return r.Engine.Ledger.Close()
	}
	return nil
}

// NewProductionRuntime is the single CLI/rehearsal construction path. It
// leaves PostgreSQL optional and never puts cleanup relays in Source.
func NewProductionRuntime(ctx context.Context, cfg *config.Config, opts RuntimeOptions) (_ *ProductionRuntime, err error) {
	if cfg == nil {
		return nil, errors.New("recovery configuration required")
	}
	if err := cfg.ValidateIdentityRecovery(); err != nil {
		return nil, err
	}
	r := cfg.IdentityRecovery
	root, err := nostr.PubKeyFromHex(r.Operator.PublicKey)
	if err != nil {
		return nil, err
	}
	old, err := nostr.PubKeyFromHex(r.OldPubkey)
	if err != nil {
		return nil, err
	}
	open := opts.OpenSigner
	if open == nil {
		open = func(ctx context.Context, signer config.ConfigFabricConfig) (nostr.Keyer, error) {
			return servicesigner.Open(ctx, config.NostrConfig{PublicKey: signer.PublicKey, Signer: signer.Signer}, servicesigner.Options{Logger: zap.NewNop()})
		}
	}
	ledger, err := OpenLedger(r.LedgerPath, cfg.Nostr.LocalStore.Path)
	if err != nil {
		return nil, err
	}
	runtime := &ProductionRuntime{Engine: &Engine{Ledger: ledger}}
	defer func() {
		if err != nil {
			_ = runtime.Close()
		}
	}()
	operator, err := open(ctx, r.Operator)
	if err != nil {
		return nil, errors.New("operator signer unavailable")
	}
	guard, err := relayguard.New(root, r.ControlledRelays)
	if err != nil {
		return nil, err
	}
	authorizeDeletion := func(ev nostr.Event) bool {
		state, loadErr := ledger.Load()
		if loadErr != nil || !slices.Contains(state.Pending, ev.ID.Hex()) {
			return false
		}
		recorded, ok := state.Records[ev.ID.Hex()]
		if !ok || recorded.ID != ev.ID {
			return false
		}
		targets := make(map[nostr.ID]nostr.Kind, len(state.Observed))
		for _, item := range state.Observed {
			id, parseErr := nostr.IDFromHex(item.ID)
			if parseErr != nil {
				return false
			}
			targets[id] = nostr.Kind(item.Kind)
		}
		return deletionguard.CheckSigned(ev, old, targets, nil) == nil
	}
	cleanupGuard, err := relayguard.NewRecoveryCleanup(root, r.ControlledRelays, r.CleanupRelays, old, authorizeDeletion)
	if err != nil {
		return nil, err
	}
	poolOpts := []adapter.RelayPoolOption{adapter.WithAuthSigner(operator), adapter.WithControlPlaneRelayGuard(guard), adapter.WithControlPlanePurpose()}
	cleanupOpts := []adapter.RelayPoolOption{adapter.WithAuthSigner(operator), adapter.WithControlPlaneRelayGuard(cleanupGuard), adapter.WithControlPlanePurpose()}
	if opts.OutboundAdmission != nil {
		poolOpts = append(poolOpts, adapter.WithOutboundAdmission(opts.OutboundAdmission))
		cleanupOpts = append(cleanupOpts, adapter.WithOutboundAdmission(opts.OutboundAdmission))
	}
	runtime.controlledPool = adapter.NewRelayPool(r.ControlledRelays, zap.NewNop(), poolOpts...)
	runtime.cleanupPool = adapter.NewRelayPool(r.CleanupRelays, zap.NewNop(), cleanupOpts...)
	runtime.controlledPool.Connect(ctx)
	runtime.cleanupPool.Connect(ctx)
	source := RelaySource{Pool: runtime.controlledPool, Relays: r.ControlledRelays}
	local := LocalStoreSource{Path: cfg.Nostr.LocalStore.Path}
	newSigner := func(ctx context.Context) (nostr.Keyer, error) { return open(ctx, r.NewIdentity) }
	oldSigner := func(ctx context.Context) (nostr.Keyer, error) {
		if r.OldSigner.PublicKey == "" {
			return nil, ErrAdapterNotRegistered
		}
		return open(ctx, r.OldSigner)
	}
	engine := runtime.Engine
	snapshots := make([]SnapshotArtifact, 0, len(r.VerifiedSnapshots))
	for _, a := range r.VerifiedSnapshots {
		snapshots = append(snapshots, SnapshotArtifact{Path: a.Path, SHA256: a.SHA256})
	}
	engine.Config = Config{ServiceID: cfg.Nostr.ServiceID, OldPubkey: old, ReasonCode: r.ReasonCode, ControlledRelays: r.ControlledRelays, CleanupRelays: r.CleanupRelays, ConsumerRoster: r.ConsumerRoster, OperatorPubkey: root, LocalStorePath: cfg.Nostr.LocalStore.Path, AcceptedEventIDs: r.AcceptedEventIDs, VerifiedSnapshots: snapshots}
	engine.Source = source
	engine.Cleanup = CleanupRelaySource{Pool: runtime.cleanupPool, Relays: r.CleanupRelays}
	engine.Local = local
	engine.Operator = operator
	engine.NewSigner = newSigner
	if r.OldSigner.PublicKey != "" {
		engine.OldSigner = oldSigner
	}
	engine.Purge = FilePurgeReceipts{Path: r.PurgeEvidencePath}
	engine.Containment = DurableContainmentAdapter{Ledger: ledger, MarkerPath: cfg.Nostr.LocalStore.ResolvedOutboxPath() + ".identity-containment", ConfirmDaemonUnreachable: opts.ConfirmDaemonUnreachable}
	engine.Acks = &AcknowledgmentReader{Source: source, Local: local, Ledger: ledger, ServiceID: cfg.Nostr.ServiceID, OperatorRoot: root.Hex(), OldPubkey: old.Hex(), Authorities: r.AcknowledgmentAuthorities}
	engine.Rotation = &LedgerRotationTracker{Ledger: ledger, Source: CanonicalCredentialSource{OldPubkey: old, Source: source, Local: local, Ledger: ledger, OldSigner: oldSigner}, SystemKeys: cfg.SecretStore.SystemKeys, UpdatesPath: opts.RotationUpdatesPath}
	engine.Now = opts.Now
	relayPolicy := cfg.Nostr
	relayPolicy.ControlledRelays = append([]string(nil), r.ControlledRelays...)
	draftPath := opts.DraftPath
	if draftPath == "" {
		draftPath = r.LedgerPath + ".sql-review-drafts.json"
	}
	sqlDrafts := &SQLReviewDraftAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger, Rows: PGReviewRows{DB: cfg.DB, OldPubkey: old.Hex()}, ArtifactPath: draftPath}
	runtime.SQLDrafts = sqlDrafts
	engine.Domains = map[string]DomainRecoveryAdapter{
		"cp-state/service-registry":          &RegistryRecoveryAdapter{Family: adapter.KindServiceRegistry, OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"cp-state/environment-registry":      &RegistryRecoveryAdapter{Family: adapter.KindEnvironmentRegistry, OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"cp-state/adoption-binding":          &RegistryRecoveryAdapter{Family: adapter.KindAdoptionBindingRecord, OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"relay-settings-and-sets":            &RelayRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger, Static: relayPolicy},
		"hostname-reservations-and-recovery": &HostnameRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"backup-canonical-families":          &BackupRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"security-canonical":                 &SecurityRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"sbom-references-and-availability":   &SBOMRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"notification-channels":              &NotificationRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger, OldSigner: oldSigner, NewSigner: newSigner},
		"discovery-system":                   &DiscoveryRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger, Config: cfg, MCPTransportEnabled: true},
		"bahia-docs":                         &DocsRecoveryAdapter{OldPubkey: old, Source: source, Local: local, Ledger: ledger},
		"sql-only-review-drafts":             sqlDrafts,
	}
	engine.RequiredDomains = []string{"cp-state/service-registry", "cp-state/environment-registry", "cp-state/adoption-binding", "relay-settings-and-sets", "hostname-reservations-and-recovery", "backup-canonical-families", "security-canonical", "sbom-references-and-availability", "notification-channels", "bahia-docs", "discovery-system", "sql-only-review-drafts"}
	return runtime, nil
}
