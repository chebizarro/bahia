package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapterruntime "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type fakeEncryptedSecretRepo struct {
	records map[uuid.UUID]*domain.ServiceSecret
}

func newFakeEncryptedSecretRepo() *fakeEncryptedSecretRepo {
	return &fakeEncryptedSecretRepo{records: map[uuid.UUID]*domain.ServiceSecret{}}
}

func (r *fakeEncryptedSecretRepo) Create(_ context.Context, s *domain.ServiceSecret) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = s.CreatedAt
	}
	copy := *s
	r.records[s.ID] = &copy
	return nil
}
func (r *fakeEncryptedSecretRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	if s, ok := r.records[id]; ok {
		copy := *s
		return &copy, nil
	}
	return nil, nil
}
func (r *fakeEncryptedSecretRepo) GetCurrentVersion(_ context.Context, secretID uuid.UUID) (*domain.SecretVersion, error) {
	if s, ok := r.records[secretID]; ok {
		return &domain.SecretVersion{ID: uuid.New(), SecretID: secretID, Version: s.Version, EncryptedValue: s.EncryptedValue, EncryptionMethod: s.EncryptionMethod, CreatedBy: s.CreatedBy, CreatedAt: s.UpdatedAt}, nil
	}
	return nil, nil
}
func (r *fakeEncryptedSecretRepo) ListVersions(ctx context.Context, secretID uuid.UUID) ([]domain.SecretVersion, error) {
	version, err := r.GetCurrentVersion(ctx, secretID)
	if err != nil || version == nil {
		return nil, err
	}
	return []domain.SecretVersion{*version}, nil
}
func (r *fakeEncryptedSecretRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	out := []domain.ServiceSecret{}
	for _, s := range r.records {
		if s.ServiceID == serviceID {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *fakeEncryptedSecretRepo) ListByServiceAndEnv(context.Context, uuid.UUID, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *fakeEncryptedSecretRepo) ListEffective(context.Context, uuid.UUID, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *fakeEncryptedSecretRepo) Update(_ context.Context, s *domain.ServiceSecret) error {
	if _, ok := r.records[s.ID]; !ok {
		return repository.ErrNotFound
	}
	s.Version++
	s.UpdatedAt = time.Now().UTC()
	copy := *s
	r.records[s.ID] = &copy
	return nil
}
func (r *fakeEncryptedSecretRepo) RecordSecretAccessAudit(context.Context, *domain.SecretAccessAudit) error {
	return nil
}
func (r *fakeEncryptedSecretRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.records, id)
	return nil
}
func (r *fakeEncryptedSecretRepo) DeleteByName(context.Context, uuid.UUID, *uuid.UUID, string) error {
	return nil
}

type fakeEncryptedServiceRepo struct{ services map[uuid.UUID]*domain.Service }

func (r *fakeEncryptedServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (r *fakeEncryptedServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if svc, ok := r.services[id]; ok {
		copy := *svc
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *fakeEncryptedServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *fakeEncryptedServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r *fakeEncryptedServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *fakeEncryptedServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *fakeEncryptedServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type fakeEncryptedMemberRepo struct{ members map[string]*domain.OrgMember }

func memberKey(orgID uuid.UUID, pubkey string) string { return orgID.String() + ":" + pubkey }
func (r *fakeEncryptedMemberRepo) Add(_ context.Context, member *domain.OrgMember) error {
	copy := *member
	r.members[memberKey(member.OrgID, member.Pubkey)] = &copy
	return nil
}
func (r *fakeEncryptedMemberRepo) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if member, ok := r.members[memberKey(orgID, pubkey)]; ok {
		copy := *member
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *fakeEncryptedMemberRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.OrgMember, error) {
	out := []domain.OrgMember{}
	for _, member := range r.members {
		if member.OrgID == orgID {
			out = append(out, *member)
		}
	}
	return out, nil
}
func (r *fakeEncryptedMemberRepo) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	out := []domain.OrgMember{}
	for _, member := range r.members {
		if member.Pubkey == pubkey {
			out = append(out, *member)
		}
	}
	return out, nil
}
func (r *fakeEncryptedMemberRepo) UpdateRole(context.Context, uuid.UUID, string, domain.Role) error {
	return nil
}
func (r *fakeEncryptedMemberRepo) Remove(context.Context, uuid.UUID, string) error { return nil }

type fakeEncryptedIntentRepo struct{ intent *domain.DeploymentIntent }

func (r *fakeEncryptedIntentRepo) Create(context.Context, *domain.DeploymentIntent) error { return nil }
func (r *fakeEncryptedIntentRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.intent, nil
}
func (r *fakeEncryptedIntentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *fakeEncryptedIntentRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *fakeEncryptedIntentRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentIntentStatus) error {
	return nil
}
func (r *fakeEncryptedIntentRepo) UpdateApproval(context.Context, uuid.UUID, domain.ApprovalStatus) error {
	return nil
}
func (r *fakeEncryptedIntentRepo) UpdateDesiredState(context.Context, uuid.UUID, *domain.DesiredServiceSpec, string) error {
	return nil
}

type fakeEncryptedRegistryMutations struct {
	createdServices []*domain.Service
	artifacts       []*domain.Artifact
	environments    map[uuid.UUID]*domain.Environment
	deploymentUnits map[uuid.UUID][]*domain.DeploymentUnit
}

func (r *fakeEncryptedRegistryMutations) RegisterArtifact(_ context.Context, artifact *domain.Artifact) error {
	copy := *artifact
	if copy.ID == uuid.Nil {
		copy.ID = uuid.New()
		artifact.ID = copy.ID
	}
	r.artifacts = append(r.artifacts, &copy)
	return nil
}

func (r *fakeEncryptedRegistryMutations) CreateService(_ context.Context, svc *domain.Service) error {
	copy := *svc
	r.createdServices = append(r.createdServices, &copy)
	return nil
}
func (r *fakeEncryptedRegistryMutations) UpdateService(_ context.Context, svc *domain.Service) error {
	for index, existing := range r.createdServices {
		if existing.ID == svc.ID {
			copy := *svc
			r.createdServices[index] = &copy
			return nil
		}
	}
	return repository.ErrNotFound
}
func (r *fakeEncryptedRegistryMutations) DeleteService(_ context.Context, id uuid.UUID, _ bool) error {
	for index, existing := range r.createdServices {
		if existing.ID == id {
			r.createdServices = append(r.createdServices[:index], r.createdServices[index+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}
func (r *fakeEncryptedRegistryMutations) CreateEnvironment(_ context.Context, env *domain.Environment) error {
	copy := *env
	if r.environments == nil {
		r.environments = map[uuid.UUID]*domain.Environment{}
	}
	r.environments[env.ID] = &copy
	return nil
}
func (r *fakeEncryptedRegistryMutations) CreateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) error {
	if err := r.CreateEnvironment(ctx, env); err != nil {
		return err
	}
	r.deploymentUnits = copyEncryptedDeploymentUnits(r.deploymentUnits, env.ID, units)
	return nil
}
func (r *fakeEncryptedRegistryMutations) GetEnvironment(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	if env := r.environments[id]; env != nil {
		copy := *env
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *fakeEncryptedRegistryMutations) UpdateEnvironment(_ context.Context, env *domain.Environment) error {
	copy := *env
	if r.environments == nil {
		r.environments = map[uuid.UUID]*domain.Environment{}
	}
	r.environments[env.ID] = &copy
	return nil
}
func (r *fakeEncryptedRegistryMutations) UpdateEnvironmentWithDeploymentUnits(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit, _ time.Time) error {
	if err := r.UpdateEnvironment(ctx, env); err != nil {
		return err
	}
	r.deploymentUnits = copyEncryptedDeploymentUnits(r.deploymentUnits, env.ID, units)
	return nil
}
func (r *fakeEncryptedRegistryMutations) DeleteEnvironment(_ context.Context, id uuid.UUID, _ bool) error {
	if r.environments == nil || r.environments[id] == nil {
		return repository.ErrNotFound
	}
	delete(r.environments, id)
	delete(r.deploymentUnits, id)
	return nil
}

func copyEncryptedDeploymentUnits(current map[uuid.UUID][]*domain.DeploymentUnit, environmentID uuid.UUID, units []*domain.DeploymentUnit) map[uuid.UUID][]*domain.DeploymentUnit {
	if current == nil {
		current = map[uuid.UUID][]*domain.DeploymentUnit{}
	}
	copied := make([]*domain.DeploymentUnit, 0, len(units))
	for _, unit := range units {
		unitCopy := *unit
		copied = append(copied, &unitCopy)
	}
	current[environmentID] = copied
	return current
}

func encryptedAuthDeps(t *testing.T, serviceID, orgID uuid.UUID, role domain.Role) (*fakeEncryptedServiceRepo, *auth.RBAC) {
	t.Helper()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	services := &fakeEncryptedServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, OrgID: orgID, Name: "api"}}}
	members := &fakeEncryptedMemberRepo{members: map[string]*domain.OrgMember{}}
	_ = members.Add(context.Background(), &domain.OrgMember{OrgID: orgID, Pubkey: requesterPubkey, Role: role})
	return services, auth.NewRBAC(members)
}

func encryptedAdminRBAC(t *testing.T, orgID uuid.UUID) *auth.RBAC {
	t.Helper()
	_, rbac := encryptedAuthDeps(t, uuid.New(), orgID, domain.RoleAdmin)
	return rbac
}

func encryptedRequesterEvent(t *testing.T) *nostr.Event {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(testRequesterKey)
	if err != nil {
		t.Fatalf("parse requester key: %v", err)
	}
	return &nostr.Event{PubKey: secret.Public()}
}

type fakeEncryptedRunRepo struct {
	run *domain.DeploymentRun
	err error
}

func (r *fakeEncryptedRunRepo) Create(context.Context, *domain.DeploymentRun) error { return nil }
func (r *fakeEncryptedRunRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentRun, error) {
	return r.run, r.err
}
func (r *fakeEncryptedRunRepo) ListByIntent(context.Context, uuid.UUID) ([]domain.DeploymentRun, error) {
	return nil, nil
}
func (r *fakeEncryptedRunRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentRunStatus, *int) error {
	return nil
}

type fakeEncryptedRunLogs struct {
	logs *adapterruntime.RunLogs
	err  error
}

func (f fakeEncryptedRunLogs) FetchRunLogs(context.Context, *domain.DeploymentRun) (*adapterruntime.RunLogs, error) {
	return f.logs, f.err
}

type fakeEncryptedArtifactRepo struct {
	artifact *domain.Artifact
	err      error
}

func (r *fakeEncryptedArtifactRepo) Create(context.Context, *domain.Artifact) error { return nil }
func (r *fakeEncryptedArtifactRepo) GetByID(context.Context, uuid.UUID) (*domain.Artifact, error) {
	return r.artifact, r.err
}
func (r *fakeEncryptedArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *fakeEncryptedArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *fakeEncryptedArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r *fakeEncryptedArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type fakeEncryptedSignatureRepo struct{ records []domain.ArtifactSignature }

func (r *fakeEncryptedSignatureRepo) Create(_ context.Context, sig *domain.ArtifactSignature) error {
	r.records = append(r.records, *sig)
	return nil
}
func (r *fakeEncryptedSignatureRepo) GetByID(context.Context, uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *fakeEncryptedSignatureRepo) ListByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return r.records, nil
}
func (r *fakeEncryptedSignatureRepo) ListVerifiedByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return r.records, nil
}
func (r *fakeEncryptedSignatureRepo) HasVerifiedSignature(context.Context, uuid.UUID) (bool, error) {
	return len(r.records) > 0, nil
}

type fakeEncryptedSignatureVerifier struct {
	sigs []domain.ArtifactSignature
	err  error
}

func (v fakeEncryptedSignatureVerifier) VerifySignatures(context.Context, *domain.Artifact) ([]domain.ArtifactSignature, error) {
	return v.sigs, v.err
}

func encryptedRouteTransport(t *testing.T, handlers *EncryptedRouteHandlers) (*EncryptedRequestTransport, *mockEncryptedPublisher) {
	t.Helper()
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	handlers.Register(transport)
	return transport, publisher
}

func makeRouteRequest(t *testing.T, operation string, payload any) *nostr.Event {
	t.Helper()
	params, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal route request params: %v", err)
	}
	request := ContextVMJSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`"route-test"`), Method: operation, Params: params}
	content, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal route request: %v", err)
	}
	return makeContextVMEvent(t, testRequesterKey, string(content))
}

func routeResultPayload(t *testing.T, ev nostr.Event) map[string]any {
	t.Helper()
	response := contextVMResponse(t, ev)
	if response.Error != nil {
		t.Fatalf("unexpected ContextVM error: %+v", response.Error)
	}
	payload, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected ContextVM result payload: %#v", response.Result)
	}
	return payload
}

func TestEncryptedRouteHandlers_ContextVMAliasPreservesCanonicalOperation(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	var observedOperation string
	h := &EncryptedRouteHandlers{}
	h.registerRouteHandler(transport, "canonical.operation", func(_ context.Context, request EncryptedRequest) (any, error) {
		observedOperation = request.Envelope.Operation
		return map[string]any{"operation": request.Envelope.Operation}, nil
	}, "contextvm/alias")

	transport.HandleEvent(context.Background(), makeRouteRequest(t, "contextvm/alias", map[string]any{"name": "alias-check"}))

	if observedOperation != "canonical.operation" {
		t.Fatalf("observed operation = %q, want canonical.operation", observedOperation)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["operation"] != "canonical.operation" {
		t.Fatalf("unexpected alias response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_CreateServiceContextVMMethodCreatesRegistryService(t *testing.T) {
	orgID := uuid.New()
	registry := &fakeEncryptedRegistryMutations{}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceCreate, map[string]any{
		"org_id": orgID.String(), "name": "payments-api", "artifact_repo": "registry.example/payments", "repository": map[string]any{"source": "github", "clone_url": "https://git.example/payments"}, "default_branch": "release", "runtime_type": "compose",
	}))

	if len(registry.createdServices) != 1 {
		t.Fatalf("created services = %d, want 1", len(registry.createdServices))
	}
	created := registry.createdServices[0]
	if created.OrgID != orgID || created.Name != "payments-api" || created.ArtifactRepo != "registry.example/payments" || created.RuntimeType != domain.RuntimeTypeCompose || created.RepoURL != "https://git.example/payments" || created.DefaultBranch != "release" {
		t.Fatalf("unexpected created service: %#v", created)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["service_id"] == "" || payload["status"] != "created" {
		t.Fatalf("unexpected create response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_RegisterArtifactContextVMMethodMutatesRegistry(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	buildID := uuid.New()
	services := &fakeEncryptedServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, OrgID: orgID, Name: "web"}}}
	registry := &fakeEncryptedRegistryMutations{}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, Services: services, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodArtifactRegister, map[string]any{
		"build_id": buildID.String(), "service_id": serviceID.String(), "image_repo": "local/web", "image_tag": "baseline", "image_digest": "sha256:abc",
	}))

	if len(registry.artifacts) != 1 {
		t.Fatalf("registered artifacts = %d, want 1", len(registry.artifacts))
	}
	artifact := registry.artifacts[0]
	if artifact.BuildID != buildID || artifact.ServiceID != serviceID || artifact.ImageRepo != "local/web" || artifact.ImageTag != "baseline" || artifact.ImageDigest != "sha256:abc" || artifact.ScanStatus != domain.ScanStatusUnknown {
		t.Fatalf("unexpected artifact: %#v", artifact)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["status"] != "registered" || payload["artifact_id"] == "" {
		t.Fatalf("unexpected register response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_UpdateServiceRefreshesOnlyAdoptedPublicEnvironment(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	serviceRecord := &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "web", RuntimeType: domain.RuntimeTypeDocker,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: &domain.AdoptedRuntimeConfig{
			TargetName: "bahia-web", HostAlias: "local", EndpointRef: "edge-01-docker",
			Environment: map[string]string{"PATH": "/usr/bin"},
		}},
	}
	services := &fakeEncryptedServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: serviceRecord}}
	registry := &fakeEncryptedRegistryMutations{createdServices: []*domain.Service{serviceRecord}}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, Services: services, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, _ := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceUpdate, map[string]any{
		"id": serviceID.String(), "adopted_public_environment": map[string]string{
			"PUBLIC_BAHIA_BOOTSTRAP_RELAYS": "wss://bahia.example/relay",
			"PUBLIC_BAHIA_SERVICE_PUBKEYS":  "abc123",
		},
	}))

	updated := registry.createdServices[0].RuntimeConfig.Adopted
	if updated.TargetName != "bahia-web" || updated.HostAlias != "local" || updated.EndpointRef != "edge-01-docker" {
		t.Fatalf("adopted identity changed: %#v", updated)
	}
	if updated.Environment["PATH"] != "/usr/bin" || updated.Environment["PUBLIC_BAHIA_BOOTSTRAP_RELAYS"] != "wss://bahia.example/relay" || updated.Environment["PUBLIC_BAHIA_SERVICE_PUBKEYS"] != "abc123" {
		t.Fatalf("unexpected adopted environment: %#v", updated.Environment)
	}
}

func TestEncryptedRouteHandlers_UpdateAndDeleteServiceContextVMMethodsMutateRegistry(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	serviceRecord := &domain.Service{
		ID:            serviceID,
		OrgID:         orgID,
		Name:          "payments-api",
		RepoURL:       "https://github.com/example/payments",
		Repository:    &domain.RepositoryRef{Source: "github", CloneURL: "https://github.com/example/payments"},
		ArtifactRepo:  "ghcr.io/example/payments",
		DefaultBranch: "main",
		RuntimeType:   domain.RuntimeTypeDocker,
	}
	services := &fakeEncryptedServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: serviceRecord}}
	registry := &fakeEncryptedRegistryMutations{createdServices: []*domain.Service{serviceRecord}}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Registry: registry,
		Services: services,
		RBAC:     encryptedAdminRBAC(t, orgID),
		Logger:   zap.NewNop(),
	})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceUpdate, map[string]any{
		"id": serviceID.String(), "name": "payments-web", "repo_url": "https://github.com/example/payments-web", "runtime_type": "compose",
	}))
	if len(registry.createdServices) != 1 || registry.createdServices[0].Name != "payments-web" || registry.createdServices[0].RuntimeType != domain.RuntimeTypeCompose {
		t.Fatalf("unexpected updated services: %#v", registry.createdServices)
	}
	if registry.createdServices[0].RepoURL != "https://github.com/example/payments-web" || registry.createdServices[0].Repository.CloneURL != "https://github.com/example/payments-web" {
		t.Fatalf("repo_url update was not synchronized: %#v", registry.createdServices[0])
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["service_id"] != serviceID.String() || payload["status"] != "updated" {
		t.Fatalf("unexpected update response: %#v", payload)
	}

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceDelete, map[string]any{
		"id": serviceID.String(), "force": true,
	}))
	if len(registry.createdServices) != 0 {
		t.Fatalf("service was not deleted: %#v", registry.createdServices)
	}
	payload = routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["service_id"] != serviceID.String() || payload["status"] != "deleted" {
		t.Fatalf("unexpected delete response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_CreateEnvironmentContextVMMethodCreatesRegistryEnvironment(t *testing.T) {
	orgID := uuid.New()
	registry := &fakeEncryptedRegistryMutations{}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodEnvironmentCreate, map[string]any{
		"org_id": orgID.String(), "name": "staging", "loom_worker_selector": map[string]any{"region": "us-east"}, "runtime_config": map[string]any{"type": "compose"}, "deploy_strategy": "blue_green", "protected": true,
	}))

	if len(registry.environments) != 1 {
		t.Fatalf("created environments = %d, want 1", len(registry.environments))
	}
	var created *domain.Environment
	for _, env := range registry.environments {
		created = env
	}
	if created.Name != "staging" || created.DeployStrategy != domain.DeployStrategyBlueGreen || !created.Protected {
		t.Fatalf("unexpected created environment: %#v", created)
	}
	if created.LoomWorkerSelector["region"] != "us-east" || created.RuntimeConfig["type"] != "compose" {
		t.Fatalf("unexpected create payload fields: %#v", created)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["environment_id"] == "" || payload["status"] != "created" {
		t.Fatalf("unexpected create response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_CreateEnvironmentContextVMMethodPersistsRichContract(t *testing.T) {
	orgID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	members := &fakeEncryptedMemberRepo{members: map[string]*domain.OrgMember{}}
	_ = members.Add(context.Background(), &domain.OrgMember{OrgID: orgID, Pubkey: requesterPubkey, Role: domain.RoleAdmin})
	registry := &fakeEncryptedRegistryMutations{}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Registry: registry,
		RBAC:     auth.NewRBAC(members),
		Logger:   zap.NewNop(),
	})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodEnvironmentCreate, map[string]any{
		"org_id":         orgID.String(),
		"name":           "max",
		"runtime_config": map[string]any{"management_mode": "direct_runtime"},
		"targeting": map[string]any{
			"default_unit_key":       "max-compose",
			"failure_domain_labels":  map[string]string{"host": "max"},
			"secret_scope_mode":      "unit",
			"default_reconcile_mode": "observe_only",
		},
		"reconcile_mode": "auto_apply",
		"deployment_units": []map[string]any{{
			"key":             "max-compose",
			"display_name":    "Max Compose",
			"runtime_type":    "compose",
			"endpoint_ref":    "max",
			"compose_dir":     "/srv/bahia/gastown",
			"network_profile": map[string]string{"zone": "home"},
			"ownership_mode":  "bahia_managed",
			"runtime_config":  map[string]any{"execution_mode": "sdk"},
		}},
	}))

	if len(registry.environments) != 1 {
		t.Fatalf("created environments = %d, want 1", len(registry.environments))
	}
	var created *domain.Environment
	for _, env := range registry.environments {
		created = env
	}
	if created.OrgID != orgID || created.Targeting.DefaultUnitKey != "max-compose" || created.Targeting.DefaultReconcileMode != domain.ReconcileModeAutoApply {
		t.Fatalf("unexpected rich environment contract: %#v", created)
	}
	if created.Targeting.SecretScopeMode != domain.SecretScopeModeUnit || created.Targeting.FailureDomainLabels["host"] != "max" {
		t.Fatalf("unexpected environment targeting: %#v", created.Targeting)
	}
	units := registry.deploymentUnits[created.ID]
	if len(units) != 1 {
		t.Fatalf("deployment units = %d, want 1", len(units))
	}
	unit := units[0]
	if unit.RuntimeType != domain.RuntimeTypeCompose || unit.EndpointRef != "max" || unit.ComposeDir != "/srv/bahia/gastown" {
		t.Fatalf("unexpected deployment unit: %#v", unit)
	}
	if unit.ReconcileMode != domain.ReconcileModeAutoApply || unit.OwnershipMode != domain.OwnershipModeBahiaManaged || unit.RuntimeConfig["execution_mode"] != "sdk" {
		t.Fatalf("unexpected deployment unit policy: %#v", unit)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["environment_id"] == "" || payload["status"] != "created" {
		t.Fatalf("unexpected create response: %#v", payload)
	}
}

func TestDecodeStrictContextVMParamsAllowsOnlyTransportMeta(t *testing.T) {
	var payload encryptedEnvironmentCreatePayload
	if err := decodeStrictContextVMParams(json.RawMessage(`{"name":"prod","_meta":{"progressToken":"environment-create-1"}}`), &payload); err != nil {
		t.Fatalf("decode transport metadata: %v", err)
	}
	if payload.Name != "prod" {
		t.Fatalf("name = %q, want prod", payload.Name)
	}

	err := decodeStrictContextVMParams(json.RawMessage(`{"name":"prod","unknown":true,"_meta":{"progressToken":"environment-create-1"}}`), &payload)
	if err == nil || !strings.Contains(err.Error(), `unknown field "unknown"`) {
		t.Fatalf("unknown business field error = %v", err)
	}
}

func TestEncryptedRouteHandlers_CreateEnvironmentRejectsDeploymentUnitSchemaViolations(t *testing.T) {
	registry := &fakeEncryptedRegistryMutations{}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, Logger: zap.NewNop()})
	tests := []struct {
		name   string
		params string
	}{
		{name: "unknown field", params: `{"name":"prod","deployment_units":[{"key":"default","runtime_type":"compose","unknown":true}]}`},
		{name: "invalid runtime", params: `{"name":"prod","deployment_units":[{"key":"default","runtime_type":"nomad"}]}`},
		{name: "duplicate key", params: `{"name":"prod","deployment_units":[{"key":"default"},{"key":"default"}]}`},
		{name: "missing targeted unit", params: `{"name":"prod","targeting":{"default_unit_key":"missing"},"deployment_units":[{"key":"default"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.CreateEnvironment(context.Background(), ContextVMRequest{
				RPC: ContextVMJSONRPCRequest{Params: json.RawMessage(test.params)},
			})
			if err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
	if len(registry.environments) != 0 {
		t.Fatalf("invalid requests mutated registry: %#v", registry.environments)
	}
}

func TestEncryptedRouteHandlers_UpdateEnvironmentContextVMMethodAcceptsStringSelector(t *testing.T) {
	envID := uuid.New()
	orgID := uuid.New()
	registry := &fakeEncryptedRegistryMutations{environments: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, OrgID: orgID, Name: "prod", DeployStrategy: domain.DeployStrategyReplace, Protected: true},
	}}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodEnvironmentUpdate, map[string]any{
		"id": envID.String(), "name": "production", "loom_worker_selector": "region=us-west,pubkey=worker-1", "runtime_config": map[string]any{"type": "docker"}, "deploy_strategy": "canary", "protected": false,
	}))

	updated := registry.environments[envID]
	if updated.Name != "production" || updated.DeployStrategy != domain.DeployStrategyCanary || updated.Protected {
		t.Fatalf("unexpected updated environment: %#v", updated)
	}
	if updated.LoomWorkerSelector["region"] != "us-west" || updated.LoomWorkerSelector["pubkey"] != "worker-1" {
		t.Fatalf("unexpected selector: %#v", updated.LoomWorkerSelector)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["environment_id"] != envID.String() || payload["status"] != "updated" {
		t.Fatalf("unexpected update response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_UpdateEnvironmentContextVMMethodPersistsExplicitUnits(t *testing.T) {
	envID := uuid.New()
	orgID := uuid.New()
	revision := time.Now().UTC()
	registry := &fakeEncryptedRegistryMutations{environments: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, OrgID: orgID, Name: "prod", RuntimeConfig: map[string]any{"type": "docker"}, DeployStrategy: domain.DeployStrategyReplace, UpdatedAt: revision},
	}}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodEnvironmentUpdate, map[string]any{
		"id":                  envID.String(),
		"expected_updated_at": revision.Format(time.RFC3339Nano),
		"reconcile_mode":      "approval_required",
		"targeting": map[string]any{
			"default_unit_key":  "max",
			"secret_scope_mode": "environment",
		},
		"deployment_units": []map[string]any{{
			"key":            "max",
			"runtime_type":   "compose",
			"endpoint_ref":   "max",
			"compose_dir":    "/srv/bahia/gastown",
			"ownership_mode": "external",
		}},
	}))

	updated := registry.environments[envID]
	if updated.Targeting.DefaultUnitKey != "max" || updated.Targeting.DefaultReconcileMode != domain.ReconcileModeApprovalRequired {
		t.Fatalf("unexpected updated targeting: %#v", updated.Targeting)
	}
	units := registry.deploymentUnits[envID]
	if len(units) != 1 || units[0].Key != "max" || units[0].ReconcileMode != domain.ReconcileModeApprovalRequired || units[0].OwnershipMode != domain.OwnershipModeExternal {
		t.Fatalf("unexpected updated deployment units: %#v", units)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["environment_id"] != envID.String() || payload["status"] != "updated" {
		t.Fatalf("unexpected update response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_UpdateEnvironmentRequiresRevisionForCompleteUnitSet(t *testing.T) {
	envID := uuid.New()
	orgID := uuid.New()
	registry := &fakeEncryptedRegistryMutations{environments: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, OrgID: orgID, Name: "prod", DeployStrategy: domain.DeployStrategyReplace, UpdatedAt: time.Now().UTC()},
	}}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})

	_, err := h.UpdateEnvironment(context.Background(), ContextVMRequest{
		Event: encryptedRequesterEvent(t),
		RPC:   ContextVMJSONRPCRequest{Params: json.RawMessage(`{"id":"` + envID.String() + `","deployment_units":[]}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "expected_updated_at is required") {
		t.Fatalf("error = %v, want required revision precondition", err)
	}
}

func TestEncryptedRouteHandlers_DeleteEnvironmentContextVMMethodDeletesRegistryEnvironment(t *testing.T) {
	envID := uuid.New()
	orgID := uuid.New()
	registry := &fakeEncryptedRegistryMutations{environments: map[uuid.UUID]*domain.Environment{
		envID: {ID: envID, OrgID: orgID, Name: "staging"},
	}}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Registry: registry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodEnvironmentDelete, map[string]any{
		"id": envID.String(), "force": true,
	}))

	if registry.environments[envID] != nil {
		t.Fatalf("environment %s was not deleted", envID)
	}
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["environment_id"] != envID.String() || payload["status"] != "deleted" {
		t.Fatalf("unexpected delete response: %#v", payload)
	}
}

func TestEncryptedRouteHandlers_ServiceSecretsCreateListRevealEncrypted(t *testing.T) {
	repo := newFakeEncryptedSecretRepo()
	serviceID := uuid.New()
	services, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleAdmin)
	encryptor, err := secrets.NewEncryptor(testServiceKey)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Secrets: repo, Encryptor: encryptor, Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationServiceSecretsCreate, map[string]any{
		"service_id": serviceID.String(), "name": "DATABASE_URL", "value": "postgres://secret", "encryption_method": string(domain.EncryptionAES256),
	}))
	if len(publisher.events) != 2 {
		t.Fatalf("create published %d events", len(publisher.events))
	}
	createdPayload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if _, leaked := createdPayload["value"]; leaked {
		t.Fatalf("create response leaked value: %#v", createdPayload)
	}

	var secretID string
	secretMap := createdPayload["secret"].(map[string]any)
	secretID, _ = secretMap["id"].(string)
	if secretID == "" {
		t.Fatalf("missing created secret id: %#v", createdPayload)
	}

	publisher.events = nil
	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceSecretsList, map[string]any{"service_id": serviceID.String()}))
	listPayload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if stringified, _ := json.Marshal(listPayload); string(stringified) == "postgres://secret" || containsJSONValue(stringified, "postgres://secret") {
		t.Fatalf("list response leaked secret value: %s", stringified)
	}

	publisher.events = nil
	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceSecretsReveal, map[string]any{"service_id": serviceID.String(), "secret_id": secretID}))
	revealed := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if value := revealed["value"]; value != "postgres://secret" {
		t.Fatalf("reveal value = %#v", value)
	}
}

func containsJSONValue(data []byte, value string) bool { return strings.Contains(string(data), value) }

func TestEncryptedRouteHandlers_ServiceSecretsDenyUnauthorizedRole(t *testing.T) {
	repo := newFakeEncryptedSecretRepo()
	serviceID := uuid.New()
	services, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleDeployer)
	encryptor, err := secrets.NewEncryptor(testServiceKey)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Secrets: repo, Encryptor: encryptor, Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationServiceSecretsList, map[string]any{"service_id": serviceID.String()}))
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error == nil || !strings.Contains(response.Error.Message, "access denied") {
		t.Fatalf("expected ContextVM access denied, got %+v", response)
	}
}

func TestEncryptedRouteHandlers_GetRunLogsSuccessAndInProgressError(t *testing.T) {
	runID := uuid.New()
	serviceID := uuid.New()
	intentID := uuid.New()
	services, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleViewer)
	run := &domain.DeploymentRun{ID: runID, DeploymentIntentID: intentID, Status: domain.RunStatusSucceeded}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Runs:     &fakeEncryptedRunRepo{run: run},
		RunLogs:  fakeEncryptedRunLogs{logs: &adapterruntime.RunLogs{RunID: runID, Stdout: "one\ntwo\nthree", Stderr: "err"}},
		Services: services,
		Intents:  &fakeEncryptedIntentRepo{intent: &domain.DeploymentIntent{ID: intentID, ServiceID: serviceID}},
		RBAC:     rbac,
		Logger:   zap.NewNop(),
	})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodDeploymentRunLogsGet, map[string]any{"run_id": runID.String(), "tail": 2, "stream": "stdout"}))
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	logs := payload["logs"].(map[string]any)
	if logs["stdout"] != "two\nthree" || logs["stderr"] != nil {
		t.Fatalf("unexpected stdout-only logs: %#v", logs)
	}

	publisher.events = nil
	run.Status = domain.RunStatusRunning
	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationDeploymentRunLogsGet, map[string]any{"run_id": runID.String()}))
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error == nil {
		t.Fatalf("expected ContextVM error for running logs, got %+v", response)
	}
}

func TestEncryptedRouteHandlers_GetRunLogsRedactsReferencedSecretsBeforeTailing(t *testing.T) {
	runID := uuid.New()
	serviceID := uuid.New()
	intentID := uuid.New()
	secretID := uuid.New()
	servicesRepo, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleViewer)
	encryptor, err := secrets.NewEncryptor(testServiceKey)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	secretValue := "pa\"ssword"
	ciphertext, err := encryptor.Encrypt(secretValue, domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	secretRepo := newFakeEncryptedSecretRepo()
	secretRepo.records[secretID] = &domain.ServiceSecret{
		ID: secretID, ServiceID: serviceID, Name: "DATABASE_PASSWORD",
		EncryptedValue: ciphertext, EncryptionMethod: domain.EncryptionAES256, Version: 1,
	}
	intent := &domain.DeploymentIntent{
		ID:        intentID,
		ServiceID: serviceID,
		DesiredState: &domain.DesiredServiceSpec{SecretRefs: []domain.DesiredSecretRef{{
			EnvVar: "DATABASE_PASSWORD", Name: "DATABASE_PASSWORD", SecretID: secretID,
		}}},
	}
	run := &domain.DeploymentRun{ID: runID, DeploymentIntentID: intentID, Status: domain.RunStatusSucceeded}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Secrets:   secretRepo,
		Encryptor: encryptor,
		Runs:      &fakeEncryptedRunRepo{run: run},
		RunLogs: fakeEncryptedRunLogs{logs: &adapterruntime.RunLogs{
			RunID:  runID,
			Stdout: "boot\nDATABASE_PASSWORD=" + secretValue + "\nready",
			Stderr: "json=pa\\\"ssword",
		}},
		Services: servicesRepo,
		Intents:  &fakeEncryptedIntentRepo{intent: intent},
		RBAC:     rbac,
		Logger:   zap.NewNop(),
	})
	transport, publisher := encryptedRouteTransport(t, h)
	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodDeploymentRunLogsGet, map[string]any{
		"run_id": runID.String(), "tail": 2, "stream": "merged",
	}))
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	logs := payload["logs"].(map[string]any)
	serialized, _ := json.Marshal(logs)
	if strings.Contains(string(serialized), secretValue) || strings.Contains(string(serialized), "pa\\\\\\\"ssword") {
		t.Fatalf("run logs leaked referenced secret: %s", serialized)
	}
	if stdout, _ := logs["stdout"].(string); stdout != "DATABASE_PASSWORD=[REDACTED]\nready" {
		t.Fatalf("stdout was not redacted before tailing: %q", stdout)
	}
}

func TestEncryptedRouteHandlers_VerifyArtifactSignaturesStoresCounts(t *testing.T) {
	artifactID := uuid.New()
	serviceID := uuid.New()
	services, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleAdmin)
	sigRepo := &fakeEncryptedSignatureRepo{}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Artifacts:  &fakeEncryptedArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "ghcr.io/acme/api", ImageDigest: "sha256:abc"}},
		Signatures: sigRepo,
		SignVerifier: fakeEncryptedSignatureVerifier{sigs: []domain.ArtifactSignature{
			{ID: uuid.New(), ArtifactID: artifactID, SignatureType: domain.SignatureCosign, SignatureRef: "ref", VerificationStatus: domain.SignatureStatusVerified},
			{ID: uuid.New(), ArtifactID: artifactID, SignatureType: domain.SignatureNostr, SignatureRef: "bad", VerificationStatus: domain.SignatureStatusRejected},
		}},
		Services: services,
		RBAC:     rbac,
		Logger:   zap.NewNop(),
	})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationArtifactSignaturesVerify, map[string]any{"artifact_id": artifactID.String()}))
	payload := routeResultPayload(t, publisher.events[len(publisher.events)-1])
	if payload["found"] != float64(2) || payload["stored"] != float64(2) || payload["verified"] != float64(1) || payload["rejected"] != float64(1) {
		t.Fatalf("unexpected verify counts: %#v", payload)
	}
	if len(sigRepo.records) != 2 || !sigRepo.records[0].Verified {
		t.Fatalf("signatures not stored/normalized: %#v", sigRepo.records)
	}
}
