package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
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

func TestHandleToolApprovalResponsePausedBeforeSQLOrPublication(t *testing.T) {
	actorKey := nostr.Generate().Hex()
	actor := testNostrPubKeyHexFromPrivateKey(t, actorKey)
	for _, source := range []string{"sql-only", "sql-row-with-event-id"} {
		t.Run(source, func(t *testing.T) {
			id := uuid.New()
			repo := newAtomicToolApprovalRepo(id, domain.ToolProvisionStatusAwaitingApproval)
			capture := &captureNostrPublisher{}
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, nil, zap.NewNop(),
				WithToolProvisioningRepository(repo), WithControlPlanePublisher(capture))
			if source == "sql-row-with-event-id" {
				request := signedLLMRequest(t, actorKey, KindToolProvisionRequest,
					fmt.Sprintf(`{"service_id":%q,"environment_id":%q,"operation":"install","tools":[{"manager":"apt","name":"curl","version":"latest"}]}`, uuid.NewString(), uuid.NewString()), nil)
				repo.intent.NostrEventID = request.ID.Hex()
			}
			// A later SQL edit to resolution must not redirect the build.
			repo.intent.ResolvedTools = []domain.ResolvedTool{{Name: "sql-package", Manager: "apt", Source: "sql-source"}}
			approval := toolApprovalEvent(t, actorKey, "approval-"+source, id, "approve")
			err := reactor.handleToolApprovalResponse(t.Context(), approval)
			if err == nil || !strings.Contains(err.Error(), "tool approval paused") {
				t.Fatalf("expected paused refusal, got %v", err)
			}
			calls, applied, logs, status := repo.counts()
			if calls != 0 || applied != 0 || logs != 0 || status != domain.ToolProvisionStatusAwaitingApproval {
				t.Fatalf("approval mutated SQL: calls=%d applied=%d logs=%d status=%s", calls, applied, logs, status)
			}
			if len(capture.events) != 0 {
				t.Fatalf("approval published %d events", len(capture.events))
			}
		})
	}
}
