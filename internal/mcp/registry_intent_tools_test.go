package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type registryPipelineHandler struct {
	calls int
	fail  bool
	apply func(*controlplane.Intent)
}

func (*registryPipelineHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteServices
}
func (*registryPipelineHandler) IsFleetScoped() bool { return true }
func (h *registryPipelineHandler) HandleIntent(_ context.Context, intent *controlplane.Intent) error {
	h.calls++
	if h.fail {
		return errors.New("registry mutation refused")
	}
	if h.apply != nil {
		h.apply(intent)
	}
	return nil
}

type registryIntentFixture struct {
	Intents []struct {
		Content    map[string]any `json:"content"`
		Coordinate string         `json:"coordinate"`
		Domain     string         `json:"domain"`
		Op         string         `json:"op"`
	} `json:"intents"`
}

func fixtureRegistryIntents(t *testing.T) registryIntentFixture {
	t.Helper()
	data, err := os.ReadFile("../../web/tests/fixtures/d70-intent-content.json")
	require.NoError(t, err)
	var fixture registryIntentFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	return fixture
}

func fixtureIntentForTool(t *testing.T, fixture registryIntentFixture, tool string) map[string]any {
	t.Helper()
	domainName := "dns"
	op := strings.TrimPrefix(tool, "bahia_assistant_dns_")
	if strings.HasPrefix(tool, "bahia_ml_") {
		domainName, op = "ml", strings.TrimPrefix(tool, "bahia_ml_")
	}
	op = strings.ReplaceAll(op, "_", "-")
	if op == "policy-create" {
		op = "policy-apply"
	}
	if op == "record-override" {
		op = "record-set"
	}
	for _, item := range fixture.Intents {
		if item.Domain == domainName && item.Op == op {
			return copyIntentFields(item.Content)
		}
	}
	t.Fatalf("fixture missing for %s (%s/%s)", tool, domainName, op)
	return nil
}

func publishRegistryState(t *testing.T, fixture canonicalMCPFixture, family int, coordinate string, content map[string]any, deleted bool) {
	t.Helper()
	var topic nostrpool.CPStateFamilyInfo
	for _, candidate := range nostrpool.CPStateFamilyTopics() {
		if candidate.LegacyKind == family {
			topic = candidate
			break
		}
	}
	require.NotEmpty(t, topic.Topic)
	fields := copyIntentFields(content)
	fields["deleted"] = deleted
	fields["revision"] = uuid.NewString()
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	createdAt := nostr.Now()
	if deleted {
		createdAt++
	}
	event := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: createdAt, Tags: nostr.Tags{
		{"d", coordinate}, {"domain", topic.Domain}, {"entity", topic.Entity}, {"schema", "bahia.cp-state.v1"},
		{"legacy_kind", fmt.Sprint(family)}, {"deleted", fmt.Sprint(deleted)}, {"t", topic.Topic},
	}, Content: string(body)}
	signer, err := controlplane.NewPrivateKeySigner(fixture.privateKey)
	require.NoError(t, err)
	require.NoError(t, controlplane.SignGoNostrEvent(context.Background(), signer, &event))
	_, err = fixture.store.SaveEvent(event)
	require.NoError(t, err)
}

func registryDeleteSeed(t *testing.T, fixture registryIntentFixture, name string) (int, string, map[string]any) {
	t.Helper()
	switch name {
	case "bahia_ml_model_delete":
		return kinds.MLModelRegistry, "model:sample", fixtureIntentForTool(t, fixture, "bahia_ml_model_create")
	case "bahia_ml_version_delete":
		return kinds.MLModelVersionRegistry, "model-version:sample:v1", fixtureIntentForTool(t, fixture, "bahia_ml_version_create")
	case "bahia_ml_endpoint_delete":
		return kinds.MLInferenceEndpointRegistry, "endpoint:inference:prod", fixtureIntentForTool(t, fixture, "bahia_ml_endpoint_create")
	case "bahia_assistant_dns_zone_delete":
		return kinds.CPStateFamilyDNSZone.LegacyKind(), "zone:example.test", fixtureIntentForTool(t, fixture, "bahia_assistant_dns_zone_create")
	case "bahia_assistant_dns_endpoint_delete":
		return kinds.CPStateFamilyDNSEndpoint.LegacyKind(), "endpoint:service:api:prod", fixtureIntentForTool(t, fixture, "bahia_assistant_dns_endpoint_create")
	case "bahia_assistant_dns_backend_delete":
		return kinds.CPStateFamilyDNSBackend.LegacyKind(), "dnsbackend:secondary", fixtureIntentForTool(t, fixture, "bahia_assistant_dns_backend_create")
	case "bahia_assistant_dns_policy_delete":
		return kinds.CPStateFamilyDNSPolicy.LegacyKind(), "dnspolicy:00000000-0000-4000-8000-000000000001", fixtureIntentForTool(t, fixture, "bahia_assistant_dns_policy_create")
	}
	t.Fatalf("unrecognized deletion %s", name)
	return 0, "", nil
}

func TestRegistryIntentToolsPipeline(t *testing.T) {
	fixture := fixtureRegistryIntents(t)
	for _, tool := range registryIntentToolDefinitions() {
		name := tool.Name
		for _, mode := range []string{"accepted", "rejected", "replay", "pending"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				actor := nostr.Generate().Public().Hex()
				orgID := uuid.New()
				server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
				canonical := attachCanonicalMCPFixture(t, server)
				handler := &registryPipelineHandler{fail: mode == "rejected"}
				domainName := "dns"
				if strings.HasPrefix(name, "bahia_ml_") {
					domainName = "ml"
				}
				proc := controlplane.NewIntentProcessor(controlplane.NewTrustSet([]string{actor}, zap.NewNop()), canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{domainName: true}}, zap.NewNop())
				proc.RegisterHandler(domainName, handler)
				server.intentProc = proc
				ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: "operator", PubKey: actor, Method: auth.MethodNIP98})
				content := fixtureIntentForTool(t, fixture, name)
				// The override-retirement handler addresses the override, but
				// reconciliation projects endpoints; a zone hint selects that
				// canonical endpoint family without inventing override state.
				if name == "bahia_assistant_dns_override_retire" {
					content["zone_name"] = "example.test"
				}
				args := map[string]any{"org_id": orgID.String(), "content": content, "idempotency_key": "stable-call"}
				if strings.HasSuffix(name, "_delete") {
					family, dtag, state := registryDeleteSeed(t, fixture, name)
					publishRegistryState(t, canonical, family, dtag, state, false)
				}
				intentID, err := mcpIntentID(name, actor, args)
				require.NoError(t, err)
				write, err := server.registryIntentWrite(ctx, name, args, intentID)
				require.NoError(t, err)
				if mode == "accepted" || mode == "replay" {
					handler.apply = func(_ *controlplane.Intent) {
						state := copyIntentFields(write.content)
						coordinate := write.coordinate
						if write.deleted {
							coordinate = write.deleteCoordinate
							if coordinate == "" {
								coordinate = write.stateValue
							}
						}
						if name == "bahia_assistant_dns_record_set" || name == "bahia_assistant_dns_record_override" || name == "bahia_assistant_dns_override_retire" {
							state = fixtureIntentForTool(t, fixture, "bahia_assistant_dns_endpoint_create")
							coordinate = "endpoint:service:api:prod"
						}
						publishRegistryState(t, canonical, write.family, coordinate, state, write.deleted)
					}
				}
				result, err := server.CallTool(ctx, name, args)
				require.NoError(t, err)
				body := mcpIntentResult(t, result)
				require.NotEmpty(t, body["intent_id"])
				require.NotEmpty(t, body["event_id"])
				require.Equal(t, 1, handler.calls)
				switch mode {
				case "accepted", "replay":
					require.False(t, result.IsError)
					require.Equal(t, "accepted", body["status"])
					if !write.deleted {
						require.IsType(t, map[string]any{}, body["state"])
					}
					if mode == "replay" {
						second, err := server.CallTool(ctx, name, args)
						require.NoError(t, err)
						again := mcpIntentResult(t, second)
						require.Equal(t, body["intent_id"], again["intent_id"])
						require.Equal(t, body["event_id"], again["event_id"])
						require.Equal(t, "accepted", again["status"])
						require.Equal(t, 1, handler.calls)
					}
				case "rejected":
					require.True(t, result.IsError)
					require.Equal(t, "rejected", body["status"])
					require.Contains(t, body["reason"], "refused")
				case "pending":
					require.False(t, result.IsError)
					require.Equal(t, "pending", body["status"])
				}
			})
		}
	}
}

func TestRegistryIntentToolWithoutProcessorFailsClosed(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err := server.CallTool(context.Background(), "bahia_ml_model_create", map[string]any{"content": map[string]any{"slug": "sample"}})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Equal(t, "error", mcpIntentResult(t, result)["status"])
}
