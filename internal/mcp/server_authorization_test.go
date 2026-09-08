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
	server := NewServer(nil, zap.NewNop())

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
	server := NewServer(nil, zap.NewNop())
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

func TestCallToolAllowsAuthorizedPubkey(t *testing.T) {
	const authorizedPubkey = "cdee943cdeadbeef000000000000000000000000000000000000000000000000"
	orgID := uuid.New()
	fixture := newMCPBuildAuthorizationFixture(orgID, []string{authorizedPubkey}, &mcpAuthMemberLookup{member: &domain.OrgMember{
		OrgID:  orgID,
		Pubkey: authorizedPubkey,
		Role:   domain.RoleOwner,
	}})
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-authorized",
		PubKey:  authorizedPubkey,
		Method:  auth.MethodNIP98,
	})

	result, err := fixture.server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": fixture.serviceID.String(),
		"git_sha":    "a1b2c3d4e5f6789012345678abcdef1234567890",
		"git_ref":    "main",
		"ci_run_id":  "ci-run-1",
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() result = %#v, want success for authorized pubkey", result)
	}
}

func TestCallToolDeniesUnauthorizedPubkey(t *testing.T) {
	const authorizedPubkey = "cdee943cdeadbeef000000000000000000000000000000000000000000000000"
	const unauthorizedPubkey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	fixture := newMCPBuildAuthorizationFixture(uuid.New(), []string{authorizedPubkey}, nil)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-unauthorized",
		PubKey:  unauthorizedPubkey,
		Method:  auth.MethodNIP98,
	})

	result, err := fixture.server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": fixture.serviceID.String(),
		"git_sha":    "a1b2c3d4e5f6789012345678abcdef1234567890",
		"git_ref":    "main",
		"ci_run_id":  "ci-run-1",
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "access denied") {
		t.Fatalf("CallTool() result = %#v, want access denied", result)
	}
}

func TestCallToolEmptyAllowlistDeniesAllExternalCallers(t *testing.T) {
	const somePubkey = "cdee943cdeadbeef000000000000000000000000000000000000000000000000"
	fixture := newMCPBuildAuthorizationFixture(uuid.New(), nil, nil)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-someone",
		PubKey:  somePubkey,
		Method:  auth.MethodNIP98,
	})

	result, err := fixture.server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": fixture.serviceID.String(),
		"git_sha":    "a1b2c3d4e5f6789012345678abcdef1234567890",
		"git_ref":    "main",
		"ci_run_id":  "ci-run-1",
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "access denied") {
		t.Fatalf("CallTool() result = %#v, want access denied (fail-closed)", result)
	}
}

func TestCallToolAllowsSystemAdminPrincipal(t *testing.T) {
	fixture := newMCPBuildAuthorizationFixture(uuid.New(), nil, nil)
	ctx := auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("admin-test"))

	result, err := fixture.server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": fixture.serviceID.String(),
		"git_sha":    "a1b2c3d4e5f6789012345678abcdef1234567890",
		"git_ref":    "main",
		"ci_run_id":  "ci-run-1",
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() result = %#v, want success for system admin principal", result)
	}
}

func TestCallToolBuildToolsEnforceTenantRBAC(t *testing.T) {
	const callerPubkey = "caller-pubkey"
	tools := []string{"bahia_list_builds", "bahia_get_build", "bahia_register_build", "bahia_update_build_status"}
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
			wantError: func(tool string) string {
				if tool == "bahia_register_build" || tool == "bahia_update_build_status" {
					return "access denied"
				}
				return ""
			},
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
					beforeBuildCount := len(fixture.builds.builds)
					beforeStatus := fixture.builds.builds[fixture.buildID].Status
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
					if tool == "bahia_register_build" && len(fixture.builds.builds) != beforeBuildCount {
						t.Fatalf("denied register mutated build count: got %d want %d", len(fixture.builds.builds), beforeBuildCount)
					}
					if tool == "bahia_update_build_status" && fixture.builds.builds[fixture.buildID].Status != beforeStatus {
						t.Fatalf("denied update mutated status: got %q want %q", fixture.builds.builds[fixture.buildID].Status, beforeStatus)
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
			server := NewServerWithOptions(registry, zap.NewNop(), ServerDeps{
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
		server: NewServerWithOptions(registry, zap.NewNop(), ServerDeps{
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
	case "bahia_register_build":
		return fixture.server.CallTool(ctx, tool, map[string]interface{}{
			"service_id": fixture.serviceID.String(),
			"git_sha":    "b1b2c3d4e5f6789012345678abcdef1234567890",
			"git_ref":    "main",
			"ci_run_id":  "new-run",
			"status":     string(domain.BuildStatusQueued),
		})
	case "bahia_update_build_status":
		return fixture.server.CallTool(ctx, tool, map[string]interface{}{
			"build_id": fixture.buildID.String(),
			"status":   string(domain.BuildStatusRunning),
		})
	default:
		return nil, errors.New("unsupported build authorization test tool")
	}
}
