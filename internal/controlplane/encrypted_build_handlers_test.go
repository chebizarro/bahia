package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
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

type buildTestStarter struct {
	request HiveCIBuildStartRequest
	calls   int
}

func (f *buildTestStarter) StartHiveCIBuild(_ context.Context, request HiveCIBuildStartRequest) (*HiveCIBuildStartResult, error) {
	f.request = request
	f.calls++
	return &HiveCIBuildStartResult{
		GitSHA:  "0123456789abcdef0123456789abcdef01234567",
		GitRef:  "refs/heads/main",
		CIRunID: "hive-run-1",
	}, nil
}

type buildTestRegistry struct {
	build         *domain.Build
	builds        []domain.Build
	calls         int
	listCalls     int
	listServiceID uuid.UUID
	listLimit     int
	listOffset    int
}

func (f *buildTestRegistry) RegisterBuild(_ context.Context, build *domain.Build) error {
	f.build = build
	f.calls++
	return nil
}

func (f *buildTestRegistry) ListBuilds(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	f.listCalls++
	f.listServiceID = serviceID
	f.listLimit = limit
	f.listOffset = offset
	return f.builds, nil
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
	if registry.build == nil || registry.build.Status != domain.BuildStatusQueued || registry.build.CISystem != domain.CISystemHiveCI {
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

func TestBuildRequestUsesRegisteredServiceRepositoryCoordinate(t *testing.T) {
	serviceID := uuid.New()
	credentialID := uuid.New()
	payload := ArcanaBuildRequest{
		ServiceID:               serviceID,
		GitRef:                  "refs/heads/main",
		RepositoryCredentialRef: credentialID,
		ArtifactRepo:            "harbor.sharegap.net/cascadia/astillero",
		BuildArgs:               map[string]string{},
	}
	orgID := uuid.New()
	starter := &buildTestStarter{}
	registry := &buildTestRegistry{}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Starter:  starter,
		Registry: registry,
		Services: buildTestServices{service: &domain.Service{
			ID: serviceID, OrgID: orgID, ArtifactRepo: payload.ArtifactRepo,
			Repository: &domain.RepositoryRef{RepoCoordinate: "chebizar-coinos.io-336e0b4c237a0c000c1e/astillero"},
		}},
		Secrets: buildTestCredentials{secret: &domain.ServiceSecret{ID: credentialID, ServiceID: serviceID, Name: "git-repository"}},
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
	if starter.request.RepositoryCoordinate != "chebizar-coinos.io-336e0b4c237a0c000c1e/astillero" {
		t.Fatalf("repository coordinate = %q", starter.request.RepositoryCoordinate)
	}
	if registry.build == nil || registry.build.Metadata["repository_coordinate"] != starter.request.RepositoryCoordinate {
		t.Fatalf("registered build metadata = %#v", registry.build)
	}
}

func TestBuildRequestRejectsBuildArgsForGenericServiceWithoutAllowlist(t *testing.T) {
	serviceID := uuid.New()
	credentialID := uuid.New()
	payload := ArcanaBuildRequest{
		ServiceID:               serviceID,
		GitRef:                  "refs/heads/main",
		RepositoryCredentialRef: credentialID,
		ArtifactRepo:            "harbor.sharegap.net/cascadia/astillero",
		BuildArgs:               map[string]string{"VITE_BLOSSOM_URL": "https://blossom.example"},
	}
	starter := &buildTestStarter{}
	registry := &buildTestRegistry{}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Starter:  starter,
		Registry: registry,
		Services: buildTestServices{service: &domain.Service{
			ID: serviceID, OrgID: uuid.New(), ArtifactRepo: payload.ArtifactRepo,
			Repository: &domain.RepositoryRef{RepoCoordinate: "chebizar-coinos.io-336e0b4c237a0c000c1e/astillero"},
		}},
		Secrets: buildTestCredentials{secret: &domain.ServiceSecret{ID: credentialID, ServiceID: serviceID, Name: "git-repository"}},
		RBAC:    auth.NewRBAC(buildTestMembers{}),
	})
	params, _ := json.Marshal(payload)
	_, err := handler.RequestBuild(context.Background(), ContextVMRequest{
		Event: &nostr.Event{},
		RPC:   ContextVMJSONRPCRequest{Params: params},
	})
	if err == nil || !strings.Contains(err.Error(), "approved public build argument allowlist") {
		t.Fatalf("generic build args error = %v", err)
	}
	if starter.calls != 0 || registry.calls != 0 {
		t.Fatalf("rejected build reached starter %d times and registry %d times", starter.calls, registry.calls)
	}
}

func TestBuildRequestTransportReplayInvokesStarterAndRegistryOnce(t *testing.T) {
	serviceID := uuid.New()
	credentialID := uuid.New()
	orgID := uuid.New()
	starter := &buildTestStarter{}
	registry := &buildTestRegistry{}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Starter: starter, Registry: registry,
		Services: buildTestServices{service: &domain.Service{
			ID: serviceID, OrgID: orgID,
			ArtifactRepo: "harbor.sharegap.net/cascadia/astillero",
			Repository:   &domain.RepositoryRef{RepoCoordinate: "chebizar-coinos.io-336e0b4c237a0c000c1e/astillero"},
		}},
		Secrets: buildTestCredentials{secret: &domain.ServiceSecret{ID: credentialID, ServiceID: serviceID}},
		RBAC:    auth.NewRBAC(buildTestMembers{}),
	})
	params := map[string]any{
		"service_id": serviceID, "git_ref": "refs/heads/main",
		"repository_credential_ref": credentialID,
		"artifact_repo":             "harbor.sharegap.net/cascadia/astillero",
		"build_args":                map[string]string{},
		"_meta":                     map[string]any{"progressToken": "build-request-replay"},
	}
	requestContent := func(id string) string {
		content, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": ContextVMMethodBuildRequest, "params": params,
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		return string(content)
	}
	store := newMemoryContextVMResponseStore()
	requesterPubkey := testNostrPubKeyFromPrivateKey(t, testRequesterKey).Hex()
	firstPublisher := &mockEncryptedPublisher{}
	firstTransport := NewEncryptedRequestTransport(nil, newResponder(t, firstPublisher), []string{requesterPubkey}, zap.NewNop(), WithContextVMResponseStore(store, 24*time.Hour))
	handler.Register(firstTransport)
	firstTransport.HandleEvent(context.Background(), makeContextVMEvent(t, testRequesterKey, requestContent("first")))

	secondPublisher := &mockEncryptedPublisher{}
	secondTransport := NewEncryptedRequestTransport(nil, newResponder(t, secondPublisher), []string{requesterPubkey}, zap.NewNop(), WithContextVMResponseStore(store, 24*time.Hour))
	handler.Register(secondTransport)
	secondTransport.HandleEvent(context.Background(), makeContextVMEvent(t, testRequesterKey, requestContent("replay")))

	if starter.calls != 1 {
		t.Fatalf("starter calls = %d, want exactly 1", starter.calls)
	}
	if registry.calls != 1 {
		t.Fatalf("registry calls = %d, want exactly 1", registry.calls)
	}
	if len(firstPublisher.events) == 0 {
		t.Fatal("first request published no response events")
	}
	firstResponse := contextVMResponse(t, firstPublisher.events[len(firstPublisher.events)-1])
	firstResult, ok := firstResponse.Result.(map[string]any)
	if !ok {
		t.Fatalf("first build result = %#v", firstResponse.Result)
	}
	if len(secondPublisher.events) != 1 {
		t.Fatalf("replay response events = %d, want exactly 1 terminal response", len(secondPublisher.events))
	}
	replayed := contextVMResponse(t, secondPublisher.events[0])
	result, ok := replayed.Result.(map[string]any)
	if !ok {
		t.Fatalf("replayed build result = %#v", replayed.Result)
	}
	buildID, _ := result["build_id"].(string)
	if buildID != registry.build.ID.String() || result["ci_run_id"] != "hive-run-1" {
		t.Fatalf("replayed build result = %#v", replayed.Result)
	}
	if result["build_id"] != firstResult["build_id"] || result["ci_run_id"] != firstResult["ci_run_id"] {
		t.Fatalf("replayed result = %#v, want cached first result %#v", result, firstResult)
	}

	otherRequesterKey := nostr.Generate().Hex()
	otherRequesterPubkey := testNostrPubKeyFromPrivateKey(t, otherRequesterKey).Hex()
	otherTransport := NewEncryptedRequestTransport(nil, newResponder(t, &mockEncryptedPublisher{}), []string{otherRequesterPubkey}, zap.NewNop(), WithContextVMResponseStore(store, 24*time.Hour))
	handler.Register(otherTransport)
	otherTransport.HandleEvent(context.Background(), makeContextVMEvent(t, otherRequesterKey, requestContent("other-requester")))
	if starter.calls != 2 || registry.calls != 2 {
		t.Fatalf("requester-scoped replay calls = starter:%d registry:%d, want 2 each", starter.calls, registry.calls)
	}
}

type buildResultTestLoader struct{ build *domain.Build }

func (f buildResultTestLoader) GetByID(context.Context, uuid.UUID) (*domain.Build, error) {
	return f.build, nil
}

type buildReadTestLoader struct {
	build *domain.Build
	calls int
}

func (f *buildReadTestLoader) GetByID(context.Context, uuid.UUID) (*domain.Build, error) {
	f.calls++
	return f.build, nil
}

type buildReadTestMembers struct{ allowedOrgID uuid.UUID }

func (f buildReadTestMembers) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if orgID != f.allowedOrgID {
		return nil, nil
	}
	return &domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleViewer}, nil
}

func (buildReadTestMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

func TestBuildReadsUseTenantAuthorizationAndBoundedHistory(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	build := domain.Build{ID: uuid.New(), ServiceID: serviceID, Status: domain.BuildStatusSucceeded}
	loader := &buildReadTestLoader{build: &build}
	registry := &buildTestRegistry{builds: []domain.Build{build}}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Builds: loader, Registry: registry,
		Services: buildTestServices{service: &domain.Service{ID: serviceID, OrgID: orgID}},
		RBAC:     auth.NewRBAC(buildReadTestMembers{allowedOrgID: orgID}),
	})

	getParams, _ := json.Marshal(map[string]any{"build_id": build.ID})
	result, err := handler.GetBuild(context.Background(), ContextVMRequest{
		Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Params: getParams},
	})
	if err != nil {
		t.Fatalf("GetBuild() error = %v", err)
	}
	if got := result.(map[string]any)["build"].(*domain.Build); got.ID != build.ID {
		t.Fatalf("GetBuild() build = %#v", got)
	}

	listParams, _ := json.Marshal(map[string]any{"service_id": serviceID, "limit": 500, "offset": -5})
	result, err = handler.ListBuilds(context.Background(), ContextVMRequest{
		Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Params: listParams},
	})
	if err != nil {
		t.Fatalf("ListBuilds() error = %v", err)
	}
	response := result.(map[string]any)
	if registry.listCalls != 1 || registry.listServiceID != serviceID || registry.listLimit != 200 || registry.listOffset != 0 {
		t.Fatalf("registry list call = calls:%d service:%s limit:%d offset:%d", registry.listCalls, registry.listServiceID, registry.listLimit, registry.listOffset)
	}
	if response["count"] != 1 || response["limit"] != 200 || response["offset"] != 0 {
		t.Fatalf("ListBuilds() response = %#v", response)
	}
}

func TestBuildReadsDenyCrossTenantBeforeListingHistory(t *testing.T) {
	serviceID := uuid.New()
	build := &domain.Build{ID: uuid.New(), ServiceID: serviceID, Status: domain.BuildStatusQueued}
	loader := &buildReadTestLoader{build: build}
	registry := &buildTestRegistry{builds: []domain.Build{*build}}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Builds: loader, Registry: registry,
		Services: buildTestServices{service: &domain.Service{ID: serviceID, OrgID: uuid.New()}},
		RBAC:     auth.NewRBAC(buildReadTestMembers{allowedOrgID: uuid.New()}),
	})

	getParams, _ := json.Marshal(map[string]any{"build_id": build.ID})
	if _, err := handler.GetBuild(context.Background(), ContextVMRequest{
		Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Params: getParams},
	}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("cross-tenant GetBuild() error = %v", err)
	}
	listParams, _ := json.Marshal(map[string]any{"service_id": serviceID})
	if _, err := handler.ListBuilds(context.Background(), ContextVMRequest{
		Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Params: listParams},
	}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("cross-tenant ListBuilds() error = %v", err)
	}
	if loader.calls != 1 {
		t.Fatalf("GetBuild() loader calls = %d, want 1", loader.calls)
	}
	if registry.listCalls != 0 {
		t.Fatalf("ListBuilds() touched registry %d times before authorization", registry.listCalls)
	}
}

type buildResultTestRegistrar struct {
	artifact *domain.Artifact
	buildID  uuid.UUID
}

func (f *buildResultTestRegistrar) RegisterBuildResult(_ context.Context, buildID uuid.UUID) (*domain.Artifact, error) {
	f.buildID = buildID
	return f.artifact, nil
}

func TestRegisterBuildResultUsesOnlyAuthorizedSuccessfulBuildID(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	buildID := uuid.New()
	artifactID := uuid.New()
	build := &domain.Build{ID: buildID, ServiceID: serviceID, Status: domain.BuildStatusSucceeded}
	registrar := &buildResultTestRegistrar{artifact: &domain.Artifact{
		ID: artifactID, BuildID: buildID, ServiceID: serviceID,
		ImageRepo: "registry.example/arcana", ImageTag: "main",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Metadata:    map[string]any{"verification": map[string]any{"source": "registry_manifest"}},
	}}
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Builds: buildResultTestLoader{build: build}, ArtifactRegistrar: registrar,
		Services: buildTestServices{service: &domain.Service{ID: serviceID, OrgID: orgID}},
		RBAC:     auth.NewRBAC(buildTestMembers{}),
	})
	params, err := json.Marshal(map[string]any{"build_id": buildID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handler.RegisterBuildResult(context.Background(), ContextVMRequest{
		Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Params: params},
	})
	if err != nil {
		t.Fatalf("RegisterBuildResult() error = %v", err)
	}
	response := result.(map[string]any)
	if registrar.buildID != buildID || response["artifact_id"] != artifactID || response["manifest_digest"] != registrar.artifact.ImageDigest {
		t.Fatalf("unexpected verified artifact response: %#v", response)
	}
}

func TestRegisterBuildResultRejectsNonSuccessfulBuild(t *testing.T) {
	serviceID := uuid.New()
	buildID := uuid.New()
	handler := NewEncryptedBuildHandlers(EncryptedBuildHandlersConfig{
		Builds:            buildResultTestLoader{build: &domain.Build{ID: buildID, ServiceID: serviceID, Status: domain.BuildStatusRunning}},
		ArtifactRegistrar: &buildResultTestRegistrar{},
		Services:          buildTestServices{service: &domain.Service{ID: serviceID, OrgID: uuid.New()}},
		RBAC:              auth.NewRBAC(buildTestMembers{}),
	})
	params, _ := json.Marshal(map[string]any{"build_id": buildID})
	_, err := handler.RegisterBuildResult(context.Background(), ContextVMRequest{
		Event: &nostr.Event{}, RPC: ContextVMJSONRPCRequest{Params: params},
	})
	if err == nil || !strings.Contains(err.Error(), "successful") {
		t.Fatalf("non-successful build error = %v", err)
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
