package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
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

func encryptedAuthDeps(t *testing.T, serviceID, orgID uuid.UUID, role domain.Role) (*fakeEncryptedServiceRepo, *auth.RBAC) {
	t.Helper()
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	services := &fakeEncryptedServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, OrgID: orgID, Name: "api"}}}
	members := &fakeEncryptedMemberRepo{members: map[string]*domain.OrgMember{}}
	_ = members.Add(context.Background(), &domain.OrgMember{OrgID: orgID, Pubkey: requesterPubkey, Role: role})
	return services, auth.NewRBAC(members)
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
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	return makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{
		Version:         EncryptedRequestWireVersion,
		Operation:       operation,
		RequesterPubkey: requesterPubkey,
		Payload:         encryptedPayload(t, payload),
	})
}

func TestEncryptedRouteHandlers_ServiceSecretsCreateListRevealEncrypted(t *testing.T) {
	repo := newFakeEncryptedSecretRepo()
	serviceID := uuid.New()
	services, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleAdmin)
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Secrets: repo, Encryptor: secrets.NewEncryptor(testServiceKey), Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationServiceSecretsCreate, map[string]any{
		"service_id": serviceID.String(), "name": "DATABASE_URL", "value": "postgres://secret", "encryption_method": string(domain.EncryptionAES256),
	}))
	if len(publisher.events) != 1 {
		t.Fatalf("create published %d events", len(publisher.events))
	}
	created := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	createdPayload := resultPayloadMap(t, created)
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
	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationServiceSecretsList, map[string]any{"service_id": serviceID.String()}))
	listed := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	listPayload := resultPayloadMap(t, listed)
	if stringified, _ := json.Marshal(listPayload); string(stringified) == "postgres://secret" || containsJSONValue(stringified, "postgres://secret") {
		t.Fatalf("list response leaked secret value: %s", stringified)
	}

	publisher.events = nil
	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationServiceSecretsReveal, map[string]any{"service_id": serviceID.String(), "secret_id": secretID}))
	revealed := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if value := resultPayloadMap(t, revealed)["value"]; value != "postgres://secret" {
		t.Fatalf("reveal value = %#v", value)
	}
}

func containsJSONValue(data []byte, value string) bool { return strings.Contains(string(data), value) }

func TestEncryptedRouteHandlers_ServiceSecretsDenyUnauthorizedRole(t *testing.T) {
	repo := newFakeEncryptedSecretRepo()
	serviceID := uuid.New()
	services, rbac := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleDeployer)
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{Secrets: repo, Encryptor: secrets.NewEncryptor(testServiceKey), Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationServiceSecretsList, map[string]any{"service_id": serviceID.String()}))
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil || !strings.Contains(envelope.Error.Message, "access denied") {
		t.Fatalf("expected encrypted access denied, got %+v", envelope)
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

	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationDeploymentRunLogsGet, map[string]any{"run_id": runID.String(), "tail": 2, "stream": "stdout"}))
	payload := resultPayloadMap(t, decryptResultEnvelope(t, publisher.events[0], testRequesterKey))
	logs := payload["logs"].(map[string]any)
	if logs["stdout"] != "two\nthree" || logs["stderr"] != nil {
		t.Fatalf("unexpected stdout-only logs: %#v", logs)
	}

	publisher.events = nil
	run.Status = domain.RunStatusRunning
	transport.HandleEvent(context.Background(), makeRouteRequest(t, EncryptedOperationDeploymentRunLogsGet, map[string]any{"run_id": runID.String()}))
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil {
		t.Fatalf("expected encrypted error for running logs, got %+v", envelope)
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
	payload := resultPayloadMap(t, decryptResultEnvelope(t, publisher.events[0], testRequesterKey))
	if payload["found"] != float64(2) || payload["stored"] != float64(2) || payload["verified"] != float64(1) || payload["rejected"] != float64(1) {
		t.Fatalf("unexpected verify counts: %#v", payload)
	}
	if len(sigRepo.records) != 2 || !sigRepo.records[0].Verified {
		t.Fatalf("signatures not stored/normalized: %#v", sigRepo.records)
	}
}
