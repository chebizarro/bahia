package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type realMCPIntentFixture struct {
	server       *Server
	canonical    canonicalMCPFixture
	services     *testServiceRepo
	environments *testEnvironmentRepo
	policies     *testPolicyRepo
	orgID        uuid.UUID
	actor        string
	stranger     string
	ctx          context.Context
	strangerCtx  context.Context
	policyClock  nostr.Timestamp
}

func newRealMCPIntentFixture(t *testing.T, domainName string, pending bool) *realMCPIntentFixture {
	t.Helper()
	f := &realMCPIntentFixture{
		services: newTestServiceRepo(), environments: newTestEnvironmentRepo(), policies: newTestPolicyRepo(),
		orgID: uuid.New(), actor: nostr.Generate().Public().Hex(), stranger: nostr.Generate().Public().Hex(),
		policyClock: nostr.Now(),
	}
	f.ctx = auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: f.actor, PubKey: f.actor, Method: auth.MethodNIP98})
	f.strangerCtx = auth.ContextWithPrincipal(context.Background(), &auth.Principal{Subject: f.stranger, PubKey: f.stranger, Method: auth.MethodNIP98})
	registry := service.NewRegistryService(f.services, f.environments, nil, nil, nil, nil, nil, &testStateRepo{}, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	f.server = newTestServerWithOptions(registry, zap.NewNop(), ServerDeps{AuthorizedPubkeys: []string{f.actor, f.stranger}})
	f.canonical = attachCanonicalMCPFixture(t, f.server)
	trust := controlplane.NewTrustSet([]string{f.actor}, zap.NewNop(), controlplane.WithBootstrapOwners(map[string]string{f.orgID.String(): f.actor}))
	proc := controlplane.NewIntentProcessor(trust, f.canonical.store, nil, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{domainName: true}}, zap.NewNop())
	canonical := nostrpool.NewRelayFirstStatePublisher(f.canonical.projector, f.canonical.sink)
	var backend controlplane.RegistryMutationBackend = registry
	if !pending {
		backend = service.NewRelayFirstRegistry(registry, canonical, zap.NewNop())
	}
	switch domainName {
	case "service":
		proc.RegisterHandler("service", controlplane.NewServiceIntentHandler(controlplane.ServiceIntentHandlerConfig{Registry: backend, Reader: f.services, Logger: zap.NewNop()}))
	case "environment":
		var publisher service.RelayFirstStatePublisher
		if !pending {
			publisher = canonical
		}
		proc.RegisterHandler("environment", controlplane.NewEnvironmentIntentHandler(backend, publisher, zap.NewNop()))
	case "policy":
		policies := service.NewPolicyService(f.policies, &testSigRepo{hasSig: true}, &testSBOMRepo{}, zap.NewNop())
		var publish controlplane.PolicyStatePublisher
		if !pending {
			publish = func(_ context.Context, policy *domain.DeploymentPolicy, deleted bool) error {
				f.publishPolicy(t, policy, deleted)
				return nil
			}
		}
		proc.RegisterHandler("policy", controlplane.NewPolicyIntentHandler(controlplane.PolicyIntentHandlerConfig{Policies: policies, Publish: publish, Logger: zap.NewNop()}))
	default:
		t.Fatalf("unknown domain %s", domainName)
	}
	f.server.intentProc = proc
	return f
}

func (f *realMCPIntentFixture) publishPolicy(t *testing.T, policy *domain.DeploymentPolicy, deleted bool) {
	t.Helper()
	tags, content := controlplane.PolicyRegistryRecord(policy, deleted)
	f.policyClock++
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: f.policyClock,
		Tags: append(nostr.Tags{{"d", policy.ID.String()}, {"domain", "policy"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", fmt.Sprint(nostrpool.KindPolicyRegistry)}, {"deleted", fmt.Sprint(deleted)}, {"t", kinds.CPStateTopicPolicyRegistry}}, tags...), Content: content}
	signer, err := controlplane.NewPrivateKeySigner(f.canonical.privateKey)
	require.NoError(t, err)
	require.NoError(t, controlplane.SignGoNostrEvent(context.Background(), signer, &ev))
	_, err = f.canonical.store.SaveEvent(ev)
	require.NoError(t, err)
}

func (f *realMCPIntentFixture) seed(t *testing.T, domainName string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	switch domainName {
	case "service":
		svc := &domain.Service{ID: id, OrgID: f.orgID, Name: "before", ArtifactRepo: "registry.example/api", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker}
		require.NoError(t, f.services.Create(context.Background(), svc))
		f.canonical.publishService(t, svc)
		f.services.mutations = 0
	case "environment":
		env := &domain.Environment{ID: id, OrgID: f.orgID, Name: "before"}
		require.NoError(t, f.environments.Create(context.Background(), env))
		f.canonical.publishEnvironment(t, env)
		f.environments.mutations = 0
	case "policy":
		env := &domain.Environment{ID: uuid.New(), OrgID: f.orgID, Name: "prod"}
		f.canonical.publishEnvironment(t, env)
		policy := &domain.DeploymentPolicy{ID: id, Name: "before", EnvironmentID: &env.ID, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, Enforcement: domain.PolicyEnforcementBlock, Enabled: true}
		require.NoError(t, f.policies.Create(context.Background(), policy))
		f.publishPolicy(t, policy, false)
		f.policies.mutations = 0
	}
	return id
}

func (f *realMCPIntentFixture) mutationCount(domainName string) int {
	switch domainName {
	case "service":
		return f.services.mutations
	case "environment":
		return f.environments.mutations
	default:
		return f.policies.mutations
	}
}

func TestMCPRealServiceEnvironmentPolicyIntentPipeline(t *testing.T) {
	cases := []struct{ name, domainName, operation string }{
		{"bahia_create_service", "service", "create"}, {"bahia_update_service", "service", "update"}, {"bahia_delete_service", "service", "delete"},
		{"bahia_create_environment", "environment", "create"}, {"bahia_update_environment", "environment", "update"}, {"bahia_delete_environment", "environment", "delete"},
		{"bahia_create_policy", "policy", "create"}, {"bahia_update_policy", "policy", "update"}, {"bahia_delete_policy", "policy", "delete"},
	}
	for _, tc := range cases {
		for _, mode := range []string{"accepted", "replay", "rejected", "pending"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				f := newRealMCPIntentFixture(t, tc.domainName, mode == "pending")
				args := map[string]any{"idempotency_key": "real-handler-call"}
				if tc.operation != "create" {
					id := f.seed(t, tc.domainName)
					switch tc.domainName {
					case "service":
						args["service_id"] = id.String()
					case "environment":
						args["environment_id"] = id.String()
					case "policy":
						args["policy_id"] = id.String()
					}
				}
				if tc.operation != "delete" {
					args["name"] = "after"
				}
				if tc.operation == "create" {
					args["org_id"] = f.orgID.String()
					switch tc.domainName {
					case "service":
						args["artifact_repo"] = "registry.example/api"
					case "environment":
						args["protected"] = false
					case "policy":
						env := &domain.Environment{ID: uuid.New(), OrgID: f.orgID, Name: "prod"}
						f.canonical.publishEnvironment(t, env)
						args["environment_id"] = env.ID.String()
						args["rules"] = []any{map[string]any{"type": "require_signature"}}
						args["enforcement"] = "block"
						args["enabled"] = true
					}
				}
				if tc.domainName == "policy" && tc.operation == "update" {
					args["enforcement"] = "warn"
				}
				ctx := f.ctx
				if mode == "rejected" {
					ctx = f.strangerCtx
				}
				first, err := f.server.CallTool(ctx, tc.name, args)
				require.NoError(t, err)
				body := mcpIntentResult(t, first)
				require.NotEmpty(t, body["intent_id"])
				require.NotEmpty(t, body["event_id"], "%v", body)
				if mode == "rejected" {
					require.True(t, first.IsError)
					require.Equal(t, "rejected", body["status"])
					require.NotEmpty(t, body["reason"])
					require.Zero(t, f.mutationCount(tc.domainName))
					return
				}
				require.False(t, first.IsError, "%v", body)
				expected := "accepted"
				if mode == "pending" {
					expected = "pending"
				}
				require.Equal(t, expected, body["status"])
				require.Equal(t, 1, f.mutationCount(tc.domainName), "real handler must mutate the in-memory repo exactly once")
				if mode == "pending" {
					require.NotContains(t, body, "state")
					return
				}
				if tc.operation == "delete" {
					require.Equal(t, true, body["deleted"])
				} else {
					state, ok := body["state"].(map[string]any)
					require.True(t, ok, "accepted result must contain canonical state: %v", body)
					require.Equal(t, "after", state["name"])
					encoded, err := json.Marshal(state)
					require.NoError(t, err)
					require.NotEmpty(t, encoded)
				}
				if mode == "replay" {
					second, err := f.server.CallTool(ctx, tc.name, args)
					require.NoError(t, err)
					again := mcpIntentResult(t, second)
					require.Equal(t, body["intent_id"], again["intent_id"])
					require.Equal(t, body["event_id"], again["event_id"])
					require.Equal(t, "accepted", again["status"])
					require.Equal(t, 1, f.mutationCount(tc.domainName), "same key must not double-apply")
				}
			})
		}
	}
}
