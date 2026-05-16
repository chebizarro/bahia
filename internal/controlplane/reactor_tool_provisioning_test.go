package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
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

	requester := "1111111111111111111111111111111111111111111111111111111111111111"
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

	event := &nostr.Event{ID: "request-event", PubKey: requester, Kind: KindToolProvisionRequest, Content: string(content)}
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
