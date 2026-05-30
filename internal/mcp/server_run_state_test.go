package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testIntentRepo struct {
	intents map[uuid.UUID]*domain.DeploymentIntent
	order   []uuid.UUID
}

func newTestIntentRepo() *testIntentRepo {
	return &testIntentRepo{intents: make(map[uuid.UUID]*domain.DeploymentIntent)}
}

func (r *testIntentRepo) Create(_ context.Context, intent *domain.DeploymentIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	if _, exists := r.intents[intent.ID]; !exists {
		r.order = append(r.order, intent.ID)
	}
	r.intents[intent.ID] = intent
	return nil
}

func (r *testIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	intent, ok := r.intents[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return intent, nil
}

func (r *testIntentRepo) GetByHiveResultEventID(_ context.Context, _ string) (*domain.DeploymentIntent, error) {
	return nil, repository.ErrNotFound
}

func (r *testIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	var out []domain.DeploymentIntent
	for _, id := range r.order {
		intent := r.intents[id]
		if intent.ServiceID != serviceID || intent.EnvironmentID != envID {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		out = append(out, *intent)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *testIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	if intent := r.intents[id]; intent != nil {
		intent.Status = status
		intent.UpdatedAt = time.Now().UTC()
	}
	return nil
}

func (r *testIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	if intent := r.intents[id]; intent != nil {
		intent.ApprovalStatus = status
		intent.UpdatedAt = time.Now().UTC()
	}
	return nil
}

type testRuntimeObservationRepo struct {
	observations map[string]*domain.RuntimeObservation
}

func newTestRuntimeObservationRepo() *testRuntimeObservationRepo {
	return &testRuntimeObservationRepo{observations: make(map[string]*domain.RuntimeObservation)}
}

func observationKey(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}

func (r *testRuntimeObservationRepo) Create(_ context.Context, obs *domain.RuntimeObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	r.observations[observationKey(obs.ServiceID, obs.EnvironmentID)] = obs
	return nil
}

func (r *testRuntimeObservationRepo) GetLatest(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	obs := r.observations[observationKey(serviceID, envID)]
	if obs == nil {
		return nil, nil
	}
	return obs, nil
}

func (r *testRuntimeObservationRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error) {
	obs := r.observations[observationKey(serviceID, envID)]
	if obs == nil || limit == 0 {
		return nil, nil
	}
	return []domain.RuntimeObservation{*obs}, nil
}

type testRuntimeStateRepo struct {
	states map[string]*domain.EnvironmentServiceState
}

func newTestRuntimeStateRepo() *testRuntimeStateRepo {
	return &testRuntimeStateRepo{states: make(map[string]*domain.EnvironmentServiceState)}
}

func stateKey(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}

func (r *testRuntimeStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	r.states[stateKey(state.ServiceID, state.EnvironmentID)] = state
	return nil
}

func (r *testRuntimeStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state := r.states[stateKey(serviceID, envID)]
	if state == nil {
		return nil, repository.ErrNotFound
	}
	return state, nil
}

func (r *testRuntimeStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	var out []domain.EnvironmentServiceState
	for _, state := range r.states {
		if state.EnvironmentID == envID {
			out = append(out, *state)
		}
	}
	return out, nil
}

func (r *testRuntimeStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	var out []domain.EnvironmentServiceState
	for _, state := range r.states {
		if state.ServiceID == serviceID {
			out = append(out, *state)
		}
	}
	return out, nil
}

func (r *testRuntimeStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	var out []domain.EnvironmentServiceState
	for _, state := range r.states {
		if state.DriftStatus == domain.DriftStatusDrifted {
			out = append(out, *state)
		}
	}
	return out, nil
}

func (r *testRuntimeStateRepo) ListDueForObservation(_ context.Context, _ time.Time) ([]domain.EnvironmentServiceState, error) {
	return r.ListAll(context.Background())
}

func (r *testRuntimeStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	out := make([]domain.EnvironmentServiceState, 0, len(r.states))
	for _, state := range r.states {
		out = append(out, *state)
	}
	return out, nil
}

func newTestMCPRunStateServer() (*Server, *testIntentRepo, *testRunRepo, *testRuntimeObservationRepo, *testRuntimeStateRepo) {
	intentRepo := newTestIntentRepo()
	runRepo := newTestRunRepo()
	observationRepo := newTestRuntimeObservationRepo()
	stateRepo := newTestRuntimeStateRepo()
	registry := service.NewRegistryService(
		nil,
		nil,
		nil,
		nil,
		intentRepo,
		runRepo,
		observationRepo,
		stateRepo,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	return NewServer(registry, zap.NewNop()), intentRepo, runRepo, observationRepo, stateRepo
}

func TestGetTools_IncludesRunStateAndIntentTools(t *testing.T) {
	server, _, _, _, _ := newTestMCPRunStateServer()
	required := map[string]bool{
		"bahia_list_states":     false,
		"bahia_list_drifted":    false,
		"bahia_get_observation": false,
		"bahia_list_intents":    false,
		"bahia_get_intent":      false,
		"bahia_list_runs":       false,
		"bahia_create_run":      false,
		"bahia_get_run":         false,
		"bahia_complete_run":    false,
	}

	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestCallTool_RunLifecycle(t *testing.T) {
	ctx := context.Background()
	server, intentRepo, runRepo, _, stateRepo := newTestMCPRunStateServer()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	now := time.Date(2026, 5, 2, 15, 0, 0, 0, time.UTC)
	intentRepo.intents[intentID] = &domain.DeploymentIntent{
		ID:             intentID,
		ServiceID:      serviceID,
		EnvironmentID:  envID,
		ArtifactID:     artifactID,
		RequestedBy:    "tester",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusApproved,
		Status:         domain.IntentStatusApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	intentRepo.order = append(intentRepo.order, intentID)

	createRes, err := server.CallTool(ctx, "bahia_create_run", map[string]interface{}{
		"intent_id":     intentID.String(),
		"worker_pubkey": "worker-pubkey",
	})
	if err != nil {
		t.Fatalf("create run call err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create run returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	runID := createPayload["run_id"].(string)
	if createPayload["status"] != "created" || createPayload["intent_id"] != intentID.String() {
		t.Fatalf("unexpected create payload: %#v", createPayload)
	}
	if intentRepo.intents[intentID].Status != domain.IntentStatusDeploying {
		t.Fatalf("intent status = %s, want deploying", intentRepo.intents[intentID].Status)
	}

	listRes, err := server.CallTool(ctx, "bahia_list_runs", map[string]interface{}{"intent_id": intentID.String()})
	if err != nil {
		t.Fatalf("list runs call err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list runs returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if listPayload["total"] != float64(1) {
		t.Fatalf("total runs = %v, want 1", listPayload["total"])
	}
	runs := listPayload["runs"].([]interface{})
	listedRun := runs[0].(map[string]interface{})
	if listedRun["id"] != runID || listedRun["worker_pubkey"] != "worker-pubkey" || listedRun["status"] != string(domain.RunStatusQueued) {
		t.Fatalf("unexpected listed run: %#v", listedRun)
	}

	getRes, err := server.CallTool(ctx, "bahia_get_run", map[string]interface{}{"run_id": runID})
	if err != nil {
		t.Fatalf("get run call err: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("get run returned error: %s", getRes.Content[0].Text)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["deployment_intent_id"] != intentID.String() || getPayload["status"] != string(domain.RunStatusQueued) {
		t.Fatalf("unexpected get run payload: %#v", getPayload)
	}

	completeRes, err := server.CallTool(ctx, "bahia_complete_run", map[string]interface{}{
		"run_id":    runID,
		"status":    "succeeded",
		"exit_code": float64(0),
	})
	if err != nil {
		t.Fatalf("complete run call err: %v", err)
	}
	if completeRes.IsError {
		t.Fatalf("complete run returned error: %s", completeRes.Content[0].Text)
	}
	completePayload := decodeResultMap(t, completeRes)
	if completePayload["status"] != "completed" || completePayload["run_id"] != runID || completePayload["message"] != "Deployment run marked as succeeded" {
		t.Fatalf("unexpected complete payload: %#v", completePayload)
	}

	parsedRunID := uuid.MustParse(runID)
	if runRepo.runs[parsedRunID].Status != domain.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded", runRepo.runs[parsedRunID].Status)
	}
	if got := *runRepo.runs[parsedRunID].ExitCode; got != 0 {
		t.Fatalf("exit code = %d, want 0", got)
	}
	if intentRepo.intents[intentID].Status != domain.IntentStatusDeployed {
		t.Fatalf("intent status = %s, want deployed", intentRepo.intents[intentID].Status)
	}
	state := stateRepo.states[stateKey(serviceID, envID)]
	if state == nil || state.LastSuccessfulRunID == nil || *state.LastSuccessfulRunID != parsedRunID {
		t.Fatalf("state was not updated with successful run: %#v", state)
	}
}

func TestCallTool_ListAndGetIntents(t *testing.T) {
	ctx := context.Background()
	server, intentRepo, _, _, _ := newTestMCPRunStateServer()
	serviceID := uuid.New()
	envID := uuid.New()
	otherEnvID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()
	now := time.Date(2026, 5, 2, 16, 0, 0, 0, time.UTC)
	_ = intentRepo.Create(ctx, &domain.DeploymentIntent{
		ID:             firstID,
		ServiceID:      serviceID,
		EnvironmentID:  envID,
		ArtifactID:     uuid.New(),
		RequestedBy:    "alice",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusApproved,
		Status:         domain.IntentStatusApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	_ = intentRepo.Create(ctx, &domain.DeploymentIntent{
		ID:             secondID,
		ServiceID:      serviceID,
		EnvironmentID:  envID,
		ArtifactID:     uuid.New(),
		RequestedBy:    "bob",
		SourceKind:     domain.SourceKindRollback,
		ApprovalStatus: domain.ApprovalStatusPending,
		Status:         domain.IntentStatusPending,
		CreatedAt:      now.Add(time.Minute),
		UpdatedAt:      now.Add(time.Minute),
	})
	_ = intentRepo.Create(ctx, &domain.DeploymentIntent{
		ID:             uuid.New(),
		ServiceID:      serviceID,
		EnvironmentID:  otherEnvID,
		ArtifactID:     uuid.New(),
		RequestedBy:    "ignored",
		SourceKind:     domain.SourceKindManual,
		ApprovalStatus: domain.ApprovalStatusApproved,
		Status:         domain.IntentStatusApproved,
		CreatedAt:      now,
		UpdatedAt:      now,
	})

	listRes, err := server.CallTool(ctx, "bahia_list_intents", map[string]interface{}{
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"limit":          float64(1),
	})
	if err != nil {
		t.Fatalf("list intents call err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list intents returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if listPayload["total"] != float64(1) {
		t.Fatalf("total intents = %v, want 1", listPayload["total"])
	}
	intents := listPayload["intents"].([]interface{})
	listedIntent := intents[0].(map[string]interface{})
	if listedIntent["id"] != firstID.String() || listedIntent["requested_by"] != "alice" {
		t.Fatalf("unexpected listed intent: %#v", listedIntent)
	}

	getRes, err := server.CallTool(ctx, "bahia_get_intent", map[string]interface{}{"intent_id": secondID.String()})
	if err != nil {
		t.Fatalf("get intent call err: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("get intent returned error: %s", getRes.Content[0].Text)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["id"] != secondID.String() || getPayload["source_kind"] != string(domain.SourceKindRollback) || getPayload["status"] != string(domain.IntentStatusPending) {
		t.Fatalf("unexpected get intent payload: %#v", getPayload)
	}
}

func TestCallTool_RuntimeStateAndObservationHandlers(t *testing.T) {
	ctx := context.Background()
	server, _, _, observationRepo, stateRepo := newTestMCPRunStateServer()
	serviceID := uuid.New()
	envID := uuid.New()
	otherServiceID := uuid.New()
	otherEnvID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	obsID := uuid.New()
	now := time.Date(2026, 5, 2, 17, 0, 0, 0, time.UTC)
	stateRepo.states[stateKey(serviceID, envID)] = &domain.EnvironmentServiceState{
		ServiceID:            serviceID,
		EnvironmentID:        envID,
		DesiredArtifactID:    &artifactID,
		DesiredIntentID:      &intentID,
		CurrentObservationID: &obsID,
		DriftStatus:          domain.DriftStatusInSync,
		UpdatedAt:            now,
	}
	stateRepo.states[stateKey(otherServiceID, envID)] = &domain.EnvironmentServiceState{
		ServiceID:     otherServiceID,
		EnvironmentID: envID,
		DriftStatus:   domain.DriftStatusDrifted,
		UpdatedAt:     now.Add(time.Minute),
	}
	stateRepo.states[stateKey(serviceID, otherEnvID)] = &domain.EnvironmentServiceState{
		ServiceID:     serviceID,
		EnvironmentID: otherEnvID,
		DriftStatus:   domain.DriftStatusDeploying,
		UpdatedAt:     now.Add(2 * time.Minute),
	}
	_ = observationRepo.Create(ctx, &domain.RuntimeObservation{
		ID:                  obsID,
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:abc123",
		ObservedImageRepo:   "registry.example.com/api",
		ObservedContainerID: "container-1",
		ObservedHost:        "worker-1",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "runtime-test",
		ObservedAt:          now,
	})

	listAllRes, err := server.CallTool(ctx, "bahia_list_states", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list all states call err: %v", err)
	}
	if listAllRes.IsError {
		t.Fatalf("list all states returned error: %s", listAllRes.Content[0].Text)
	}
	if total := decodeResultMap(t, listAllRes)["total"]; total != float64(3) {
		t.Fatalf("total states = %v, want 3", total)
	}

	listEnvRes, err := server.CallTool(ctx, "bahia_list_states", map[string]interface{}{"environment_id": envID.String()})
	if err != nil {
		t.Fatalf("list env states call err: %v", err)
	}
	if listEnvRes.IsError {
		t.Fatalf("list env states returned error: %s", listEnvRes.Content[0].Text)
	}
	listEnvPayload := decodeResultMap(t, listEnvRes)
	if listEnvPayload["total"] != float64(2) {
		t.Fatalf("env state total = %v, want 2", listEnvPayload["total"])
	}

	driftedRes, err := server.CallTool(ctx, "bahia_list_drifted", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list drifted call err: %v", err)
	}
	if driftedRes.IsError {
		t.Fatalf("list drifted returned error: %s", driftedRes.Content[0].Text)
	}
	driftedPayload := decodeResultMap(t, driftedRes)
	if driftedPayload["total"] != float64(1) {
		t.Fatalf("drifted total = %v, want 1", driftedPayload["total"])
	}
	drifted := driftedPayload["drifted"].([]interface{})[0].(map[string]interface{})
	if drifted["service_id"] != otherServiceID.String() || drifted["drift_status"] != string(domain.DriftStatusDrifted) {
		t.Fatalf("unexpected drifted state: %#v", drifted)
	}

	obsRes, err := server.CallTool(ctx, "bahia_get_observation", map[string]interface{}{
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
	})
	if err != nil {
		t.Fatalf("get observation call err: %v", err)
	}
	if obsRes.IsError {
		t.Fatalf("get observation returned error: %s", obsRes.Content[0].Text)
	}
	obsPayload := decodeResultMap(t, obsRes)
	if obsPayload["id"] != obsID.String() || obsPayload["image_digest"] != "sha256:abc123" || obsPayload["container_id"] != "container-1" || obsPayload["health_status"] != string(domain.HealthStatusHealthy) {
		t.Fatalf("unexpected observation payload: %#v", obsPayload)
	}
}
