package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQdrantBackendReportsLifecycleCapabilities(t *testing.T) {
	capabilities := NewQdrantBackend().Capabilities()

	require.Equal(t, service.BackendCapabilities{
		SnapshotCreate: true,
		SnapshotVerify: true,
		Restore:        true,
		Retention:      false,
		Probe:          true,
	}, capabilities)
}

func TestQdrantBackendHealthDelegates(t *testing.T) {
	api := &recordingQdrantAPI{
		snapshots: []qdrantSnapshotDesc{{
			Name: "snap-" + uuid.New().String(),
			Size: 512,
		}},
	}
	backend := NewQdrantBackend(withQdrantAPI(api))
	repo := qdrantRepositoryFixture()

	err := backend.Health(context.Background(), repo)

	require.NoError(t, err)
	require.True(t, api.healthCalled)
}

func TestQdrantBackendCreateSnapshotCreatesAndDownloads(t *testing.T) {
	stagingDir := t.TempDir()
	api := &recordingQdrantAPI{
		snapshotResult: &qdrantSnapshotResult{Name: "snap-1", Size: 1024},
	}
	backend := NewQdrantBackend(withQdrantAPI(api), WithQdrantStagingDir(stagingDir))
	repo := qdrantRepositoryFixture()
	recipe := qdrantRecipeFixture(repo.ID)
	run := qdrantRunFixture(recipe)

	result, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{
		Run: run, Recipe: recipe, Repository: repo,
	})

	require.NoError(t, err)
	require.Equal(t, "snap-1", result.SnapshotID)
	require.True(t, api.createCalled)
	require.True(t, api.pollCalled)
	require.True(t, api.downloadCalled)
	evidence, ok := result.Evidence["qdrant_snapshot"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "snap-1", evidence["snapshot_name"])
	require.Contains(t, evidence["snapshot_file"].(string), run.ID.String()+".snapshot")
}

func TestQdrantBackendCreateSnapshotFailsWithoutInputs(t *testing.T) {
	backend := NewQdrantBackend()

	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{})
	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
}

func TestQdrantBackendCreateSnapshotFailsOnAPIError(t *testing.T) {
	api := &recordingQdrantAPI{createErr: fmt.Errorf("connection refused")}
	backend := NewQdrantBackend(withQdrantAPI(api))
	repo := qdrantRepositoryFixture()
	recipe := qdrantRecipeFixture(repo.ID)
	run := qdrantRunFixture(recipe)

	_, err := backend.CreateSnapshot(context.Background(), service.BackupSnapshotRequest{
		Run: run, Recipe: recipe, Repository: repo,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
}

func TestQdrantBackendVerifySnapshotVerifiesChecksum(t *testing.T) {
	stagingDir := t.TempDir()
	snapshotContent := []byte("mock snapshot data")
	dumpFile := filepath.Join(stagingDir, "test-snap.snapshot")
	os.WriteFile(dumpFile, snapshotContent, 0600)

	api := &recordingQdrantAPI{}
	backend := NewQdrantBackend(withQdrantAPI(api), WithQdrantStagingDir(stagingDir))
	repo := qdrantRepositoryFixture()
	recipe := qdrantRecipeFixture(repo.ID)
	run := qdrantRunFixture(recipe)
	run.Metadata = map[string]any{
		"snapshot_evidence": map[string]any{
			"qdrant_snapshot": map[string]any{
				"snapshot_file":   dumpFile,
				"checksum_sha256": "1c2c05f7bf8cee99768ace5127cdf8b8876604ea4e296e18d070c768be50031b",
			},
		},
	}


	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{
		Run: run, Repository: repo,
		SnapshotID: "snap-1", Mode: domain.BackupVerificationQdrantSnapshotVerify,
	})

	require.NoError(t, err)
	require.True(t, result.Verified)
	require.Equal(t, domain.BackupVerificationSucceeded, result.Status)
}

func TestQdrantBackendVerifySnapshotRejectsMismatchedChecksum(t *testing.T) {
	stagingDir := t.TempDir()
	dumpFile := filepath.Join(stagingDir, "test-snap.snapshot")
	os.WriteFile(dumpFile, []byte("corrupted data"), 0600)

	api := &recordingQdrantAPI{}
	backend := NewQdrantBackend(withQdrantAPI(api), WithQdrantStagingDir(stagingDir))
	repo := qdrantRepositoryFixture()
	recipe := qdrantRecipeFixture(repo.ID)
	run := qdrantRunFixture(recipe)
	run.Metadata = map[string]any{
		"snapshot_evidence": map[string]any{
			"qdrant_snapshot": map[string]any{
				"snapshot_file":   dumpFile,
				"checksum_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}

	result, err := backend.VerifySnapshot(context.Background(), service.BackupVerifyRequest{
		Run: run, Repository: repo,
		SnapshotID: "snap-1", Mode: domain.BackupVerificationQdrantSnapshotVerify,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendExecution)
	require.False(t, result.Verified)
	require.Equal(t, domain.BackupVerificationFailed, result.Status)
}

func TestQdrantBackendRestoreUploadsAndRecovers(t *testing.T) {
	stagingDir := t.TempDir()
	snapshotContent := []byte("mock snapshot data")
	sourceRunID := uuid.New()
	dumpFile := filepath.Join(stagingDir, sourceRunID.String()+".snapshot")
	os.WriteFile(dumpFile, snapshotContent, 0600)

	api := &recordingQdrantAPI{
		snapshots: []qdrantSnapshotDesc{{
			Name: "snap-" + uuid.New().String(),
			Size: 512,
		}},
	}
	backend := NewQdrantBackend(withQdrantAPI(api), WithQdrantStagingDir(stagingDir))
	repo := qdrantRepositoryFixture()
	recipe := qdrantRecipeFixture(repo.ID)
	sourceRun := qdrantSucceededSourceRunFixture(recipe)
	sourceRun.ID = sourceRunID
	sourceRun.Metadata = map[string]any{
		"snapshot_evidence": map[string]any{
			"qdrant_snapshot": map[string]any{
				"snapshot_file":   dumpFile,
				"checksum_sha256": "1c2c05f7bf8cee99768ace5127cdf8b8876604ea4e296e18d070c768be50031b",
				"snapshot_name":   "snap-backup-1",
			},
		},
	}
	restoreRun := qdrantRestoreRunFixture(sourceRun)
	restoreRun.RestoreTargetRef = "qdrant:restored-collection"

	result, err := backend.Restore(context.Background(), service.BackupRestoreRequest{
		Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo,
	})

	require.NoError(t, err)
	require.True(t, api.uploadCalled)
	require.True(t, api.recoverCalled)
	require.Equal(t, domain.BackupVerificationSucceeded, result.VerificationStatus)
}

func TestQdrantBackendRestoreMismatchedRunFails(t *testing.T) {
	api := &recordingQdrantAPI{}
	backend := NewQdrantBackend(withQdrantAPI(api))
	repo := qdrantRepositoryFixture()
	recipe := qdrantRecipeFixture(repo.ID)
	sourceRun := qdrantSucceededSourceRunFixture(recipe)
	restoreRun := qdrantRestoreRunFixture(sourceRun)
	restoreRun.BackupRunID = uuid.New()

	_, err := backend.Restore(context.Background(), service.BackupRestoreRequest{
		Run: restoreRun, SourceRun: sourceRun, Recipe: recipe, Repository: repo,
	})

	require.ErrorIs(t, err, service.ErrBackupBackendConfiguration)
}

type recordingQdrantAPI struct {
	healthCalled    bool
	createCalled    bool
	pollCalled      bool
	downloadCalled  bool
	uploadCalled    bool
	recoverCalled   bool
	listCalled      bool

	snapshotResult *qdrantSnapshotResult
	createErr      error
	downloadCS     string
	snapshots      []qdrantSnapshotDesc
}

func (r *recordingQdrantAPI) health(ctx context.Context) error {
	r.healthCalled = true
	return nil
}

func (r *recordingQdrantAPI) createSnapshot(ctx context.Context, collection string) (*qdrantSnapshotResult, error) {
	r.createCalled = true
	if r.createErr != nil {
		return nil, r.createErr
	}
	if r.snapshotResult != nil {
		return r.snapshotResult, nil
	}
	return &qdrantSnapshotResult{Name: "test-snap-" + uuid.New().String(), Size: 512}, nil
}

func (r *recordingQdrantAPI) pollSnapshotExists(ctx context.Context, collection, snapshotName string) error {
	r.pollCalled = true
	return nil
}

func (r *recordingQdrantAPI) downloadSnapshot(ctx context.Context, collection, snapshotName, destPath string) (string, error) {
	r.downloadCalled = true
	if r.downloadCS != "" {
		return r.downloadCS, nil
	}
	os.WriteFile(destPath, []byte("mock snapshot data"), 0600)
	cs, _ := sha256File(destPath)
	return cs, nil
}

func (r *recordingQdrantAPI) uploadSnapshot(ctx context.Context, collection, snapshotPath string) error {
	r.uploadCalled = true
	return nil
}

func (r *recordingQdrantAPI) recoverFromSnapshot(ctx context.Context, collection, snapshotName string) error {
	r.recoverCalled = true
	return nil
}

func (r *recordingQdrantAPI) listSnapshots(ctx context.Context, collection string) ([]qdrantSnapshotDesc, error) {
	r.listCalled = true
	return r.snapshots, nil
}

func qdrantRepositoryFixture() *domain.BackupRepository {
	return &domain.BackupRepository{
		ID:            uuid.New(),
		Name:          "qdrant-primary",
		Backend:       domain.BackupBackendQdrantSnapshot,
		RepositoryURI: "http://localhost:6333",
		Metadata: map[string]any{
			"qdrant_url":    "http://localhost:6333",
			"qdrant_api_key": "test-key",
		},
	}
}

func qdrantRecipeFixture(repoID uuid.UUID) *domain.BackupRecipe {
	return &domain.BackupRecipe{
		ID: uuid.New(), Name: "qdrant-daily", Version: "v1",
		Backend:          domain.BackupBackendQdrantSnapshot,
		RepositoryID:     repoID,
		TargetRef:        "my-collection",
		VerificationMode: domain.BackupVerificationQdrantSnapshotVerify,
	}
}

func qdrantRunFixture(recipe *domain.BackupRecipe) *domain.BackupRun {
	return &domain.BackupRun{
		ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: recipe.RepositoryID,
		RequestedBy: "pubkey", RequestEventID: "event", RequestKind: 38400,
		RequestDTag: "qdrant-daily", Status: domain.RunStatusQueued,
		Backend: domain.BackupBackendQdrantSnapshot, TargetRef: recipe.TargetRef,
		VerificationStatus: domain.BackupVerificationPending,
	}
}

func qdrantSucceededSourceRunFixture(recipe *domain.BackupRecipe) *domain.BackupRun {
	run := qdrantRunFixture(recipe)
	run.Status = domain.RunStatusSucceeded
	run.SnapshotCreated = true
	run.SnapshotID = "snap-backup-1"
	run.VerificationStatus = domain.BackupVerificationSucceeded
	run.Metadata = map[string]any{}
	return run
}

func qdrantRestoreRunFixture(source *domain.BackupRun) *domain.BackupRestoreRun {
	return &domain.BackupRestoreRun{
		ID: uuid.New(), BackupRunID: source.ID,
		RecipeID: source.RecipeID, RepositoryID: source.RepositoryID,
		SnapshotID: source.SnapshotID, RestoreTargetRef: "qdrant:restored-collection",
		RequestedBy: "pubkey", RequestEventID: "restore-event", RequestKind: 38402,
		RequestDTag: "restore:qdrant-daily", ApprovalStatus: domain.BackupApprovalNotRequired,
		Status: domain.RunStatusQueued, Backend: domain.BackupBackendQdrantSnapshot,
		VerificationStatus: domain.BackupVerificationPending,
	}
}
