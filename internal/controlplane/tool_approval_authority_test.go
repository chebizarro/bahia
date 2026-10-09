package controlplane

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func signedToolApprovalIntent(t *testing.T, key string, target uuid.UUID) *Intent {
	t.Helper()
	event := signedLLMRequest(t, key, 30900,
		fmt.Sprintf(`{"intent_id":%q,"action":"approve","reason":"reviewed by operator"}`, target),
		nostr.Tags{{"d", "tool-approval:" + target.String()}, {"domain", "tool"}, {"op", "approval-response"},
			{"schema", "bahia.intent.tool.v1"}, {"org", testOrgID().String()},
			{"intent_id", uuid.NewString()}, {"t", "bahia-intent"}})
	intent, err := ParseIntent(event)
	require.NoError(t, err)
	intent.Actor = event.PubKey.Hex()
	return intent
}

func TestToolApprovalRejectsFabricatedAndDivergentAuthority(t *testing.T) {
	key := nostr.Generate().Hex()
	actor := testNostrPubKeyHexFromPrivateKey(t, key)
	target := uuid.New()
	repo := newAtomicToolApprovalRepo(target, domain.ToolProvisionStatusAwaitingApproval)
	repo.intent.ResolvedTools = []domain.ResolvedTool{{Name: "sql-only-package", Manager: "apt"}}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
	handler := NewToolIntentHandler(reactor)
	valid := signedToolApprovalIntent(t, key, target)
	for name, change := range map[string]func(*Intent){
		"fabricated MCP signature": func(i *Intent) { copy := *i.Event; copy.Sig = [64]byte{}; i.Event = &copy },
		"wrong actor":              func(i *Intent) { i.Actor = testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex()) },
		"wrong resource":           func(i *Intent) { i.Coordinate = "tool-approval:" + uuid.NewString() },
		"divergent content": func(i *Intent) {
			i.Content = map[string]any{"intent_id": target.String(), "action": "reject", "reason": "changed"}
		},
		"tampered signed body": func(i *Intent) { copy := *i.Event; copy.Content = `{"action":"reject"}`; i.Event = &copy },
	} {
		t.Run(name, func(t *testing.T) {
			intent := *valid
			change(&intent)
			require.ErrorIs(t, handler.HandleIntent(t.Context(), &intent), errToolApprovalAuthority)
			calls, applied, logs, status := repo.counts()
			require.Zero(t, calls+applied+logs)
			require.Equal(t, domain.ToolProvisionStatusAwaitingApproval, status)
		})
	}
}

func TestToolApprovalProcessorDoesNotSignFabricatedMCPOutcome(t *testing.T) {
	key := nostr.Generate().Hex()
	actor := testNostrPubKeyHexFromPrivateKey(t, key)
	target := uuid.New()
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(newAtomicToolApprovalRepo(target, domain.ToolProvisionStatusAwaitingApproval)))
	p, statuses := d70Processor(t, "tool", actor, NewToolIntentHandler(reactor))
	unsigned := signedToolApprovalIntent(t, key, target)
	copy := *unsigned.Event
	copy.Sig = [64]byte{}
	unsigned.Event = &copy
	p.markProcessed(unsigned)
	require.True(t, p.IsProcessed(unsigned.IntentID))
	require.ErrorIs(t, p.ProcessInProcess(t.Context(), unsigned), errToolApprovalAuthority)
	require.Empty(t, statuses.events)
	valid := signedToolApprovalIntent(t, key, target)
	p.markProcessed(valid)
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), valid), "tool approval paused")
	require.Len(t, statuses.events, 1)
	require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
}

func TestToolApprovalRefusalStatusFailureIsObservable(t *testing.T) {
	key := nostr.Generate().Hex()
	actor := testNostrPubKeyHexFromPrivateKey(t, key)
	target := uuid.New()
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(newAtomicToolApprovalRepo(target, domain.ToolProvisionStatusAwaitingApproval)))
	status := NewIntentStatusPublisher(func(context.Context, nostr.Event) error { return errors.New("relay unavailable") }, &testSigner{}, zap.NewNop())
	p := NewIntentProcessor(NewTrustSet([]string{actor}, zap.NewNop()), openTestStore(t), status,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"tool": true}}, zap.NewNop())
	p.RegisterHandler("tool", NewToolIntentHandler(reactor))
	intent := signedToolApprovalIntent(t, key, target)
	require.ErrorContains(t, p.ProcessInProcess(t.Context(), intent), "rejection status publication failed")
	require.False(t, p.IsProcessed(intent.IntentID))
}

func TestToolApprovalReplayAcrossRestartNeverConsumesSQLApproval(t *testing.T) {
	key := nostr.Generate().Hex()
	actor := testNostrPubKeyHexFromPrivateKey(t, key)
	target := uuid.New()
	repo := newAtomicToolApprovalRepo(target, domain.ToolProvisionStatusAwaitingApproval)
	repo.intent.ResolvedTools = []domain.ResolvedTool{{Name: "divergent-sql-tool", Manager: "apt"}}
	intent := signedToolApprovalIntent(t, key, target)
	var statuses []nostr.Event
	status := NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		statuses = append(statuses, ev)
		return nil
	}, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	path := filepath.Join(t.TempDir(), "tool-approval-intents.db")
	for attempt := 0; attempt < 2; attempt++ {
		store, err := localstore.Open(path)
		require.NoError(t, err)
		reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
		processor := NewIntentProcessor(NewTrustSet([]string{actor}, zap.NewNop()), store, status,
			IntentProcessorConfig{EnabledDomains: map[string]bool{"tool": true}}, zap.NewNop())
		processor.RegisterHandler("tool", NewToolIntentHandler(reactor))
		require.ErrorContains(t, processor.ProcessInProcess(t.Context(), intent), "tool approval paused")
		require.False(t, processor.IsProcessed(intent.IntentID))
		require.NoError(t, store.Close())
	}
	require.Len(t, statuses, 2)
	for _, event := range statuses {
		require.True(t, event.CheckID())
		require.True(t, event.VerifySignature())
		require.Equal(t, "rejected", tagValueNostr(event.Tags, "status"))
		require.Equal(t, intent.Event.ID.Hex(), tagValueNostr(event.Tags, "e"))
	}
	calls, applied, logs, state := repo.counts()
	require.Zero(t, calls+applied+logs)
	require.Equal(t, domain.ToolProvisionStatusAwaitingApproval, state)
}
