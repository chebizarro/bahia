package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory"
)

type fakeCLISoulFactoryClient struct {
	listSoulsFn         func(context.Context, int, string) ([]domain.AgentSoul, error)
	getSoulFn           func(context.Context, string) (*domain.AgentSoul, error)
	listTemplatesFn     func(context.Context, int, string) ([]domain.SoulTemplate, error)
	publishProvisionFn  func(context.Context, domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error)
	awaitProvisioningFn func(context.Context, *soulfactory.SoulFactoryRequestReceipt, func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error)
	executeSoulActionFn func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error)
	awaitSoulActionFn   func(context.Context, string) (*nostr.Event, error)
	closeCalls          int
}

func (f *fakeCLISoulFactoryClient) AwaitSoulActionResult(ctx context.Context, requestID string) (*nostr.Event, error) {
	return f.awaitSoulActionFn(ctx, requestID)
}

func (f *fakeCLISoulFactoryClient) Close() {
	f.closeCalls++
}

func (f *fakeCLISoulFactoryClient) ListSouls(ctx context.Context, limit int, status string) ([]domain.AgentSoul, error) {
	return f.listSoulsFn(ctx, limit, status)
}

func (f *fakeCLISoulFactoryClient) GetSoul(ctx context.Context, agentID string) (*domain.AgentSoul, error) {
	return f.getSoulFn(ctx, agentID)
}

func (f *fakeCLISoulFactoryClient) ListTemplates(ctx context.Context, limit int, tier string) ([]domain.SoulTemplate, error) {
	return f.listTemplatesFn(ctx, limit, tier)
}

func (f *fakeCLISoulFactoryClient) PublishProvisionRequest(ctx context.Context, req domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error) {
	return f.publishProvisionFn(ctx, req)
}

func (f *fakeCLISoulFactoryClient) AwaitProvisioningResult(ctx context.Context, receipt *soulfactory.SoulFactoryRequestReceipt, onStatus func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
	return f.awaitProvisioningFn(ctx, receipt, onStatus)
}

func (f *fakeCLISoulFactoryClient) ExecuteSoulAction(ctx context.Context, soulRef string, action domain.SoulActionType, reason, newBrief string) (*nostr.Event, error) {
	return f.executeSoulActionFn(ctx, soulRef, action, reason, newBrief)
}

func TestSoulFactoryCLIListUsesRelayBackedClient(t *testing.T) {
	setupSoulFactoryCLIEnv(t)
	outputFormat = "json"

	client := &fakeCLISoulFactoryClient{
		listSoulsFn: func(_ context.Context, limit int, status string) ([]domain.AgentSoul, error) {
			if limit != 50 || status != "active" {
				t.Fatalf("ListSouls args = (%d, %q), want (50, %q)", limit, status, "active")
			}
			return []domain.AgentSoul{{AgentID: "scout", Name: "Scout", Status: domain.SoulStatusActive}}, nil
		},
		getSoulFn:       func(context.Context, string) (*domain.AgentSoul, error) { return nil, nil },
		listTemplatesFn: func(context.Context, int, string) ([]domain.SoulTemplate, error) { return nil, nil },
		publishProvisionFn: func(context.Context, domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error) {
			return nil, nil
		},
		awaitProvisioningFn: func(context.Context, *soulfactory.SoulFactoryRequestReceipt, func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
			return nil, nil
		},
		executeSoulActionFn: func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error) {
			return nil, nil
		},
	}
	withFakeCLISoulFactoryClient(t, client)

	cmd := soulFactoryCommands()
	cmd.SetArgs([]string{"list", "--status", "active"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if client.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", client.closeCalls)
	}
}

func TestSoulFactoryCLIProvisionFollowPublishesAndAwaitsResult(t *testing.T) {
	setupSoulFactoryCLIEnv(t)
	outputFormat = "json"

	client := &fakeCLISoulFactoryClient{
		listSoulsFn:     func(context.Context, int, string) ([]domain.AgentSoul, error) { return nil, nil },
		getSoulFn:       func(context.Context, string) (*domain.AgentSoul, error) { return nil, nil },
		listTemplatesFn: func(context.Context, int, string) ([]domain.SoulTemplate, error) { return nil, nil },
		publishProvisionFn: func(_ context.Context, req domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error) {
			if req.AgentID != "scout" || req.Name != "Scout" || req.Brief != "do work" || req.TemplateRef != "" {
				t.Fatalf("PublishProvisionRequest() req = %+v", req)
			}
			return &soulfactory.SoulFactoryRequestReceipt{RequestID: "req-1", RequesterPubkey: "author", AcceptedRelays: 1}, nil
		},
		awaitProvisioningFn: func(_ context.Context, receipt *soulfactory.SoulFactoryRequestReceipt, onStatus func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
			if receipt.RequestID != "req-1" {
				t.Fatalf("AwaitProvisioningResult() receipt = %+v", receipt)
			}
			if onStatus != nil {
				onStatus(soulfactory.SoulFactoryStatusEvent{Step: "generate", Message: "Generating soul"})
			}
			return &domain.ProvisioningRun{RequestID: "req-1", AgentID: "scout", Status: domain.ProvisioningStatusCompleted}, nil
		},
		executeSoulActionFn: func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error) {
			return nil, nil
		},
	}
	withFakeCLISoulFactoryClient(t, client)

	cmd := soulFactoryCommands()
	cmd.SetArgs([]string{"provision", "scout", "--name", "Scout", "--brief", "do work", "--follow"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestSoulFactoryCLITemplateAndGetCommandsUseQueryClient(t *testing.T) {
	setupSoulFactoryCLIEnv(t)
	outputFormat = "json"

	client := &fakeCLISoulFactoryClient{
		listSoulsFn: func(context.Context, int, string) ([]domain.AgentSoul, error) { return nil, nil },
		getSoulFn: func(_ context.Context, agentID string) (*domain.AgentSoul, error) {
			if agentID != "scout" {
				t.Fatalf("GetSoul() agentID = %q, want scout", agentID)
			}
			return &domain.AgentSoul{AgentID: "scout", Name: "Scout", Status: domain.SoulStatusActive}, nil
		},
		listTemplatesFn: func(_ context.Context, limit int, tier string) ([]domain.SoulTemplate, error) {
			if limit <= 0 {
				t.Fatalf("ListTemplates() limit = %d, want positive", limit)
			}
			return []domain.SoulTemplate{{Identifier: "research-agent", Name: "Research Agent", Tier: domain.SoulTierStandard}}, nil
		},
		publishProvisionFn: func(context.Context, domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error) {
			return nil, nil
		},
		awaitProvisioningFn: func(context.Context, *soulfactory.SoulFactoryRequestReceipt, func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
			return nil, nil
		},
		executeSoulActionFn: func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error) {
			return nil, nil
		},
	}
	withFakeCLISoulFactoryClient(t, client)

	cmd := soulFactoryCommands()
	cmd.SetArgs([]string{"get", "scout"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("get Execute() error = %v", err)
	}

	cmd = soulFactoryCommands()
	cmd.SetArgs([]string{"templates", "get", "research-agent"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("templates get Execute() error = %v", err)
	}
}

func TestSoulFactoryCLILifecycleCommandsUseSignedActions(t *testing.T) {
	setupSoulFactoryCLIEnv(t)
	outputFormat = "json"

	var calls []string
	client := &fakeCLISoulFactoryClient{
		listSoulsFn: func(context.Context, int, string) ([]domain.AgentSoul, error) { return nil, nil },
		getSoulFn: func(_ context.Context, agentID string) (*domain.AgentSoul, error) {
			return &domain.AgentSoul{AgentID: agentID, Name: "Scout", Status: domain.SoulStatusActive}, nil
		},
		listTemplatesFn: func(context.Context, int, string) ([]domain.SoulTemplate, error) { return nil, nil },
		publishProvisionFn: func(context.Context, domain.ProvisioningRequest) (*soulfactory.SoulFactoryRequestReceipt, error) {
			return nil, nil
		},
		awaitProvisioningFn: func(context.Context, *soulfactory.SoulFactoryRequestReceipt, func(soulfactory.SoulFactoryStatusEvent)) (*domain.ProvisioningRun, error) {
			return nil, nil
		},
		executeSoulActionFn: func(_ context.Context, soulRef string, action domain.SoulActionType, reason, newBrief string) (*nostr.Event, error) {
			calls = append(calls, strings.Join([]string{soulRef, string(action), reason, newBrief}, "|"))
			content := "{}"
			if action == domain.SoulActionRegenerate {
				content = `{"regenerated":true}`
			}
			return &nostr.Event{ID: nostr.ID{0x01}, Tags: nostr.Tags{{"status", "completed"}}, Content: content}, nil
		},
	}
	withFakeCLISoulFactoryClient(t, client)

	cmd := soulFactoryCommands()
	cmd.SetIn(strings.NewReader("scout\n"))
	cmd.SetArgs([]string{"suspend", "scout", "--reason", "maintenance"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("suspend Execute() error = %v", err)
	}

	cmd = soulFactoryCommands()
	cmd.SetArgs([]string{"regenerate", "scout", "--brief", "new brief"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("regenerate Execute() error = %v", err)
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

func setupSoulFactoryCLIEnv(t *testing.T) {
	t.Helper()
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	t.Setenv("BAHIA_NOSTR_RELAYS", "wss://relay.example")
}

func withFakeCLISoulFactoryClient(t *testing.T, client cliSoulFactoryClient) {
	t.Helper()
	withFakeCLISoulFactoryClientTimeout(t, client, nil)
}

// withFakeCLISoulFactoryClientTimeout also records the reply timeout each
// client is built with.
func withFakeCLISoulFactoryClientTimeout(t *testing.T, client cliSoulFactoryClient, replyTimeouts *[]time.Duration) {
	t.Helper()
	previous := newCLISoulFactoryClient
	newCLISoulFactoryClient = func(relays []string, privateKey string, replyTimeout time.Duration) (cliSoulFactoryClient, error) {
		if replyTimeouts != nil {
			*replyTimeouts = append(*replyTimeouts, replyTimeout)
		}
		if len(relays) != 1 || relays[0] != "wss://relay.example" {
			t.Fatalf("newCLISoulFactoryClient() relays = %+v", relays)
		}
		if strings.TrimSpace(privateKey) == "" {
			t.Fatal("newCLISoulFactoryClient() privateKey was empty")
		}
		return client, nil
	}
	t.Cleanup(func() {
		newCLISoulFactoryClient = previous
		outputFormat = "table"
	})
}

// The reply timeout comes from --reply-timeout, else the env form of
// soul_factory.reply_timeout, else zero (the client default).
func TestSoulFactoryCLIReplyTimeoutFromFlagOrEnv(t *testing.T) {
	setupSoulFactoryCLIEnv(t)
	var timeouts []time.Duration
	client := &fakeCLISoulFactoryClient{
		listSoulsFn: func(context.Context, int, string) ([]domain.AgentSoul, error) { return nil, nil },
	}
	withFakeCLISoulFactoryClientTimeout(t, client, &timeouts)

	run := func(args ...string) error {
		cmd := soulFactoryCommands()
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := run("list"); err != nil {
		t.Fatalf("list error = %v", err)
	}
	t.Setenv(soulReplyTimeoutEnv, "90s")
	if err := run("list"); err != nil {
		t.Fatalf("list with env error = %v", err)
	}
	if err := run("list", "--reply-timeout", "2m"); err != nil {
		t.Fatalf("list with flag error = %v", err)
	}
	want := []time.Duration{0, 90 * time.Second, 2 * time.Minute}
	if len(timeouts) != len(want) {
		t.Fatalf("reply timeouts = %v, want %v", timeouts, want)
	}
	for i := range want {
		if timeouts[i] != want[i] {
			t.Fatalf("reply timeouts = %v, want %v", timeouts, want)
		}
	}

	t.Setenv(soulReplyTimeoutEnv, "soon")
	if err := run("list"); err == nil || !strings.Contains(err.Error(), soulReplyTimeoutEnv) {
		t.Fatalf("invalid env error = %v, want a parse error naming %s", err, soulReplyTimeoutEnv)
	}
	if err := run("list", "--reply-timeout", "-1s"); err == nil {
		t.Fatal("negative --reply-timeout was accepted")
	}
}

// A soul action whose wait ends without a terminal result is reported as an
// unknown outcome naming the request, and souls await picks the late result up.
func TestSoulFactoryCLIActionTimeoutIsOutcomeUnknownAndAwaitPicksUpResult(t *testing.T) {
	setupSoulFactoryCLIEnv(t)
	var awaited []string
	client := &fakeCLISoulFactoryClient{
		getSoulFn: func(_ context.Context, agentID string) (*domain.AgentSoul, error) {
			return &domain.AgentSoul{AgentID: agentID, Status: domain.SoulStatusActive}, nil
		},
		executeSoulActionFn: func(context.Context, string, domain.SoulActionType, string, string) (*nostr.Event, error) {
			return nil, &soulfactory.NoTerminalResultError{RequestID: "request-1", Timeout: time.Minute, Cause: context.DeadlineExceeded}
		},
		awaitSoulActionFn: func(_ context.Context, requestID string) (*nostr.Event, error) {
			awaited = append(awaited, requestID)
			return &nostr.Event{ID: nostr.ID{0x02}, Tags: nostr.Tags{{"status", "completed"}, {"action", "suspend"}, {"agent-id", "scout"}}, Content: "{}"}, nil
		},
	}
	withFakeCLISoulFactoryClient(t, client)

	cmd := soulFactoryCommands()
	cmd.SetArgs([]string{"suspend", "scout"})
	err := cmd.Execute()
	if !errors.Is(err, soulfactory.ErrNoTerminalResult) || !strings.Contains(err.Error(), "outcome unknown") || !strings.Contains(err.Error(), "bahia souls await request-1") {
		t.Fatalf("suspend error = %v, want an outcome-unknown error naming souls await request-1", err)
	}

	cmd = soulFactoryCommands()
	cmd.SetArgs([]string{"await", "request-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("await Execute() error = %v", err)
	}
	if len(awaited) != 1 || awaited[0] != "request-1" {
		t.Fatalf("AwaitSoulActionResult() calls = %v, want [request-1]", awaited)
	}
}
