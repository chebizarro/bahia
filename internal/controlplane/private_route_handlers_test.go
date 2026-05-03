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

type fakePrivateSecretRepo struct {
	records map[uuid.UUID]*domain.ServiceSecret
}

func newFakePrivateSecretRepo() *fakePrivateSecretRepo {
	return &fakePrivateSecretRepo{records: map[uuid.UUID]*domain.ServiceSecret{}}
}

func (r *fakePrivateSecretRepo) Create(_ context.Context, s *domain.ServiceSecret) error {
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
func (r *fakePrivateSecretRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	if s, ok := r.records[id]; ok {
		copy := *s
		return &copy, nil
	}
	return nil, nil
}
func (r *fakePrivateSecretRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	out := []domain.ServiceSecret{}
	for _, s := range r.records {
		if s.ServiceID == serviceID {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *fakePrivateSecretRepo) ListByServiceAndEnv(context.Context, uuid.UUID, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *fakePrivateSecretRepo) ListEffective(context.Context, uuid.UUID, uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}
func (r *fakePrivateSecretRepo) Update(_ context.Context, s *domain.ServiceSecret) error {
	if _, ok := r.records[s.ID]; !ok {
		return repository.ErrNotFound
	}
	s.Version++
	s.UpdatedAt = time.Now().UTC()
	copy := *s
	r.records[s.ID] = &copy
	return nil
}
func (r *fakePrivateSecretRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.records, id)
	return nil
}
func (r *fakePrivateSecretRepo) DeleteByName(context.Context, uuid.UUID, *uuid.UUID, string) error {
	return nil
}

type fakePrivateServiceRepo struct{ services map[uuid.UUID]*domain.Service }

func (r *fakePrivateServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (r *fakePrivateServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if svc, ok := r.services[id]; ok {
		copy := *svc
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *fakePrivateServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *fakePrivateServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r *fakePrivateServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *fakePrivateServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *fakePrivateServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type fakePrivateMemberRepo struct{ members map[string]*domain.OrgMember }

func memberKey(orgID uuid.UUID, pubkey string) string { return orgID.String() + ":" + pubkey }
func (r *fakePrivateMemberRepo) Add(_ context.Context, member *domain.OrgMember) error {
	copy := *member
	r.members[memberKey(member.OrgID, member.Pubkey)] = &copy
	return nil
}
func (r *fakePrivateMemberRepo) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if member, ok := r.members[memberKey(orgID, pubkey)]; ok {
		copy := *member
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *fakePrivateMemberRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.OrgMember, error) {
	out := []domain.OrgMember{}
	for _, member := range r.members {
		if member.OrgID == orgID {
			out = append(out, *member)
		}
	}
	return out, nil
}
func (r *fakePrivateMemberRepo) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	out := []domain.OrgMember{}
	for _, member := range r.members {
		if member.Pubkey == pubkey {
			out = append(out, *member)
		}
	}
	return out, nil
}
func (r *fakePrivateMemberRepo) UpdateRole(context.Context, uuid.UUID, string, domain.Role) error {
	return nil
}
func (r *fakePrivateMemberRepo) Remove(context.Context, uuid.UUID, string) error { return nil }

type fakePrivateIntentRepo struct{ intent *domain.DeploymentIntent }

func (r *fakePrivateIntentRepo) Create(context.Context, *domain.DeploymentIntent) error { return nil }
func (r *fakePrivateIntentRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.intent, nil
}
func (r *fakePrivateIntentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *fakePrivateIntentRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *fakePrivateIntentRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentIntentStatus) error {
	return nil
}
func (r *fakePrivateIntentRepo) UpdateApproval(context.Context, uuid.UUID, domain.ApprovalStatus) error {
	return nil
}

func privateAuthDeps(t *testing.T, serviceID, orgID uuid.UUID, role domain.Role) (*fakePrivateServiceRepo, *auth.RBAC) {
	t.Helper()
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	services := &fakePrivateServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, OrgID: orgID, Name: "api"}}}
	members := &fakePrivateMemberRepo{members: map[string]*domain.OrgMember{}}
	_ = members.Add(context.Background(), &domain.OrgMember{OrgID: orgID, Pubkey: requesterPubkey, Role: role})
	return services, auth.NewRBAC(members)
}

type fakePrivateRunRepo struct {
	run *domain.DeploymentRun
	err error
}

func (r *fakePrivateRunRepo) Create(context.Context, *domain.DeploymentRun) error { return nil }
func (r *fakePrivateRunRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentRun, error) {
	return r.run, r.err
}
func (r *fakePrivateRunRepo) ListByIntent(context.Context, uuid.UUID) ([]domain.DeploymentRun, error) {
	return nil, nil
}
func (r *fakePrivateRunRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentRunStatus, *int) error {
	return nil
}

type fakePrivateRunLogs struct {
	logs *adapterruntime.RunLogs
	err  error
}

func (f fakePrivateRunLogs) FetchRunLogs(context.Context, *domain.DeploymentRun) (*adapterruntime.RunLogs, error) {
	return f.logs, f.err
}

type fakePrivateArtifactRepo struct {
	artifact *domain.Artifact
	err      error
}

func (r *fakePrivateArtifactRepo) Create(context.Context, *domain.Artifact) error { return nil }
func (r *fakePrivateArtifactRepo) GetByID(context.Context, uuid.UUID) (*domain.Artifact, error) {
	return r.artifact, r.err
}
func (r *fakePrivateArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *fakePrivateArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *fakePrivateArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r *fakePrivateArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type fakePrivateSignatureRepo struct{ records []domain.ArtifactSignature }

func (r *fakePrivateSignatureRepo) Create(_ context.Context, sig *domain.ArtifactSignature) error {
	r.records = append(r.records, *sig)
	return nil
}
func (r *fakePrivateSignatureRepo) GetByID(context.Context, uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *fakePrivateSignatureRepo) ListByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return r.records, nil
}
func (r *fakePrivateSignatureRepo) ListVerifiedByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return r.records, nil
}
func (r *fakePrivateSignatureRepo) HasVerifiedSignature(context.Context, uuid.UUID) (bool, error) {
	return len(r.records) > 0, nil
}

type fakePrivateSignatureVerifier struct {
	sigs []domain.ArtifactSignature
	err  error
}

func (v fakePrivateSignatureVerifier) VerifySignatures(context.Context, *domain.Artifact) ([]domain.ArtifactSignature, error) {
	return v.sigs, v.err
}

func privateRouteTransport(t *testing.T, handlers *PrivateRouteHandlers) (*PrivateTransport, *mockPrivatePublisher) {
	t.Helper()
	publisher := &mockPrivatePublisher{}
	transport := NewPrivateTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	handlers.Register(transport)
	return transport, publisher
}

func makeRouteRequest(t *testing.T, operation string, payload any) *nostr.Event {
	t.Helper()
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	return makePrivateRequestEvent(t, testRequesterKey, PrivateRequestEnvelope{
		Version:         PrivateTransportVersion,
		Operation:       operation,
		RequesterPubkey: requesterPubkey,
		Payload:         privatePayload(t, payload),
	})
}

func TestPrivateRouteHandlers_ServiceSecretsCreateListRevealEncrypted(t *testing.T) {
	repo := newFakePrivateSecretRepo()
	serviceID := uuid.New()
	services, rbac := privateAuthDeps(t, serviceID, uuid.New(), domain.RoleAdmin)
	h := NewPrivateRouteHandlers(PrivateRouteHandlersConfig{Secrets: repo, Encryptor: secrets.NewEncryptor(testServiceKey), Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := privateRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationServiceSecretsCreate, map[string]any{
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
	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationServiceSecretsList, map[string]any{"service_id": serviceID.String()}))
	listed := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	listPayload := resultPayloadMap(t, listed)
	if stringified, _ := json.Marshal(listPayload); string(stringified) == "postgres://secret" || containsJSONValue(stringified, "postgres://secret") {
		t.Fatalf("list response leaked secret value: %s", stringified)
	}

	publisher.events = nil
	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationServiceSecretsReveal, map[string]any{"service_id": serviceID.String(), "secret_id": secretID}))
	revealed := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if value := resultPayloadMap(t, revealed)["value"]; value != "postgres://secret" {
		t.Fatalf("reveal value = %#v", value)
	}
}

func containsJSONValue(data []byte, value string) bool { return strings.Contains(string(data), value) }

func TestPrivateRouteHandlers_ServiceSecretsDenyUnauthorizedRole(t *testing.T) {
	repo := newFakePrivateSecretRepo()
	serviceID := uuid.New()
	services, rbac := privateAuthDeps(t, serviceID, uuid.New(), domain.RoleDeployer)
	h := NewPrivateRouteHandlers(PrivateRouteHandlersConfig{Secrets: repo, Encryptor: secrets.NewEncryptor(testServiceKey), Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := privateRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationServiceSecretsList, map[string]any{"service_id": serviceID.String()}))
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil || !strings.Contains(envelope.Error.Message, "access denied") {
		t.Fatalf("expected encrypted access denied, got %+v", envelope)
	}
}

func TestPrivateRouteHandlers_GetRunLogsSuccessAndInProgressError(t *testing.T) {
	runID := uuid.New()
	serviceID := uuid.New()
	intentID := uuid.New()
	services, rbac := privateAuthDeps(t, serviceID, uuid.New(), domain.RoleViewer)
	run := &domain.DeploymentRun{ID: runID, DeploymentIntentID: intentID, Status: domain.RunStatusSucceeded}
	h := NewPrivateRouteHandlers(PrivateRouteHandlersConfig{
		Runs:     &fakePrivateRunRepo{run: run},
		RunLogs:  fakePrivateRunLogs{logs: &adapterruntime.RunLogs{RunID: runID, Stdout: "one\ntwo\nthree", Stderr: "err"}},
		Services: services,
		Intents:  &fakePrivateIntentRepo{intent: &domain.DeploymentIntent{ID: intentID, ServiceID: serviceID}},
		RBAC:     rbac,
		Logger:   zap.NewNop(),
	})
	transport, publisher := privateRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationDeploymentRunLogsGet, map[string]any{"run_id": runID.String(), "tail": 2, "stream": "stdout"}))
	payload := resultPayloadMap(t, decryptResultEnvelope(t, publisher.events[0], testRequesterKey))
	logs := payload["logs"].(map[string]any)
	if logs["stdout"] != "two\nthree" || logs["stderr"] != nil {
		t.Fatalf("unexpected stdout-only logs: %#v", logs)
	}

	publisher.events = nil
	run.Status = domain.RunStatusRunning
	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationDeploymentRunLogsGet, map[string]any{"run_id": runID.String()}))
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil {
		t.Fatalf("expected encrypted error for running logs, got %+v", envelope)
	}
}

func TestPrivateRouteHandlers_VerifyArtifactSignaturesStoresCounts(t *testing.T) {
	artifactID := uuid.New()
	serviceID := uuid.New()
	services, rbac := privateAuthDeps(t, serviceID, uuid.New(), domain.RoleAdmin)
	sigRepo := &fakePrivateSignatureRepo{}
	h := NewPrivateRouteHandlers(PrivateRouteHandlersConfig{
		Artifacts:  &fakePrivateArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "ghcr.io/acme/api", ImageDigest: "sha256:abc"}},
		Signatures: sigRepo,
		SignVerifier: fakePrivateSignatureVerifier{sigs: []domain.ArtifactSignature{
			{ID: uuid.New(), ArtifactID: artifactID, SignatureType: domain.SignatureCosign, SignatureRef: "ref", VerificationStatus: domain.SignatureStatusVerified},
			{ID: uuid.New(), ArtifactID: artifactID, SignatureType: domain.SignatureNostr, SignatureRef: "bad", VerificationStatus: domain.SignatureStatusRejected},
		}},
		Services: services,
		RBAC:     rbac,
		Logger:   zap.NewNop(),
	})
	transport, publisher := privateRouteTransport(t, h)

	transport.HandleEvent(context.Background(), makeRouteRequest(t, PrivateOperationArtifactSignaturesVerify, map[string]any{"artifact_id": artifactID.String()}))
	payload := resultPayloadMap(t, decryptResultEnvelope(t, publisher.events[0], testRequesterKey))
	if payload["found"] != float64(2) || payload["stored"] != float64(2) || payload["verified"] != float64(1) || payload["rejected"] != float64(1) {
		t.Fatalf("unexpected verify counts: %#v", payload)
	}
	if len(sigRepo.records) != 2 || !sigRepo.records[0].Verified {
		t.Fatalf("signatures not stored/normalized: %#v", sigRepo.records)
	}
}
