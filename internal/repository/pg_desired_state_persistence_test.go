package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DeploymentIntent: desired_state + desired_hash round-trip
// ---------------------------------------------------------------------------

func TestPgDeploymentIntentRepository_DesiredStateRoundTrip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgDeploymentIntentRepository{pool: mock}

	svcID := uuid.New()
	envID := uuid.New()
	artID := uuid.New()

	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        svcID,
		EnvironmentID:    envID,
		ArtifactID:       artID,
		StableServiceKey: "my-service",
		ImageRef:         "registry.example.com/my-service:v1.2.3",
		Env:              map[string]string{"APP_ENV": "production"},
		SecretRefs: []domain.DesiredSecretRef{
			{EnvVar: "DB_PASSWORD", Name: "db-pass", SecretID: uuid.New(), RedactedValue: "REDACTED(db-pass)"},
		},
		Ports:         []string{"8080:80"},
		RestartPolicy: "always",
	}
	spec.ComputeDesiredHash()

	// Verify the spec has a hash set
	require.NotEmpty(t, spec.DesiredHash)
	require.Contains(t, spec.DesiredHash, "sha256:")

	// --- Test Create with desired_state ---
	mock.ExpectExec("INSERT INTO deployment_intents").
		WithArgs(
			pgxmock.AnyArg(), // id
			pgxmock.AnyArg(), // service_id
			pgxmock.AnyArg(), // environment_id
			pgxmock.AnyArg(), // deployment_unit_id
			pgxmock.AnyArg(), // artifact_id
			pgxmock.AnyArg(), // requested_by
			pgxmock.AnyArg(), // source_kind
			pgxmock.AnyArg(), // approval_status
			pgxmock.AnyArg(), // status
			pgxmock.AnyArg(), // supersedes_intent_id
			pgxmock.AnyArg(), // approval_metadata JSON
			pgxmock.AnyArg(), // metadata JSON
			pgxmock.AnyArg(), // desired_state JSON
			pgxmock.AnyArg(), // desired_hash
			pgxmock.AnyArg(), // created_at
			pgxmock.AnyArg(), // approved_at
			pgxmock.AnyArg(), // updated_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	di := &domain.DeploymentIntent{
		ServiceID:      svcID,
		EnvironmentID:  envID,
		ArtifactID:     artID,
		RequestedBy:    "npub1test",
		SourceKind:     "manual",
		ApprovalStatus: "not_required",
		Status:         "pending",
		DesiredState:   spec,
		DesiredHash:    spec.DesiredHash,
	}
	err = repo.Create(context.Background(), di)
	require.NoError(t, err)

	// --- Test GetByID returns desired_state ---
	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)

	mock.ExpectQuery("SELECT .+ FROM deployment_intents WHERE id").
		WithArgs(di.ID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "service_id", "environment_id", "deployment_unit_id", "artifact_id", "requested_by", "source_kind",
			"approval_status", "status", "supersedes_intent_id", "approval_metadata", "metadata",
			"desired_state", "desired_hash", "created_at", "approved_at", "updated_at",
		}).AddRow(
			di.ID, svcID, envID, nil, artID, "npub1test", "manual",
			"not_required", "pending", nil, []byte(`{}`), []byte(`{}`),
			specJSON, spec.DesiredHash, di.CreatedAt, nil, di.UpdatedAt,
		))

	got, err := repo.GetByID(context.Background(), di.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.DesiredState)
	assert.Equal(t, spec.DesiredHash, got.DesiredHash)
	assert.Equal(t, spec.StableServiceKey, got.DesiredState.StableServiceKey)
	assert.Equal(t, spec.ImageRef, got.DesiredState.ImageRef)
	assert.Equal(t, "REDACTED(db-pass)", got.DesiredState.SecretRefs[0].RedactedValue)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDeploymentIntentRepository_NilDesiredState(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgDeploymentIntentRepository{pool: mock}

	id := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .+ FROM deployment_intents WHERE id").
		WithArgs(id).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "service_id", "environment_id", "deployment_unit_id", "artifact_id", "requested_by", "source_kind",
			"approval_status", "status", "supersedes_intent_id", "approval_metadata", "metadata",
			"desired_state", "desired_hash", "created_at", "approved_at", "updated_at",
		}).AddRow(
			id, uuid.New(), uuid.New(), nil, uuid.New(), "npub1test", "manual",
			"not_required", "pending", nil, []byte(`{}`), []byte(`{}`),
			nil, "", now, nil, now,
		))

	got, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.DesiredState)
	assert.Empty(t, got.DesiredHash)

	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// DeploymentRun: apply_metadata round-trip
// ---------------------------------------------------------------------------

func TestPgDeploymentRunRepository_ApplyMetadataRoundTrip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := &PgDeploymentRunRepository{pool: mock}

	runID := uuid.New()
	intentID := uuid.New()
	applyMeta := map[string]any{
		"renderer":      "compose",
		"revision_hash": "sha256:abc123",
		"target":        "docker-compose",
	}

	// Test Create
	mock.ExpectExec("INSERT INTO deployment_runs").
		WithArgs(
			pgxmock.AnyArg(), // id
			pgxmock.AnyArg(), // deployment_intent_id
			pgxmock.AnyArg(), // deployment_unit_id
			pgxmock.AnyArg(), // loom_job_id
			pgxmock.AnyArg(), // worker_pubkey
			pgxmock.AnyArg(), // worker_name
			pgxmock.AnyArg(), // status
			pgxmock.AnyArg(), // exit_code
			pgxmock.AnyArg(), // stdout_ref
			pgxmock.AnyArg(), // stderr_ref
			pgxmock.AnyArg(), // started_at
			pgxmock.AnyArg(), // finished_at
			pgxmock.AnyArg(), // metadata
			pgxmock.AnyArg(), // apply_metadata
			pgxmock.AnyArg(), // created_at
			pgxmock.AnyArg(), // updated_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	dr := &domain.DeploymentRun{
		DeploymentIntentID: intentID,
		Status:             "running",
		ApplyMetadata:      applyMeta,
	}
	err = repo.Create(context.Background(), dr)
	require.NoError(t, err)

	// Test GetByID returns apply_metadata
	applyMetaJSON, _ := json.Marshal(applyMeta)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .+ FROM deployment_runs WHERE id").
		WithArgs(runID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "deployment_intent_id", "deployment_unit_id", "loom_job_id", "worker_pubkey", "worker_name",
			"status", "exit_code", "stdout_ref", "stderr_ref", "started_at", "finished_at",
			"metadata", "apply_metadata", "created_at", "updated_at",
		}).AddRow(
			runID, intentID, nil, "", "", "",
			"succeeded", nil, "", "", nil, nil,
			[]byte(`{}`), applyMetaJSON, now, now,
		))

	got, err := repo.GetByID(context.Background(), runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "compose", got.ApplyMetadata["renderer"])
	assert.Equal(t, "sha256:abc123", got.ApplyMetadata["revision_hash"])

	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// EnvironmentServiceState: desired_runtime_state + desired_hash round-trip
// ---------------------------------------------------------------------------

func TestPgEnvironmentServiceStateRepository_DesiredRuntimeStateRoundTrip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgEnvironmentServiceStateRepositoryWithDB(mock)

	svcID := uuid.New()
	envID := uuid.New()
	artID := uuid.New()

	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        svcID,
		EnvironmentID:    envID,
		ArtifactID:       artID,
		StableServiceKey: "my-service",
		ImageRef:         "registry.example.com/my-service:v1.2.3",
		Env:              map[string]string{"APP_ENV": "production"},
		RestartPolicy:    "always",
	}
	spec.ComputeDesiredHash()

	// Test Upsert
	mock.ExpectExec("INSERT INTO environment_service_state").
		WithArgs(
			pgxmock.AnyArg(), // service_id
			pgxmock.AnyArg(), // environment_id
			pgxmock.AnyArg(), // deployment_unit_id
			pgxmock.AnyArg(), // desired_artifact_id
			pgxmock.AnyArg(), // desired_intent_id
			pgxmock.AnyArg(), // last_successful_run_id
			pgxmock.AnyArg(), // current_observation_id
			pgxmock.AnyArg(), // drift_status
			pgxmock.AnyArg(), // desired_runtime_state JSON
			pgxmock.AnyArg(), // desired_hash
			pgxmock.AnyArg(), // reconcile_failure_metadata JSON
			pgxmock.AnyArg(), // reconcile_backoff_until
			pgxmock.AnyArg(), // reconcile_consecutive_failures
			pgxmock.AnyArg(), // last_reconciled_at
			pgxmock.AnyArg(), // updated_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	state := &domain.EnvironmentServiceState{
		ServiceID:           svcID,
		EnvironmentID:       envID,
		DriftStatus:         "unknown",
		DesiredRuntimeState: spec,
		DesiredHash:         spec.DesiredHash,
	}
	err = repo.Upsert(context.Background(), state)
	require.NoError(t, err)

	// Test Get returns desired_runtime_state
	specJSON, _ := json.Marshal(spec)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .+ FROM environment_service_state WHERE service_id").
		WithArgs(svcID, envID).
		WillReturnRows(pgxmock.NewRows([]string{
			"service_id", "environment_id", "deployment_unit_id", "desired_artifact_id", "desired_intent_id",
			"last_successful_run_id", "current_observation_id", "drift_status",
			"desired_runtime_state", "desired_hash", "reconcile_failure_metadata", "reconcile_backoff_until", "reconcile_consecutive_failures", "last_reconciled_at", "updated_at",
		}).AddRow(
			svcID, envID, nil, nil, nil, nil, nil, "unknown",
			specJSON, spec.DesiredHash, nil, nil, 0, nil, now,
		))

	got, err := repo.Get(context.Background(), svcID, envID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.DesiredRuntimeState)
	assert.Equal(t, spec.DesiredHash, got.DesiredHash)
	assert.Equal(t, spec.StableServiceKey, got.DesiredRuntimeState.StableServiceKey)

	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// RuntimeObservation: normalized_state + normalized_hash round-trip
// ---------------------------------------------------------------------------

func TestPgRuntimeObservationRepository_NormalizedStateRoundTrip(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgRuntimeObservationRepositoryWithDB(mock)

	svcID := uuid.New()
	envID := uuid.New()

	normalized := &domain.NormalizedObservation{
		SchemaVersion: domain.DesiredStateSchemaVersion,
		ImageRef:      "registry.example.com/my-service:v1.2.3",
		ImageDigest:   "sha256:deadbeef",
		Env:           map[string]string{"APP_ENV": "production"},
		SecretEnvKeys: []string{"DB_PASSWORD"},
		Ports:         []string{"8080:80"},
		RestartPolicy: "always",
		BahiaLabels: map[string]string{
			"bahia.managed":    "true",
			"bahia.service_id": svcID.String(),
		},
	}
	normalized.ComputeObservationHash()
	require.NotEmpty(t, normalized.ObservationHash)

	// Test Create
	mock.ExpectExec("INSERT INTO runtime_observations").
		WithArgs(
			pgxmock.AnyArg(), // id
			pgxmock.AnyArg(), // service_id
			pgxmock.AnyArg(), // environment_id
			pgxmock.AnyArg(), // deployment_unit_id
			pgxmock.AnyArg(), // observed_image_digest
			pgxmock.AnyArg(), // observed_image_repo
			pgxmock.AnyArg(), // observed_container_id
			pgxmock.AnyArg(), // observed_host
			pgxmock.AnyArg(), // observed_version
			pgxmock.AnyArg(), // health_status
			pgxmock.AnyArg(), // source
			pgxmock.AnyArg(), // metadata JSON
			pgxmock.AnyArg(), // normalized_state JSON
			pgxmock.AnyArg(), // normalized_hash
			pgxmock.AnyArg(), // observed_at
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	obs := &domain.RuntimeObservation{
		ServiceID:           svcID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:deadbeef",
		ObservedImageRepo:   "registry.example.com/my-service",
		ObservedContainerID: "container-abc",
		ObservedHost:        "worker-1",
		ObservedVersion:     "v1.2.3",
		HealthStatus:        "healthy",
		Source:              "compose",
		NormalizedState:     normalized,
		NormalizedHash:      normalized.ObservationHash,
	}
	err = repo.Create(context.Background(), obs)
	require.NoError(t, err)

	// Test GetLatest returns normalized_state
	normalizedJSON, _ := json.Marshal(normalized)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .+ FROM runtime_observations").
		WithArgs(svcID, envID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "service_id", "environment_id", "deployment_unit_id", "observed_image_digest", "observed_image_repo",
			"observed_container_id", "observed_host", "observed_version", "health_status", "source",
			"metadata", "normalized_state", "normalized_hash", "observed_at",
		}).AddRow(
			obs.ID, svcID, envID, nil, "sha256:deadbeef", "registry.example.com/my-service",
			"container-abc", "worker-1", "v1.2.3", "healthy", "compose",
			[]byte(`{}`), normalizedJSON, normalized.ObservationHash, now,
		))

	got, err := repo.GetLatest(context.Background(), svcID, envID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.NormalizedState)
	assert.Equal(t, normalized.ObservationHash, got.NormalizedHash)
	assert.Equal(t, "registry.example.com/my-service:v1.2.3", got.NormalizedState.ImageRef)
	assert.Equal(t, []string{"DB_PASSWORD"}, got.NormalizedState.SecretEnvKeys)
	assert.Equal(t, "true", got.NormalizedState.BahiaLabels["bahia.managed"])

	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Secret redaction verification
// ---------------------------------------------------------------------------

func TestDesiredStateSecretRedaction(t *testing.T) {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    uuid.New(),
		ArtifactID:       uuid.New(),
		StableServiceKey: "test-svc",
		ImageRef:         "test:latest",
		Env:              map[string]string{"APP_ENV": "production"},
		SecretRefs: []domain.DesiredSecretRef{
			{EnvVar: "DB_PASSWORD", Name: "db-pass", SecretID: uuid.New(), RedactedValue: "REDACTED(db-pass)"},
			{EnvVar: "API_KEY", Name: "api-key", SecretID: uuid.New(), RedactedValue: "REDACTED(api-key)"},
		},
	}

	// Verify no plaintext secrets
	assert.False(t, spec.ContainsPlaintextSecret(), "spec should not contain plaintext secrets")

	// Serialize and verify no plaintext in JSON output
	data, err := json.Marshal(spec)
	require.NoError(t, err)
	jsonStr := string(data)

	// Should contain redacted placeholders
	assert.Contains(t, jsonStr, "REDACTED(db-pass)")
	assert.Contains(t, jsonStr, "REDACTED(api-key)")

	// Should NOT contain actual secret values (only redacted refs)
	assert.NotContains(t, jsonStr, "actual-secret-value")

	// Verify env map only has non-secret values
	assert.Equal(t, "production", spec.Env["APP_ENV"])
	_, hasSecret := spec.Env["DB_PASSWORD"]
	assert.False(t, hasSecret, "secret env vars should not be in Env map")
}

func TestDesiredStateSecretRedaction_DetectsPlaintext(t *testing.T) {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.New(),
		EnvironmentID:    uuid.New(),
		ArtifactID:       uuid.New(),
		StableServiceKey: "test-svc",
		ImageRef:         "test:latest",
		SecretRefs: []domain.DesiredSecretRef{
			{EnvVar: "DB_PASSWORD", Name: "db-pass", SecretID: uuid.New(), RedactedValue: "actual-plaintext-password"},
		},
	}

	// Should detect non-redacted value
	assert.True(t, spec.ContainsPlaintextSecret(), "spec should detect plaintext secret")
}

func TestNormalizedObservation_SecretRedaction(t *testing.T) {
	obs := &domain.NormalizedObservation{
		SchemaVersion: domain.DesiredStateSchemaVersion,
		ImageRef:      "test:latest",
		Env:           map[string]string{"APP_ENV": "production"},
		SecretEnvKeys: []string{"DB_PASSWORD", "API_KEY"},
	}

	// Serialize and verify no plaintext secrets
	data, err := json.Marshal(obs)
	require.NoError(t, err)
	jsonStr := string(data)

	// Should list secret keys but not values
	assert.Contains(t, jsonStr, "DB_PASSWORD")
	assert.Contains(t, jsonStr, "API_KEY")

	// env should only have non-secret values
	assert.Equal(t, "production", obs.Env["APP_ENV"])
	_, hasSecret := obs.Env["DB_PASSWORD"]
	assert.False(t, hasSecret, "secret env vars should not be in Env map")
}
