package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func signedMCPBackupRunEvent(t *testing.T, key nostr.SecretKey) nostr.Event {
	t.Helper()
	id, err := uuid.NewV7()
	require.NoError(t, err)
	recipeID, repoID := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	repository := domain.BackupRepository{ID: repoID, Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/backup", CreatedAt: now, UpdatedAt: now}
	recipe := domain.BackupRecipe{ID: recipeID, Name: "database", Version: "1", Backend: domain.BackupBackendKopia, RepositoryID: repoID, TargetRef: "/database", VerificationMode: domain.BackupVerificationNone, CreatedAt: now, UpdatedAt: now}
	req := client.PublishIntentRequest{Domain: "backup", Op: "run", OrgID: uuid.NewString(), Coordinate: "backup-run:" + id.String(), IntentID: "backup-request:" + id.String(), Content: map[string]any{
		"id": id.String(), "recipe_id": recipeID.String(), "repository_id": repoID.String(),
		"backend": recipe.Backend, "target_ref": recipe.TargetRef, "verification_mode": recipe.VerificationMode,
		"execution_snapshot": domain.BackupExecutionSnapshot{RecipeEventID: nostr.Generate().Public().Hex(), RepositoryEventID: nostr.Generate().Public().Hex(), Recipe: recipe, Repository: repository},
	}}
	event, err := (&client.IntentPublisher{}).BuildIntentEvent(req)
	require.NoError(t, err)
	require.NoError(t, event.Sign(key))
	return event
}

func signedEventArgs(t *testing.T, event nostr.Event) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, json.Unmarshal(encoded, &object))
	return map[string]any{"signed_intent_event": object}
}

func TestMCPBackupRunRequiresRelayObservedOperatorSignature(t *testing.T) {
	tool := backupToolDefinition("request_backup_run")
	require.Equal(t, []string{"signed_intent_event"}, tool.InputSchema["required"])
	require.NotContains(t, tool.InputSchema["properties"].(map[string]interface{}), "recipe_id")
	key := nostr.Generate()
	actor := key.Public().Hex()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	store := testStateStore(t)
	server.stateStore = store
	processor := controlplane.NewIntentProcessor(controlplane.NewTrustSet([]string{actor}, zap.NewNop()), store, nil,
		controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"backup": true}}, zap.NewNop())
	processor.RegisterHandler("backup", controlplane.NewBackupIntentHandler(controlplane.BackupIntentHandlerConfig{Logger: zap.NewNop()}))
	server.intentProc = processor
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: actor, Method: auth.MethodNIP98})
	event := signedMCPBackupRunEvent(t, key)
	intent, err := controlplane.ParseIntent(&event)
	require.NoError(t, err)
	for _, name := range []string{"request_backup_run", "bahia_request_backup_run"} {
		unsigned, err := server.CallTool(ctx, name, map[string]any{"recipe_id": uuid.NewString()})
		require.NoError(t, err)
		require.True(t, unsigned.IsError)
		require.Equal(t, "rejected", mcpIntentResult(t, unsigned)["status"])
		require.Contains(t, mcpIntentResult(t, unsigned)["reason"], "operator-signed")
	}
	args := signedEventArgs(t, event)
	missing, err := server.CallTool(ctx, "request_backup_run", args)
	require.NoError(t, err)
	require.True(t, missing.IsError)
	require.Contains(t, mcpIntentResult(t, missing)["reason"], "local relay subscription")
	require.False(t, processor.IsProcessed(intent.IntentID))

	tampered := event
	tampered.Content += " "
	bad, err := server.CallTool(ctx, "request_backup_run", signedEventArgs(t, tampered))
	require.NoError(t, err)
	require.True(t, bad.IsError)
	require.Contains(t, mcpIntentResult(t, bad)["reason"], "event id does not match")

	other := nostr.Generate().Public().Hex()
	otherCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "other", PubKey: other, Method: auth.MethodNIP98})
	mismatch, err := server.CallTool(otherCtx, "request_backup_run", args)
	require.NoError(t, err)
	require.True(t, mismatch.IsError)
	require.Contains(t, mcpIntentResult(t, mismatch)["reason"], "author differs")

	_, err = store.SaveEvent(event)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		result, err := server.CallTool(ctx, "request_backup_run", args)
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Contains(t, mcpIntentResult(t, result)["reason"], "canonical acceptance receipts are unavailable")
	}
	require.False(t, processor.IsProcessed(intent.IntentID))
}
