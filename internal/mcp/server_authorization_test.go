package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestCallToolRejectsUnauthenticatedCaller(t *testing.T) {
	server := newTestServer(nil, zap.NewNop())

	result, err := server.CallTool(context.Background(), "bahia_delete_service", map[string]interface{}{
		"service_id": uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "authentication required") {
		t.Fatalf("CallTool() result = %#v, want authentication error", result)
	}
}

func TestCallToolRejectsAuthenticatedCallerOutsideOperatorAllowlist(t *testing.T) {
	server := newTestServer(nil, zap.NewNop())
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-unknown",
		PubKey:  "unknown-pubkey",
		Method:  auth.MethodNIP98,
	})

	result, err := server.CallTool(ctx, "bahia_delete_service", map[string]interface{}{
		"service_id": uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "access denied") {
		t.Fatalf("CallTool() result = %#v, want access denied", result)
	}
}

func TestCallToolBuildToolsEnforceTenantRBAC(t *testing.T) {
	const callerPubkey = "caller-pubkey"
	tools := []string{"bahia_list_builds", "bahia_get_build"}
	tests := []struct {
		name      string
		allowlist []string
		principal *auth.Principal
		lookup    func(uuid.UUID) auth.OrgMemberLookup
		wantError func(string) string
	}{
		{
			name:      "same tenant owner",
			allowlist: []string{callerPubkey},
			principal: &auth.Principal{Subject: "npub-caller", PubKey: callerPubkey, Method: auth.MethodNIP98},
			lookup: func(orgID uuid.UUID) auth.OrgMemberLookup {
				return &mcpAuthMemberLookup{member: &domain.OrgMember{OrgID: orgID, Pubkey: callerPubkey, Role: domain.RoleOwner}}
			},
			wantError: func(string) string { return "" },
		},
		{
			name:      "same tenant viewer",
			allowlist: []string{callerPubkey},
			principal: &auth.Principal{Subject: "npub-caller", PubKey: callerPubkey, Method: auth.MethodNIP98},
			lookup: func(orgID uuid.UUID) auth.OrgMemberLookup {
				return &mcpAuthMemberLookup{member: &domain.OrgMember{OrgID: orgID, Pubkey: callerPubkey, Role: domain.RoleViewer}}
			},
			wantError: func(string) string { return "" },
		},
		{
			name:      "cross tenant owner",
			allowlist: []string{callerPubkey},
			principal: &auth.Principal{Subject: "npub-caller", PubKey: callerPubkey, Method: auth.MethodNIP98},
			lookup: func(uuid.UUID) auth.OrgMemberLookup {
				return &mcpAuthMemberLookup{member: &domain.OrgMember{OrgID: uuid.New(), Pubkey: callerPubkey, Role: domain.RoleOwner}}
			},
			wantError: func(string) string { return "access denied" },
		},
		{
			name:      "RBAC unconfigured",
			allowlist: []string{callerPubkey},
			principal: &auth.Principal{Subject: "npub-caller", PubKey: callerPubkey, Method: auth.MethodNIP98},
			lookup:    func(uuid.UUID) auth.OrgMemberLookup { return nil },
			wantError: func(string) string { return "service authorization is not configured" },
		},
		{
			name:      "operator allowlist empty",
			principal: &auth.Principal{Subject: "npub-caller", PubKey: callerPubkey, Method: auth.MethodNIP98},
			lookup: func(orgID uuid.UUID) auth.OrgMemberLookup {
				return &mcpAuthMemberLookup{member: &domain.OrgMember{OrgID: orgID, Pubkey: callerPubkey, Role: domain.RoleOwner}}
			},
			wantError: func(string) string { return "access denied" },
		},
		{
			name:      "system admin bypass",
			principal: auth.SystemPrincipal("assistant"),
			lookup:    func(uuid.UUID) auth.OrgMemberLookup { return nil },
			wantError: func(string) string { return "" },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range tools {
				t.Run(tool, func(t *testing.T) {
					orgID := uuid.New()
					fixture := newMCPBuildAuthorizationFixture(orgID, tc.allowlist, tc.lookup(orgID))
					canonical := attachCanonicalMCPFixture(t, fixture.server)
					service, err := fixture.server.registry.GetService(context.Background(), fixture.serviceID)
					if err != nil {
						t.Fatal(err)
					}
					canonical.publishService(t, service)
					canonical.publishBuild(t, fixture.builds.builds[fixture.buildID])
					ctx := auth.ContextWithPrincipal(context.Background(), tc.principal)

					result, err := callMCPBuildAuthorizationTool(ctx, fixture, tool)
					if err != nil {
						t.Fatalf("CallTool(%s) error = %v", tool, err)
					}
					wantError := tc.wantError(tool)
					if wantError == "" {
						if result == nil || result.IsError {
							t.Fatalf("CallTool(%s) result = %#v, want success", tool, result)
						}
						return
					}
					if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, wantError) {
						t.Fatalf("CallTool(%s) result = %#v, want error containing %q", tool, result, wantError)
					}

				})
			}
		})
	}
}

func TestCallToolRejectsCrossTenantSecretMutation(t *testing.T) {
	for _, tool := range []string{"bahia_update_secret", "bahia_delete_secret"} {
		t.Run(tool, func(t *testing.T) {
			serviceRepo := newTestServiceRepo()
			secretRepo := newTestSecretRepo()
			encryptor, err := secrets.NewEncryptor("2222222222222222222222222222222222222222222222222222222222222222")
			if err != nil {
				t.Fatalf("NewEncryptor() error = %v", err)
			}

			ownerOrgID := uuid.New()
			callerOrgID := uuid.New()
			serviceID := uuid.New()
			secretID := uuid.New()
			serviceRepo.services[serviceID] = &domain.Service{ID: serviceID, OrgID: ownerOrgID, Name: "owner-service"}
			secretRepo.secrets[secretID] = &domain.ServiceSecret{
				ID:             secretID,
				ServiceID:      serviceID,
				EncryptedValue: []byte("unchanged"),
				Version:        1,
			}

			const callerPubkey = "caller-pubkey"
			registry := newMCPAuthorizationRegistry(serviceRepo, nil)
			server := newTestServerWithOptions(registry, zap.NewNop(), ServerDeps{
				SecretsRepo:       secretRepo,
				Encryptor:         encryptor,
				AuthorizedPubkeys: []string{callerPubkey},
				RBAC: auth.NewRBAC(&mcpAuthMemberLookup{member: &domain.OrgMember{
					OrgID:  callerOrgID,
					Pubkey: callerPubkey,
					Role:   domain.RoleOwner,
				}}),
			})
			ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
				Subject: "npub-caller",
				PubKey:  callerPubkey,
				Method:  auth.MethodNIP98,
			})
			arguments := map[string]interface{}{"secret_id": secretID.String()}
			if tool == "bahia_update_secret" {
				arguments["value"] = "attacker-value"
			}

			result, err := server.CallTool(ctx, tool, arguments)
			if err != nil {
				t.Fatalf("CallTool() error = %v", err)
			}
			if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "access denied") {
				t.Fatalf("CallTool() result = %#v, want cross-tenant access denied", result)
			}
			stored := secretRepo.secrets[secretID]
			if stored == nil {
				t.Fatal("cross-tenant delete removed the secret")
			}
			if string(stored.EncryptedValue) != "unchanged" || stored.Version != 1 {
				t.Fatalf("cross-tenant update mutated secret: %#v", stored)
			}
		})
	}
}

type mcpAuthMemberLookup struct {
	member *domain.OrgMember
}

func (m *mcpAuthMemberLookup) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if m.member != nil && m.member.OrgID == orgID && m.member.Pubkey == pubkey {
		return m.member, nil
	}
	return nil, errors.New("member not found")
}

func (m *mcpAuthMemberLookup) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	if m.member != nil && m.member.Pubkey == pubkey {
		return []domain.OrgMember{*m.member}, nil
	}
	return nil, nil
}

type mcpBuildAuthorizationFixture struct {
	server    *Server
	builds    *testBuildRepo
	serviceID uuid.UUID
	buildID   uuid.UUID
}

func newMCPBuildAuthorizationFixture(serviceOrgID uuid.UUID, allowlist []string, lookup auth.OrgMemberLookup) mcpBuildAuthorizationFixture {
	serviceID := uuid.New()
	buildID := uuid.New()
	serviceRepo := newTestServiceRepo()
	serviceRepo.services[serviceID] = &domain.Service{ID: serviceID, OrgID: serviceOrgID, Name: "governed-service"}
	buildRepo := newTestBuildRepo()
	buildRepo.builds[buildID] = &domain.Build{
		ID:        buildID,
		ServiceID: serviceID,
		GitSHA:    "a1b2c3d4e5f6789012345678abcdef1234567890",
		GitRef:    "main",
		CISystem:  "hive-ci",
		CIRunID:   "existing-run",
		Status:    domain.BuildStatusQueued,
	}

	var rbac *auth.RBAC
	if lookup != nil {
		rbac = auth.NewRBAC(lookup)
	}
	registry := newMCPAuthorizationRegistry(serviceRepo, buildRepo)
	return mcpBuildAuthorizationFixture{
		server: newTestServerWithOptions(registry, zap.NewNop(), ServerDeps{
			AuthorizedPubkeys: allowlist,
			RBAC:              rbac,
		}),
		builds:    buildRepo,
		serviceID: serviceID,
		buildID:   buildID,
	}
}

func newMCPAuthorizationRegistry(serviceRepo *testServiceRepo, buildRepo *testBuildRepo) *service.RegistryService {
	return service.NewRegistryService(
		serviceRepo,
		nil,
		buildRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
}

func callMCPBuildAuthorizationTool(ctx context.Context, fixture mcpBuildAuthorizationFixture, tool string) (*ToolResult, error) {
	switch tool {
	case "bahia_list_builds":
		return fixture.server.CallTool(ctx, tool, map[string]interface{}{"service_id": fixture.serviceID.String()})
	case "bahia_get_build":
		return fixture.server.CallTool(ctx, tool, map[string]interface{}{"build_id": fixture.buildID.String()})
	default:
		return nil, errors.New("unsupported build authorization test tool")
	}
}
