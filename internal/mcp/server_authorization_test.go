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

func TestCallToolRejectsCrossTenantSecretMutation(t *testing.T) {
	for _, tool := range []string{"bahia_update_secret", "bahia_delete_secret"} {
		t.Run(tool, func(t *testing.T) {
			server, serviceRepo := newTestMCPServiceServer()
			secretRepo := newTestSecretRepo()
			encryptor, err := secrets.NewEncryptor("mcp-authorization-test-key")
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
			server.secretsRepo = secretRepo
			server.encryptor = encryptor
			server.authorizedPubkeys = []string{callerPubkey}
			server.rbac = auth.NewRBAC(&mcpAuthMemberLookup{member: &domain.OrgMember{
				OrgID:  callerOrgID,
				Pubkey: callerPubkey,
				Role:   domain.RoleOwner,
			}})
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
