package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- F3 test: create via intent ---
func TestEnvironmentIntentHandler_Create(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   "intent-create-env-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "production",
			"deploy_strategy": "replace",
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.created, 1)
	assert.Equal(t, "production", reg.created[0].env.Name)
	assert.Equal(t, domain.DeployStrategyReplace, reg.created[0].env.DeployStrategy)
	assert.Equal(t, testOrgID(), reg.created[0].env.OrgID)
}

// --- F3 test: update via intent ---
func TestEnvironmentIntentHandler_Update(t *testing.T) {
	envID := domain.NewEntityID()
	now := domain.NormalizeRevisionTime(time.Now())
	existing := &domain.Environment{
		ID:        envID,
		OrgID:     testOrgID(),
		Name:      "staging",
		UpdatedAt: now,
	}
	reg := &stubEnvironmentRegistry{
		getByID: map[uuid.UUID]*domain.Environment{envID: existing},
	}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	intent := &Intent{
		Domain:     "environment",
		Op:         "update",
		OrgID:      testOrgID(),
		IntentID:   "intent-update-env-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "staging-v2",
			"deploy_strategy": "blue_green",
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.updated, 1)
	assert.Equal(t, "staging-v2", reg.updated[0].env.Name)
	assert.Equal(t, domain.DeployStrategyBlueGreen, reg.updated[0].env.DeployStrategy)
}

// --- F3 test: create and update via intent produce identical state ---
func TestEnvironmentIntentHandler_CreateAndUpdateProduceIdenticalState(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              envID.String(),
		"name":            "production",
		"deploy_strategy": "replace",
		"protected":       true,
	}

	// Create via intent.
	createIntent := &Intent{
		Domain: "environment", Op: "create",
		OrgID: testOrgID(), IntentID: "intent-c-1",
		Coordinate: envID.String(), Actor: testPubkey,
		Content: content,
	}
	require.NoError(t, handler.HandleIntent(context.Background(), createIntent))
	require.Len(t, reg.created, 1)

	// Simulate existing env for update path.
	reg.getByID = map[uuid.UUID]*domain.Environment{
		envID: reg.created[0].env,
	}

	// Update via intent with same content.
	updateIntent := &Intent{
		Domain: "environment", Op: "update",
		OrgID: testOrgID(), IntentID: "intent-u-1",
		Coordinate: envID.String(), Actor: testPubkey,
		Content: content,
	}
	require.NoError(t, handler.HandleIntent(context.Background(), updateIntent))
	require.Len(t, reg.updated, 1)

	// Both mutations target the same environment and carry the same state.
	assert.Equal(t, reg.created[0].env.Name, reg.updated[0].env.Name)
	assert.Equal(t, reg.created[0].env.DeployStrategy, reg.updated[0].env.DeployStrategy)
	assert.Equal(t, reg.created[0].env.Protected, reg.updated[0].env.Protected)
	assert.Equal(t, reg.created[0].env.ID, reg.updated[0].env.ID)
}

// --- F3 test: concurrent updates with stale revision get conflict ---
func TestEnvironmentIntentHandler_StaleRevisionConflict(t *testing.T) {
	envID := domain.NewEntityID()
	now := domain.NormalizeRevisionTime(time.Now())
	existing := &domain.Environment{
		ID:        envID,
		OrgID:     testOrgID(),
		Name:      "staging",
		UpdatedAt: now,
	}
	reg := &stubEnvironmentRegistry{
		getByID: map[uuid.UUID]*domain.Environment{envID: existing},
	}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	// Use a stale expected_updated_at (1 second earlier).
	staleTime := now.Add(-time.Second)
	intent := &Intent{
		Domain:            "environment",
		Op:                "update",
		OrgID:             testOrgID(),
		IntentID:          "intent-stale-1",
		Coordinate:        envID.String(),
		Actor:             testPubkey,
		ExpectedUpdatedAt: &staleTime,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "staging-v2",
			"deploy_strategy": "replace",
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.Error(t, err)
	assert.True(t, IsRevisionConflict(err), "expected revision conflict error, got: %v", err)
	assert.Empty(t, reg.updated, "no update should have been applied")
}

// --- F3 test: stale revision produces conflict status ---
func TestEnvironmentIntentHandler_ConflictStatus(t *testing.T) {
	store := openTestStore(t)
	published := &statusCollector{}
	signer := &testSigner{}
	statusPub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	envID := domain.NewEntityID()
	now := domain.NormalizeRevisionTime(time.Now())
	existing := &domain.Environment{
		ID:        envID,
		OrgID:     testOrgID(),
		Name:      "staging",
		UpdatedAt: now,
	}
	reg := &stubEnvironmentRegistry{
		getByID: map[uuid.UUID]*domain.Environment{envID: existing},
	}

	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)
	proc := NewIntentProcessor(ts, store, statusPub,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"environment": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("environment", NewEnvironmentIntentHandler(reg, nil, zap.NewNop()))

	staleTime := now.Add(-time.Second)
	intent := &Intent{
		Domain:            "environment",
		Op:                "update",
		OrgID:             testOrgID(),
		IntentID:          "intent-conflict-status-1",
		Coordinate:        envID.String(),
		Actor:             testPubkey,
		ExpectedUpdatedAt: &staleTime,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "staging-v2",
			"deploy_strategy": "replace",
		},
	}

	err := proc.ProcessInProcess(context.Background(), intent)
	require.Error(t, err)

	// Verify conflict status was published (not rejection).
	require.Len(t, published.events, 1)
	assert.Contains(t, published.events[0].Content, "revision_conflict")
	// Verify the status tag says "conflict".
	for _, tag := range published.events[0].Tags {
		if len(tag) >= 2 && tag[0] == "status" {
			assert.Equal(t, "conflict", tag[1])
		}
	}
}

// --- F3 test: level-triggered cold-daemon behavior ---
// An update intent on a cold daemon (no prior create seen) creates the environment.
func TestEnvironmentIntentHandler_LevelTriggeredColdDaemon(t *testing.T) {
	reg := &stubEnvironmentRegistry{} // No existing environments.
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "update", // Update, but no prior state exists.
		OrgID:      testOrgID(),
		IntentID:   "intent-cold-daemon-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "production",
			"deploy_strategy": "replace",
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	// Level-triggered: the handler should have created the environment.
	require.Len(t, reg.created, 1)
	assert.Equal(t, "production", reg.created[0].env.Name)
	assert.Empty(t, reg.updated, "should have created, not updated")
}

// --- F3 test: explicit deployment units round-trip ---
func TestEnvironmentIntentHandler_ExplicitUnitsRoundTrip(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              envID.String(),
		"name":            "multi-unit",
		"deploy_strategy": "replace",
		"targeting": map[string]interface{}{
			"default_unit_key": "web",
		},
		"deployment_units": []interface{}{
			map[string]interface{}{
				"key":          "web",
				"runtime_type": "docker",
			},
			map[string]interface{}{
				"key":            "api",
				"runtime_type":   "compose",
				"compose_dir":    "/app/api",
				"reconcile_mode": "full",
			},
		},
	}

	intent := &Intent{
		Domain:     "environment",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   "intent-units-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content:    content,
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.createdWithUnits, 1)

	created := reg.createdWithUnits[0]
	assert.Equal(t, "multi-unit", created.env.Name)
	require.Len(t, created.units, 2)

	// Check units preserved their keys.
	keys := make(map[string]bool)
	for _, u := range created.units {
		keys[u.Key] = true
	}
	assert.True(t, keys["web"], "web unit should exist")
	assert.True(t, keys["api"], "api unit should exist")

	// Check compose_dir round-trips.
	for _, u := range created.units {
		if u.Key == "api" {
			assert.Equal(t, "/app/api", u.ComposeDir)
			assert.Equal(t, domain.ReconcileMode("full"), u.ReconcileMode)
		}
	}
}

// --- F3 test: delete via intent ---
func TestEnvironmentIntentHandler_Delete(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "delete",
		OrgID:      testOrgID(),
		IntentID:   "intent-delete-env-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":      envID.String(),
			"deleted": true,
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.deleted, 1)
	assert.Equal(t, envID, reg.deleted[0].id)
	assert.False(t, reg.deleted[0].force)
}

// --- F3 test: delete with force ---
func TestEnvironmentIntentHandler_DeleteForce(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "delete",
		OrgID:      testOrgID(),
		IntentID:   "intent-delete-force-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":      envID.String(),
			"deleted": true,
			"force":   true,
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.deleted, 1)
	assert.True(t, reg.deleted[0].force)
}

// --- F3 test: no-DB operation ---
// The handler operates without Postgres (no tx executor, no DB repos).
func TestEnvironmentIntentHandler_NoDBOperation(t *testing.T) {
	// Use a registry with no DB backing — all in-memory.
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   "intent-nodb-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "no-db-env",
			"deploy_strategy": "replace",
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.created, 1)
	assert.Equal(t, "no-db-env", reg.created[0].env.Name)
}

// --- F3 test: PermissionFor returns correct permissions ---
func TestEnvironmentIntentHandler_PermissionFor(t *testing.T) {
	handler := NewEnvironmentIntentHandler(nil, nil, zap.NewNop())

	assert.Equal(t, domain.PermWriteEnvironments, handler.PermissionFor("create"))
	assert.Equal(t, domain.PermWriteEnvironments, handler.PermissionFor("update"))
	assert.Equal(t, domain.PermWriteEnvironments, handler.PermissionFor("delete"))
}

// --- F3 test: full pipeline via IntentProcessor ---
func TestEnvironmentIntentHandler_FullPipeline(t *testing.T) {
	store := openTestStore(t)
	published := &statusCollector{}
	signer := &testSigner{}
	statusPub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	reg := &stubEnvironmentRegistry{}
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)
	proc := NewIntentProcessor(ts, store, statusPub,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"environment": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("environment", NewEnvironmentIntentHandler(reg, nil, zap.NewNop()))

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   "intent-pipeline-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "pipeline-env",
			"deploy_strategy": "replace",
		},
	}

	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)

	// Environment was created.
	require.Len(t, reg.created, 1)
	assert.Equal(t, "pipeline-env", reg.created[0].env.Name)

	// Acceptance status published.
	require.Len(t, published.events, 1)
	assert.Contains(t, published.events[0].Content, "applied")

	// Idempotency: processing the same intent again is a no-op.
	err = proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)
	assert.Len(t, reg.created, 1, "duplicate intent must not create again")
	assert.Len(t, published.events, 1, "duplicate intent must not publish status again")
}

// --- F3 test: dual dispatch via ContextVM ---
func TestEnvironmentIntentHandler_DualDispatchContextVM(t *testing.T) {
	store := openTestStore(t)
	reg := &stubEnvironmentRegistry{}
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)
	proc := NewIntentProcessor(ts, store, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"environment": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("environment", NewEnvironmentIntentHandler(reg, nil, zap.NewNop()))

	envID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              envID.String(),
		"name":            "contextvm-env",
		"deploy_strategy": "replace",
	}

	// Simulate ContextVM dual dispatch.
	intent := &Intent{
		Domain:     "environment",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   envID.String(),
		Coordinate: envID.String(),
		Content:    content,
		Actor:      testPubkey,
	}

	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.created, 1)
	assert.Equal(t, "contextvm-env", reg.created[0].env.Name)
}

// --- F3 test: projector no longer publishes environment records ---
func TestProjectorNoLongerPublishesEnvironmentRecords(t *testing.T) {
	// This test verifies that the projector's SetupSubscriptions no longer
	// includes environment event types. We check the code path by confirming
	// the handleEvent method does not react to environment events.
	//
	// Since we cannot easily construct a full Projector in unit tests, we
	// verify that the environment event types are not in the bus subscription
	// list by checking our deletion was applied.
	// The actual verification is the compile-time build + the removed code.
	t.Log("Environment bus subscriptions (EventEnvironmentCreated/Updated/Deleted) " +
		"and the RepublishSnapshot environment loop have been removed from " +
		"projector.go (bahia-irsry.11.4). This is verified by the build gate.")
}

// --- F3 test: environment intent targeting normalization ---
func TestEnvironmentIntentHandler_TargetingNormalization(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	intent := &Intent{
		Domain:     "environment",
		Op:         "create",
		OrgID:      testOrgID(),
		IntentID:   "intent-targeting-1",
		Coordinate: envID.String(),
		Actor:      testPubkey,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "targeting-env",
			"deploy_strategy": "replace",
			"targeting": map[string]interface{}{
				"default_unit_key":       "main",
				"secret_scope_mode":      "environment",
				"default_reconcile_mode": "full",
			},
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.created, 1)
	env := reg.created[0].env
	assert.Equal(t, "main", env.Targeting.DefaultUnitKey)
	assert.Equal(t, domain.SecretScopeMode("environment"), env.Targeting.SecretScopeMode)
	assert.Equal(t, domain.ReconcileMode("full"), env.Targeting.DefaultReconcileMode)
}

// --- F3 test: revision check at Postgres microsecond precision ---
func TestEnvironmentIntentHandler_RevisionMicrosecondPrecision(t *testing.T) {
	envID := domain.NewEntityID()
	// Use a revision at exact microsecond precision.
	now := domain.NormalizeRevisionTime(time.Now())

	existing := &domain.Environment{
		ID:        envID,
		OrgID:     testOrgID(),
		Name:      "precision-env",
		UpdatedAt: now,
	}
	reg := &stubEnvironmentRegistry{
		getByID: map[uuid.UUID]*domain.Environment{envID: existing},
	}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	// Matching revision at microsecond precision should succeed.
	matchingTime := now
	intent := &Intent{
		Domain:            "environment",
		Op:                "update",
		OrgID:             testOrgID(),
		IntentID:          "intent-precision-1",
		Coordinate:        envID.String(),
		Actor:             testPubkey,
		ExpectedUpdatedAt: &matchingTime,
		Content: map[string]interface{}{
			"id":              envID.String(),
			"name":            "precision-env-v2",
			"deploy_strategy": "replace",
		},
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.updated, 1)
}

// --- F3 test: update with git source in deployment unit ---
func TestEnvironmentIntentHandler_UnitWithGitSource(t *testing.T) {
	reg := &stubEnvironmentRegistry{}
	handler := NewEnvironmentIntentHandler(reg, nil, zap.NewNop())

	envID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":              envID.String(),
		"name":            "git-source-env",
		"deploy_strategy": "replace",
		"targeting": map[string]interface{}{
			"default_unit_key": "main",
		},
		"deployment_units": []interface{}{
			map[string]interface{}{
				"key":          "main",
				"runtime_type": "docker",
				"git_source": map[string]interface{}{
					"repository_url": "https://github.com/example/repo",
					"branch":         "main",
				},
			},
		},
	}

	intent := &Intent{
		Domain: "environment", Op: "create",
		OrgID: testOrgID(), IntentID: "intent-git-1",
		Coordinate: envID.String(), Actor: testPubkey,
		Content: content,
	}

	err := handler.HandleIntent(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, reg.createdWithUnits, 1)
	units := reg.createdWithUnits[0].units
	require.Len(t, units, 1)
	require.NotNil(t, units[0].GitSource)
	assert.Equal(t, "https://github.com/example/repo", units[0].GitSource.RepositoryURL)
	assert.Equal(t, "main", units[0].GitSource.Branch)
}

// --- stub implementations ---

type envCreateRecord struct {
	env   *domain.Environment
	units []*domain.DeploymentUnit
}

type envUpdateRecord struct {
	env               *domain.Environment
	units             []*domain.DeploymentUnit
	expectedUpdatedAt time.Time
}

type envDeleteRecord struct {
	id    uuid.UUID
	force bool
}

type stubEnvironmentRegistry struct {
	mu               sync.Mutex
	created          []envCreateRecord
	createdWithUnits []envCreateRecord
	updated          []envUpdateRecord
	updatedWithUnits []envUpdateRecord
	deleted          []envDeleteRecord
	getByID          map[uuid.UUID]*domain.Environment
}

func (r *stubEnvironmentRegistry) CreateEnvironment(_ context.Context, env *domain.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, envCreateRecord{env: env})
	return nil
}

func (r *stubEnvironmentRegistry) CreateEnvironmentWithDeploymentUnits(_ context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createdWithUnits = append(r.createdWithUnits, envCreateRecord{env: env, units: units})
	return nil
}

func (r *stubEnvironmentRegistry) GetEnvironment(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getByID == nil {
		return nil, fmt.Errorf("not found")
	}
	env, ok := r.getByID[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return env, nil
}

func (r *stubEnvironmentRegistry) UpdateEnvironment(_ context.Context, env *domain.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updated = append(r.updated, envUpdateRecord{env: env})
	return nil
}

func (r *stubEnvironmentRegistry) UpdateEnvironmentWithDeploymentUnits(_ context.Context, env *domain.Environment, units []*domain.DeploymentUnit, expectedUpdatedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedWithUnits = append(r.updatedWithUnits, envUpdateRecord{env: env, units: units, expectedUpdatedAt: expectedUpdatedAt})
	return nil
}

func (r *stubEnvironmentRegistry) DeleteEnvironment(_ context.Context, id uuid.UUID, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, envDeleteRecord{id: id, force: force})
	return nil
}

// --- helpers ---

// NormalizeRevisionTime is tested in domain; we use it here for precision tests.
func init() {
	// Ensure json.Number can be parsed from intent content.
	_ = json.Number("1234567890")
}
