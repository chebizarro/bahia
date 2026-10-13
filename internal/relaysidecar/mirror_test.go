package relaysidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"go.uber.org/zap"
)

type testMirrorPool struct{ *nostrAdapter.RelayPool }

func (p testMirrorPool) ConnectionStatus() []MirrorConnectionStatus {
	var status []MirrorConnectionStatus
	for _, relay := range p.HealthSnapshot().Relays {
		status = append(status, MirrorConnectionStatus{RelayURL: relay.URL, Connected: relay.Connected, LastError: relay.LastError})
	}
	return status
}

func testMirrorTransport(t *testing.T, cfg config.NostrConfig, signer nostr.Signer) MirrorTransport {
	t.Helper()
	targets, err := cfg.MirrorExternalRelays()
	if err != nil {
		t.Fatal(err)
	}
	pool := nostrAdapter.NewRelayPool(targets, zap.NewNop(), nostrAdapter.WithAuthSigner(signer))
	return testMirrorPool{pool}
}

func testMirror(t *testing.T, store *eventStore, service nostr.SecretKey, urls ...string) *mirrorForwarder {
	t.Helper()
	guard, err := relayguard.New(service.Public(), urls)
	if err != nil {
		t.Fatal(err)
	}
	mirror := &mirrorForwarder{
		store: store, policy: newPolicy(service.Public()), guard: guard, logger: zap.NewNop(),
		waitRetry: func(context.Context, time.Duration) bool { return false },
	}
	for _, url := range urls {
		cursor, err := store.mirrorCursor(url)
		if err != nil {
			t.Fatal(err)
		}
		target := &mirrorTarget{url: url, wake: make(chan struct{}, 1)}
		target.cursor.Store(cursor)
		mirror.targets = append(mirror.targets, target)
	}
	return mirror
}

func TestMirrorPerRelayOKFalseDuplicateAndRestartCursor(t *testing.T) {
	dir := t.TempDir()
	store := openTestEventStore(t, dir)
	store.shared.mirrorIndexEnabled.Store(true)
	service := nostr.Generate()
	event := signedStoreEvent(t, service, 1, nostr.Now(), nil, "replicate")
	if err := store.Save(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	first, second := "wss://first.example", "wss://second.example"
	mirror := testMirror(t, store, service, first, second)
	mirror.waitRetry = func(context.Context, time.Duration) bool {
		t.Fatal("blocked OK must park the relay, not retry on a timer")
		return false
	}
	mirror.publish = func(_ context.Context, got nostr.Event, targets []string) ([]nostrAdapter.PublishResult, error) {
		if got.ID != event.ID || len(targets) != 1 || targets[0] != first {
			t.Fatalf("unexpected publish: %s %v", got.ID.Hex(), targets)
		}
		return []nostrAdapter.PublishResult{{RelayURL: first, Reason: "blocked: refused"}}, nil
	}
	mirror.runTarget(t.Context(), mirror.targets[0])
	if cursor, err := store.mirrorCursor(first); err != nil || cursor != 0 {
		t.Fatalf("OK=false advanced cursor: %d, %v", cursor, err)
	}
	status, err := mirror.Status()
	if err != nil || status[0].Backlog != 1 || status[0].Failure != "blocked: refused" {
		t.Fatalf("rejected relay health = %+v, %v", status, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	mirror.publish = func(_ context.Context, got nostr.Event, targets []string) ([]nostrAdapter.PublishResult, error) {
		cancel()
		return []nostrAdapter.PublishResult{{RelayURL: first, Accepted: true}}, nil
	}
	mirror.runTarget(ctx, mirror.targets[0])
	if cursor, err := store.mirrorCursor(first); err != nil || cursor != 1 {
		t.Fatalf("OK=true cursor = %d, %v", cursor, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	mirror.publish = func(_ context.Context, got nostr.Event, targets []string) ([]nostrAdapter.PublishResult, error) {
		cancel()
		return []nostrAdapter.PublishResult{{RelayURL: second, Reason: "duplicate: already stored"}}, nil
	}
	mirror.runTarget(ctx, mirror.targets[1])
	if cursor, err := store.mirrorCursor(second); err != nil || cursor != 1 {
		t.Fatalf("duplicate OK cursor = %d, %v", cursor, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestEventStore(t, dir)
	if cursor, err := reopened.mirrorCursor(first); err != nil || cursor != 1 {
		t.Fatalf("restarted first cursor = %d, %v", cursor, err)
	}
	if cursor, err := reopened.mirrorCursor(second); err != nil || cursor != 1 {
		t.Fatalf("restarted second cursor = %d, %v", cursor, err)
	}
}

func TestMirrorStartupReconcilesCrashGapWithoutResendingAckedHistory(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	service := nostr.Generate()
	event := signedStoreEvent(t, service, 1, nostr.Now(), nil, "accepted before mirror index")
	if err := store.Save(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if last, err := store.mirrorLastSequence(); err != nil || last != 0 {
		t.Fatalf("disabled mirror queue = %d, %v", last, err)
	}
	if err := store.ensureMirrorIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.advanceMirrorCursor("wss://first.example", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureMirrorIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last, err := store.mirrorLastSequence(); err != nil || last != 1 {
		t.Fatalf("reconciled queue duplicated event: %d, %v", last, err)
	}
	if cursor, err := store.mirrorCursor("wss://first.example"); err != nil || cursor != 1 {
		t.Fatalf("acknowledged cursor moved: %d, %v", cursor, err)
	}
}

func TestMirrorIndexEnabledByReplacementHandleCoversActiveWrites(t *testing.T) {
	dir := t.TempDir()
	active := openTestEventStore(t, dir)
	replacement := openTestEventStore(t, dir)
	service := nostr.Generate()
	before := signedStoreEvent(t, service, 1, nostr.Now(), nil, "before reload")
	if err := active.Save(t.Context(), before); err != nil {
		t.Fatal(err)
	}
	if err := replacement.ensureMirrorIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := signedStoreEvent(t, service, 1, nostr.Now(), nil, "during reload")
	if err := active.Save(t.Context(), after); err != nil {
		t.Fatal(err)
	}
	if err := replacement.ensureMirrorIndex(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last, err := replacement.mirrorLastSequence(); err != nil || last != 2 {
		t.Fatalf("shared mirror queue last=%d, err=%v, want 2", last, err)
	}
	for _, want := range []nostr.ID{before.ID, after.ID} {
		var found bool
		for cursor := uint64(0); ; {
			seq, id, next, err := replacement.mirrorNext(cursor)
			if err != nil {
				t.Fatal(err)
			}
			if !next {
				break
			}
			if id == want {
				found = true
			}
			cursor = seq
		}
		if !found {
			t.Fatalf("event %s missing from shared mirror queue", want.Hex())
		}
	}
}

func TestMirrorSkipsEventCurrentPostingPolicyWouldRefuse(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	store.shared.mirrorIndexEnabled.Store(true)
	service := nostr.Generate()
	old := signedStoreEvent(t, service, 1, nostr.Timestamp(time.Now().Add(-2*365*24*time.Hour).Unix()), nil, "old")
	if err := store.Save(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	mirror := testMirror(t, store, service, "wss://first.example")
	mirror.publish = func(context.Context, nostr.Event, []string) ([]nostrAdapter.PublishResult, error) {
		t.Fatal("posting-policy rejected event was mirrored")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	mirror.policy.now = func() nostr.Timestamp {
		cancel()
		return nostr.Now()
	}
	mirror.runTarget(ctx, mirror.targets[0])
	if cursor, err := store.mirrorCursor("wss://first.example"); err != nil || cursor != 1 {
		t.Fatalf("posting-policy skip cursor = %d, %v", cursor, err)
	}
	if skipped := mirror.targets[0].skipped.Load(); skipped != 1 {
		t.Fatalf("posting-policy skip count = %d, want 1", skipped)
	}
}

func TestMirrorTargetsFailClosedAndSidecarSaveWakesForwarder(t *testing.T) {
	service := nostr.Generate()
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.MirrorExternal = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334/relay"
	cfg.Sidecar.BackendURL = "ws://localhost:3334/relay"
	cfg.ServiceRelays = []string{cfg.Sidecar.PublicURL, "wss://first.example"}
	if _, err := New(t.Context(), cfg, localSigner(t, service), zap.NewNop()); err == nil || !strings.Contains(err.Error(), "not in nostr.controlled_relays") {
		t.Fatalf("uncontrolled mirror target accepted: %v", err)
	}
	cfg.ControlledRelays = []string{"wss://first.example"}
	if _, err := New(t.Context(), cfg, localSigner(t, service), zap.NewNop()); err == nil || !strings.Contains(err.Error(), "requires a Nostr relay transport") {
		t.Fatalf("sidecar silently enabled mirror without a transport: %v", err)
	}
	signer := localSigner(t, service)
	server, err := New(t.Context(), cfg, signer, zap.NewNop(), WithMirrorTransport(testMirrorTransport(t, cfg, signer)))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSidecarTest(t, server)
	event := signedStoreEvent(t, service, 1, nostr.Now(), nil, "new")
	if _, err := server.Relay().AddEvent(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.mirror.targets[0].wake:
	default:
		t.Fatal("accepted store event did not wake sidecar mirror")
	}
	if seq, id, found, err := server.store.mirrorNext(0); err != nil || !found || seq != 1 || id != event.ID {
		t.Fatalf("stored event not indexed for mirror: seq=%d id=%s found=%t err=%v", seq, id.Hex(), found, err)
	}
	ready := httptest.NewRecorder()
	server.Handler().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"status":"warn"`) || !strings.Contains(ready.Body.String(), `"backlog":1`) {
		t.Fatalf("mirror readiness = %d %s", ready.Code, ready.Body.String())
	}
	metrics := httptest.NewRecorder()
	server.Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), `bahia_relay_sidecar_mirror_backlog_events{relay="wss://first.example"} 1`) {
		t.Fatalf("mirror metrics = %d %s", metrics.Code, metrics.Body.String())
	}
	cfg.ServiceRelays = []string{"wss://relay.damus.io"}
	if _, err := config.Defaults().Nostr.MirrorExternalRelays(); err == nil {
		// A disabled sidecar cannot be a mirroring boundary.
		t.Fatal("disabled sidecar accepted mirror target request")
	}
	if _, err := cfg.MirrorExternalRelays(); err == nil || !strings.Contains(err.Error(), "public service relay") {
		t.Fatalf("public mirror target accepted: %v", err)
	}
}

func TestSidecarMirrorForwardsAcceptedEventsToExternalRelay(t *testing.T) {
	service := nostr.Generate()
	externalCfg := sidecarTestConfig(t)
	externalCfg.Sidecar.Enabled = true
	externalCfg.Sidecar.PublicURL = "ws://localhost:3334/relay"
	external, err := New(t.Context(), externalCfg, localSigner(t, service), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer closeSidecarTest(t, external)
	accepted := make(chan nostr.ID, 2)
	prior := external.Relay().OnEventSaved
	external.Relay().OnEventSaved = func(ctx context.Context, event nostr.Event) {
		prior(ctx, event)
		accepted <- event.ID
	}
	httpServer := httptest.NewServer(external.Handler())
	defer httpServer.Close()
	externalURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")

	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334/relay"
	cfg.Sidecar.BackendURL = cfg.Sidecar.PublicURL
	cfg.Sidecar.MirrorExternal = true
	cfg.ServiceRelays = []string{cfg.Sidecar.PublicURL, externalURL}
	cfg.ControlledRelays = []string{externalURL}
	signer := localSigner(t, service)
	sidecar, err := New(t.Context(), cfg, signer, zap.NewNop(), WithMirrorTransport(testMirrorTransport(t, cfg, signer)))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSidecarTest(t, sidecar)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sidecar.mirror.Run(ctx)
	}()
	first := signedStoreEvent(t, service, 1, nostr.Now(), nil, "first")
	second := signedStoreEvent(t, service, 1, nostr.Now(), nil, "second")
	for _, event := range []nostr.Event{first, second} {
		if _, err := sidecar.Relay().AddEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []nostr.ID{first.ID, second.ID} {
		select {
		case got := <-accepted:
			if got != want {
				t.Fatalf("external event = %s, want %s", got.Hex(), want.Hex())
			}
		case <-ctx.Done():
			t.Fatalf("mirror did not publish %s: %v", want.Hex(), ctx.Err())
		}
	}
	if status, err := sidecar.mirror.Status(); err != nil || status[0].Cursor < 1 {
		t.Fatalf("mirror did not persist first external OK: %+v, %v", status, err)
	}
	cancel()
	<-done
}

func TestMirrorRateLimitRetriesWithoutDeliveryPolling(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	store.shared.mirrorIndexEnabled.Store(true)
	service := nostr.Generate()
	event := signedStoreEvent(t, service, 1, nostr.Now(), nil, "retry")
	if err := store.Save(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	url := "wss://first.example"
	mirror := testMirror(t, store, service, url)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts, retries := 0, 0
	mirror.publish = func(context.Context, nostr.Event, []string) ([]nostrAdapter.PublishResult, error) {
		attempts++
		if attempts == 1 {
			return []nostrAdapter.PublishResult{{RelayURL: url, Reason: "rate-limited: slow down"}}, nil
		}
		cancel()
		return []nostrAdapter.PublishResult{{RelayURL: url, Accepted: true}}, nil
	}
	mirror.waitRetry = func(context.Context, time.Duration) bool {
		retries++
		return true
	}
	mirror.runTarget(ctx, mirror.targets[0])
	if attempts != 2 || retries != 1 {
		t.Fatalf("rate-limit delivery attempts=%d retries=%d, want 2/1", attempts, retries)
	}
	if cursor, err := store.mirrorCursor(url); err != nil || cursor != 1 {
		t.Fatalf("rate-limit recovery cursor = %d, %v", cursor, err)
	}
}

func TestMirrorTransportErrorWithoutRelayResultRetries(t *testing.T) {
	store := openTestEventStore(t, t.TempDir())
	store.shared.mirrorIndexEnabled.Store(true)
	service := nostr.Generate()
	event := signedStoreEvent(t, service, 1, nostr.Now(), nil, "retry transport")
	if err := store.Save(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	url := "wss://first.example"
	mirror := testMirror(t, store, service, url)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts, retries := 0, 0
	mirror.publish = func(context.Context, nostr.Event, []string) ([]nostrAdapter.PublishResult, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("relay transport unavailable")
		}
		cancel()
		return []nostrAdapter.PublishResult{{RelayURL: url, Accepted: true}}, nil
	}
	mirror.waitRetry = func(context.Context, time.Duration) bool {
		retries++
		return true
	}
	mirror.runTarget(ctx, mirror.targets[0])
	if attempts != 2 || retries != 1 {
		t.Fatalf("transport delivery attempts=%d retries=%d, want 2/1", attempts, retries)
	}
	if cursor, err := store.mirrorCursor(url); err != nil || cursor != 1 {
		t.Fatalf("transport recovery cursor = %d, %v", cursor, err)
	}
}
