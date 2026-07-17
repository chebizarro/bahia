package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testDeploymentIntentRepo struct {
	intents map[uuid.UUID]*domain.DeploymentIntent
	order   []uuid.UUID
}

func newTestDeploymentIntentRepo() *testDeploymentIntentRepo {
	return &testDeploymentIntentRepo{intents: make(map[uuid.UUID]*domain.DeploymentIntent)}
}

func (m *testDeploymentIntentRepo) Create(_ context.Context, di *domain.DeploymentIntent) error {
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	now := time.Now().UTC()
	if di.CreatedAt.IsZero() {
		di.CreatedAt = now
	}
	di.UpdatedAt = now
	m.intents[di.ID] = di
	m.order = append([]uuid.UUID{di.ID}, m.order...)
	return nil
}

func (m *testDeploymentIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	intent, ok := m.intents[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return intent, nil
}

func (m *testDeploymentIntentRepo) GetByHiveResultEventID(_ context.Context, eventID string) (*domain.DeploymentIntent, error) {
	for _, intent := range m.intents {
		if intent.Metadata != nil && intent.Metadata["hive_ci_result_event_id"] == eventID {
			return intent, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testDeploymentIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	out := make([]domain.DeploymentIntent, 0, len(m.intents))
	for _, id := range m.order {
		intent := m.intents[id]
		if intent.ServiceID == serviceID && intent.EnvironmentID == envID {
			out = append(out, *intent)
		}
	}
	if offset >= len(out) {
		return []domain.DeploymentIntent{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *testDeploymentIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	intent, ok := m.intents[id]
	if !ok {
		return repository.ErrNotFound
	}
	intent.Status = status
	intent.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *testDeploymentIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	intent, ok := m.intents[id]
	if !ok {
		return repository.ErrNotFound
	}
	intent.ApprovalStatus = status
	if status == domain.ApprovalStatusApproved {
		now := time.Now().UTC()
		intent.ApprovedAt = &now
	}
	intent.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *testDeploymentIntentRepo) UpdateDesiredState(_ context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	intent, ok := m.intents[id]
	if !ok {
		return repository.ErrNotFound
	}
	intent.DesiredState = desiredState
	intent.DesiredHash = desiredHash
	intent.UpdatedAt = time.Now().UTC()
	return nil
}

type testDeploymentStateRepo struct {
	states map[string]*domain.EnvironmentServiceState
}

func newTestDeploymentStateRepo() *testDeploymentStateRepo {
	return &testDeploymentStateRepo{states: make(map[string]*domain.EnvironmentServiceState)}
}

func deploymentStateKey(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}

func (m *testDeploymentStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	state.UpdatedAt = time.Now().UTC()
	m.states[deploymentStateKey(state.ServiceID, state.EnvironmentID)] = state
	return nil
}

func (m *testDeploymentStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state, ok := m.states[deploymentStateKey(serviceID, envID)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return state, nil
}

func (m *testDeploymentStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	out := make([]domain.EnvironmentServiceState, 0)
	for _, state := range m.states {
		if state.EnvironmentID == envID {
			out = append(out, *state)
		}
	}
	return out, nil
}

func (m *testDeploymentStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	out := make([]domain.EnvironmentServiceState, 0)
	for _, state := range m.states {
		if state.ServiceID == serviceID {
			out = append(out, *state)
		}
	}
	return out, nil
}

func (m *testDeploymentStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	out := make([]domain.EnvironmentServiceState, 0)
	for _, state := range m.states {
		if state.DriftStatus == domain.DriftStatusDrifted {
			out = append(out, *state)
		}
	}
	return out, nil
}

func (m *testDeploymentStateRepo) ListDueForObservation(_ context.Context, _ time.Time) ([]domain.EnvironmentServiceState, error) {
	return m.ListAll(authorizedMCPContext())
}

func (m *testDeploymentStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	out := make([]domain.EnvironmentServiceState, 0, len(m.states))
	for _, state := range m.states {
		out = append(out, *state)
	}
	return out, nil
}

type captureServiceCommandPublisher struct {
	deploy   *controlplane.ServiceDeployCommand
	rollback *controlplane.ServiceRollbackCommand
	approval *controlplane.ServiceApprovalCommand
	err      error
}

func (p *captureServiceCommandPublisher) PublishDeployRequest(_ context.Context, cmd controlplane.ServiceDeployCommand) (*controlplane.ServiceCommandReceipt, error) {
	p.deploy = &cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.ServiceCommandReceipt{RequestEventID: "deploy-event", RequestPubkey: "operator", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindDeploymentIntentRegistry, StateKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, ServiceID: cmd.ServiceID.String(), EnvironmentID: cmd.EnvironmentID.String(), ArtifactID: cmd.ArtifactID.String()}, nil
}

func (p *captureServiceCommandPublisher) PublishRollbackRequest(_ context.Context, cmd controlplane.ServiceRollbackCommand) (*controlplane.ServiceCommandReceipt, error) {
	p.rollback = &cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.ServiceCommandReceipt{RequestEventID: "rollback-event", RequestPubkey: "operator", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindDeploymentIntentRegistry, StateKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, ServiceID: cmd.ServiceID.String(), EnvironmentID: cmd.EnvironmentID.String()}, nil
}

func (p *captureServiceCommandPublisher) PublishDeploymentApprovalRequest(_ context.Context, cmd controlplane.ServiceApprovalCommand) (*controlplane.ServiceCommandReceipt, error) {
	p.approval = &cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.ServiceCommandReceipt{RequestEventID: "approval-event", RequestPubkey: "operator", RequestKind: controlplane.KindContextVMMessage, StatusKind: controlplane.KindNIP38Status, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindDeploymentIntentRegistry, StateKind: controlplane.KindCASControlState, DTag: cmd.IdempotencyKey, IdempotencyKey: cmd.IdempotencyKey, Status: "submitted", PublishedRelays: 1, IntentID: cmd.IntentID.String(), Decision: cmd.Decision}, nil
}

type deploymentTestFixture struct {
	server    *Server
	services  *testServiceRepo
	envs      *testEnvironmentRepo
	artifacts *testArtifactRepo
	intents   *testDeploymentIntentRepo
	state     *testDeploymentStateRepo
	commands  *captureServiceCommandPublisher
	serviceID uuid.UUID
	envID     uuid.UUID
}

func newTestMCPDeploymentServer(t *testing.T, protected bool) *deploymentTestFixture {
	t.Helper()

	svcRepo := newTestServiceRepo()
	envRepo := newTestEnvironmentRepo()
	artifactRepo := newTestArtifactRepo()
	intentRepo := newTestDeploymentIntentRepo()
	stateRepo := newTestDeploymentStateRepo()
	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		nil,
		artifactRepo,
		intentRepo,
		nil,
		nil,
		stateRepo,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)

	serviceID := uuid.New()
	envID := uuid.New()
	svcRepo.services[serviceID] = &domain.Service{
		ID:            serviceID,
		Name:          "api",
		ArtifactRepo:  "registry.example.com/api",
		DefaultBranch: "main",
		RuntimeType:   domain.RuntimeTypeDocker,
	}
	envRepo.environments[envID] = &domain.Environment{
		ID:             envID,
		Name:           "staging",
		Protected:      protected,
		DeployStrategy: domain.DeployStrategyReplace,
	}

	commands := &captureServiceCommandPublisher{}
	return &deploymentTestFixture{
		server:    NewServerWithOptions(registry, zap.NewNop(), ServerDeps{ServiceCommandPublisher: commands}),
		services:  svcRepo,
		envs:      envRepo,
		artifacts: artifactRepo,
		intents:   intentRepo,
		state:     stateRepo,
		commands:  commands,
		serviceID: serviceID,
		envID:     envID,
	}
}

func (f *deploymentTestFixture) addArtifact(id uuid.UUID, digest string) uuid.UUID {
	if id == uuid.Nil {
		id = uuid.New()
	}
	f.artifacts.artifacts[id] = &domain.Artifact{
		ID:          id,
		ServiceID:   f.serviceID,
		ImageRepo:   "registry.example.com/api",
		ImageTag:    "latest",
		ImageDigest: digest,
		ScanStatus:  domain.ScanStatusClean,
	}
	return id
}

func TestGetTools_IncludesDeploymentWorkflowTools(t *testing.T) {
	fixture := newTestMCPDeploymentServer(t, false)
	tools := fixture.server.GetTools()

	required := map[string]bool{
		"bahia_deploy":                false,
		"bahia_create_intent":         false,
		"bahia_rollback":              false,
		"bahia_get_deployment_status": false,
		"bahia_approve_deployment":    false,
		"bahia_approve_intent":        false,
		"bahia_reject_deployment":     false,
		"bahia_reject_intent":         false,
	}

	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}

	for name, present := range required {
		if !present {
			t.Fatalf("missing deployment workflow tool %s", name)
		}
	}
}

func TestCallTool_DeployAndCreateIntent_CreateDeploymentIntent(t *testing.T) {
	ctx := authorizedMCPContext()

	for _, toolName := range []string{"bahia_deploy", "bahia_create_intent"} {
		t.Run(toolName, func(t *testing.T) {
			fixture := newTestMCPDeploymentServer(t, false)
			artifactID := fixture.addArtifact(uuid.Nil, "sha256:new")

			result, err := fixture.server.CallTool(ctx, toolName, map[string]interface{}{
				"service_id":     fixture.serviceID.String(),
				"environment_id": fixture.envID.String(),
				"artifact_id":    artifactID.String(),
				"requested_by":   "alice",
			})
			if err != nil {
				t.Fatalf("call err: %v", err)
			}
			if result.IsError {
				t.Fatalf("%s returned error: %s", toolName, result.Content[0].Text)
			}

			payload := decodeResultMap(t, result)
			if payload["status"] != "submitted" || payload["request_event_id"] != "deploy-event" || payload["request_kind"].(float64) != float64(controlplane.KindContextVMMessage) {
				t.Fatalf("unexpected signer-first deploy receipt: %#v", payload)
			}
			if payload["service_id"] != fixture.serviceID.String() || payload["environment_id"] != fixture.envID.String() || payload["artifact_id"] != artifactID.String() {
				t.Fatalf("unexpected deploy payload: %#v", payload)
			}
			if fixture.commands.deploy == nil {
				t.Fatalf("expected deploy command to be published")
			}
			if fixture.commands.deploy.RequestedBy != "alice" || fixture.commands.deploy.ServiceID != fixture.serviceID || fixture.commands.deploy.EnvironmentID != fixture.envID || fixture.commands.deploy.ArtifactID != artifactID {
				t.Fatalf("unexpected captured deploy command: %#v", fixture.commands.deploy)
			}
			if len(fixture.intents.intents) != 0 {
				t.Fatalf("direct registry fallback persisted intents: %#v", fixture.intents.intents)
			}
		})
	}
}

func TestCallTool_ApprovalAndRejectionFlows(t *testing.T) {
	ctx := authorizedMCPContext()

	for _, tc := range []struct {
		name     string
		tool     string
		decision string
	}{
		{name: "approve deployment tool", tool: "bahia_approve_deployment", decision: "approve"},
		{name: "approve intent alias", tool: "bahia_approve_intent", decision: "approve"},
		{name: "reject deployment tool", tool: "bahia_reject_deployment", decision: "reject"},
		{name: "reject intent alias", tool: "bahia_reject_intent", decision: "reject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestMCPDeploymentServer(t, true)
			intentID := uuid.New()
			res, err := fixture.server.CallTool(ctx, tc.tool, map[string]interface{}{"intent_id": intentID.String(), "requested_by": "approver"})
			if err != nil {
				t.Fatalf("call err: %v", err)
			}
			if res.IsError {
				t.Fatalf("%s returned error: %s", tc.tool, res.Content[0].Text)
			}
			payload := decodeResultMap(t, res)
			if payload["status"] != "submitted" || payload["request_event_id"] != "approval-event" || payload["intent_id"] != intentID.String() || payload["decision"] != tc.decision {
				t.Fatalf("unexpected approval receipt: %#v", payload)
			}
			if fixture.commands.approval == nil || fixture.commands.approval.IntentID != intentID || fixture.commands.approval.Decision != tc.decision || fixture.commands.approval.AgentID != "approver" {
				t.Fatalf("unexpected captured approval command: %#v", fixture.commands.approval)
			}
			if len(fixture.intents.intents) != 0 {
				t.Fatalf("direct registry fallback persisted intents: %#v", fixture.intents.intents)
			}
		})
	}
}

func TestCallTool_Rollback_CreatesRollbackIntent(t *testing.T) {
	ctx := authorizedMCPContext()
	fixture := newTestMCPDeploymentServer(t, false)

	result, err := fixture.server.CallTool(ctx, "bahia_rollback", map[string]interface{}{
		"service_id":     fixture.serviceID.String(),
		"environment_id": fixture.envID.String(),
		"requested_by":   "operator",
	})
	if err != nil {
		t.Fatalf("rollback call err: %v", err)
	}
	if result.IsError {
		t.Fatalf("rollback returned error: %s", result.Content[0].Text)
	}
	payload := decodeResultMap(t, result)
	if payload["status"] != "submitted" || payload["request_event_id"] != "rollback-event" || payload["service_id"] != fixture.serviceID.String() || payload["environment_id"] != fixture.envID.String() {
		t.Fatalf("unexpected rollback receipt: %#v", payload)
	}
	if fixture.commands.rollback == nil || fixture.commands.rollback.ServiceID != fixture.serviceID || fixture.commands.rollback.EnvironmentID != fixture.envID || fixture.commands.rollback.AgentID != "operator" {
		t.Fatalf("unexpected captured rollback command: %#v", fixture.commands.rollback)
	}
	if len(fixture.intents.intents) != 0 {
		t.Fatalf("direct registry fallback persisted intents: %#v", fixture.intents.intents)
	}
}

func TestCallTool_GetDeploymentStatus(t *testing.T) {
	ctx := authorizedMCPContext()
	fixture := newTestMCPDeploymentServer(t, false)
	artifactID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()
	lastReconciled := time.Date(2026, 5, 2, 12, 34, 56, 0, time.UTC)
	if err := fixture.state.Upsert(ctx, &domain.EnvironmentServiceState{
		ServiceID:            fixture.serviceID,
		EnvironmentID:        fixture.envID,
		DesiredArtifactID:    &artifactID,
		DesiredIntentID:      &intentID,
		LastSuccessfulRunID:  &runID,
		DriftStatus:          domain.DriftStatusDrifted,
		LastReconciledAt:     &lastReconciled,
		CurrentObservationID: ptrUUID(uuid.New()),
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	result, err := fixture.server.CallTool(ctx, "bahia_get_deployment_status", map[string]interface{}{
		"service_id":     fixture.serviceID.String(),
		"environment_id": fixture.envID.String(),
	})
	if err != nil {
		t.Fatalf("status call err: %v", err)
	}
	if result.IsError {
		t.Fatalf("status returned error: %s", result.Content[0].Text)
	}
	payload := decodeResultMap(t, result)
	if payload["service_id"] != fixture.serviceID.String() || payload["environment_id"] != fixture.envID.String() {
		t.Fatalf("unexpected status identity payload: %#v", payload)
	}
	if payload["desired_artifact_id"] != artifactID.String() {
		t.Fatalf("expected desired artifact %s, got %v", artifactID, payload["desired_artifact_id"])
	}
	if payload["desired_intent_id"] != intentID.String() {
		t.Fatalf("expected desired intent %s, got %v", intentID, payload["desired_intent_id"])
	}
	if payload["last_successful_run_id"] != runID.String() {
		t.Fatalf("expected last successful run %s, got %v", runID, payload["last_successful_run_id"])
	}
	if payload["drift_status"] != string(domain.DriftStatusDrifted) {
		t.Fatalf("expected drifted status, got %v", payload["drift_status"])
	}
	if payload["last_reconciled_at"] != "2026-05-02T12:34:56Z" {
		t.Fatalf("unexpected last_reconciled_at: %v", payload["last_reconciled_at"])
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID {
	return &id
}
