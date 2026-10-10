package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrout"
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

func (l *signerLedger) open(_ context.Context, cfg config.NostrConfig, _ *nostrout.Admission, _ *zap.Logger) (*serviceSigner, error) {
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
	return &runtimeSupervisor{rootCtx: t.Context(), logger: zap.NewNop(), factory: factory, openSigner: ledger.open, initAdmission: isolatedAdmission}
}

// isolatedAdmission builds a controller of the test's own instead of the
// process-wide one, which only the first initialization in a process sets.
func isolatedAdmission(cfg nostrout.Config) (*nostrout.Admission, error) {
	return nostrout.New(cfg), nil
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
	_, err := openServiceSigner(t.Context(), config.Defaults().Nostr, nil, zap.NewNop())
	if !errors.Is(err, servicesigner.ErrNotConfigured) {
		t.Fatalf("openServiceSigner() error = %v, want ErrNotConfigured", err)
	}
}

// A client key file rotated under an unchanged path is a new signer: the
// reload opens a session with the new key instead of keeping the old one.
func TestReloadReopensTheSignerWhenTheClientKeyFileRotates(t *testing.T) {
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, nil)
	path := filepath.Join(t.TempDir(), "client.key")
	write := func(key string) {
		if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := enabledConfig("aa")
	cfg.Nostr.Signer.ClientSecretKeyFile = path
	write(strings.Repeat("1", 64))
	for range 2 {
		if err := supervisor.replace(cfg); err != nil {
			t.Fatalf("replace: %v", err)
		}
	}
	if len(ledger.opened) != 1 {
		t.Fatalf("opened %d signers for an unchanged key file, want 1", len(ledger.opened))
	}
	write(strings.Repeat("2", 64))
	if err := supervisor.replace(cfg); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(ledger.opened) != 2 || ledger.opened[0].closes != 1 {
		t.Fatalf("rotated key file: opened %d signers, old closed %d times; want 2 and 1", len(ledger.opened), ledger.opened[0].closes)
	}
	if err := supervisor.shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenServiceSignerAbortsWhenTheProcessStops(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.PrivateKey = nostr.Generate().Hex()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if signer, err := openServiceSigner(ctx, cfg, nil, zap.NewNop()); !errors.Is(err, context.Canceled) {
		signer.Close()
		t.Fatalf("openServiceSigner() error = %v, want context.Canceled", err)
	}
	signer, err := openServiceSigner(t.Context(), cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("openServiceSigner(): %v", err)
	}
	signer.Close()
}

// The sidecar's NIP-46 requests pass the admission controller built from
// its own nostr.outbound settings: an active kill switch refuses the connect
// before any request reaches the bunker relay.
func TestSidecarNIP46RequestsHonourTheConfiguredOutboundAdmission(t *testing.T) {
	relay := khatru.NewRelay()
	var published atomic.Int32
	relay.OnEphemeralEvent = func(context.Context, nostr.Event) { published.Add(1) }
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	stop := filepath.Join(t.TempDir(), "nostr-publish.stop")
	if err := os.WriteFile(stop, []byte("stop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := enabledConfig(nostr.Generate().Public().Hex())
	cfg.Nostr.Signer.BunkerURI = "bunker://" + nostr.Generate().Public().Hex() + "?relay=" + url.QueryEscape("ws"+strings.TrimPrefix(server.URL, "http"))
	cfg.Nostr.Signer.ClientSecretKey = nostr.Generate().Hex()
	cfg.Nostr.Signer.Timeout = time.Minute
	cfg.Nostr.Outbound.KillSwitchFile = stop
	cfg.Nostr.Outbound.Lanes.Signer = config.NostrOutboundLaneConfig{RatePerMinute: 7, Burst: 2}

	var initialized []nostrout.Config
	supervisor := &runtimeSupervisor{rootCtx: t.Context(), logger: zap.NewNop(), factory: newSidecar, openSigner: openServiceSigner,
		initAdmission: func(c nostrout.Config) (*nostrout.Admission, error) {
			initialized = append(initialized, c)
			return isolatedAdmission(c)
		}}
	start := time.Now()
	err := supervisor.replace(cfg)
	if !errors.Is(err, nostrout.ErrKillSwitch) {
		t.Fatalf("replace() error = %v, want the kill switch to refuse the NIP-46 connect", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("refusal took %s; admission must refuse before waiting on the bunker", elapsed)
	}
	if len(initialized) != 1 || initialized[0].PurposeBudgets[nostrout.PurposeSigner] != (nostrout.PurposeBudget{RatePerMinute: 7, Burst: 2}) {
		t.Fatalf("admission initialized with %+v, want the configured signer lane", initialized)
	}
	if supervisor.signer != nil || supervisor.active != nil {
		t.Fatal("a refused signer must not become the supervisor's")
	}
	if n := published.Load(); n != 0 {
		t.Fatalf("bunker relay received %d NIP-46 requests under the kill switch", n)
	}
}

// Outbound admission is fixed at process start in the sidecar as in
// bahia-server: a reload that changes nostr.outbound is rejected and the
// running sidecar keeps serving with its signer.
func TestReloadThatChangesOutboundAdmissionIsRejected(t *testing.T) {
	ledger := &signerLedger{}
	supervisor := newTestSupervisor(t, ledger, nil)
	var fixed *nostrout.Config
	supervisor.initAdmission = func(c nostrout.Config) (*nostrout.Admission, error) {
		if fixed == nil {
			fixed = &c
		} else if !reflect.DeepEqual(*fixed, c) {
			return nil, nostrout.ErrConfigFixed
		}
		return isolatedAdmission(c)
	}
	cfg := enabledConfig("aa")
	if err := supervisor.replace(cfg); err != nil {
		t.Fatalf("initial runtime: %v", err)
	}
	active, signer := supervisor.active, supervisor.signer

	changed := enabledConfig("aa")
	changed.Nostr.Outbound.Lanes.Signer = config.NostrOutboundLaneConfig{RatePerMinute: 1, Burst: 1}
	if err := supervisor.replace(changed); !errors.Is(err, nostrout.ErrConfigFixed) {
		t.Fatalf("replace() error = %v, want ErrConfigFixed", err)
	}
	if supervisor.active != active || supervisor.signer != signer || len(ledger.opened) != 1 || signer.close == nil {
		t.Fatal("a rejected reload must leave the running sidecar and its signer untouched")
	}
	if err := supervisor.replace(cfg); err != nil {
		t.Fatalf("reload with the original outbound settings: %v", err)
	}
	if err := supervisor.shutdown(); err != nil {
		t.Fatal(err)
	}
}
