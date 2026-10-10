package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/relayadmin"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// sidecarTestHarness holds an in-process sidecar with its admin client and
// websocket URL, used by syncer tests.
type sidecarTestHarness struct {
	sidecar     *relaysidecar.Server
	adminClient *relayadmin.Client
	wsURL       string
	// serviceKey is the sidecar's nostr.private_key: the daemon's identity,
	// always admitted by the default read_auth_mode (enforce).
	serviceKey nostr.SecretKey
}

type observedIntentAuthorsAdmin struct {
	inner   IntentAuthorsAdmin
	applied chan []string
}

type scriptedIntentAuthorsCall struct {
	pubkeys []string
	reply   chan error
}

type scriptedIntentAuthorsAdmin struct {
	calls chan scriptedIntentAuthorsCall
}

func (a *scriptedIntentAuthorsAdmin) SetIntentAuthors(ctx context.Context, _ string, pubkeys []string) error {
	call := scriptedIntentAuthorsCall{pubkeys: append([]string(nil), pubkeys...), reply: make(chan error, 1)}
	select {
	case a.calls <- call:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-call.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func awaitIntentAuthorsCall(t *testing.T, ctx context.Context, calls <-chan scriptedIntentAuthorsCall, want []string) scriptedIntentAuthorsCall {
	t.Helper()
	select {
	case call := <-calls:
		require.Equal(t, want, call.pubkeys)
		return call
	case <-ctx.Done():
		t.Fatal("intent author sync call did not arrive", ctx.Err())
		return scriptedIntentAuthorsCall{}
	}
}

func (a *observedIntentAuthorsAdmin) SetIntentAuthors(ctx context.Context, ref string, pubkeys []string) error {
	if err := a.inner.SetIntentAuthors(ctx, ref, pubkeys); err != nil {
		return err
	}
	copyOfPubkeys := append([]string(nil), pubkeys...)
	select {
	case a.applied <- copyOfPubkeys:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func awaitAppliedIntentAuthors(t *testing.T, ctx context.Context, applied <-chan []string, want []string) {
	t.Helper()
	select {
	case got := <-applied:
		require.Equal(t, want, got)
	case <-ctx.Done():
		t.Fatal("intent author sync did not complete", ctx.Err())
	}
}

func startSidecarTestHarness(t *testing.T, adminKey nostr.SecretKey) sidecarTestHarness {
	t.Helper()

	dataDir := t.TempDir()
	policyPath := filepath.Join(dataDir, "relay-admin-policy.json")
	policy := map[string]any{
		"version":        1,
		"administrators": []string{adminKey.Public().Hex()},
		"allowed_pubkeys": []map[string]string{
			{"pubkey": adminKey.Public().Hex(), "reason": "admin"},
		},
		"banned_pubkeys":      []any{},
		"metadata":            map[string]string{"name": "test", "description": "test"},
		"used_authorizations": map[string]any{},
		"applied_config":      map[string]any{},
	}
	policyJSON, err := json.MarshalIndent(policy, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(policyPath, policyJSON, 0o600))

	// NIP-42 binds AUTH events to the relay URL, so the sidecar's public URL
	// must be the address the daemon pool dials.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serviceKey := nostr.Generate()
	sidecarCfg := config.Defaults().Nostr
	sidecarCfg.PrivateKey = serviceKey.Hex()
	sidecarCfg.Sidecar.DataDir = dataDir
	sidecarCfg.Sidecar.Enabled = true
	sidecarCfg.Sidecar.PublicURL = "ws://" + listener.Addr().String()
	sidecarCfg.Sidecar.AdministratorPubkeys = []string{adminKey.Public().Hex()}
	sidecarCfg.Sidecar.AdminPolicyPath = policyPath
	sidecar, err := relaysidecar.New(sidecarCfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sidecar.Close() })

	httpServer := httptest.NewUnstartedServer(sidecar.Handler())
	httpServer.Listener = listener
	httpServer.Start()
	t.Cleanup(httpServer.Close)

	adminClient, err := relayadmin.NewClient(relayadmin.Config{
		Enabled: true,
		Signer:  keyer.NewPlainKeySigner(adminKey),
		Targets: []relayadmin.Target{{
			Ref:                  "test-sidecar",
			RelayURL:             sidecarCfg.Sidecar.PublicURL,
			HTTPURL:              httpServer.URL,
			AdministratorPubkeys: []string{adminKey.Public().Hex()},
		}},
	})
	require.NoError(t, err)

	return sidecarTestHarness{
		sidecar:     sidecar,
		adminClient: adminClient,
		wsURL:       "ws" + strings.TrimPrefix(httpServer.URL, "http"),
		serviceKey:  serviceKey,
	}
}

func signedIntentForTest(t *testing.T, sk nostr.SecretKey, orgID string) nostr.Event {
	t.Helper()
	ev := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-test-" + sk.Public().Hex()[:8]},
			{"t", "bahia-intent"},
			{"domain", "service"},
			{"op", "create"},
			{"org", orgID},
			{"intent_id", "test-" + sk.Public().Hex()[:8]},
		},
		Content: `{"name":"test-service"}`,
	}
	require.NoError(t, ev.Sign(sk))
	return ev
}

// TestIntentAuthorsSyncerPushesToSidecar verifies the end-to-end flow:
//   - A TrustSet with a bootstrap owner
//   - An in-process sidecar with an admin endpoint and restricted writes
//   - The syncer pushes the bootstrap owner's pubkey to the sidecar
//   - The owner's intent event is accepted by the sidecar
//   - A non-member's intent event is blocked
func TestIntentAuthorsSyncerPushesToSidecar(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminKey := nostr.Generate()
	memberKey := nostr.Generate()
	outsiderKey := nostr.Generate()

	h := startSidecarTestHarness(t, adminKey)

	orgID := "00000000-0000-0000-0000-000000000001"
	trustSet := NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{
		orgID: memberKey.Public().Hex(),
	}))
	admin := &observedIntentAuthorsAdmin{inner: h.adminClient, applied: make(chan []string, 1)}

	syncer := NewIntentAuthorsSyncer(IntentAuthorsSyncerConfig{
		TrustSet:   trustSet,
		Admin:      admin,
		TargetRefs: []string{"test-sidecar"},
		Logger:     zap.NewNop(),
	})

	syncCtx, syncCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = syncer.Run(syncCtx)
	}()
	awaitAppliedIntentAuthors(t, ctx, admin.applied, []string{memberKey.Public().Hex()})

	relay, err := nostr.RelayConnect(ctx, h.wsURL, nostr.RelayOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })

	err = relay.Publish(ctx, signedIntentForTest(t, memberKey, orgID))
	require.NoError(t, err, "bootstrap owner's intent should be accepted")

	err = relay.Publish(ctx, signedIntentForTest(t, outsiderKey, orgID))
	require.Error(t, err, "non-member's intent should be blocked")

	syncCancel()
	<-done
}

func TestIntentAuthorsSyncerInitialEmptySetClearsSidecar(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	adminKey := nostr.Generate()
	staleAuthor := nostr.Generate()
	h := startSidecarTestHarness(t, adminKey)
	require.NoError(t, h.adminClient.SetIntentAuthors(ctx, "test-sidecar", []string{staleAuthor.Public().Hex()}))

	relay, err := nostr.RelayConnect(ctx, h.wsURL, nostr.RelayOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	orgID := "00000000-0000-0000-0000-000000000001"
	require.NoError(t, relay.Publish(ctx, signedIntentForTest(t, staleAuthor, orgID)))

	syncer := NewIntentAuthorsSyncer(IntentAuthorsSyncerConfig{
		TrustSet: NewTrustSet(nil, zap.NewNop()), Admin: h.adminClient,
		TargetRefs: []string{"test-sidecar"}, Logger: zap.NewNop(),
	})
	syncer.push(ctx)
	require.False(t, syncer.SyncStatus().OutOfSync)
	afterClear := signedIntentForTest(t, staleAuthor, orgID)
	afterClear.Tags = append(afterClear.Tags, nostr.Tag{"nonce", "after-clear"})
	require.NoError(t, afterClear.Sign(staleAuthor))
	require.ErrorContains(t, relay.Publish(ctx, afterClear), "blocked")
}

func TestIntentAuthorsSyncerRevocationSupersedesFailedAddition(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admin := &scriptedIntentAuthorsAdmin{calls: make(chan scriptedIntentAuthorsCall)}
	syncer := NewIntentAuthorsSyncer(IntentAuthorsSyncerConfig{
		TrustSet: NewTrustSet(nil, zap.NewNop()), Admin: admin,
		TargetRefs: []string{"test-sidecar"}, Logger: zap.NewNop(),
	})
	done := make(chan error, 1)
	go func() { done <- syncer.Run(ctx) }()
	initial := awaitIntentAuthorsCall(t, ctx, admin.calls, nil)
	initial.reply <- nil

	firstMember := nostr.Generate().Public().Hex()
	secondMember := nostr.Generate().Public().Hex()
	syncer.TrackPubkey(firstMember)
	initialAddition := awaitIntentAuthorsCall(t, ctx, admin.calls, []string{firstMember})
	initialAddition.reply <- nil
	syncer.TrackPubkey(secondMember)
	both := []string{firstMember, secondMember}
	sort.Strings(both)
	failedAddition := awaitIntentAuthorsCall(t, ctx, admin.calls, both)
	failedAddition.reply <- errors.New("transient relay administration failure")
	syncer.UntrackPubkey(firstMember)
	revocation := awaitIntentAuthorsCall(t, ctx, admin.calls, []string{secondMember})
	revocation.reply <- nil

	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

// TestIntentAuthorsSyncerMembershipMutationReachesSidecar verifies that a
// Postgres org membership change (via the NotifyingOrgMemberRepository)
// propagates to the sidecar so the new member's intents are accepted and,
// after removal, are blocked again.
func TestIntentAuthorsSyncerMembershipMutationReachesSidecar(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminKey := nostr.Generate()
	newMemberKey := nostr.Generate()

	h := startSidecarTestHarness(t, adminKey)

	orgID := "00000000-0000-0000-0000-000000000001"
	trustSet := NewTrustSet(nil, zap.NewNop())
	admin := &observedIntentAuthorsAdmin{inner: h.adminClient, applied: make(chan []string, 4)}

	syncer := NewIntentAuthorsSyncer(IntentAuthorsSyncerConfig{
		TrustSet:   trustSet,
		Admin:      admin,
		TargetRefs: []string{"test-sidecar"},
		Logger:     zap.NewNop(),
	})

	syncCtx, syncCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = syncer.Run(syncCtx)
	}()
	awaitAppliedIntentAuthors(t, ctx, admin.applied, nil)

	// Before tracking: new member's intent is blocked.
	relay, err := nostr.RelayConnect(ctx, h.wsURL, nostr.RelayOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })

	err = relay.Publish(ctx, signedIntentForTest(t, newMemberKey, orgID))
	require.Error(t, err, "should be blocked before membership is added")

	// Simulate Postgres Add via TrackPubkey (what NotifyingOrgMemberRepository does).
	syncer.TrackPubkey(newMemberKey.Public().Hex())
	awaitAppliedIntentAuthors(t, ctx, admin.applied, []string{newMemberKey.Public().Hex()})

	// After tracking: new member's intent is accepted.
	afterAdd := signedIntentForTest(t, newMemberKey, orgID)
	afterAdd.Tags = append(afterAdd.Tags, nostr.Tag{"nonce", "after-add"})
	require.NoError(t, afterAdd.Sign(newMemberKey))
	err = relay.Publish(ctx, afterAdd)
	require.NoError(t, err, "should be accepted after membership is added")

	// Simulate Postgres Remove via UntrackPubkey.
	syncer.UntrackPubkey(newMemberKey.Public().Hex())
	awaitAppliedIntentAuthors(t, ctx, admin.applied, nil)

	// After removal: intent is blocked again.
	afterRemoval := signedIntentForTest(t, newMemberKey, orgID)
	afterRemoval.Tags = append(afterRemoval.Tags, nostr.Tag{"nonce", "after-removal"})
	require.NoError(t, afterRemoval.Sign(newMemberKey))
	err = relay.Publish(ctx, afterRemoval)
	require.Error(t, err, "should be blocked after membership is removed")

	syncCancel()
	<-done
}
