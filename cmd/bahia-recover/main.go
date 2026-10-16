package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/identityrecovery"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: bahia-recover {plan|apply|status|resume|abort} --config FILE [--confirm-phase PHASE|--confirm-abort]")
		return 2
	}
	op := args[0]
	if op != "plan" && op != "apply" && op != "status" && op != "resume" && op != "abort" {
		_, _ = fmt.Fprintln(stderr, "unknown recovery command")
		return 2
	}
	flags := flag.NewFlagSet(op, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "Bahia config file")
	phase := flags.String("confirm-phase", "", "exact next phase")
	abort := flags.Bool("confirm-abort", false, "confirm pre-cutover abort")
	if err := flags.Parse(args[1:]); err != nil || *path == "" || len(flags.Args()) != 0 {
		_, _ = fmt.Fprintln(stderr, "config file and valid flags required")
		return 2
	}
	cfg, err := config.LoadReadOnly(*path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "configuration could not be loaded")
		return 1
	}
	if err = cfg.ValidateIdentityRecovery(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	root, err := nostr.PubKeyFromHex(cfg.IdentityRecovery.Operator.PublicKey)
	if err != nil {
		return 1
	}
	old, err := nostr.PubKeyFromHex(cfg.IdentityRecovery.OldPubkey)
	if err != nil {
		return 1
	}
	r := cfg.IdentityRecovery
	engineCfg := identityrecovery.Config{ServiceID: cfg.Nostr.ServiceID, OldPubkey: old, ReasonCode: r.ReasonCode, ControlledRelays: r.ControlledRelays, ConsumerRoster: r.ConsumerRoster, OperatorPubkey: root, LocalStorePath: cfg.Nostr.LocalStore.Path, AcceptedEventIDs: r.AcceptedEventIDs}
	if op == "status" || op == "plan" {
		state, readErr := identityrecovery.LoadStateReadOnly(r.LedgerPath)
		if readErr != nil {
			_, _ = fmt.Fprintln(stderr, "recovery ledger unavailable")
			return 1
		}
		if state.IncidentID != "" && (state.OldPubkey != old.Hex() || state.OperatorPubkey != root.Hex()) {
			_, _ = fmt.Fprintln(stderr, "recovery ledger identity mismatch")
			return 1
		}
		next, has := state.Next()
		view := struct {
			State   identityrecovery.State   `json:"state"`
			Next    identityrecovery.Phase   `json:"next_phase,omitempty"`
			HasNext bool                     `json:"has_next"`
			Phases  []identityrecovery.Phase `json:"phases,omitempty"`
		}{State: state, Next: next, HasNext: has}
		if op == "plan" {
			view.Phases = identityrecovery.Phases
		}
		if err = json.NewEncoder(stdout).Encode(view); err != nil {
			return 1
		}
		return 0
	}
	if op == "abort" && !(*abort) {
		_, _ = fmt.Fprintln(stderr, "--confirm-abort required")
		return 2
	}
	if (op == "apply" || op == "resume") && strings.TrimSpace(*phase) == "" {
		_, _ = fmt.Fprintln(stderr, "--confirm-phase required")
		return 2
	}
	ledger, err := identityrecovery.OpenLedger(r.LedgerPath, cfg.Nostr.LocalStore.Path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "recovery ledger unavailable")
		return 1
	}
	defer func() { _ = ledger.Close() }()
	engine := identityrecovery.Engine{Config: engineCfg, Ledger: ledger}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	operator, err := servicesigner.Open(sessionCtx, config.NostrConfig{PublicKey: r.Operator.PublicKey, Signer: r.Operator.Signer}, servicesigner.Options{Logger: zap.NewNop()})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "operator signer unavailable")
		return 1
	}
	guard, err := relayguard.New(root, r.ControlledRelays)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid controlled relay policy")
		return 1
	}
	pool := adapter.NewRelayPool(r.ControlledRelays, zap.NewNop(), adapter.WithAuthSigner(operator), adapter.WithControlPlaneRelayGuard(guard), adapter.WithControlPlanePurpose())
	defer pool.Close()
	pool.Connect(ctx)
	engine.Operator = operator
	engine.Source = relaySource{pool: pool, relays: r.ControlledRelays}
	engine.Local = localSource{path: cfg.Nostr.LocalStore.Path}
	engine.NewSigner = func(ctx context.Context) (nostr.Keyer, error) {
		return servicesigner.Open(ctx, config.NostrConfig{PublicKey: r.NewIdentity.PublicKey, Signer: r.NewIdentity.Signer}, servicesigner.Options{Logger: zap.NewNop()})
	}
	recoveryRelayPolicy := cfg.Nostr
	recoveryRelayPolicy.ControlledRelays = append([]string(nil), r.ControlledRelays...)
	engine.Domains = map[string]identityrecovery.DomainRecoveryAdapter{
		"cp-state/service-registry":          &identityrecovery.RegistryRecoveryAdapter{Family: adapter.KindServiceRegistry, OldPubkey: old, Source: engine.Source, Local: localSource{path: cfg.Nostr.LocalStore.Path}, Ledger: ledger},
		"cp-state/environment-registry":      &identityrecovery.RegistryRecoveryAdapter{Family: adapter.KindEnvironmentRegistry, OldPubkey: old, Source: engine.Source, Local: localSource{path: cfg.Nostr.LocalStore.Path}, Ledger: ledger},
		"cp-state/adoption-binding":          &identityrecovery.RegistryRecoveryAdapter{Family: adapter.KindAdoptionBindingRecord, OldPubkey: old, Source: engine.Source, Local: localSource{path: cfg.Nostr.LocalStore.Path}, Ledger: ledger},
		"relay-settings-and-sets":            &identityrecovery.RelayRecoveryAdapter{OldPubkey: old, Source: engine.Source, Local: localSource{path: cfg.Nostr.LocalStore.Path}, Ledger: ledger, Static: recoveryRelayPolicy},
		"hostname-reservations-and-recovery": &identityrecovery.HostnameRecoveryAdapter{OldPubkey: old, Source: engine.Source, Local: localSource{path: cfg.Nostr.LocalStore.Path}, Ledger: ledger},
		"backup-canonical-families":          &identityrecovery.BackupRecoveryAdapter{OldPubkey: old, Source: engine.Source, Local: localSource{path: cfg.Nostr.LocalStore.Path}, Ledger: ledger},
	}
	engine.RequiredDomains = []string{
		"cp-state/service-registry", "cp-state/environment-registry", "cp-state/adoption-binding",
		"relay-settings-and-sets", "hostname-reservations-and-recovery", "backup-canonical-families",
		"security-and-sbom", "notification-docs-discovery-system", "sql-only-review-drafts",
	}
	if r.OldSigner.PublicKey != "" {
		engine.OldSigner = func(ctx context.Context) (nostr.Keyer, error) {
			return servicesigner.Open(ctx, config.NostrConfig{PublicKey: r.OldSigner.PublicKey, Signer: r.OldSigner.Signer}, servicesigner.Options{Logger: zap.NewNop()})
		}
	}
	switch op {
	case "abort":
		err = engine.Abort(ctx)
	case "resume":
		err = engine.Resume(ctx, identityrecovery.Phase(*phase))
	default:
		err = engine.Apply(ctx, identityrecovery.Phase(*phase))
	}
	if err != nil {
		if errors.Is(err, identityrecovery.ErrAdapterNotRegistered) {
			_, _ = fmt.Fprintln(stderr, "phase adapter not registered; phase remains incomplete")
		} else {
			_, _ = fmt.Fprintln(stderr, "recovery phase failed:", err)
		}
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "recovery phase recorded")
	return 0
}
