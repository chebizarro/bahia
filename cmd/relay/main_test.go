package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
)

type supervisorTestRuntime struct {
	started chan struct{}
	stopped atomic.Bool
	signer  nostr.Signer
}

func (r *supervisorTestRuntime) Run(ctx context.Context) error {
	close(r.started)
	<-ctx.Done()
	r.stopped.Store(true)
	return nil
}

func (r *supervisorTestRuntime) Close() error { return nil }

// signerLedger opens fake service signers and records every open and close.
type signerLedger struct {
	opened   []*testSigner
	failOpen error
}

type testSigner struct {
	nostr.Keyer
	closes  int
	runtime *supervisorTestRuntime // the runtime signing with it, if any
	// signingAtClose reports whether that runtime was still running at close.
	signingAtClose bool
}

func (l *signerLedger) open(_ context.Context, cfg config.NostrConfig) (*serviceSigner, error) {
	if l.failOpen != nil {
		return nil, l.failOpen
	}
	signer := &testSigner{}
	l.opened = append(l.opened, signer)
	return &serviceSigner{cfg: cfg, keyer: signer, close: func() {
		signer.closes++
		signer.signingAtClose = signer.runtime != nil && !signer.runtime.stopped.Load()
	}}, nil
}

func newTestSupervisor(t *testing.T, ledger *signerLedger, factory sidecarFactory) *runtimeSupervisor {
	t.Helper()
	if factory == nil {
		factory = func(_ context.Context, _ config.NostrConfig, signer nostr.Signer, _ *zap.Logger) (sidecarRuntime, error) {
			runtime := &supervisorTestRuntime{started: make(chan struct{}), signer: signer}
			signer.(*testSigner).runtime = runtime
			return runtime, nil
		}
	}
	return &runtimeSupervisor{rootCtx: t.Context(), logger: zap.NewNop(), factory: factory, openSigner: ledger.open}
}

func enabledConfig(publicKey string) *config.Config {
	cfg := config.Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Signer.Method = config.NostrSignerNIP46
	cfg.Nostr.Signer.BunkerURI = "bunker://example"
	cfg.Nostr.PublicKey = publicKey
	return cfg
}

func TestReloadInitializationFailureKeepsActiveRuntimeServing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	current := &supervisorTestRuntime{started: make(chan struct{})}
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, func(context.Context, config.NostrConfig, nostr.Signer, *zap.Logger) (sidecarRuntime, error) {
		return nil, errors.New("replacement initialization failed")
	})
	supervisor.rootCtx = ctx
	supervisor.start(current)
	<-current.started

	if err := supervisor.replace(enabledConfig("aa")); err == nil {
		t.Fatal("replace() error = nil, want initialization failure")
	}
	if current.stopped.Load() {
		t.Fatal("active runtime stopped before replacement initialized")
	}
	if supervisor.active == nil || supervisor.active.runtime != current {
		t.Fatal("active runtime was replaced after replacement initialization failure")
	}
	if len(ledger.opened) != 1 || ledger.opened[0].closes != 1 {
		t.Fatalf("candidate signer of the failed replacement: opened %d, want 1 closed once", len(ledger.opened))
	}
	if err := supervisor.stop(); err != nil {
		t.Fatalf("stop active runtime: %v", err)
	}
}

func TestReloadKeepsTheSignerWhenItsConfigIsUnchanged(t *testing.T) {
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, nil)
	for range 3 {
		if err := supervisor.replace(enabledConfig("AA")); err != nil {
			t.Fatalf("replace: %v", err)
		}
		// Case-insensitive pubkey: the same signer.
		if err := supervisor.replace(enabledConfig("aa")); err != nil {
			t.Fatalf("replace: %v", err)
		}
	}
	if len(ledger.opened) != 1 {
		t.Fatalf("opened %d signers across unchanged reloads, want 1", len(ledger.opened))
	}
	signer := ledger.opened[0]
	if signer.closes != 0 {
		t.Fatalf("reused signer closed %d times during reloads", signer.closes)
	}
	if err := supervisor.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if signer.closes != 1 {
		t.Fatalf("signer closed %d times at shutdown, want 1", signer.closes)
	}
	if signer.signingAtClose {
		t.Fatal("signer closed while its runtime was still running")
	}
	if err := supervisor.shutdown(); err != nil || signer.closes != 1 {
		t.Fatalf("second shutdown: err %v, closes %d; want idempotent", err, signer.closes)
	}
}

func TestReloadRotatesAChangedSignerAfterTheSwap(t *testing.T) {
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, nil)
	if err := supervisor.replace(enabledConfig("aa")); err != nil {
		t.Fatalf("initial replace: %v", err)
	}
	if err := supervisor.replace(enabledConfig("bb")); err != nil {
		t.Fatalf("rotating replace: %v", err)
	}
	if len(ledger.opened) != 2 {
		t.Fatalf("opened %d signers, want 2", len(ledger.opened))
	}
	old, current := ledger.opened[0], ledger.opened[1]
	if old.closes != 1 || old.signingAtClose {
		t.Fatalf("old signer: closes %d, closed while signing %v; want closed once after its runtime stopped", old.closes, old.signingAtClose)
	}
	if current.closes != 0 || supervisor.active.runtime.(*supervisorTestRuntime).signer != current {
		t.Fatal("the new runtime must sign with the new, open signer")
	}
	if err := supervisor.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if current.closes != 1 {
		t.Fatalf("new signer closed %d times at shutdown, want 1", current.closes)
	}
}

func TestReloadKeepsTheSignerWhenTheNewOneFailsToOpen(t *testing.T) {
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, nil)
	if err := supervisor.replace(enabledConfig("aa")); err != nil {
		t.Fatalf("initial replace: %v", err)
	}
	active := supervisor.active
	ledger.failOpen = errors.New("bunker unreachable")
	if err := supervisor.replace(enabledConfig("bb")); err == nil {
		t.Fatal("replace() error = nil, want signer open failure")
	}
	if supervisor.active != active || ledger.opened[0].closes != 0 {
		t.Fatal("a failed signer open must keep the running runtime and its signer")
	}
	if err := supervisor.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestDisablingTheSidecarClosesItsSigner(t *testing.T) {
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, nil)
	if err := supervisor.replace(enabledConfig("aa")); err != nil {
		t.Fatalf("initial replace: %v", err)
	}
	disabled := enabledConfig("aa")
	disabled.Nostr.Sidecar.Enabled = false
	if err := supervisor.replace(disabled); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if supervisor.active != nil || ledger.opened[0].closes != 1 || ledger.opened[0].signingAtClose {
		t.Fatal("disabling must stop the runtime, then close its signer once")
	}
}

func TestOpenServiceSignerRequiresAServiceIdentity(t *testing.T) {
	_, err := openServiceSigner(t.Context(), config.Defaults().Nostr)
	if !errors.Is(err, servicesigner.ErrNotConfigured) {
		t.Fatalf("openServiceSigner() error = %v, want ErrNotConfigured", err)
	}
}

func TestSameServiceSigner(t *testing.T) {
	base := enabledConfig("aa").Nostr
	cases := map[string]struct {
		change func(*config.NostrConfig)
		same   bool
	}{
		"identical":         {func(*config.NostrConfig) {}, true},
		"pubkey case":       {func(c *config.NostrConfig) { c.PublicKey = "AA" }, true},
		"pubkey":            {func(c *config.NostrConfig) { c.PublicKey = "bb" }, false},
		"bunker":            {func(c *config.NostrConfig) { c.Signer.BunkerURI = "bunker://other" }, false},
		"timeout":           {func(c *config.NostrConfig) { c.Signer.Timeout = 1 }, false},
		"method":            {func(c *config.NostrConfig) { c.Signer.Method = config.NostrSignerNIP55L }, false},
		"unrelated sidecar": {func(c *config.NostrConfig) { c.Sidecar.ListenAddr = "127.0.0.1:1" }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			other := base
			tc.change(&other)
			if got := sameServiceSigner(base, other); got != tc.same {
				t.Fatalf("sameServiceSigner() = %v, want %v", got, tc.same)
			}
		})
	}
	local := config.Defaults().Nostr
	local.PrivateKey = "11"
	padded := local
	padded.PrivateKey = " 11\n"
	if !sameServiceSigner(local, padded) {
		t.Fatal("a trimmed-equal private key is the same local signer")
	}
	rotated := local
	rotated.PrivateKey = "22"
	if sameServiceSigner(local, rotated) {
		t.Fatal("a rotated private key is a different signer")
	}
}

func TestOpenServiceSignerAbortsWhenTheProcessStops(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.PrivateKey = nostr.Generate().Hex()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if signer, err := openServiceSigner(ctx, cfg); !errors.Is(err, context.Canceled) {
		signer.Close()
		t.Fatalf("openServiceSigner() error = %v, want context.Canceled", err)
	}
	signer, err := openServiceSigner(t.Context(), cfg)
	if err != nil {
		t.Fatalf("openServiceSigner(): %v", err)
	}
	signer.Close()
}
