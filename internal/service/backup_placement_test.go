package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBackupPlacementSelectsLeastQueuedEligibleWorker(t *testing.T) {
	definition := backupPlacementDefinition([]string{"site:west"}, []string{"backup.snapshot_create"}, nil)
	busy := backupPlacementWorker("pk-busy", "busy", 9)
	idle := backupPlacementWorker("pk-idle", "idle", 1)
	repo := &mockWorkerRepo{workers: []domain.Worker{busy, idle}}

	decision, err := NewBackupPlacementService(repo, MustBackupBackendResolver(&recordingBackupBackend{}), zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.True(t, decision.Placeable)
	require.Equal(t, domain.BackupExecutorSelectionLeastQueued, decision.SelectionPolicy)
	require.Equal(t, []string{"pk-idle"}, decision.SelectedWorkerPubKeys)
	require.Len(t, decision.Candidates, 2)
	require.Equal(t, "pk-idle", decision.Candidates[0].WorkerPubKey)
	require.True(t, decision.Candidates[0].Eligible)
	require.Equal(t, domain.BackupPlacementReasonPlaceable, decision.Reasons[0].Code)
}

func TestBackupPlacementFailsClosedWhenBackendLacksRequiredLifecycleCapability(t *testing.T) {
	definition := backupPlacementDefinition([]string{"site:west"}, []string{"backup.snapshot_create"}, nil)
	repo := &mockWorkerRepo{workers: []domain.Worker{backupPlacementWorker("pk-worker", "worker", 0)}}

	decision, err := NewBackupPlacementService(repo, MustBackupBackendResolver(baseOnlyBackupBackend{}), zap.NewNop()).ValidatePlacement(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.False(t, decision.Placeable)
	require.Empty(t, decision.SelectedWorkerPubKeys)
	require.NotEmpty(t, decision.Candidates)
	require.True(t, decision.Candidates[0].Eligible, "worker can satisfy targeting but backend must still block placement")
	require.Len(t, decision.Reasons, 1)
	require.Equal(t, domain.BackupPlacementReasonBackendUnsupported, decision.Reasons[0].Code)
	require.Contains(t, decision.Reasons[0].MissingCapabilities, "snapshot_create")
}

func TestBackupPlacementExplainsWorkerRejections(t *testing.T) {
	definition := backupPlacementDefinition([]string{"site:west"}, []string{"backup.snapshot_create"}, nil)
	cordoned := backupPlacementWorker("pk-cordoned", "cordoned", 0)
	cordoned.SchedulingState = domain.WorkerSchedulingCordoned
	wrongSite := backupPlacementWorker("pk-east", "east", 0)
	wrongSite.Labels = map[string]string{"site": "east", "role": "backup"}
	missingCapability := backupPlacementWorker("pk-no-cap", "no-cap", 0)
	missingCapability.Capabilities.Features = []string{"restore"}
	repo := &mockWorkerRepo{workers: []domain.Worker{cordoned, wrongSite, missingCapability}}

	decision, err := NewBackupPlacementService(repo, MustBackupBackendResolver(&recordingBackupBackend{}), zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.False(t, decision.Placeable)
	require.Empty(t, decision.SelectedWorkerPubKeys)
	require.Len(t, decision.Candidates, 3)
	byWorker := map[string]domain.BackupPlacementCandidate{}
	for _, candidate := range decision.Candidates {
		byWorker[candidate.WorkerPubKey] = candidate
	}
	requireCandidateReason(t, byWorker["pk-cordoned"], domain.BackupPlacementReasonWorkerScheduling)
	requireCandidateReason(t, byWorker["pk-east"], domain.BackupPlacementReasonLabelMismatch)
	requireCandidateReason(t, byWorker["pk-no-cap"], domain.BackupPlacementReasonCapabilityMismatch)
	require.Equal(t, domain.BackupPlacementReasonNoWorkers, decision.Reasons[0].Code)
}

func TestBackupPlacementAllEligiblePolicySelectsEveryMatchingWorkerDeterministically(t *testing.T) {
	definition := backupPlacementDefinition([]string{"site:west"}, []string{"backup.snapshot_create"}, map[string]any{"executor_selection_policy": "all_eligible"})
	second := backupPlacementWorker("pk-second", "second", 0)
	first := backupPlacementWorker("pk-first", "first", 4)
	repo := &mockWorkerRepo{workers: []domain.Worker{second, first}}

	decision, err := NewBackupPlacementService(repo, MustBackupBackendResolver(&recordingBackupBackend{}), zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.True(t, decision.Placeable)
	require.Equal(t, domain.BackupExecutorSelectionAllEligible, decision.SelectionPolicy)
	require.Equal(t, []string{"pk-first", "pk-second"}, decision.SelectedWorkerPubKeys)
}

func TestBackupPlacementFirstEligiblePolicyUsesStablePubkeyOrder(t *testing.T) {
	definition := backupPlacementDefinition([]string{"site:west"}, []string{"backup.snapshot_create"}, map[string]any{"executor_selection_policy": "first_eligible"})
	zWorker := backupPlacementWorker("pk-z", "z", 0)
	aWorker := backupPlacementWorker("pk-a", "a", 9)
	repo := &mockWorkerRepo{workers: []domain.Worker{zWorker, aWorker}}

	decision, err := NewBackupPlacementService(repo, MustBackupBackendResolver(&recordingBackupBackend{}), zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.True(t, decision.Placeable)
	require.Equal(t, domain.BackupExecutorSelectionFirstEligible, decision.SelectionPolicy)
	require.Equal(t, []string{"pk-a"}, decision.SelectedWorkerPubKeys)
}

func TestBackupPlacementLifecycleRequirementsNeedBackendResolver(t *testing.T) {
	definition := backupPlacementDefinition([]string{"site:west"}, []string{"backup.snapshot_create"}, nil)
	repo := &mockWorkerRepo{workers: []domain.Worker{backupPlacementWorker("pk-worker", "worker", 0)}}

	decision, err := NewBackupPlacementService(repo, nil, zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.False(t, decision.Placeable)
	require.Equal(t, domain.BackupPlacementReasonBackendUnsupported, decision.Reasons[0].Code)
	require.Contains(t, decision.Reasons[0].Message, "resolver is required")
}

func TestBackupPlacementRejectsInvalidSelectionPolicy(t *testing.T) {
	definition := backupPlacementDefinition(nil, nil, map[string]any{"executor_selection_policy": "random"})
	repo := &mockWorkerRepo{workers: []domain.Worker{backupPlacementWorker("pk-worker", "worker", 0)}}

	_, err := NewBackupPlacementService(repo, MustBackupBackendResolver(&recordingBackupBackend{}), zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.ErrorIs(t, err, domain.ErrInvalidValue)
	require.Contains(t, err.Error(), "selection policy")
}

func TestBackupPlacementDefaultsToGenericBackupCapabilityWhenDefinitionHasNoExplicitRequirements(t *testing.T) {
	definition := backupPlacementDefinition(nil, nil, nil)
	generic := backupPlacementWorker("pk-generic", "generic", 0)
	noBackup := makeWorker("pk-general", "general", 0, 0, "", "linux/amd64")
	noBackup.SchedulingState = domain.WorkerSchedulingActive
	repo := &mockWorkerRepo{workers: []domain.Worker{noBackup, generic}}

	decision, err := NewBackupPlacementService(repo, MustBackupBackendResolver(&recordingBackupBackend{}), zap.NewNop()).ResolveExecutors(t.Context(), BackupPlacementRequest{
		Definition:  definition,
		BackendKind: domain.BackupBackendKopia,
	})

	require.NoError(t, err)
	require.True(t, decision.Placeable)
	require.Equal(t, []string{"pk-generic"}, decision.SelectedWorkerPubKeys)
	requireCandidateReason(t, decision.Candidates[1], domain.BackupPlacementReasonCapabilityMismatch)
}

func requireCandidateReason(t *testing.T, candidate domain.BackupPlacementCandidate, code domain.BackupPlacementReasonCode) {
	t.Helper()
	for _, reason := range candidate.Reasons {
		if reason.Code == code {
			return
		}
	}
	t.Fatalf("expected candidate %s to include reason %s, got %#v", candidate.WorkerPubKey, code, candidate.Reasons)
}

func backupPlacementDefinition(labels []string, requirements []string, metadata map[string]any) *domain.BackupDefinition {
	return &domain.BackupDefinition{
		ID:                     uuid.New(),
		Name:                   "daily-service",
		RepositoryID:           uuid.New(),
		RepositoryName:         "primary",
		PolicyID:               uuid.New(),
		PolicyName:             "verified",
		RecipeID:               uuid.New(),
		RecipeName:             "service-data",
		RecipeVersion:          "v1",
		ExecutorLabels:         labels,
		CapabilityRequirements: requirements,
		Metadata:               metadata,
		CreatedBy:              "creator-pubkey",
	}
}

func backupPlacementWorker(pubkey, name string, queue int) domain.Worker {
	worker := makeWorker(pubkey, name, queue, 0, "", "linux/amd64")
	worker.SchedulingState = domain.WorkerSchedulingActive
	worker.Labels = map[string]string{"site": "west", "role": "backup"}
	worker.Capabilities = domain.WorkerCapabilities{WorkloadKinds: []string{"backup"}, Features: []string{"snapshot_create", "snapshot_verify"}}
	return worker
}
