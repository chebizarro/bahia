package controlplane

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBackupExecutionSnapshotRequiresSignedACKedRegistryVersions(t *testing.T) {
	dir := t.TempDir()
	events, err := localstore.Open(filepath.Join(dir, "events.db"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.db"))
	require.NoError(t, err)
	defer func() { _ = events.Close(); _ = outbox.Close() }()
	key := nostr.Generate()
	now := time.Now().UTC().Truncate(time.Second)
	repo := domain.BackupRepository{ID: uuid.New(), Name: "repo", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/data", CreatedAt: now, UpdatedAt: now}
	policy := domain.BackupPolicy{ID: uuid.New(), Name: "policy", RequireVerification: false, VerificationMode: domain.BackupVerificationNone, CreatedAt: now, UpdatedAt: now}
	recipe := domain.BackupRecipe{ID: uuid.New(), Name: "recipe", Version: "1", Backend: domain.BackupBackendKopia, RepositoryID: repo.ID, PolicyID: &policy.ID, TargetRef: "/data", VerificationMode: domain.BackupVerificationNone, CreatedAt: now, UpdatedAt: now}
	recipeTags, recipeContent := nostradapter.BackupRecipeRegistryRecord(&recipe, false)
	recipeEvent := configSnapshotEvent(t, key, kinds.BackupRecipeRegistry, kinds.CPStateTopicBackupRecipe, nostradapter.BackupRecipeDTag(recipe.ID), recipeTags, recipeContent)
	repoTags, repoContent := nostradapter.BackupRepositoryRegistryRecord(&repo, false)
	repoEvent := configSnapshotEvent(t, key, kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), repoTags, repoContent)
	policyTags, policyContent := nostradapter.BackupPolicyRegistryRecord(&policy, false)
	policyEvent := configSnapshotEvent(t, key, kinds.BackupPolicyRegistry, kinds.CPStateTopicBackupPolicy, nostradapter.BackupPolicyDTag(policy.ID), policyTags, policyContent)
	snapshot := &domain.BackupExecutionSnapshot{RecipeEventID: recipeEvent.ID.Hex(), RepositoryEventID: repoEvent.ID.Hex(), PolicyEventID: policyEvent.ID.Hex(), Recipe: recipe, Repository: repo, Policy: &policy}
	reader, err := NewLocalBackupRunReceipts(events, outbox, key.Public().Hex())
	require.NoError(t, err)
	proof := reader.(backupExecutionConfigProof)
	for _, ev := range []nostr.Event{recipeEvent, repoEvent, policyEvent} {
		_, err = events.SaveEvent(ev)
		require.NoError(t, err)
	}
	require.ErrorContains(t, proof.VerifyBackupExecutionConfig(t.Context(), snapshot), "no ACKed relay delivery receipt")
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: recipeEvent, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitPublisherRound(recipeEvent.ID, localstore.OutboxRound{Target: "control-plane", Rounds: 1, Delivered: true,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Rejected: "denied"}}, State: localstore.OutboxPending})
	require.ErrorContains(t, err, "lacks verified target quorum")
	require.ErrorContains(t, proof.VerifyBackupExecutionConfig(t.Context(), snapshot), "no ACKed relay delivery receipt", "a delivery flag without relay OK is not quorum proof")
	_, err = outbox.CommitPublisherRound(recipeEvent.ID, localstore.OutboxRound{Target: "control-plane", Rounds: 2, Delivered: false,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPending})
	require.NoError(t, err)
	require.ErrorContains(t, proof.VerifyBackupExecutionConfig(t.Context(), snapshot), "no ACKed relay delivery receipt", "one OK without the publisher's delivered/quorum marker is insufficient")
	for _, ev := range []nostr.Event{recipeEvent, repoEvent, policyEvent} {
		_, err = outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "control-plane"})
		require.NoError(t, err)
		_, err = outbox.CommitPublisherRound(ev.ID, localstore.OutboxRound{Target: "control-plane", Rounds: 1, Delivered: true,
			Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay.example"}, Required: 1},
			Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
		require.NoError(t, err)
	}
	require.NoError(t, proof.VerifyBackupExecutionConfig(t.Context(), snapshot))
	foreign := configSnapshotEvent(t, nostr.Generate(), kinds.BackupRepositoryRegistry, kinds.CPStateTopicBackupRepository, nostradapter.BackupRepositoryDTag(repo.ID), repoTags, repoContent)
	_, err = events.SaveEvent(foreign)
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: foreign, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitPublisherRound(foreign.ID, localstore.OutboxRound{Target: "control-plane", Rounds: 1, Delivered: true,
		Policy: localstore.DeliveryPolicy{WriteRelays: []string{"wss://relay.example"}, Required: 1},
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
	require.NoError(t, err)
	wrongAuthor := *snapshot
	wrongAuthor.RepositoryEventID = foreign.ID.Hex()
	require.ErrorContains(t, proof.VerifyBackupExecutionConfig(t.Context(), &wrongAuthor), "invalid signed envelope", "an ACKed foreign author cannot supply the config version")
	requestKey := nostr.Generate()
	request := backupReceiptRequest(t, requestKey)
	var requested map[string]any
	require.NoError(t, json.Unmarshal([]byte(request.Content), &requested))
	requested["recipe_id"], requested["repository_id"], requested["policy_id"] = recipe.ID.String(), repo.ID.String(), policy.ID.String()
	requested["backend"], requested["target_ref"], requested["verification_mode"] = recipe.Backend, recipe.TargetRef, recipe.VerificationMode
	requested["execution_snapshot"] = snapshot
	payload, err := json.Marshal(requested)
	require.NoError(t, err)
	request.Content = string(payload)
	require.NoError(t, request.Sign(requestKey))
	run := backupReceiptRun(request, domain.RunStatusQueued)
	runEvent := backupReceiptEvent(t, key, run)
	_, err = events.SaveEvent(runEvent)
	require.NoError(t, err)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: runEvent, Target: "control-plane"})
	require.NoError(t, err)
	_, err = outbox.CommitRound(runEvent.ID, localstore.OutboxRound{Rounds: 1, Delivered: true,
		Relays: map[string]localstore.RelayDelivery{"wss://relay.example": {Accepted: true}}, State: localstore.OutboxPublished})
	require.NoError(t, err)
	storedRun, err := reader.GetBackupRunReceipt(t.Context(), run.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot, storedRun.ExecutionSnapshot)
	intent, err := ParseIntent(&request)
	require.NoError(t, err)
	intent.Actor = request.PubKey.Hex()
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{RunReceipts: reader, Logger: zap.NewNop()})
	require.ErrorContains(t, handler.HandleIntent(t.Context(), intent), "canonical execution recovery is unavailable")
	changed := *snapshot
	changed.Repository = repo
	changed.Repository.RepositoryURI = "file:/attacker"
	require.ErrorContains(t, proof.VerifyBackupExecutionConfig(t.Context(), &changed), "differs from signed execution snapshot")
	changed = *snapshot
	changed.PolicyEventID = uuid.NewString()
	require.Error(t, proof.VerifyBackupExecutionConfig(t.Context(), &changed))
	removed, err := outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Positive(t, removed)
	_, found, err := outbox.Get(recipeEvent.ID)
	require.NoError(t, err)
	require.False(t, found, "config proof must not depend on the prunable outbox row")
	require.NoError(t, events.DeleteEvent(recipeEvent.ID))
	require.NoError(t, proof.VerifyBackupExecutionConfig(t.Context(), snapshot))
	require.NoError(t, events.Close())
	require.NoError(t, outbox.Close())
	events, err = localstore.Open(filepath.Join(dir, "events.db"))
	require.NoError(t, err)
	outbox, err = localstore.OpenOutbox(filepath.Join(dir, "outbox.db"))
	require.NoError(t, err)
	reader, err = NewLocalBackupRunReceipts(events, outbox, key.Public().Hex())
	require.NoError(t, err)
	require.NoError(t, reader.(backupExecutionConfigProof).VerifyBackupExecutionConfig(t.Context(), snapshot))
}

func TestBackupExecutionSnapshotRejectsRequestReceiptDrift(t *testing.T) {
	request := backupReceiptRequest(t, nostr.Generate())
	run := backupReceiptRun(request, domain.RunStatusQueued)
	recipe := domain.BackupRecipe{ID: run.RecipeID, Name: "recipe", Version: "1", RepositoryID: run.RepositoryID, PolicyID: run.PolicyID, Backend: run.Backend, TargetRef: run.TargetRef, VerificationMode: run.VerificationMode}
	repo := domain.BackupRepository{ID: run.RepositoryID, Name: "repo", RepositoryURI: "file:/data", Backend: run.Backend}
	snapshot := &domain.BackupExecutionSnapshot{RecipeEventID: nostr.Generate().Public().Hex(), RepositoryEventID: nostr.Generate().Public().Hex(), Recipe: recipe, Repository: repo}
	run.ExecutionSnapshot = snapshot
	if run.PolicyID != nil {
		snapshot.Policy = &domain.BackupPolicy{ID: *run.PolicyID, Name: "policy", VerificationMode: domain.BackupVerificationNone}
		snapshot.PolicyEventID = nostr.Generate().Public().Hex()
	}
	requested := run
	require.NoError(t, validateBackupExecutionSnapshot(&requested, &run))
	run.ExecutionSnapshot = &domain.BackupExecutionSnapshot{}
	require.ErrorContains(t, validateBackupExecutionSnapshot(&requested, &run), "ACKed run receipt")
	run.ExecutionSnapshot = snapshot
	requested.TargetRef = "/different"
	require.ErrorContains(t, validateBackupExecutionSnapshot(&requested, &run), "signed run inputs")
	intent, err := ParseIntent(&request)
	require.NoError(t, err)
	intent.Actor = request.PubKey.Hex()
	handler := NewBackupIntentHandler(BackupIntentHandlerConfig{RunReceipts: staticBackupRunReceipt{run: run}, Logger: zap.NewNop()})
	require.Error(t, handler.HandleIntent(t.Context(), intent), "unsigned or unproven snapshot cannot enable execution")
}

func TestBackupExecutionSnapshotRefusesQueuedPartialAndPrunedRejection(t *testing.T) {
	for _, state := range []string{"queued", "partial", "refused"} {
		t.Run(state, func(t *testing.T) {
			path := t.TempDir()
			events, err := localstore.Open(filepath.Join(path, "events.db"))
			require.NoError(t, err)
			defer events.Close()
			outbox, err := localstore.OpenOutbox(filepath.Join(path, "outbox.db"))
			require.NoError(t, err)
			defer outbox.Close()
			key := nostr.Generate()
			recipe := domain.BackupRecipe{ID: uuid.New(), Name: "recipe", Version: "1", Backend: domain.BackupBackendKopia, RepositoryID: uuid.New(), TargetRef: "/data", VerificationMode: domain.BackupVerificationNone}
			tags, content := nostradapter.BackupRecipeRegistryRecord(&recipe, false)
			ev := configSnapshotEvent(t, key, kinds.BackupRecipeRegistry, kinds.CPStateTopicBackupRecipe, nostradapter.BackupRecipeDTag(recipe.ID), tags, content)
			_, err = events.SaveEvent(ev)
			require.NoError(t, err)
			_, err = outbox.Enqueue(localstore.OutboxEntry{Event: ev, Target: "control-plane"})
			require.NoError(t, err)
			policy := localstore.DeliveryPolicy{WriteRelays: []string{"wss://a", "wss://b"}, Required: 2}
			switch state {
			case "partial":
				_, err = outbox.CommitPublisherRound(ev.ID, localstore.OutboxRound{Target: "control-plane", Policy: policy, State: localstore.OutboxPending,
					Relays: map[string]localstore.RelayDelivery{"wss://a": {Accepted: true}}})
			case "refused":
				_, err = outbox.CommitPublisherRound(ev.ID, localstore.OutboxRound{Target: "control-plane", Policy: policy, State: localstore.OutboxFailed,
					Relays: map[string]localstore.RelayDelivery{"wss://a": {Accepted: true}, "wss://b": {Rejected: "blocked: denied"}}})
			}
			require.NoError(t, err)
			_, err = outbox.Prune(time.Now().Add(time.Hour), time.Now().Add(time.Hour))
			require.NoError(t, err)
			proof, found, err := outbox.GetDeliveryProof(ev.ID)
			require.NoError(t, err)
			require.False(t, found, "an undelivered event cannot leave a durable acceptance proof")
			require.Empty(t, proof)
			reader, err := NewLocalBackupRunReceipts(events, outbox, key.Public().Hex())
			require.NoError(t, err)
			snapshot := &domain.BackupExecutionSnapshot{RecipeEventID: ev.ID.Hex(), Recipe: recipe}
			require.ErrorContains(t, reader.(backupExecutionConfigProof).VerifyBackupExecutionConfig(t.Context(), snapshot), "no ACKed relay delivery receipt")
		})
	}
}

func configSnapshotEvent(t *testing.T, key nostr.SecretKey, legacy int, topic, dtag string, tags nostr.Tags, content string) nostr.Event {
	t.Helper()
	tags = append(nostr.Tags{{"d", dtag}, {"t", topic}, {"domain", "backup"}, {"schema", kinds.CASControlStateSchema}, {"legacy_kind", strconv.Itoa(legacy)}, {"deleted", "false"}}, tags...)
	var wire map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &wire))
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: tags, Content: content}
	require.NoError(t, ev.Sign(key))
	return ev
}
