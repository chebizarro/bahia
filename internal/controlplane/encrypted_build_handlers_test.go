package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

func validArcanaBuildRequest() ArcanaBuildRequest {
	return ArcanaBuildRequest{
		ServiceID:               uuid.New(),
		GitRef:                  "refs/heads/main",
		RepositoryCredentialRef: uuid.New(),
		ArtifactRepo:            "registry.example/arcana",
		BuildArgs: map[string]string{
			"VITE_ARCANA_SIGNER_MODE": "nip07",
			"VITE_BLOSSOM_URL":        "https://blossom.example",
		},
	}
}

func TestValidateArcanaBuildRequestPublicAllowlist(t *testing.T) {
	payload := validArcanaBuildRequest()
	for _, name := range ArcanaPublicBuildArgNames {
		payload.BuildArgs = map[string]string{name: "public-value"}
		if name == "VITE_ARCANA_SIGNER_MODE" {
			payload.BuildArgs[name] = "nip46"
		}
		if err := validateArcanaBuildRequest(payload); err != nil {
			t.Fatalf("%s should be allowed: %v", name, err)
		}
	}

	payload.BuildArgs = map[string]string{"GITHUB_TOKEN": "secret"}
	err := validateArcanaBuildRequest(payload)
	if err == nil || !strings.Contains(err.Error(), "not an approved public") {
		t.Fatalf("secret build arg error = %v", err)
	}
}

func TestBuildRequestStrictlyRejectsCredentialValues(t *testing.T) {
	params := json.RawMessage(`{
		"service_id":"00000000-0000-0000-0000-000000000001",
		"git_ref":"main",
		"repository_credential_ref":"00000000-0000-0000-0000-000000000002",
		"artifact_repo":"registry.example/arcana",
		"build_args":{},
		"github_token":"must-not-cross-nostr"
	}`)
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{})
	_, err := handler.RequestBuild(context.Background(), ContextVMRequest{
		RPC: ContextVMJSONRPCRequest{Params: params},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown field "github_token"`) {
		t.Fatalf("credential value field error = %v", err)
	}
}

func TestBuildRequestFailsClosedWithoutMirrorInitiator(t *testing.T) {
	payload := validArcanaBuildRequest()
	params, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{})
	_, err = handler.RequestBuild(context.Background(), ContextVMRequest{
		RPC: ContextVMJSONRPCRequest{Params: params},
	})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unavailable initiator error = %v", err)
	}
}

type buildTestServices struct{ service *domain.Service }

func (f buildTestServices) GetByID(context.Context, uuid.UUID) (*domain.Service, error) {
	return f.service, nil
}

type buildTestCredentials struct{ secret *domain.ServiceSecret }

func (f buildTestCredentials) GetByID(context.Context, uuid.UUID) (*domain.ServiceSecret, error) {
	return f.secret, nil
}

type buildTestMembers struct{}

func (buildTestMembers) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	return &domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleOwner}, nil
}
func (buildTestMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

type buildTestStarter struct{ request HiveCIBuildStartRequest }

func (f *buildTestStarter) StartHiveCIBuild(_ context.Context, request HiveCIBuildStartRequest) (*HiveCIBuildStartResult, error) {
	f.request = request
	return &HiveCIBuildStartResult{
		GitSHA:  "0123456789abcdef0123456789abcdef01234567",
		GitRef:  "refs/heads/main",
		CIRunID: "hive-run-1",
	}, nil
}

type buildTestRegistry struct{ build *domain.Build }

func (f *buildTestRegistry) RegisterBuild(_ context.Context, build *domain.Build) error {
	f.build = build
	return nil
}

func TestBuildRequestPersistsOnlySafeMetadataAfterInitiatorAcceptance(t *testing.T) {
	payload := validArcanaBuildRequest()
	orgID := uuid.New()
	secret := &domain.ServiceSecret{ID: payload.RepositoryCredentialRef, ServiceID: payload.ServiceID, Name: "github-private-repository"}
	starter := &buildTestStarter{}
	registry := &buildTestRegistry{}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Starter:  starter,
		Registry: registry,
		Services: buildTestServices{service: &domain.Service{
			ID: payload.ServiceID, OrgID: orgID, ArtifactRepo: payload.ArtifactRepo,
			Repository: &domain.RepositoryRef{RepoCoordinate: ArcanaRepositoryCoordinate},
		}},
		Secrets: buildTestCredentials{secret: secret},
		RBAC:    auth.NewRBAC(buildTestMembers{}),
	})
	params, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.RequestBuild(context.Background(), ContextVMRequest{
		Event: &nostr.Event{},
		RPC:   ContextVMJSONRPCRequest{Params: params},
	})
	if err != nil {
		t.Fatalf("RequestBuild() error = %v", err)
	}
	if registry.build == nil || registry.build.Status != domain.BuildStatusQueued {
		t.Fatalf("registered build = %#v", registry.build)
	}
	if starter.request.CredentialRef != payload.RepositoryCredentialRef {
		t.Fatalf("opaque credential ref = %s", starter.request.CredentialRef)
	}
	encoded, err := json.Marshal(registry.build.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), payload.RepositoryCredentialRef.String()) ||
		strings.Contains(strings.ToLower(string(encoded)), "credential") ||
		strings.Contains(strings.ToLower(string(encoded)), "token") {
		t.Fatalf("public build metadata leaked credential material: %s", encoded)
	}
}

func TestIsArcanaService(t *testing.T) {
	if !isArcanaService(&domain.Service{Repository: &domain.RepositoryRef{RepoCoordinate: ArcanaRepositoryCoordinate}}) {
		t.Fatal("NIP-34 mapped coordinate should be recognized")
	}
	if !isArcanaService(&domain.Service{RepoURL: ArcanaRepositoryURL + ".git"}) {
		t.Fatal("canonical GitHub URL should be recognized")
	}
	if isArcanaService(&domain.Service{RepoURL: "https://github.com/example/other"}) {
		t.Fatal("unrelated repository should be rejected")
	}
}
