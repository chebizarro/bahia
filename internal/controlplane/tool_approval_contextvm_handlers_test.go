package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type atomicToolApprovalRepo struct {
	*toolProvisioningRepoFake
	mu            sync.Mutex
	decisionCalls int
	applied       int
	approvalLogs  int
}

func newAtomicToolApprovalRepo(id uuid.UUID, status domain.ToolProvisionStatus) *atomicToolApprovalRepo {
	return &atomicToolApprovalRepo{toolProvisioningRepoFake: &toolProvisioningRepoFake{intent: &domain.ToolProvisionIntent{ID: id, Status: status}}}
}

func (r *atomicToolApprovalRepo) ApplyToolApprovalDecision(_ context.Context, id uuid.UUID, decision domain.ToolProvisionStatus, actorPubkey string, decidedAt time.Time) (*domain.ToolProvisionIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisionCalls++
	if r.intent == nil || r.intent.ID != id {
		return nil, repository.ErrNotFound
	}
	if r.intent.Status != domain.ToolProvisionStatusAwaitingApproval {
		return nil, repository.ErrConflict
	}
	copy := *r.intent
	copy.Status = decision
	if decision == domain.ToolProvisionStatusApproved {
		copy.ApprovedBy = actorPubkey
		copy.ApprovedAt = &decidedAt
	}
	r.intent = &copy
	r.applied++
	return &copy, nil
}

func (r *atomicToolApprovalRepo) LogApproval(context.Context, uuid.UUID, string, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvalLogs++
	return nil
}

func (r *atomicToolApprovalRepo) counts() (calls, applied, logs int, status domain.ToolProvisionStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.decisionCalls, r.applied, r.approvalLogs, r.intent.Status
}

type countingToolApprovalProcessor struct{ calls atomic.Int64 }

func (p *countingToolApprovalProcessor) ProcessIntent(context.Context, uuid.UUID) error {
	return nil
}

func (p *countingToolApprovalProcessor) ProcessApprovedIntent(context.Context, uuid.UUID) error {
	p.calls.Add(1)
	return nil
}

func toolApprovalEvent(t *testing.T, privateKey, requestID string, intentID uuid.UUID, action string) *nostr.Event {
	t.Helper()
	return makeContextVMEvent(t, privateKey, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":%q,"params":{"intent_id":%q,"action":%q,"reason":"operator reviewed"}}`, requestID, ContextVMMethodToolApprovalResponse, intentID.String(), action))
}

func toolApprovalContextVMRequest(t *testing.T, event *nostr.Event) ContextVMRequest {
	t.Helper()
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
		t.Fatalf("decode ContextVM request: %v", err)
	}
	return ContextVMRequest{Event: event, RPC: rpc}
}

func TestToolApprovalContextVMPlainAndWrappedRequestsReachHandler(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, tc := range []struct {
		name     string
		wrapKind int
	}{
		{name: "plain"},
		{name: "gift wrap", wrapKind: KindContextVMGiftWrap},
		{name: "ephemeral wrap", wrapKind: KindContextVMEphemeralWrap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intentID := uuid.New()
			repo := newAtomicToolApprovalRepo(intentID, domain.ToolProvisionStatusAwaitingApproval)
			processor := &countingToolApprovalProcessor{}
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requester}, zap.NewNop())
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{requester}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
			reactor.toolCoordinator = processor
			reactor.RegisterToolApprovalContextVMHandlers(transport, NewFleetOperatorGate([]string{requester}))

			inner := toolApprovalEvent(t, testRequesterKey, tc.name, intentID, "approve")
			event := inner
			if tc.wrapKind != 0 {
				event = wrapContextVMEvent(t, inner, tc.wrapKind)
			}
			transport.HandleEvent(t.Context(), event)

			if len(publisher.events) != 2 {
				t.Fatalf("published events = %d, want progress ack and response", len(publisher.events))
			}
			var response ContextVMJSONRPCResponse
			if tc.wrapKind == 0 {
				response = contextVMResponse(t, publisher.events[1])
			} else {
				response = unwrapContextVMResponse(t, publisher.events[1], testRequesterKey)
			}
			if response.Error != nil {
				t.Fatalf("tool approval response failed: %+v", response.Error)
			}
			_, applied, _, status := repo.counts()
			if applied != 1 || status != domain.ToolProvisionStatusApproved || processor.calls.Load() != 1 {
				t.Fatalf("applied=%d status=%q provisioning=%d, want 1 approved 1", applied, status, processor.calls.Load())
			}
		})
	}
}

func TestToolApprovalContextVMGateRejectsBeforeStorage(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	other := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	for _, tc := range []struct {
		name       string
		requestKey string
		gateKeys   []string
		want       string
	}{
		{name: "unauthorized", requestKey: testOtherKey, gateKeys: []string{requester}, want: fleetOperatorUnauthorizedError},
		{name: "empty allowlist", requestKey: testRequesterKey, want: fleetOperatorNotConfiguredError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intentID := uuid.New()
			repo := newAtomicToolApprovalRepo(intentID, domain.ToolProvisionStatusAwaitingApproval)
			processor := &countingToolApprovalProcessor{}
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requester, other}, zap.NewNop())
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{requester, other}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
			reactor.toolCoordinator = processor
			reactor.RegisterToolApprovalContextVMHandlers(transport, NewFleetOperatorGate(tc.gateKeys))

			transport.HandleEvent(t.Context(), toolApprovalEvent(t, tc.requestKey, tc.name, intentID, "approve"))
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || !strings.Contains(response.Error.Message, tc.want) {
				t.Fatalf("response error = %+v, want %q", response.Error, tc.want)
			}
			calls, applied, logs, _ := repo.counts()
			if calls != 0 || applied != 0 || logs != 0 || processor.calls.Load() != 0 {
				t.Fatalf("storage/provisioning reached before gate: calls=%d applied=%d logs=%d provisioning=%d", calls, applied, logs, processor.calls.Load())
			}
		})
	}
}

func TestToolApprovalContextVMRegistrationIsRequired(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requester}, zap.NewNop())
	transport.HandleEvent(t.Context(), toolApprovalEvent(t, testRequesterKey, "unregistered", uuid.New(), "approve"))
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("response error = %+v, want -32601 method not found", response.Error)
	}
}

func TestToolApprovalReplayIsRejectedWithoutRetriggeringProvisioning(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	intentID := uuid.New()
	repo := newAtomicToolApprovalRepo(intentID, domain.ToolProvisionStatusAwaitingApproval)
	processor := &countingToolApprovalProcessor{}
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requester}, zap.NewNop())
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{requester}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
	reactor.toolCoordinator = processor
	reactor.RegisterToolApprovalContextVMHandlers(transport, NewFleetOperatorGate([]string{requester}))

	transport.HandleEvent(t.Context(), toolApprovalEvent(t, testRequesterKey, "first", intentID, "approve"))
	transport.HandleEvent(t.Context(), toolApprovalEvent(t, testRequesterKey, "replay", intentID, "approve"))
	if len(publisher.events) != 4 {
		t.Fatalf("published events = %d, want one progress/response pair per request", len(publisher.events))
	}
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error == nil || !strings.Contains(response.Error.Message, repository.ErrConflict.Error()) {
		t.Fatalf("replay response = %+v, want already-decided rejection", response.Error)
	}
	calls, applied, logs, status := repo.counts()
	if calls != 2 || applied != 1 || logs != 1 || status != domain.ToolProvisionStatusApproved || processor.calls.Load() != 1 {
		t.Fatalf("calls=%d applied=%d logs=%d status=%q provisioning=%d, want 2 1 1 approved 1", calls, applied, logs, status, processor.calls.Load())
	}
}

func TestToolApprovalConcurrentResponsesApplyExactlyOnce(t *testing.T) {
	requester := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for iteration := 0; iteration < 32; iteration++ {
		intentID := uuid.New()
		repo := newAtomicToolApprovalRepo(intentID, domain.ToolProvisionStatusAwaitingApproval)
		processor := &countingToolApprovalProcessor{}
		transport := NewEncryptedRequestTransport(nil, newResponder(t, &mockEncryptedPublisher{}), []string{requester}, zap.NewNop())
		reactor := NewReactor(Config{AuthorizedPubkeys: []string{requester}}, nil, nil, nil, zap.NewNop(), WithToolProvisioningRepository(repo))
		reactor.toolCoordinator = processor
		reactor.RegisterToolApprovalContextVMHandlers(transport, NewFleetOperatorGate([]string{requester}))
		handler := transport.contextVMHandlers[ContextVMMethodToolApprovalResponse]

		start := make(chan struct{})
		results := make(chan error, 2)
		for response := 0; response < 2; response++ {
			event := toolApprovalEvent(t, testRequesterKey, fmt.Sprintf("concurrent-%d-%d", iteration, response), intentID, "approve")
			request := toolApprovalContextVMRequest(t, event)
			go func() {
				<-start
				_, err := handler(context.Background(), request)
				results <- err
			}()
		}
		close(start)
		var successes int
		for response := 0; response < 2; response++ {
			if <-results == nil {
				successes++
			}
		}
		calls, applied, logs, status := repo.counts()
		if successes != 1 || calls != 2 || applied != 1 || logs != 1 || status != domain.ToolProvisionStatusApproved || processor.calls.Load() != 1 {
			t.Fatalf("iteration %d: successes=%d calls=%d applied=%d logs=%d status=%q provisioning=%d", iteration, successes, calls, applied, logs, status, processor.calls.Load())
		}
	}
}
