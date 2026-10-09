package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type toolProvisioningRepoFake struct {
	intent       *domain.ToolProvisionIntent
	updates      []domain.ToolProvisionStatus
	denylist     []domain.ToolDenylistEntry
	listStatuses [][]domain.ToolProvisionStatus
}

type approvalToolRepo struct {
	*toolProvisioningRepoFake
	applied int
}

func (r *approvalToolRepo) ApplyToolApprovalDecision(_ context.Context, id uuid.UUID, status domain.ToolProvisionStatus, _ string, _ time.Time) (*domain.ToolProvisionIntent, error) {
	r.applied++
	if r.intent == nil || r.intent.ID != id {
		return nil, fmt.Errorf("tool intent missing")
	}
	r.intent.Status = status
	return r.intent, nil
}

type recordingToolApprovalProcessor struct{ approved int }

func (*recordingToolApprovalProcessor) ProcessIntent(context.Context, uuid.UUID) error { return nil }
func (p *recordingToolApprovalProcessor) ProcessApprovedIntent(context.Context, uuid.UUID) error {
	p.approved++
	return nil
}

func TestToolApprovalRequiresCanonicalRequestAndAcceptance(t *testing.T) {
	requesterKey := nostr.Generate().Hex()
	operatorKey := nostr.Generate().Hex()
	serviceKey := nostr.Generate().Hex()
	serviceSigner, err := NewPrivateKeySigner(serviceKey)
	if err != nil {
		t.Fatal(err)
	}
	serviceID, envID, intentID := uuid.New(), uuid.New(), uuid.New()
	tools := []domain.ToolRequest{{Manager: "apt", Name: "curl", Version: "latest"}}
	requestBody, _ := json.Marshal(map[string]any{"service_id": serviceID.String(), "environment_id": envID.String(), "tools": tools})
	request := signedLLMRequest(t, requesterKey, KindToolProvisionRequest, string(requestBody), nil)
	row := &domain.ToolProvisionIntent{ID: intentID, ServiceID: serviceID, EnvironmentID: envID, RequestedTools: tools, Status: domain.ToolProvisionStatusAwaitingApproval, ApprovalRequired: true, NostrEventID: request.ID.Hex(), RequesterPubkey: request.PubKey.Hex()}
	repo := &approvalToolRepo{toolProvisioningRepoFake: &toolProvisioningRepoFake{intent: row}}
	processor := &recordingToolApprovalProcessor{}
	responses := &captureNostrPublisher{published: 1}
	store := openTestStore(t)
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{request.PubKey.Hex(), testNostrPubKeyHexFromPrivateKey(t, operatorKey)}}, nil, nil, serviceSigner, zap.NewNop(), WithCanonicalWorkflowEvents(store), WithToolProvisioningRepository(repo), WithToolResponder(NewToolResponder(responses, serviceSigner, zap.NewNop(), nil)))
	reactor.toolCoordinator = processor
	approval := signedLLMRequest(t, operatorKey, KindToolApprovalResponse, fmt.Sprintf(`{"intent_id":%q,"action":"approve"}`, intentID.String()), nil)
	if err := reactor.handleToolApprovalResponse(context.Background(), approval); err == nil {
		t.Fatal("SQL-only awaiting-approval row was accepted")
	}
	if repo.applied != 0 || processor.approved != 0 || len(responses.events) != 0 {
		t.Fatal("SQL-only row caused approval, execution, or publication")
	}
	retainTestWorkflowEvent(t, store, request)
	if err := reactor.handleToolApprovalResponse(context.Background(), approval); err == nil {
		t.Fatal("signed request without service acceptance was accepted")
	}
	receiptBody, _ := json.Marshal(map[string]any{"intent_id": intentID.String(), "service_id": serviceID.String(), "environment_id": envID.String(), "step": "queued"})
	receipt := signedLLMRequest(t, serviceKey, KindCASControlState, string(receiptBody), nostr.Tags{{"d", "tool-provisioning:" + intentID.String()}, {"e", request.ID.Hex()}, {"intent", intentID.String()}})
	retainTestWorkflowEvent(t, store, receipt)
	row.RequestedTools = []domain.ToolRequest{{Manager: "apt", Name: "different", Version: "latest"}}
	if err := reactor.handleToolApprovalResponse(context.Background(), approval); err == nil {
		t.Fatal("mismatched SQL request payload was accepted")
	}
	row.RequestedTools = tools
	if err := reactor.handleToolApprovalResponse(context.Background(), approval); err != nil {
		t.Fatalf("valid signed request and acceptance refused: %v", err)
	}
	if repo.applied != 1 || processor.approved != 1 || len(responses.events) != 1 {
		t.Fatalf("valid approval results: applied=%d executed=%d published=%d", repo.applied, processor.approved, len(responses.events))
	}
}

func (r *toolProvisioningRepoFake) CreateIntent(_ context.Context, intent *domain.ToolProvisionIntent) error {
	copy := *intent
	r.intent = &copy
	return nil
}

func (r *toolProvisioningRepoFake) GetIntent(_ context.Context, id uuid.UUID) (*domain.ToolProvisionIntent, error) {
	if r.intent == nil || r.intent.ID != id {
		return nil, nil
	}
	copy := *r.intent
	return &copy, nil
}

func (r *toolProvisioningRepoFake) UpdateIntent(_ context.Context, intent *domain.ToolProvisionIntent) error {
	copy := *intent
	r.intent = &copy
	r.updates = append(r.updates, intent.Status)
	return nil
}

func (r *toolProvisioningRepoFake) ListPendingApprovalIntents(context.Context) ([]domain.ToolProvisionIntent, error) {
	return nil, nil
}

func (r *toolProvisioningRepoFake) ListIntentsByStatus(_ context.Context, statuses ...domain.ToolProvisionStatus) ([]domain.ToolProvisionIntent, error) {
	r.listStatuses = append(r.listStatuses, append([]domain.ToolProvisionStatus(nil), statuses...))
	if r.intent == nil {
		return nil, nil
	}
	for _, status := range statuses {
		if r.intent.Status == status {
			return []domain.ToolProvisionIntent{*r.intent}, nil
		}
	}
	return nil, nil
}

func (r *toolProvisioningRepoFake) CreateRun(context.Context, *domain.ToolProvisionRun) error {
	return nil
}
func (r *toolProvisioningRepoFake) GetRun(context.Context, uuid.UUID) (*domain.ToolProvisionRun, error) {
	return nil, nil
}
func (r *toolProvisioningRepoFake) UpdateRun(context.Context, *domain.ToolProvisionRun) error {
	return nil
}
func (r *toolProvisioningRepoFake) GetProfileState(context.Context, uuid.UUID, uuid.UUID) (*domain.ToolProfileState, error) {
	return nil, nil
}
func (r *toolProvisioningRepoFake) UpsertProfileState(context.Context, *domain.ToolProfileState) error {
	return nil
}
func (r *toolProvisioningRepoFake) AddToDenylist(context.Context, *domain.ToolDenylistEntry) error {
	return nil
}
func (r *toolProvisioningRepoFake) RemoveFromDenylist(context.Context, string, string) error {
	return nil
}
func (r *toolProvisioningRepoFake) IsDenylisted(_ context.Context, packageName, manager string) (bool, error) {
	for _, entry := range r.denylist {
		if entry.PackageName == packageName && entry.Manager == manager {
			return true, nil
		}
	}
	return false, nil
}
func (r *toolProvisioningRepoFake) ListDenylist(context.Context) ([]domain.ToolDenylistEntry, error) {
	return append([]domain.ToolDenylistEntry(nil), r.denylist...), nil
}
func (r *toolProvisioningRepoFake) LogApproval(context.Context, uuid.UUID, string, string, string) error {
	return nil
}

func TestHandleToolProvisionRequestProcessesIntentFromEvent(t *testing.T) {
	t.Parallel()

	requesterKey := nostr.Generate().Hex()
	requester := testNostrPubKeyHexFromPrivateKey(t, requesterKey)
	serviceID := uuid.New()
	envID := uuid.New()
	repo := &toolProvisioningRepoFake{denylist: []domain.ToolDenylistEntry{{
		PackageName: "curl",
		Manager:     "apt",
		Reason:      "blocked by test policy",
	}}}
	security := service.NewToolSecurityService(repo, nil, zap.NewNop(), service.ToolSecurityConfig{})
	coordinator := service.NewToolProvisioningCoordinator(repo, nil, nil, security, nil, nil, nil, nil, zap.NewNop(), service.ToolProvisioningConfig{})
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requester}}, nil, nil, nil, zap.NewNop(),
		WithToolProvisioningRepository(repo),
		WithToolProvisioningCoordinator(coordinator),
	)

	content, err := json.Marshal(map[string]any{
		"service_id":     serviceID.String(),
		"environment_id": envID.String(),
		"operation":      "install",
		"tools": []map[string]string{{
			"manager": "apt",
			"name":    "curl",
			"version": "latest",
			"source":  "debian",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	event := &nostr.Event{ID: testNostrID("request-event"), PubKey: testNostrPubKeyFromPrivateKey(t, requesterKey), Kind: KindToolProvisionRequest, Content: string(content)}
	if err := reactor.handleToolProvisionRequest(context.Background(), event); err != nil {
		t.Fatalf("handle tool provision request: %v", err)
	}
	if repo.intent == nil {
		t.Fatal("expected tool provisioning intent to be created")
	}
	if repo.intent.Status != domain.ToolProvisionStatusFailed {
		t.Fatalf("expected event-driven processing to advance intent to failed, got %q", repo.intent.Status)
	}
	if len(repo.listStatuses) != 0 {
		t.Fatalf("handler should not use repository polling/listing to discover the new intent, got calls: %#v", repo.listStatuses)
	}
}
