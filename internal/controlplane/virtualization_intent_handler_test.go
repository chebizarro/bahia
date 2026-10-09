package controlplane

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func virtualizationSignedRequest(t *testing.T, key nostr.SecretKey, org uuid.UUID, request virtualizationOperationRequest) *nostr.Event {
	t.Helper()
	content, err := json.Marshal(request)
	require.NoError(t, err)
	ev := &nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", "vm-operation:" + request.OperationID.String()},
		{"domain", "virtualization"}, {"schema", "bahia.intent.virtualization.v1"},
		{"op", "request"}, {"org", org.String()},
		{"intent_id", request.IdempotencyKey}, {"t", "bahia-intent"}, {"t", "virtualization"},
	}, Content: string(content)}
	require.NoError(t, ev.Sign(key))
	return ev
}

func virtualizationParsedRequest(t *testing.T, ev *nostr.Event) *Intent {
	t.Helper()
	intent, err := ParseIntent(ev)
	require.NoError(t, err)
	intent.Actor = ev.PubKey.Hex()
	return intent
}

func TestVirtualizationIntentRefusesValidRequestWithoutCanonicalExecutor(t *testing.T) {
	actor := nostr.Generate()
	org := uuid.New()
	request := virtualizationOperationRequest{OperationID: uuid.Must(uuid.NewV7()), ResourceID: uuid.New(), ResourceKind: domain.PersistentVMResource, Action: domain.VMOperationStart, ExpectedGeneration: 1, IdempotencyKey: uuid.Must(uuid.NewV7()).String(), Reason: "operator requested start"}
	ev := virtualizationSignedRequest(t, actor, org, request)
	intent := virtualizationParsedRequest(t, ev)
	storePath := filepath.Join(t.TempDir(), "intent.db")
	var statuses []nostr.Event
	status := NewIntentStatusPublisher(func(_ context.Context, ev nostr.Event) error {
		statuses = append(statuses, ev)
		return nil
	}, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	newProcessor := func(store *localstore.Store) *IntentProcessor {
		processor := NewIntentProcessor(NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{org.String(): actor.Public().Hex()})), store, status, IntentProcessorConfig{EnabledDomains: map[string]bool{"virtualization": true}}, zap.NewNop())
		processor.RegisterHandler("virtualization", NewVirtualizationIntentHandler())
		return processor
	}
	for attempt := 0; attempt < 2; attempt++ {
		store, err := localstore.Open(storePath)
		require.NoError(t, err)
		processor := newProcessor(store)
		require.ErrorIs(t, processor.ProcessRelayIntent(context.Background(), ev), readmodel.ErrVirtualizationUnavailable)
		require.False(t, processor.IsProcessed(intent.IntentID), "refused requests cannot become accepted across restart")
		require.NoError(t, store.Close())
	}
	require.Len(t, statuses, 2)
	for _, event := range statuses {
		require.True(t, event.CheckID())
		require.True(t, event.VerifySignature())
		require.Equal(t, "rejected", extractTag(event, "status"))
		require.Equal(t, ev.ID.Hex(), extractTag(event, "e"))
	}
	foreign := virtualizationSignedRequest(t, nostr.Generate(), org, request)
	store, err := localstore.Open(storePath)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	processor := newProcessor(store)
	require.NoError(t, processor.ProcessRelayIntent(t.Context(), foreign),
		"unknown signer is dropped before virtualization admission")
	require.Len(t, statuses, 2)
	require.False(t, processor.IsProcessed(intent.IntentID))
	require.Equal(t, domain.PermWriteDeployments, NewVirtualizationIntentHandler().PermissionFor("request"))
}

func TestVirtualizationIntentRejectsUnboundOrForgedRequests(t *testing.T) {
	actor := nostr.Generate()
	org := uuid.New()
	base := virtualizationOperationRequest{OperationID: uuid.Must(uuid.NewV7()), ResourceID: uuid.New(), ResourceKind: domain.PersistentVMResource, Action: domain.VMOperationStart, ExpectedGeneration: 1, IdempotencyKey: uuid.Must(uuid.NewV7()).String(), Reason: "operator requested start"}
	for name, mutate := range map[string]func(*virtualizationOperationRequest){
		"wrong resource kind": func(r *virtualizationOperationRequest) { r.ResourceKind = domain.ExecutionPlaneResource },
		"wrong action":        func(r *virtualizationOperationRequest) { r.Action = domain.VMOperationDelete },
		"absent generation":   func(r *virtualizationOperationRequest) { r.ExpectedGeneration = 0 },
		"absent resource":     func(r *virtualizationOperationRequest) { r.ResourceID = uuid.Nil },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			ev := virtualizationSignedRequest(t, actor, org, request)
			require.Error(t, NewVirtualizationIntentHandler().HandleIntent(t.Context(), virtualizationParsedRequest(t, ev)))
		})
	}
	t.Run("wrong coordinate", func(t *testing.T) {
		ev := virtualizationSignedRequest(t, actor, org, base)
		ev.Tags[0][1] = "vm-operation:" + uuid.NewString()
		require.NoError(t, ev.Sign(actor))
		require.ErrorContains(t, NewVirtualizationIntentHandler().HandleIntent(t.Context(), virtualizationParsedRequest(t, ev)), "binding")
	})
	t.Run("wrong signer", func(t *testing.T) {
		ev := virtualizationSignedRequest(t, nostr.Generate(), org, base)
		intent := virtualizationParsedRequest(t, ev)
		intent.Actor = actor.Public().Hex()
		require.ErrorContains(t, NewVirtualizationIntentHandler().HandleIntent(t.Context(), intent), "envelope")
	})
	t.Run("tampered signature", func(t *testing.T) {
		ev := virtualizationSignedRequest(t, actor, org, base)
		ev.Content += " "
		require.ErrorContains(t, NewVirtualizationIntentHandler().HandleIntent(t.Context(), virtualizationParsedRequest(t, ev)), "signed")
	})
	t.Run("divergent content", func(t *testing.T) {
		ev := virtualizationSignedRequest(t, actor, org, base)
		intent := virtualizationParsedRequest(t, ev)
		intent.Content["resource_id"] = uuid.NewString()
		require.ErrorContains(t, NewVirtualizationIntentHandler().HandleIntent(t.Context(), intent), "envelope")
	})
	t.Run("duplicate domain tag", func(t *testing.T) {
		ev := virtualizationSignedRequest(t, actor, org, base)
		ev.Tags = append(ev.Tags, nostr.Tag{"domain", "virtualization"})
		require.NoError(t, ev.Sign(actor))
		require.ErrorContains(t, NewVirtualizationIntentHandler().HandleIntent(t.Context(), virtualizationParsedRequest(t, ev)), "envelope")
	})
}
