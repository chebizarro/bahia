package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/openagentsinc/bahia/internal/service"
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

type fakeEncryptedDeploymentUnitReader struct {
	units []domain.DeploymentUnit
	err   error
}

func (r fakeEncryptedDeploymentUnitReader) ListByEnvironment(context.Context, uuid.UUID) ([]domain.DeploymentUnit, error) {
	return r.units, r.err
}

func (r fakeEncryptedDeploymentUnitReader) ResolveDefault(_ context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	if r.err != nil {
		return nil, r.err
	}
	return domain.NewImplicitDefaultDeploymentUnit(env)
}

type fakeEncryptedRegistryMutations struct {
	createdServices []*domain.Service
	artifacts       []*domain.Artifact
	environments    map[uuid.UUID]*domain.Environment
	deploymentUnits map[uuid.UUID][]*domain.DeploymentUnit
	importObserved  func(service.ImportObservedArtifactInput) (*service.ImportObservedArtifactResult, error)
	importCalls     []service.ImportObservedArtifactInput
}

func (r *fakeEncryptedRegistryMutations) ImportObservedArtifact(_ context.Context, in service.ImportObservedArtifactInput) (*service.ImportObservedArtifactResult, error) {
	r.importCalls = append(r.importCalls, in)
	if r.importObserved != nil {
		return r.importObserved(in)
	}
	return &service.ImportObservedArtifactResult{Status: "imported", Artifact: &domain.Artifact{ID: uuid.New()}}, nil
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
func (r *fakeEncryptedRegistryMutations) UpdateServiceWithExpectedRevision(ctx context.Context, svc *domain.Service, expectedUpdatedAt time.Time) error {
	for _, existing := range r.createdServices {
		if existing.ID == svc.ID && !existing.UpdatedAt.Equal(expectedUpdatedAt) {
			return fmt.Errorf("service revision conflict: %w: %w", repository.ErrConflict, repository.ErrStaleRevision)
		}
	}
	return r.UpdateService(ctx, svc)
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
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), contextVMTestAuthorizedPubkeys(t), zap.NewNop())
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
	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodDeploymentRunLogsGet, map[string]any{"run_id": runID.String()}))
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

func TestEncryptedRouteHandlersRedactionReadsV2AndRefusesLegacy(t *testing.T) {
	ctx := context.Background()
	key, err := secrets.NewRandomDataKey()
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	ciphertext, err := key.Seal(id, 2, []byte("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	repo := newFakeEncryptedSecretRepo()
	repo.records[id] = &domain.ServiceSecret{ID: id, Version: 2, EncryptedValue: ciphertext, EncryptionMethod: domain.EncryptionAES256V2}
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Secrets: repo, Encryptor: key})
	intent := &domain.DeploymentIntent{DesiredState: &domain.DesiredServiceSpec{SecretRefs: []domain.DesiredSecretRef{{SecretID: id}}}}
	logs := &adapterruntime.RunLogs{Stdout: "token=private-token"}
	if err := h.redactRunLogSecrets(ctx, intent, logs); err != nil {
		t.Fatal(err)
	}
	if logs.Stdout != "token=[REDACTED]" {
		t.Fatalf("v2 secret was not redacted: %q", logs.Stdout)
	}
	repo.records[id].EncryptionMethod = domain.EncryptionAES256
	logs.Stdout = "token=private-token"
	if err := h.redactRunLogSecrets(ctx, intent, logs); err == nil {
		t.Fatal("v2-only reader accepted legacy history")
	}
}
