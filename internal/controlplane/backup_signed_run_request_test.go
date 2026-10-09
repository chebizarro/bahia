package controlplane

import (
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func signedBackupRunFixture(t *testing.T, key nostr.SecretKey, mutate func(map[string]any)) nostr.Event {
	t.Helper()
	runID, err := uuid.NewV7()
	require.NoError(t, err)
	credentialID := uuid.New()
	policyID := uuid.New()
	repositoryID := uuid.New()
	recipeID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	repository := domain.BackupRepository{ID: repositoryID, Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "file:/backup", CredentialProfile: "backup-operator", CreatedAt: now, UpdatedAt: now}
	policy := domain.BackupPolicy{ID: policyID, Name: "verify", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify, CreatedAt: now, UpdatedAt: now}
	recipe := domain.BackupRecipe{ID: recipeID, Name: "database", Version: "1", Backend: domain.BackupBackendKopia, RepositoryID: repositoryID, PolicyID: &policyID, TargetRef: "/database", VerificationMode: domain.BackupVerificationKopiaSnapshotVerify, CreatedAt: now, UpdatedAt: now}
	content := map[string]any{
		"id": runID.String(), "recipe_id": recipeID.String(), "repository_id": repositoryID.String(), "policy_id": policyID.String(),
		"backend": domain.BackupBackendKopia, "target_ref": recipe.TargetRef, "verification_mode": recipe.VerificationMode,
		"execution_snapshot": domain.BackupExecutionSnapshot{RecipeEventID: nostr.Generate().Public().Hex(), RepositoryEventID: nostr.Generate().Public().Hex(), PolicyEventID: nostr.Generate().Public().Hex(), CredentialVersionID: &credentialID, Recipe: recipe, Repository: repository, Policy: &policy},
	}
	if mutate != nil {
		mutate(content)
	}
	encoded, err := json.Marshal(content)
	require.NoError(t, err)
	event := nostr.Event{Kind: 30900, CreatedAt: nostr.Now(), Content: string(encoded), Tags: nostr.Tags{
		{"d", "backup-run:" + runID.String()}, {"t", "bahia-intent"}, {"domain", "backup"}, {"op", "run"},
		{"schema", "bahia.intent.backup.v1"}, {"intent_id", "backup-request:" + runID.String()}, {"org", uuid.NewString()},
	}}
	require.NoError(t, event.Sign(key))
	return event
}

func TestSignedBackupRunRequestBindsResolvedInputsAndCredentialVersion(t *testing.T) {
	key := nostr.Generate()
	event := signedBackupRunFixture(t, key, nil)
	intent, err := ValidateSignedBackupRunRequest(&event, key.Public().Hex())
	require.NoError(t, err)
	require.Equal(t, "backup", intent.Domain)
	require.Equal(t, "run", intent.Op)
	require.Equal(t, event.ID, intent.Event.ID)

	mutations := []struct {
		name, want string
		change     func(map[string]any)
	}{
		{"unbound repository", "resolved execution inputs", func(m map[string]any) { delete(m, "repository_id") }},
		{"recipe drift", "signed run inputs", func(m map[string]any) { m["target_ref"] = "/other" }},
		{"unversioned credential", "immutable credential version", func(m map[string]any) {
			s := m["execution_snapshot"].(domain.BackupExecutionSnapshot)
			s.CredentialVersionID = nil
			m["execution_snapshot"] = s
		}},
		{"lifecycle injection", "non-request field", func(m map[string]any) { m["status"] = "succeeded" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			bad := signedBackupRunFixture(t, key, tc.change)
			_, err := ValidateSignedBackupRunRequest(&bad, key.Public().Hex())
			require.ErrorContains(t, err, tc.want)
		})
	}
	_, err = ValidateSignedBackupRunRequest(&event, nostr.Generate().Public().Hex())
	require.ErrorContains(t, err, "author differs")
	event.Content += " "
	_, err = ValidateSignedBackupRunRequest(&event, key.Public().Hex())
	require.ErrorContains(t, err, "event id does not match")
}
