package soulfactory

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"log/slog"
)

type fakeMCPSoulFactoryClient struct {
	listSoulsFn         func(context.Context, int, string) ([]domain.AgentSoul, error)
	getSoulFn           func(context.Context, string) (*domain.AgentSoul, error)
	listTemplatesFn     func(context.Context, int, string) ([]domain.SoulTemplate, error)
	publishProvisionFn  func(context.Context, domain.ProvisioningRequest) (*SoulFactoryRequestReceipt, error)
	executeSoulActionFn func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error)
	closeCalls          int
}

func (f *fakeMCPSoulFactoryClient) Close() {
	f.closeCalls++
}

func (f *fakeMCPSoulFactoryClient) ListSouls(ctx context.Context, limit int, status string) ([]domain.AgentSoul, error) {
	return f.listSoulsFn(ctx, limit, status)
}

func (f *fakeMCPSoulFactoryClient) GetSoul(ctx context.Context, agentID string) (*domain.AgentSoul, error) {
	return f.getSoulFn(ctx, agentID)
}

func (f *fakeMCPSoulFactoryClient) ListTemplates(ctx context.Context, limit int, tier string) ([]domain.SoulTemplate, error) {
	return f.listTemplatesFn(ctx, limit, tier)
}

func (f *fakeMCPSoulFactoryClient) PublishProvisionRequest(ctx context.Context, req domain.ProvisioningRequest) (*SoulFactoryRequestReceipt, error) {
	return f.publishProvisionFn(ctx, req)
}

func (f *fakeMCPSoulFactoryClient) ExecuteSoulAction(ctx context.Context, soulRef string, action domain.SoulActionType, reason, newBrief string) (*nostr.Event, error) {
	return f.executeSoulActionFn(ctx, soulRef, action, reason, newBrief)
}

func TestMCPServerListAndGetUseRelayBackedClient(t *testing.T) {
	client := &fakeMCPSoulFactoryClient{
		listSoulsFn: func(_ context.Context, limit int, status string) ([]domain.AgentSoul, error) {
			if limit != 25 || status != "active" {
				t.Fatalf("ListSouls args = (%d, %q)", limit, status)
			}
			return []domain.AgentSoul{{AgentID: "scout", Name: "Scout", Status: domain.SoulStatusActive}}, nil
		},
		getSoulFn: func(_ context.Context, agentID string) (*domain.AgentSoul, error) {
			if agentID != "scout" {
				t.Fatalf("GetSoul() agentID = %q, want scout", agentID)
			}
			return &domain.AgentSoul{AgentID: "scout", Name: "Scout", Status: domain.SoulStatusActive}, nil
		},
		listTemplatesFn:    func(context.Context, int, string) ([]domain.SoulTemplate, error) { return nil, nil },
		publishProvisionFn: func(context.Context, domain.ProvisioningRequest) (*SoulFactoryRequestReceipt, error) { return nil, nil },
		executeSoulActionFn: func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error) {
			return nil, nil
		},
	}
	server := newTestMCPServer(t, client)

	result, err := server.CallTool(t.Context(), "soul_factory_list_souls", map[string]interface{}{"status": "active", "limit": 25})
	if err != nil {
		t.Fatalf("CallTool(list) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(list) = %+v, want success", result)
	}

	result, err = server.CallTool(t.Context(), "soul_factory_get_soul", map[string]interface{}{"agent_id": "scout"})
	if err != nil {
		t.Fatalf("CallTool(get) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(get) = %+v, want success", result)
	}
	if client.closeCalls != 2 {
		t.Fatalf("Close() calls = %d, want 2", client.closeCalls)
	}
}

func TestMCPServerTemplateAndProvisionUseRealClient(t *testing.T) {
	client := &fakeMCPSoulFactoryClient{
		listSoulsFn: func(context.Context, int, string) ([]domain.AgentSoul, error) { return nil, nil },
		getSoulFn:   func(context.Context, string) (*domain.AgentSoul, error) { return nil, nil },
		listTemplatesFn: func(_ context.Context, limit int, tier string) ([]domain.SoulTemplate, error) {
			if tier != "standard" || limit != 50 {
				t.Fatalf("ListTemplates args = (%d, %q)", limit, tier)
			}
			return []domain.SoulTemplate{{Identifier: "research-agent", Name: "Research Agent"}}, nil
		},
		publishProvisionFn: func(_ context.Context, req domain.ProvisioningRequest) (*SoulFactoryRequestReceipt, error) {
			if req.AgentID != "scout" || req.Brief != "do work" || req.Tier != domain.SoulTierStandard {
				t.Fatalf("PublishProvisionRequest() req = %+v", req)
			}
			return &SoulFactoryRequestReceipt{RequestID: "req-1", RequesterPubkey: "author", AcceptedRelays: 1}, nil
		},
		executeSoulActionFn: func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error) {
			return nil, nil
		},
	}
	server := newTestMCPServer(t, client)

	result, err := server.CallTool(t.Context(), "soul_factory_list_templates", map[string]interface{}{"tier": "standard"})
	if err != nil {
		t.Fatalf("CallTool(list_templates) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(list_templates) = %+v, want success", result)
	}

	result, err = server.CallTool(t.Context(), "soul_factory_provision", map[string]interface{}{"agent_id": "scout", "brief": "do work", "tier": "standard"})
	if err != nil {
		t.Fatalf("CallTool(provision) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(provision) = %+v, want success", result)
	}
}

func TestMCPServerActionAndRegenerateUseSignedActions(t *testing.T) {
	var calls []string
	client := &fakeMCPSoulFactoryClient{
		listSoulsFn: func(context.Context, int, string) ([]domain.AgentSoul, error) { return nil, nil },
		getSoulFn: func(_ context.Context, agentID string) (*domain.AgentSoul, error) {
			return &domain.AgentSoul{AgentID: agentID, Name: "Scout", Status: domain.SoulStatusActive}, nil
		},
		listTemplatesFn:    func(context.Context, int, string) ([]domain.SoulTemplate, error) { return nil, nil },
		publishProvisionFn: func(context.Context, domain.ProvisioningRequest) (*SoulFactoryRequestReceipt, error) { return nil, nil },
		executeSoulActionFn: func(_ context.Context, soulRef string, action domain.SoulActionType, reason, newBrief string) (*nostr.Event, error) {
			calls = append(calls, strings.Join([]string{soulRef, string(action), reason, newBrief}, "|"))
			return &nostr.Event{ID: soulTestID("mcp-action-result"), Tags: nostr.Tags{{"status", "completed"}}, Content: `{"ok":true}`}, nil
		},
	}
	server := newTestMCPServer(t, client)

	result, err := server.CallTool(t.Context(), "soul_factory_action", map[string]interface{}{"agent_id": "scout", "action": "suspend", "reason": "maintenance"})
	if err != nil {
		t.Fatalf("CallTool(action) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(action) = %+v, want success", result)
	}

	result, err = server.CallTool(t.Context(), "soul_factory_regenerate", map[string]interface{}{"agent_id": "scout", "new_brief": "new brief"})
	if err != nil {
		t.Fatalf("CallTool(regenerate) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(regenerate) = %+v, want success", result)
	}

	if len(calls) != 2 {
		t.Fatalf("ExecuteSoulAction() calls = %d, want 2", len(calls))
	}
	if calls[0] != "scout|suspend|maintenance|" {
		t.Fatalf("first ExecuteSoulAction() call = %q", calls[0])
	}
	if calls[1] != "scout|regenerate||new brief" {
		t.Fatalf("second ExecuteSoulAction() call = %q", calls[1])
	}
}

func TestMCPServerGetStatusReturnsRunState(t *testing.T) {
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, newFakeSigner(t), slog.Default())
	run := &domain.ProvisioningRun{RequestID: "req-1", AgentID: "scout", Status: domain.ProvisioningStatusRunning}
	reactor.runs[run.RequestID] = run
	server := NewMCPServer(reactor, nil, slog.Default())

	result, err := server.CallTool(t.Context(), "soul_factory_get_status", map[string]interface{}{"request_id": "req-1"})
	if err != nil {
		t.Fatalf("CallTool(get_status) error = %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("CallTool(get_status) = %+v, want success", result)
	}
}

func newTestMCPServer(t *testing.T, client mcpSoulFactoryClient) *MCPServer {
	t.Helper()
	previous := newMCPSoulFactoryClient
	newMCPSoulFactoryClient = func(relays []string, signer soulClientSigner) (mcpSoulFactoryClient, error) {
		if len(relays) != 1 || relays[0] != "wss://relay.example" {
			t.Fatalf("newMCPSoulFactoryClient() relays = %+v", relays)
		}
		if signer == nil {
			t.Fatal("newMCPSoulFactoryClient() signer was nil")
		}
		return client, nil
	}
	t.Cleanup(func() { newMCPSoulFactoryClient = previous })
	return NewMCPServer(NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, newFakeSigner(t), slog.Default()), nil, slog.Default())
}
