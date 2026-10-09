package controlplane

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
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
		{"expiration", strconv.FormatInt(time.Now().UTC().Add(backupRunRequestValidity).Unix(), 10)},
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

func TestSignedBackupRunRequestHasBoundedFreshnessAcrossRestart(t *testing.T) {
	key := nostr.Generate()
	created := time.Now().UTC().Truncate(time.Second)
	event := signedBackupRunFixture(t, key, nil)
	event.CreatedAt = nostr.Timestamp(created.Unix())
	setExpiry := func(seconds int64) {
		for i := range event.Tags {
			if event.Tags[i][0] == "expiration" {
				event.Tags[i][1] = strconv.FormatInt(seconds, 10)
			}
		}
		require.NoError(t, event.Sign(key))
	}
	setExpiry(created.Add(backupRunRequestValidity).Unix())
	actor := key.Public().Hex()
	for _, elapsed := range []time.Duration{0, 5 * time.Minute, backupRunRequestValidity - time.Second} {
		_, err := validateSignedBackupRunRequest(&event, actor, created.Add(elapsed))
		require.NoError(t, err, "same signed event must remain valid within its window after a process restart")
	}
	_, err := validateSignedBackupRunRequest(&event, actor, created.Add(backupRunRequestValidity))
	require.ErrorContains(t, err, "expired")
	setExpiry(created.Add(30 * time.Minute).Unix())
	_, err = validateSignedBackupRunRequest(&event, actor, created.Add(backupRunRequestValidity+time.Second))
	require.ErrorContains(t, err, "older than")
	_, err = validateSignedBackupRunRequest(&event, actor, created)
	require.ErrorContains(t, err, "within 15 minutes")
	setExpiry(created.Add(backupRunRequestValidity).Unix())
	_, err = validateSignedBackupRunRequest(&event, actor, created.Add(-nostradapter.InboundEventMaxFutureSkew+time.Second))
	require.NoError(t, err, "Nostr's configured future clock skew remains accepted")
	_, err = validateSignedBackupRunRequest(&event, actor, created.Add(-nostradapter.InboundEventMaxFutureSkew-time.Second))
	require.ErrorContains(t, err, "too far in future")

	event.Tags = append(event.Tags, nostr.Tag{"expiration", strconv.FormatInt(created.Add(time.Minute).Unix(), 10)})
	require.NoError(t, event.Sign(key))
	_, err = validateSignedBackupRunRequest(&event, actor, created)
	require.ErrorContains(t, err, "exactly one")
	event.Tags = event.Tags[:len(event.Tags)-1]
	for i := range event.Tags {
		if event.Tags[i][0] == "expiration" {
			event.Tags = append(event.Tags[:i], event.Tags[i+1:]...)
			break
		}
	}
	require.NoError(t, event.Sign(key))
	_, err = validateSignedBackupRunRequest(&event, actor, created)
	require.ErrorContains(t, err, "unexpired NIP-40 expiration")
}
